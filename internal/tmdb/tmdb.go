// Package tmdb turns a library folder's title and year into movie metadata from
// themoviedb.org, and reports honestly when it cannot.
//
// The whole package is one search call. Its only subtlety is which string to
// search with: the library replaces the illegal ':' in a filename with " - ",
// but "Spider-Man" and "X-Men" contain real hyphens, so undoing that
// substitution mechanically corrupts them. See docs/decisions.md D9 —
// SearchMovie queries the raw folder title and disambiguates by year.
package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is TMDB's v3 API root. Overridable via WithBaseURL so tests can
// point the client at an httptest.Server.
const DefaultBaseURL = "https://api.themoviedb.org/3"

// requestTimeout bounds a single HTTP request. A scan makes one call per
// unmatched folder and blocks the /api/scan response, so a hung TMDB must fail
// fast rather than stall the whole scan.
const requestTimeout = 10 * time.Second

// separator is what the library writes where a title has a colon, because a
// colon is illegal in a filename: "Avengers: Infinity War" is stored as
// "Avengers - Infinity War". Only this three-character string is a substitution;
// a bare "-" is a real hyphen.
const separator = " - "

// ErrUnauthorized reports that TMDB rejected the API key. It is a configuration
// problem, not a lookup outcome: without this distinction a wrong key looks
// exactly like "none of your 29 movies exist". Callers test it with errors.Is.
var ErrUnauthorized = errors.New("tmdb: api key rejected")

// ErrNotFound reports that TMDB has no title with that id — a film for Movie, a
// show for Show. One sentinel covers both, so a caller that already branches on
// it keeps working when it gains a television screen; its message says "title"
// rather than "movie" for that reason, since T89 made it wrap "tmdb show 1396:"
// as often as it wraps a film.
//
// It is deliberately distinct from SearchMovie's (nil, nil) "no match": an id
// arrives from a URL a human can type, so the API layer answers 404 rather than
// a vague 502 about a dependency that was perfectly healthy.
var ErrNotFound = errors.New("tmdb: no such title")

// Match is the metadata we keep from a search hit. PosterPath stays the bare
// TMDB path ("/xyz.jpg"); building an image URL is the UI's job and downloading
// the image is nobody's.
type Match struct {
	TMDBID     int
	Title      string
	Year       int
	Overview   string
	PosterPath string

	// BackdropPath is the wide still the movie screen uses as its banner, and
	// VoteAverage is TMDB's own score. Both have been in every recorded fixture
	// from the start and were merely dropped by the decode struct — nothing new
	// is requested to get them.
	//
	// genre_ids stays dropped: turning ids into names needs /genre/movie/list and
	// a second cache, and Details returns full genre objects anyway, which is
	// where genres are actually shown.
	BackdropPath string
	VoteAverage  float64
}

// Details is one title as /movie/{id} or /tv/{id} describes it — everything the
// screen shows, in a single request.
//
// It embeds Match so a card and a detail page cannot come to disagree about what
// a title or a year is. Runtime is 0 and Status is "Planned" for a film TMDB has
// no release for; ReleaseDate is the full date, while Match.Year is derived from
// it.
//
// One type serves both kinds. The fields a film has and a show does not are zero
// for a show, and vice versa — see the television block below for why they are
// separate fields rather than reused ones.
type Details struct {
	Match

	Tagline string
	Runtime int // minutes; films only — a show's per-episode runtime is EpisodeRuntime
	Genres  []string
	// Status is "Released", "Post Production" or "Planned" for a film, and
	// "Returning Series", "Ended", "Canceled" or "In Production" for a show.
	Status           string
	ReleaseDate      string // "2019-04-24"; films only — a show has FirstAirDate
	OriginalLanguage string // ISO 639-1
	SpokenLanguages  []string
	Studios          []string
	Homepage         string

	// IMDBID is `tt` plus digits, exactly as TMDB spells it, and it is now
	// filled for both kinds. A film's arrives on /movie/{id} itself; a show's
	// arrives through append_to_response=external_ids, which costs no second
	// round trip (see Show). It is reported verbatim — an indexer that wants
	// the bare digits strips the prefix at its own boundary, because one
	// source's URL format is not TMDB's business.
	IMDBID string

	// Television only, and zero for a film.
	//
	// FirstAirDate and EpisodeRuntime are separate fields rather than reuses of
	// ReleaseDate and Runtime because the meanings genuinely differ, and a
	// caller reading a field that means two things has to know which kind it is
	// holding — exactly the branch this type exists to avoid. "Released
	// 2008-01-20" is a lie about a show that ran until 2013, and a Runtime of
	// 47 next to a film's 181 is comparing an episode to a whole picture. What
	// is uniform is Match.Year, which is the first-aired year for a show and the
	// release year for a film; a renderer that only needs the year never
	// branches.
	//
	// EpisodeRuntime is TMDB's episode_run_time[0]. The array exists because a
	// show's episode length varies — Breaking Bad records [45, 47] — and TMDB's
	// own pages show the first entry.
	FirstAirDate     string // "2008-01-20"; Match.Year is derived from it
	LastAirDate      string // "2013-09-29"; the most recent aired episode, not a promise of the end
	NumberOfSeasons  int
	NumberOfEpisodes int
	EpisodeRuntime   int // minutes

	// Seasons is every season TMDB lists, in TMDB's own order, and it is NOT
	// what NumberOfSeasons counts. Measured on Silo (tv 125988):
	// number_of_seasons is 4, while seasons[] reports 10, 10, 10 and **0**
	// episodes — the fourth is announced and unaired. A control built from the
	// count therefore offers a season with nothing behind it, which is exactly
	// what the show screen did until this field existed.
	//
	// It also carries season_number 0, which TMDB uses for Specials. That is
	// reported rather than filtered here: this package says what TMDB says, and
	// whether a caller can express "Specials" is the caller's problem — for the
	// search API it currently is not, because 0 already means "no season
	// constraint" there.
	Seasons []Season
}

// Season is one season as /tv/{id}'s seasons[] describes it: enough to draw a
// picker and nothing more.
//
// EpisodeCount is the field the count cannot give, and 0 is a real, common
// value — an announced season that has not aired. Name is TMDB's own label
// ("Season 1", "Specials") rather than something built from Number, because
// Specials is not "Season 0" to anybody reading it.
type Season struct {
	Number       int
	Name         string
	EpisodeCount int
	AirDate      string // "2008-01-20", and empty for a season with no date yet
}

// Client queries the TMDB v3 API. The zero value is not usable; call New.
type Client struct {
	apiKey  string
	http    *http.Client
	baseURL string
}

// Option customises a Client.
type Option func(*Client)

// WithBaseURL points the client at another API root, so tests can substitute an
// httptest.Server. A trailing slash is trimmed so paths join predictably.
func WithBaseURL(base string) Option {
	return func(c *Client) {
		c.baseURL = strings.TrimSuffix(base, "/")
	}
}

// New returns a Client using the given API key and HTTP client. The HTTP client
// is injected so tests never reach the network; a nil one gets a default with a
// timeout, since http.DefaultClient has none and would hang forever.
func New(apiKey string, httpClient *http.Client, opts ...Option) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	c := &Client{
		apiKey:  apiKey,
		http:    httpClient,
		baseURL: DefaultBaseURL,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// SearchMovie looks up one movie by its folder title and year.
//
// A nil Match with a nil error means "TMDB does not know this film, or not at
// this year" — a normal outcome that the caller records as tmdb_id = NULL. Six
// of the 29 library titles are 2026 releases where a confident-but-wrong match
// is entirely plausible, so a wrong year is treated as no match at all.
//
// The raw folder title is the first and usually only query: TMDB's search is
// fuzzy enough that "Avengers - Infinity War" finds "Avengers: Infinity War"
// (verified against the live API for all 8 hyphenated library titles). Only a
// genuinely empty result triggers the one fallback, with " - " collapsed to a
// space. There is no third attempt and no "-" → ":" rewrite.
func (c *Client) SearchMovie(ctx context.Context, title string, year int) (*Match, error) {
	if strings.TrimSpace(title) == "" {
		return nil, errors.New("tmdb: search with empty title")
	}

	results, err := c.search(ctx, title, year)
	if err != nil {
		return nil, err
	}

	// Zero results — not "results we rejected" — is the only trigger for the
	// fallback. A result set that exists but disagrees on the year means TMDB
	// knows the title and this is not it; searching again with a different
	// spelling would only widen the chance of a wrong match.
	if len(results) == 0 {
		if collapsed := collapseSeparator(title); collapsed != title {
			results, err = c.search(ctx, collapsed, year)
			if err != nil {
				return nil, fmt.Errorf("collapsed from %q: %w", title, err)
			}
		}
	}

	return pick(results, year), nil
}

// searchResponse is the subset of TMDB's /search/movie payload we read.
type searchResponse struct {
	Results []searchResult `json:"results"`
}

type searchResult struct {
	ID           int     `json:"id"`
	Title        string  `json:"title"`
	ReleaseDate  string  `json:"release_date"`
	Overview     string  `json:"overview"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	VoteAverage  float64 `json:"vote_average"`
}

// detailsResponse is /movie/{id}. It embeds searchResult because TMDB repeats
// those fields verbatim there; only what the movie screen shows is decoded, and
// TMDB sends about thirty more.
type detailsResponse struct {
	searchResult

	Tagline          string `json:"tagline"`
	Runtime          int    `json:"runtime"`
	Status           string `json:"status"`
	Homepage         string `json:"homepage"`
	IMDBID           string `json:"imdb_id"`
	OriginalLanguage string `json:"original_language"`

	Genres              []struct{ Name string } `json:"genres"`
	ProductionCompanies []struct{ Name string } `json:"production_companies"`
	SpokenLanguages     []struct {
		EnglishName string `json:"english_name"`
	} `json:"spoken_languages"`
}

// statusResponse is TMDB's error envelope, e.g.
// {"status_code":7,"status_message":"Invalid API key: ..."}.
type statusResponse struct {
	StatusMessage string `json:"status_message"`
}

// search performs exactly one /search/movie request. It never retries: TMDB is
// not flaky enough to justify hammering it during a synchronous scan.
func (c *Client) search(ctx context.Context, query string, year int) ([]searchResult, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("include_adult", "false")
	if year > 0 {
		params.Set("primary_release_year", strconv.Itoa(year))
	}

	var body searchResponse
	if err := c.get(ctx, "/search/movie", params, fmt.Sprintf("search %q", query), &body); err != nil {
		return nil, err
	}
	return body.Results, nil
}

// get performs exactly one GET against the API and decodes it into out.
//
// EVERY endpoint in this package goes through here, and that is the whole reason
// it exists. The query string carries api_key=, so a transport failure has to be
// scrubbed or a DNS error prints the key into the logs — and with five endpoints
// that is five chances to forget. One place to get right, and
// TestEveryEndpointScrubsTheAPIKey is table-driven over every exported method so
// a sixth cannot quietly skip it.
//
// what is a short description for error messages — `search "Dune"`, `movie 1234`
// — and never contains the URL.
func (c *Client) get(ctx context.Context, path string, params url.Values, what string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	if params == nil {
		params = url.Values{}
	}
	params.Set("api_key", c.apiKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+params.Encode(), nil)
	if err != nil {
		return fmt.Errorf("tmdb %s: build request: %w", what, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// scrubURL, not err: *url.Error stringifies the whole URL, api_key and
		// all. Unwrapping keeps the cause, so errors.Is against context.Canceled
		// and DeadlineExceeded still works.
		return fmt.Errorf("tmdb %s: %w", what, scrubURL(err))
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return fmt.Errorf("tmdb %s: %w: %s", what, ErrUnauthorized, statusMessage(resp.Body))
	case http.StatusNotFound:
		return fmt.Errorf("tmdb %s: %w: %s", what, ErrNotFound, statusMessage(resp.Body))
	default:
		return fmt.Errorf("tmdb %s: unexpected status %s: %s", what, resp.Status, statusMessage(resp.Body))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("tmdb %s: decode response: %w", what, err)
	}
	return nil
}

// pick returns the first result released in the wanted year, or nil. TMDB orders
// results by popularity, so the first year-matching hit is the right one — a
// search for "Supergirl" in 1984 must not return the 2026 remake just because it
// leads the list.
func pick(results []searchResult, year int) *Match {
	for _, r := range results {
		if r.ID == 0 {
			continue
		}
		got := releaseYear(r.ReleaseDate)
		if year > 0 && got != year {
			continue
		}
		match := toMatch(r)
		return &match
	}
	return nil
}

// toMatch converts one decoded result. pick and every browse method go through
// it, so a card built by a search and a card built by /movie/{id} cannot come to
// disagree about what a field means.
func toMatch(r searchResult) Match {
	return Match{
		TMDBID:       r.ID,
		Title:        r.Title,
		Year:         releaseYear(r.ReleaseDate),
		Overview:     r.Overview,
		PosterPath:   r.PosterPath,
		BackdropPath: r.BackdropPath,
		VoteAverage:  r.VoteAverage,
	}
}

// toMatches converts a page of results, skipping any without an id — a result
// nothing can link to is not a card.
func toMatches(results []searchResult) []Match {
	// Non-nil so an empty page marshals as [] rather than null; the UI iterates it.
	out := make([]Match, 0, len(results))
	for _, r := range results {
		if r.ID == 0 {
			continue
		}
		out = append(out, toMatch(r))
	}
	return out
}

// collapseSeparator undoes the library's colon substitution the only safe way:
// by removing " - " entirely rather than restoring a colon. The substitution is
// not reliably a colon (it may be a real dash), and TMDB's search tolerates the
// missing punctuation. Replacing every "-" would turn "Spider-Man" into
// "Spider:Man" — and "Spider-Man - No Way Home" contains one of each.
func collapseSeparator(title string) string {
	return strings.ReplaceAll(title, separator, " ")
}

// releaseYear extracts the year from TMDB's "2014-11-05". Unreleased entries can
// carry an empty date, which yields 0 and therefore never matches a wanted year.
func releaseYear(releaseDate string) int {
	if len(releaseDate) < 4 {
		return 0
	}
	year, err := strconv.Atoi(releaseDate[:4])
	if err != nil {
		return 0
	}
	return year
}

// statusMessage pulls TMDB's human-readable explanation out of an error body so
// the operator sees "Invalid API key: You must be granted a valid key" rather
// than a bare 401. The read is capped because this is an untrusted response.
func statusMessage(r io.Reader) string {
	var s statusResponse
	if err := json.NewDecoder(io.LimitReader(r, 4<<10)).Decode(&s); err != nil || s.StatusMessage == "" {
		return "no message"
	}
	return s.StatusMessage
}

// scrubURL drops the request URL from a transport error. *url.Error stringifies
// the full URL, and ours carries api_key= in the query string — a DNS failure
// would otherwise print the API key into the logs. Unwrapping keeps the cause
// intact, so errors.Is against context.Canceled and friends still works.
func scrubURL(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) && uerr.Err != nil {
		return uerr.Err
	}
	return err
}

// SearchMovies returns every film TMDB knows for a free-text query, in TMDB's
// own popularity order.
//
// It is deliberately not SearchMovie. That one picks a single result and rejects
// a disagreeing year, because it serves a scan deciding what to write to a
// database, where a confident wrong match is invisible. This serves a human
// looking at posters — who can tell Avengers: Endgame from Avengers: Doomsday,
// and who was never shown the choice before.
//
// year narrows the query when non-zero and never filters the results afterwards.
//
// A film with no poster comes back like any other. The UI has a fallback for it,
// 15 of the 29 real library folders have no poster, and dropping data to make a
// grid look tidy is not this project's habit.
func (c *Client) SearchMovies(ctx context.Context, query string, year int) ([]Match, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("tmdb: search with empty query")
	}

	results, err := c.search(ctx, query, year)
	if err != nil {
		return nil, err
	}

	// The same fallback SearchMovie has, for the same reason: pasting a library
	// folder name — "Avengers - Infinity War" — into the search box is a real
	// thing to do while looking at the Library screen.
	if len(results) == 0 {
		if collapsed := collapseSeparator(query); collapsed != query {
			if results, err = c.search(ctx, collapsed, year); err != nil {
				return nil, fmt.Errorf("collapsed from %q: %w", query, err)
			}
		}
	}
	return toMatches(results), nil
}

// Movie fetches one film by TMDB id. An id TMDB does not have is ErrNotFound.
func (c *Client) Movie(ctx context.Context, id int) (*Details, error) {
	if id <= 0 {
		return nil, fmt.Errorf("tmdb movie %d: not a tmdb id", id)
	}

	var body detailsResponse
	if err := c.get(ctx, "/movie/"+strconv.Itoa(id), nil, fmt.Sprintf("movie %d", id), &body); err != nil {
		return nil, err
	}

	details := Details{
		Match:            toMatch(body.searchResult),
		Tagline:          body.Tagline,
		Runtime:          body.Runtime,
		Status:           body.Status,
		ReleaseDate:      body.ReleaseDate,
		OriginalLanguage: body.OriginalLanguage,
		Homepage:         body.Homepage,
		IMDBID:           body.IMDBID,
	}
	for _, genre := range body.Genres {
		details.Genres = append(details.Genres, genre.Name)
	}
	for _, studio := range body.ProductionCompanies {
		details.Studios = append(details.Studios, studio.Name)
	}
	for _, language := range body.SpokenLanguages {
		details.SpokenLanguages = append(details.SpokenLanguages, language.EnglishName)
	}
	return &details, nil
}

// Trending returns this week's trending films.
func (c *Client) Trending(ctx context.Context) ([]Match, error) {
	return c.list(ctx, "/trending/movie/week", "trending")
}

// Popular returns TMDB's popular films, first page.
func (c *Client) Popular(ctx context.Context) ([]Match, error) {
	return c.list(ctx, "/movie/popular", "popular")
}

// TopRated returns TMDB's highest-rated films, first page.
//
// It is the rail that does not move: trending turns over weekly and popular
// tracks whatever is being watched right now, so a home screen built from those
// two alone shows the same handful of new releases from both. Top rated is the
// back catalogue, and it is the only rail here that will still be worth reading
// in a year.
func (c *Client) TopRated(ctx context.Context) ([]Match, error) {
	return c.list(ctx, "/movie/top_rated", "top rated")
}

// NowPlaying returns films currently in cinemas.
//
// /movie/now_playing rather than /movie/upcoming, and the difference is whether
// a release can be acted on. This is a downloader: a rail of films that have not
// been released is a rail where every card leads to a search with nothing behind
// it, which is the failure the Discover screen is otherwise careful to avoid.
//
// The envelope carries a `dates` object beside `results` that nothing here
// reads; searchResponse ignores unknown fields, so it decodes as any other page.
func (c *Client) NowPlaying(ctx context.Context) ([]Match, error) {
	return c.list(ctx, "/movie/now_playing", "now playing")
}

func (c *Client) list(ctx context.Context, path, what string) ([]Match, error) {
	var body searchResponse
	if err := c.get(ctx, path, nil, what, &body); err != nil {
		return nil, err
	}
	return toMatches(body.Results), nil
}
