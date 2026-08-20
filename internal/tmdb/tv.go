package tmdb

// The television half of the client. Every method here has a movie counterpart
// in tmdb.go and keeps its contract exactly — the year rejection, the one " - "
// fallback, ErrUnauthorized/ErrNotFound, and the scrubbed URL. What differs is
// the payload, and that difference is the entire reason this file exists.
//
// TMDB names two fields differently for television: a show carries `name` and
// `first_air_date` where a film carries `title` and `release_date`. Nothing else
// about a search hit changes. So a TV payload decodes into tvResult and is
// mapped onto the movie-shaped searchResult immediately, before anything else
// in the package sees it. That keeps pick, toMatch and toMatches as single
// implementations — a show card and a film card cannot drift apart, because
// they are built by the same code — and callers get a tmdb.Match either way and
// never branch on which kind of thing they searched for.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// tvResult is the subset of a TMDB television object we read. It is the same
// information searchResult holds under two different names, which is why it
// exists only long enough to be converted.
type tvResult struct {
	ID           int     `json:"id"`
	Name         string  `json:"name"`
	FirstAirDate string  `json:"first_air_date"`
	Overview     string  `json:"overview"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	VoteAverage  float64 `json:"vote_average"`
}

// tvSearchResponse is the envelope /search/tv, /trending/tv/week and
// /tv/popular all share — the same shape as searchResponse, over TV objects.
type tvSearchResponse struct {
	Results []tvResult `json:"results"`
}

// asMovieShaped renames the two fields that differ and hands back the struct the
// rest of the package already works in. Year handling comes free: pick and
// toMatch both read ReleaseDate through releaseYear, so mapping first_air_date
// onto it means a show is disambiguated by its first-aired year with no second
// implementation of the rule.
func (r tvResult) asMovieShaped() searchResult {
	return searchResult{
		ID:           r.ID,
		Title:        r.Name,
		ReleaseDate:  r.FirstAirDate,
		Overview:     r.Overview,
		PosterPath:   r.PosterPath,
		BackdropPath: r.BackdropPath,
		VoteAverage:  r.VoteAverage,
	}
}

// asMovieShapedAll converts a whole page.
func asMovieShapedAll(results []tvResult) []searchResult {
	out := make([]searchResult, 0, len(results))
	for _, r := range results {
		out = append(out, r.asMovieShaped())
	}
	return out
}

// SearchShow looks up one show by its folder title and first-aired year — the
// exact counterpart of SearchMovie, including both of its subtleties.
//
// A nil Match with a nil error means "TMDB does not know this show, or not at
// this year", the normal outcome a caller records as tmdb_id = NULL. A
// disagreeing year is treated as no match at all: television has more remakes
// and more same-named national adaptations than film does — TMDB lists four
// distinct shows called "The Office" — so a confident wrong match is if
// anything more likely here than it is for a film.
//
// The year compared is first_air_date's. A long-running show is anchored on the
// year it started, which is what a library folder's "(2008)" means as well.
//
// The raw folder title is the first and usually only query, and only a
// genuinely empty result triggers the one fallback with " - " collapsed to a
// space. See docs/decisions.md D9 and the title-parsing trap in CLAUDE.md: the
// library writes " - " where a title has a colon, but "Spider-Man" and "X-Men"
// have real hyphens, so undoing that substitution mechanically corrupts them.
// Television has the same shape of name — "Star Wars - The Clone Wars" is a
// substitution, "Marvel's Agents of S.H.I.E.L.D." is not — so it gets the same
// treatment rather than a rule of its own.
func (c *Client) SearchShow(ctx context.Context, title string, year int) (*Match, error) {
	if strings.TrimSpace(title) == "" {
		return nil, errors.New("tmdb: search with empty title")
	}

	results, err := c.searchTV(ctx, title, year)
	if err != nil {
		return nil, err
	}

	// Zero results — not "results we rejected" — is the only trigger, for the
	// reason SearchMovie gives: a result set that exists but disagrees on the
	// year means TMDB knows the title and this is not it.
	if len(results) == 0 {
		if collapsed := collapseSeparator(title); collapsed != title {
			results, err = c.searchTV(ctx, collapsed, year)
			if err != nil {
				return nil, fmt.Errorf("collapsed from %q: %w", title, err)
			}
		}
	}

	return pick(results, year), nil
}

// SearchShows returns every show TMDB knows for a free-text query, in TMDB's own
// popularity order — the counterpart of SearchMovies, and deliberately not
// SearchShow.
//
// That one picks a single result and rejects a disagreeing year because it
// serves a scan writing to a database, where a confident wrong match is
// invisible. This serves a human looking at posters, who can tell the 2005
// Office from the 2001 one.
//
// year narrows the query when non-zero and never filters the results afterwards.
func (c *Client) SearchShows(ctx context.Context, query string, year int) ([]Match, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("tmdb: search with empty query")
	}

	results, err := c.searchTV(ctx, query, year)
	if err != nil {
		return nil, err
	}

	// The same fallback SearchShow has, for the same reason SearchMovies keeps
	// one: pasting a library folder name into the search box is a real thing to
	// do while looking at the Library screen.
	if len(results) == 0 {
		if collapsed := collapseSeparator(query); collapsed != query {
			if results, err = c.searchTV(ctx, collapsed, year); err != nil {
				return nil, fmt.Errorf("collapsed from %q: %w", query, err)
			}
		}
	}
	return toMatches(results), nil
}

// searchTV performs exactly one /search/tv request. It never retries, for the
// reason search does not: TMDB is not flaky enough to justify hammering it
// during a synchronous scan.
//
// The year parameter is first_air_date_year, not primary_release_year — TMDB
// rejects nothing when given the wrong one, it simply ignores it, so a
// copy-paste from the movie path would silently stop narrowing anything.
func (c *Client) searchTV(ctx context.Context, query string, year int) ([]searchResult, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("include_adult", "false")
	if year > 0 {
		params.Set("first_air_date_year", strconv.Itoa(year))
	}

	var body tvSearchResponse
	if err := c.get(ctx, "/search/tv", params, fmt.Sprintf("search show %q", query), &body); err != nil {
		return nil, err
	}
	return asMovieShapedAll(body.Results), nil
}

// tvDetailsResponse is /tv/{id}. It embeds tvResult because TMDB repeats those
// fields verbatim there, exactly as detailsResponse embeds searchResult.
//
// The film fields that have no television equivalent are simply absent rather
// than mapped to something close: there is no runtime (episode_run_time is a
// list of per-episode lengths), no release_date, and no imdb_id — TMDB serves a
// show's IMDB id from /tv/{id}/external_ids, a second request nothing needs yet.
type tvDetailsResponse struct {
	tvResult

	Tagline          string `json:"tagline"`
	Status           string `json:"status"`
	Homepage         string `json:"homepage"`
	OriginalLanguage string `json:"original_language"`
	LastAirDate      string `json:"last_air_date"`
	NumberOfSeasons  int    `json:"number_of_seasons"`
	NumberOfEpisodes int    `json:"number_of_episodes"`
	EpisodeRunTime   []int  `json:"episode_run_time"`

	Genres              []struct{ Name string } `json:"genres"`
	ProductionCompanies []struct{ Name string } `json:"production_companies"`
	SpokenLanguages     []struct {
		EnglishName string `json:"english_name"`
	} `json:"spoken_languages"`
}

// Show fetches one television show by TMDB id — the counterpart of Movie. An id
// TMDB does not have is ErrNotFound, the same sentinel Movie returns, so a
// caller that already answers 404 on it needs no new branch.
//
// A show id and a film id are different namespaces: 1396 is Breaking Bad here
// and a Hong Kong action film on /movie/1396. Nothing in this package can tell
// them apart, and nothing should try — the caller knows which kind it asked for
// because it chose the method.
func (c *Client) Show(ctx context.Context, id int) (*Details, error) {
	if id <= 0 {
		return nil, fmt.Errorf("tmdb show %d: not a tmdb id", id)
	}

	var body tvDetailsResponse
	if err := c.get(ctx, "/tv/"+strconv.Itoa(id), nil, fmt.Sprintf("show %d", id), &body); err != nil {
		return nil, err
	}

	details := Details{
		// The same conversion the search path uses, so a card and this page
		// cannot disagree about a title or a year.
		Match:            toMatch(body.asMovieShaped()),
		Tagline:          body.Tagline,
		Status:           body.Status,
		OriginalLanguage: body.OriginalLanguage,
		Homepage:         body.Homepage,
		FirstAirDate:     body.FirstAirDate,
		LastAirDate:      body.LastAirDate,
		NumberOfSeasons:  body.NumberOfSeasons,
		NumberOfEpisodes: body.NumberOfEpisodes,
	}
	// episode_run_time is a list because a show's episode length varies, and it
	// is empty for plenty of shows. The first entry is what TMDB's own pages
	// show; averaging the list would invent a number TMDB never states.
	if len(body.EpisodeRunTime) > 0 {
		details.EpisodeRuntime = body.EpisodeRunTime[0]
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
