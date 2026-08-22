package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DulsaraNethmin/curator/internal/jellyfin"
	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/tmdb"
)

// Browser is the TMDB catalogue as the browsing screens see it.
//
// It is a SECOND interface over the same *tmdb.Client rather than four more
// methods on Matcher. Matcher is phase 1's scan-time lookup, and widening it
// would force every phase 1 fake to implement four methods it never calls, for a
// feature it does not test. A nil Browser is a supported state, exactly like a
// nil Matcher and a nil Dispatcher.
type Browser interface {
	SearchMovies(ctx context.Context, query string, year int) ([]tmdb.Match, error)
	Movie(ctx context.Context, id int) (*tmdb.Details, error)
	Trending(ctx context.Context) ([]tmdb.Match, error)
	Popular(ctx context.Context) ([]tmdb.Match, error)
	TopRated(ctx context.Context) ([]tmdb.Match, error)
	NowPlaying(ctx context.Context) ([]tmdb.Match, error)

	// Television's six, and they are SIX MORE METHODS rather than a media type
	// on the six above. TMDB has two endpoints per question — /search/tv beside
	// /search/movie, /tv/{id} beside /movie/{id} — and one *tmdb.Client
	// implements all twelve, so the seam that would gain a branch here is the
	// only place the branch would not already exist. It also means a fake that
	// answers films cannot silently answer shows with them.
	SearchShows(ctx context.Context, query string, year int) ([]tmdb.Match, error)
	Show(ctx context.Context, id int) (*tmdb.Details, error)
	TrendingShows(ctx context.Context) ([]tmdb.Match, error)
	PopularShows(ctx context.Context) ([]tmdb.Match, error)
	TopRatedShows(ctx context.Context) ([]tmdb.Match, error)
	OnTheAir(ctx context.Context) ([]tmdb.Match, error)

	// The genre rails, and they take an id rather than being a method each —
	// which is the opposite of the rule above, on purpose. The pairs above are
	// two DIFFERENT TMDB endpoints with different paths; these two are one
	// endpoint per media type with a parameter, and fourteen methods named
	// after genres would be a table pretending to be an interface. The split by
	// media type stays, because /discover/movie and /discover/tv are still two
	// paths and their genre ids are two different vocabularies (28 is Action
	// for a film and nothing at all for a show).
	//
	// `what` is the label an error is phrased with — "action films" — and never
	// reaches TMDB.
	MoviesByGenre(ctx context.Context, genreID int, what string) ([]tmdb.Match, error)
	ShowsByGenre(ctx context.Context, genreID int, what string) ([]tmdb.Match, error)
}

// WithBrowser attaches the catalogue and returns the server.
func (s *Server) WithBrowser(b Browser) *Server {
	s.browser = b
	return s
}

// MediaServer is the Open in Jellyfin link, and it is deliberately one method.
//
// An interface here rather than *jellyfin.Client for the reason Browser is one:
// the handler's behaviour when the lookup misses, when the key is revoked and
// when the server is off is the interesting part, and all three are awkward to
// produce from a real client. It is the fourth optional dependency with this
// shape — a nil one means no link at all, which is the rule the rest of the UI
// already follows for an unconfigured integration.
type MediaServer interface {
	// FindMovie answers the item for a TMDB id, or jellyfin.ErrNotFound.
	FindMovie(ctx context.Context, tmdbID, year int) (jellyfin.Item, error)

	// FindSeries is the same question for a show, and it is a second method
	// rather than a media type on the first because IncludeItemTypes is the
	// only thing separating the two id spaces on a Jellyfin server: TMDB
	// numbers films and shows independently, so asking for a Movie with a tv
	// id can LAND on an unrelated film rather than merely miss (T92).
	FindSeries(ctx context.Context, tmdbID, year int) (jellyfin.Item, error)
}

// WithJellyfin attaches the media server the movie screen can link into, and
// the base URL a BROWSER reaches it at — which is not the one curator uses when
// the two are in Docker together (docs/phase-8.md, jellyfin_public_url).
func (s *Server) WithJellyfin(m MediaServer, publicURL string) *Server {
	s.jellyfin = m
	s.jellyfinURL = publicURL
	return s
}

// RegisterBrowse mounts the TMDB-backed routes.
//
// Everything TMDB-backed lives under /api/tmdb/, and the prefix is the rule: if
// it is under /api/tmdb/, it goes dark without a key. /api/movies stays the
// library — what curator actually has — and the two can never be confused.
// Discover and search take ?media=tv rather than growing a route each, because
// they are one screen asking one question about a different kind of thing — and
// the parameter is what the UI's Movies|Shows toggle sets. A show's DETAIL page
// is its own route, mirroring /api/tmdb/movies/{id}: the two id spaces overlap,
// and a single route disambiguating a bare number by a query parameter is how a
// film ends up rendered as a show.
func (s *Server) RegisterBrowse(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tmdb/discover", s.handleDiscover)
	mux.HandleFunc("GET /api/tmdb/search", s.handleTMDBSearch)
	mux.HandleFunc("GET /api/tmdb/movies/{id}", s.handleTMDBMovie)
	mux.HandleFunc("GET /api/tmdb/shows/{id}", s.handleTMDBShow)
}

// parseMedia reads ?media=. Absent or empty is a film, because every request
// made before phase 11 was one and a client that has not been told about
// television must keep meaning what it meant — the same default
// indexer.Query.Media takes, for the same reason.
//
// Anything else is a 400 rather than a silent fallback to films: a typo that
// returned films for ?media=tvshow would be a search that quietly answered a
// different question.
func parseMedia(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return store.MediaTypeMovie, nil
	case store.MediaTypeMovie:
		return store.MediaTypeMovie, nil
	case store.MediaTypeTV:
		return store.MediaTypeTV, nil
	default:
		return "", fmt.Errorf("media %q: not a media type (%q or %q)",
			raw, store.MediaTypeMovie, store.MediaTypeTV)
	}
}

// media reads the selector and answers the request itself when it cannot be
// served: a 400 for a media type that does not exist, and a 503 naming
// LIBRARY_TV for television on an install that has not turned it on.
//
// The television gate runs BEFORE the browser check on every route that has
// one, and the order is deliberate. Both are 503s naming a variable, and this
// one is the more fundamental fact: television being off is not something a
// TMDB key changes, and pointing somebody at TMDB_API_KEY when the Shows tab
// does not exist on their install sends them to fix the wrong line.
func (s *Server) media(w http.ResponseWriter, r *http.Request) (string, bool) {
	mediaType, err := parseMedia(r.URL.Query().Get("media"))
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return "", false
	}
	if mediaType == store.MediaTypeTV && s.televisionOff(w) {
		return "", false
	}
	return mediaType, true
}

// libraryStateBody is what curator already has for a film, or null.
//
// state is a store.Status* value or "downloading" — not a vocabulary invented
// for the UI. "downloading" comes from the downloads table rather than
// movies.status, because store.StatusDownloading is never written; see
// store.LibraryByTMDBID.
type libraryStateBody struct {
	MovieID     int64  `json:"movie_id"`
	State       string `json:"state"`
	LibraryPath string `json:"library_path,omitempty"`
}

// movieCard is one film as a poster grid shows it.
type movieCard struct {
	TMDBID       int               `json:"tmdb_id"`
	Title        string            `json:"title"`
	Year         int               `json:"year"`
	Overview     string            `json:"overview"`
	PosterPath   string            `json:"poster_path"`
	BackdropPath string            `json:"backdrop_path"`
	VoteAverage  float64           `json:"vote_average"`
	Library      *libraryStateBody `json:"library"`
}

// movieDetailBody is a card plus everything the movie screen shows.
type movieDetailBody struct {
	movieCard

	Tagline          string   `json:"tagline"`
	Runtime          int      `json:"runtime"`
	Genres           []string `json:"genres"`
	Status           string   `json:"status"`
	ReleaseDate      string   `json:"release_date"`
	OriginalLanguage string   `json:"original_language"`
	SpokenLanguages  []string `json:"spoken_languages"`
	Studios          []string `json:"studios"`
	Homepage         string   `json:"homepage"`
	IMDBID           string   `json:"imdb_id"`

	// JellyfinURL opens this film in Jellyfin, and it is ABSENT rather than
	// empty in the two states that mean "there is no link": no Jellyfin
	// configured, and a film curator does not have on disk. The UI draws
	// nothing for an absent one — not a disabled button and not a tooltip
	// explaining what you are missing, which is the rule the rest of the
	// screens already follow for an unconfigured integration.
	//
	// It is a deep link to the item when the lookup found one and a link to a
	// Jellyfin SEARCH for the title when it did not, and the UI is not told
	// which. Both land somewhere useful, the difference is not one a user can
	// act on, and a "we could not find it in Jellyfin" caption would be curator
	// explaining its own plumbing.
	JellyfinURL string `json:"jellyfin_url,omitempty"`
}

// showDetailBody is a card plus everything the show screen shows.
//
// It shares movieCard with the film — a poster, a title, a year and what
// curator already has are the same facts about both — and diverges below it,
// where the two genuinely differ. Runtime and release_date are absent rather
// than zero: TMDB has no runtime for a series (episode_run_time is a list of
// per-episode lengths, carried here as episode_runtime), and first_air_date is
// the date that exists.
//
// imdb_id used to be absent for a third reason — a show's cost a second TMDB
// request and nothing read it — and since T97 both halves of that are false.
// append_to_response brings it back on the request already being made, and the
// show screen sends it to /api/search because EZTV is keyed by it and has no
// keyword surface at all.
type showDetailBody struct {
	movieCard

	Tagline string   `json:"tagline"`
	Genres  []string `json:"genres"`
	// Status is a show's vocabulary here — "Returning Series", "Ended",
	// "Canceled", "In Production" — where a film's is "Released".
	Status           string   `json:"status"`
	OriginalLanguage string   `json:"original_language"`
	SpokenLanguages  []string `json:"spoken_languages"`
	Studios          []string `json:"studios"`
	Homepage         string   `json:"homepage"`

	// IMDBID is "tt14688458", exactly as TMDB spells it. The screen hands it
	// straight to /api/search, which is the only thing that reads it — EZTV
	// strips the prefix at its own boundary. Empty when TMDB has no id, which
	// is a normal state and simply means EZTV declines that search.
	IMDBID string `json:"imdb_id"`

	FirstAirDate string `json:"first_air_date"`
	// LastAirDate is the most recently aired episode and NOT a promise that the
	// show has finished; status is where that is said.
	LastAirDate string `json:"last_air_date"`
	// Seasons is a COUNT and SeasonList is the list, and they are both here
	// because they answer different questions. Silo reports 4 seasons and lists
	// one of them with zero episodes, so a picker built from the count offers a
	// search with nothing behind it — which is what the show screen did until
	// T98. The count is what the facts panel shows.
	Seasons        int          `json:"seasons"`
	SeasonList     []seasonBody `json:"season_list"`
	Episodes       int          `json:"episodes"`
	EpisodeRuntime int          `json:"episode_runtime"` // minutes, TMDB's episode_run_time[0]

	// Absent rather than empty in the two states that mean "there is no link",
	// exactly as on the film's page: no Jellyfin configured, and a show curator
	// does not have on disk.
	JellyfinURL string `json:"jellyfin_url,omitempty"`
}

// seasonBody is one season, and episode_count is the whole reason it exists —
// it is the number `seasons` above cannot give.
//
// Every season TMDB lists is sent, unfiltered, including season_number 0
// (Specials) and seasons with no episodes yet. The API reports what TMDB says;
// which of them a person may click is the screen's decision, and it is not the
// same decision for both — an unaired season has nothing to search for, while
// Specials cannot be ASKED for at all, because ?season=0 already means "no
// season constraint" here (see parseSeason).
type seasonBody struct {
	Number int `json:"number"`
	// Name is TMDB's own label — "Season 1", "Specials" — rather than something
	// built from Number, because Specials is not "Season 0" to anybody reading
	// it.
	Name         string `json:"name"`
	EpisodeCount int    `json:"episode_count"`
	// AirDate is "2023-05-04", and empty for a season TMDB has no date for.
	AirDate string `json:"air_date,omitempty"`
}

// discoverRow is one rail on the Discover screen.
//
// OK and Error are the indexers[] shape, deliberately. One rail failing is a
// success carrying the other, and a failing source that is invisible is minter's
// 200-carrying-a-failure wearing a third hat.
type discoverRow struct {
	ID      string      `json:"id"`
	Title   string      `json:"title"`
	OK      bool        `json:"ok"`
	Error   string      `json:"error,omitempty"`
	Results []movieCard `json:"results"`
}

// discoverRail is one rail's identity and where its cards come from.
//
// The id, the title and the call are ONE STRUCT rather than the parallel slices
// this was written as, and that is the whole reason the type exists. Two slices
// indexed in lockstep are correct while there are two rails and a transposition
// waiting to happen once there are four: nothing about `fetch[i]` says it
// belongs to `rows[i]`, so a rail inserted in one list and appended to the other
// draws top-rated films under the heading "Trending this week" and passes every
// test in this package, because both lists are still the right length.
type discoverRail struct {
	id    string
	title string
	fetch func(context.Context) ([]tmdb.Match, error)
}

// The genre rails, as ids TMDB understands and the words a person reads.
//
// **Two tables and not one, because TMDB keeps two genre vocabularies.** A film
// is Action (28) and Science Fiction (878); a show is "Action & Adventure"
// (10759) and "Sci-Fi & Fantasy" (10765), and those ids mean nothing on the
// other endpoint. Crucially /discover does not FAIL on a foreign id — it returns
// a plausible page of the wrong thing — so a single shared table would be a bug
// that looks exactly like a working screen. They are deliberately not factored
// together.
//
// Eight each, and the count is a judgement rather than a limit. Every rail is a
// TMDB request on a cold cache, and past about a dozen the screen stops being a
// place to browse and becomes a place to scroll; these are the eight a general
// library actually gets asked for.
var movieGenres = []struct {
	id    int
	title string
}{
	{28, "Action"},
	{35, "Comedy"},
	{878, "Science fiction"},
	{53, "Thriller"},
	{27, "Horror"},
	{16, "Animation"},
	{18, "Drama"},
	{99, "Documentary"},
}

var showGenres = []struct {
	id    int
	title string
}{
	{10759, "Action & adventure"},
	{35, "Comedy"},
	{10765, "Sci-fi & fantasy"},
	{80, "Crime"},
	{9648, "Mystery"},
	{16, "Animation"},
	{18, "Drama"},
	{99, "Documentary"},
}

// discoverRails is the home screen, in the order it is drawn. Callers must have
// checked s.browser is non-nil.
//
// **The ids are shared across media types and the titles are not, and the split
// is deliberate.** An id names a SLOT — the UI keys its rails on it and never
// learns that television exists — while the title says what that slot holds
// here. Most mean the same thing on both sides, so they read the same;
// `in_release` does not, because "in cinemas" and "on the air" are two different
// facts and not one fact said twice. That is the rule the original comment was
// reaching for: the toggle already says Movies or Shows, so a title must not
// repeat it, but it must still be true.
//
// The order is a claim about what a person came for. Trending leads because it
// is the reason to open this screen at all; top rated sits third because it is
// the only rail up there that does not turn over weekly. The genres follow in a
// fixed order rather than a personalised one — curator has no idea what anybody
// likes, and inventing a ranking from nothing would be a lie told in a layout.
func (s *Server) discoverRails(mediaType string) []discoverRail {
	if mediaType == store.MediaTypeTV {
		rails := []discoverRail{
			{"trending", "Trending this week", s.browser.TrendingShows},
			{"popular", "Popular", s.browser.PopularShows},
			{"top_rated", "Top rated", s.browser.TopRatedShows},
			{"in_release", "On the air this week", s.browser.OnTheAir},
		}
		for _, g := range showGenres {
			rails = append(rails, s.genreRail(g.id, g.title, "shows", s.browser.ShowsByGenre))
		}
		return rails
	}

	rails := []discoverRail{
		{"trending", "Trending this week", s.browser.Trending},
		{"popular", "Popular", s.browser.Popular},
		{"top_rated", "Top rated", s.browser.TopRated},
		{"in_release", "In cinemas now", s.browser.NowPlaying},
	}
	for _, g := range movieGenres {
		rails = append(rails, s.genreRail(g.id, g.title, "films", s.browser.MoviesByGenre))
	}
	return rails
}

// genreRail closes one genre id into the no-argument shape every other rail has,
// so handleDiscover keeps one loop over one kind of thing.
//
// The id is in the rail id — "genre_28" — rather than the title slugged, because
// the title is prose that may be reworded and the id is the thing that actually
// identifies the row. The UI keys React on it and nothing else reads it.
func (s *Server) genreRail(
	id int,
	title, noun string,
	by func(context.Context, int, string) ([]tmdb.Match, error),
) discoverRail {
	// what is the error's words, not TMDB's: "action films could not be loaded".
	what := strings.ToLower(title) + " " + noun
	return discoverRail{
		id:    fmt.Sprintf("genre_%d", id),
		title: title,
		fetch: func(ctx context.Context) ([]tmdb.Match, error) { return by(ctx, id, what) },
	}
}

// railTTL is how long a rail's cards are reused.
//
// Fifteen minutes against lists TMDB itself recomputes daily (top rated, the
// genres), weekly (trending) or on a cinema's schedule (now playing) — so this
// is not a staleness trade at all, it is a floor on how often curator asks a
// question whose answer has not changed. Short enough that a genuinely new
// trending list appears within the quarter hour; long enough that opening
// Discover, pressing Shows, pressing Movies and going back is one round of
// requests instead of four.
const railTTL = 15 * time.Minute

// discoverCache holds the TMDB half of each rail — the cards, and never the
// library state merged onto them.
//
// **That split is the whole design.** A card carries `library: {...}` saying
// whether curator already has the film, and that changes the instant somebody
// presses Download; caching the finished response would leave a poster without
// its badge for fifteen minutes and make the button look broken. So what is kept
// is the [] tmdb.Match, and LibraryByTMDBID is re-read and re-merged on every
// request, which it already was — it is one indexed SQLite read.
//
// **Failures are not cached.** A rail that failed is a rail to try again, and
// fifteen minutes of a remembered error is how a transient 502 becomes a screen
// somebody reports as broken. The cost of that choice is that a hard TMDB outage
// re-asks every fifteen seconds somebody reloads; the log line in handleDiscover
// is what makes that visible.
//
// The zero value is usable: the map is made on first write.
type discoverCache struct {
	mu      sync.Mutex
	entries map[string]railEntry
	// now is time.Now except in tests, which need to age an entry without
	// sleeping for a quarter of an hour.
	now func() time.Time
}

type railEntry struct {
	matches []tmdb.Match
	at      time.Time
}

func (c *discoverCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// get returns the cached cards for a key and whether they are still fresh.
func (c *discoverCache) get(key string) ([]tmdb.Match, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || c.clock().Sub(entry.at) >= railTTL {
		return nil, false
	}
	return entry.matches, true
}

func (c *discoverCache) put(key string, matches []tmdb.Match) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]railEntry{}
	}
	c.entries[key] = railEntry{matches: matches, at: c.clock()}
}

// discoverSet is everything a discover request needs before a single rail is
// fetched: which catalogue was asked for, the rails to draw, and the library
// snapshot their cards are badged against.
//
// It exists because there are two routes onto this screen — one response and one
// stream — and every fact they disagree about would be a bug nobody could see
// from either one alone. The rail table is built once, here, and both routes
// walk it.
type discoverSet struct {
	media   string
	rails   []discoverRail
	library map[int64]store.LibraryState
}

// beginDiscover runs the gates both discover routes share and reads the library.
// It answers the request itself and returns false when the screen cannot be
// drawn at all.
//
// **The library read happens BEFORE the fan-out, and it has to.** A stream has
// written its first bytes by the time the last rail lands, so a store failure
// discovered then could not be a status code any more — it would be a 200 that
// stopped halfway. Reading it first keeps the one thing here that is a hard 5xx
// (D41's shape: the rails are soft failures, the database is not) able to answer
// as one. The cost is a snapshot taken a second earlier than it used to be,
// against a fifteen-minute rail cache; the badge it carries is still re-read on
// every request, which is the guarantee T102 actually made.
func (s *Server) beginDiscover(w http.ResponseWriter, r *http.Request) (discoverSet, bool) {
	mediaType, ok := s.media(w, r)
	if !ok {
		return discoverSet{}, false
	}
	if s.browser == nil {
		s.failTMDB(w, errTMDBUnconfigured)
		return discoverSet{}, false
	}

	// Scoped to the media type these cards are, because TMDB's two id
	// sequences overlap: one map keyed on a bare number would badge Severance's
	// poster with the film holding movie id 95396 (D48).
	library, err := s.store.LibraryByTMDBID(r.Context(), mediaType)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return discoverSet{}, false
	}

	return discoverSet{media: mediaType, rails: s.discoverRails(mediaType), library: library}, true
}

// eachRail fetches every rail concurrently and calls emit once per rail, **in
// the order the rails resolve** — which is not the order they are drawn in, and
// is why emit is handed the index rather than left to append.
//
// That signature is T102's lesson restated for a second caller. Two slices
// indexed in lockstep drew top-rated films under "Trending this week" and passed
// every test, because both were still the right length; an emit that appended
// would do the same thing here the first time a cached rail overtook a fetched
// one, which is every request with a warm cache and one cold entry.
//
// emit is called from this goroutine and never concurrently, so a caller may
// write to the response from inside it without a lock.
func (s *Server) eachRail(ctx context.Context, set discoverSet, emit func(int, discoverRow)) {
	type finished struct {
		index int
		row   discoverRow
	}

	// Buffered by the rail count, so a fetch never blocks on the writer. An
	// unbuffered channel would hold a TMDB goroutine open behind a slow client
	// for as long as it took that client to read, and there is one send per rail
	// so this can never fill.
	done := make(chan finished, len(set.rails))

	// Concurrently, and at twelve rails it is the difference between a screen
	// and a stall: these are twelve sequential ten-second timeouts otherwise,
	// which is two minutes of home page. The fan-out is bounded by the table
	// above and TMDB is one host at ~50 requests a second, so twelve in flight
	// is not something to rate-limit against — and on a warm cache it is zero.
	var wg sync.WaitGroup
	for i := range set.rails {
		rail := set.rails[i]
		// Keyed by media type as well as rail id: "genre_16" is Animation in
		// both vocabularies and two entirely different pages, and one shared key
		// would serve cartoons to whichever tab asked second.
		key := set.media + "/" + rail.id
		if cached, ok := s.rails.get(key); ok {
			done <- finished{i, s.railRow(set, rail, cached, nil)}
			continue
		}
		wg.Add(1)
		go func(i int, rail discoverRail, key string) {
			defer wg.Done()
			matches, err := rail.fetch(ctx)
			if err == nil {
				s.rails.put(key, matches)
			}
			done <- finished{i, s.railRow(set, rail, matches, err)}
		}(i, rail, key)
	}
	go func() {
		wg.Wait()
		close(done)
	}()

	for rail := range done {
		emit(rail.index, rail.row)
	}
}

// railRow turns one rail's answer into the row a screen draws.
func (s *Server) railRow(set discoverSet, rail discoverRail, matches []tmdb.Match, err error) discoverRow {
	row := discoverRow{ID: rail.id, Title: rail.title}
	if err != nil {
		// A failed rail is named, never hidden, and never fatal.
		//
		// The sentence is the same one failTMDB writes, because this is the
		// same failure — it is only the envelope that differs, and a reader
		// should not learn two vocabularies for one dependency. What was here
		// before was `err.Error()` under a comment calling the chain "exactly
		// what the operator needs", which is true and is the reason it now goes
		// to the log instead of onto the home screen (D41).
		//
		// Logged here rather than through logCause: this envelope is 200, so a
		// gate on the status would drop the one 5xx-shaped failure in the
		// response. Warn rather than Error, because the page still draws and
		// /api/logs is a screen where red means something is broken.
		s.log.Warn("discover: a rail failed and is drawn empty", "row", row.ID, "err", err)
		row.OK = false
		row.Error = tmdbSentence(err)
		row.Results = []movieCard{}
		return row
	}
	row.OK = true
	row.Results = toCards(matches, set.library)
	return row
}

func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	set, ok := s.beginDiscover(w, r)
	if !ok {
		return
	}

	rows := make([]discoverRow, len(set.rails))
	s.eachRail(r.Context(), set, func(i int, row discoverRow) { rows[i] = row })

	s.respond(w, http.StatusOK, map[string]any{"media": set.media, "rows": rows})
}

func (s *Server) handleTMDBSearch(w http.ResponseWriter, r *http.Request) {
	mediaType, ok := s.media(w, r)
	if !ok {
		return
	}
	if s.browser == nil {
		s.failTMDB(w, errTMDBUnconfigured)
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if query == "" {
		s.fail(w, http.StatusBadRequest, errors.New("query is required"))
		return
	}
	year, err := parseYear(r.URL.Query().Get("year"))
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}

	search := s.browser.SearchMovies
	if mediaType == store.MediaTypeTV {
		search = s.browser.SearchShows
	}
	matches, err := search(r.Context(), query, year)
	if err != nil {
		s.failTMDB(w, err)
		return
	}
	library, err := s.store.LibraryByTMDBID(r.Context(), mediaType)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	s.respond(w, http.StatusOK, map[string]any{
		"query":   query,
		"year":    year,
		"media":   mediaType,
		"results": toCards(matches, library),
	})
}

func (s *Server) handleTMDBMovie(w http.ResponseWriter, r *http.Request) {
	if s.browser == nil {
		s.failTMDB(w, errTMDBUnconfigured)
		return
	}

	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		s.fail(w, http.StatusBadRequest, errors.New("id must be a tmdb id"))
		return
	}

	details, err := s.browser.Movie(r.Context(), id)
	if err != nil {
		s.failTMDB(w, err)
		return
	}
	library, err := s.store.LibraryByTMDBID(r.Context(), store.MediaTypeMovie)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	body := movieDetailBody{
		movieCard:        toCard(details.Match, library),
		Tagline:          details.Tagline,
		Runtime:          details.Runtime,
		Genres:           details.Genres,
		Status:           details.Status,
		ReleaseDate:      details.ReleaseDate,
		OriginalLanguage: details.OriginalLanguage,
		SpokenLanguages:  details.SpokenLanguages,
		Studios:          details.Studios,
		Homepage:         details.Homepage,
		IMDBID:           details.IMDBID,
	}
	// [] and never null: the UI iterates all three.
	if body.Genres == nil {
		body.Genres = []string{}
	}
	if body.Studios == nil {
		body.Studios = []string{}
	}
	if body.SpokenLanguages == nil {
		body.SpokenLanguages = []string{}
	}

	body.JellyfinURL = s.jellyfinLink(r.Context(), store.MediaTypeMovie, body.movieCard)

	s.respond(w, http.StatusOK, body)
}

// handleTMDBShow is handleTMDBMovie for a show, and the fields it does NOT
// carry are the reason it is a second handler rather than a branch.
//
// tmdb.Details serves both kinds with the film-only fields zero for a show, so
// a shared body would put `"runtime": 0` and `"release_date": ""` on every show
// — a screen reading them would draw "0 min" for a series that ran five years.
// The television fields (first air date, last air date, seasons, episodes) have
// no film equivalent to be squeezed into either.
func (s *Server) handleTMDBShow(w http.ResponseWriter, r *http.Request) {
	if s.televisionOff(w) {
		return
	}
	if s.browser == nil {
		s.failTMDB(w, errTMDBUnconfigured)
		return
	}

	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		s.fail(w, http.StatusBadRequest, errors.New("id must be a tmdb id"))
		return
	}

	details, err := s.browser.Show(r.Context(), id)
	if err != nil {
		s.failTMDB(w, err)
		return
	}
	library, err := s.store.LibraryByTMDBID(r.Context(), store.MediaTypeTV)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	body := showDetailBody{
		movieCard:        toCard(details.Match, library),
		Tagline:          details.Tagline,
		Genres:           details.Genres,
		Status:           details.Status,
		OriginalLanguage: details.OriginalLanguage,
		SpokenLanguages:  details.SpokenLanguages,
		Studios:          details.Studios,
		Homepage:         details.Homepage,
		IMDBID:           details.IMDBID,
		FirstAirDate:     details.FirstAirDate,
		LastAirDate:      details.LastAirDate,
		Seasons:          details.NumberOfSeasons,
		SeasonList:       toSeasons(details.Seasons),
		Episodes:         details.NumberOfEpisodes,
		EpisodeRuntime:   details.EpisodeRuntime,
	}
	// [] and never null: the UI iterates all three.
	if body.Genres == nil {
		body.Genres = []string{}
	}
	if body.Studios == nil {
		body.Studios = []string{}
	}
	if body.SpokenLanguages == nil {
		body.SpokenLanguages = []string{}
	}

	body.JellyfinURL = s.jellyfinLink(r.Context(), store.MediaTypeTV, body.movieCard)

	s.respond(w, http.StatusOK, body)
}

// toSeasons converts TMDB's season list for the wire, [] and never null
// because the UI iterates it.
func toSeasons(seasons []tmdb.Season) []seasonBody {
	out := make([]seasonBody, 0, len(seasons))
	for _, s := range seasons {
		out = append(out, seasonBody{
			Number:       s.Number,
			Name:         s.Name,
			EpisodeCount: s.EpisodeCount,
			AirDate:      s.AirDate,
		})
	}
	return out
}

func toCards(matches []tmdb.Match, library map[int64]store.LibraryState) []movieCard {
	cards := make([]movieCard, 0, len(matches)) // [] and never null
	for _, m := range matches {
		cards = append(cards, toCard(m, library))
	}
	return cards
}

func toCard(m tmdb.Match, library map[int64]store.LibraryState) movieCard {
	card := movieCard{
		TMDBID:       m.TMDBID,
		Title:        m.Title,
		Year:         m.Year,
		Overview:     m.Overview,
		PosterPath:   m.PosterPath,
		BackdropPath: m.BackdropPath,
		VoteAverage:  m.VoteAverage,
	}

	state, ok := library[int64(m.TMDBID)]
	if !ok {
		return card // curator has never heard of this film; library stays null
	}

	body := &libraryStateBody{MovieID: state.MovieID, State: state.Status}
	if state.Downloading {
		// The download is the truth here, not movies.status, which never says
		// 'downloading' — see store.LibraryByTMDBID.
		body.State = store.StatusDownloading
	}
	if state.LibraryPath != nil {
		body.LibraryPath = *state.LibraryPath
	}
	card.Library = body
	return card
}

// jellyfinLink is the Open in Jellyfin URL for one film, or "" for no link.
//
// **It can never fail the page.** Every outcome except "found it" produces the
// search link, which needs no network at all and cannot 404 — a revoked key, a
// Jellyfin that is switched off, a film it has not indexed yet, and a film it
// has never matched to TMDB all land on the same fallback. That is why this
// returns a string and not an error: there is no failure here for a caller to
// handle, only a better link and a worse one.
//
// **Only for a film curator has on disk.** Linking a film nobody owns into a
// media server that certainly does not have it is a link to a search for
// something that is not there. store.StatusImported is the test rather than
// library != nil, because a wanted or downloading film is in the database and
// not on the disk.
func (s *Server) jellyfinLink(ctx context.Context, mediaType string, card movieCard) string {
	tmdbID := int64(card.TMDBID)
	imported := card.Library != nil && card.Library.State == store.StatusImported
	return s.jellyfinLinkFor(ctx, mediaType, &tmdbID, card.Year, card.Title, imported)
}

// jellyfinLinkFor is jellyfinLink's body, reached from the catalogue page above
// and from a library row that has no catalogue entry at all ([D35]).
//
// **A nil tmdbID is not a failure to report.** It is a row TMDB never matched
// ([D6]), and it is D32's miss arriving one step earlier than a lookup that
// comes back empty — so it takes the same search link, without a lookup it has
// no id to make. That is the whole reason this split exists: the fallback was
// already written for a Jellyfin that has not matched the film, and a curator
// that has not matched it is the same situation seen from the other end.
//
// [D35]: ../../docs/decisions.md
// [D6]: ../../docs/decisions.md
func (s *Server) jellyfinLinkFor(ctx context.Context, mediaType string, tmdbID *int64, year int, title string, imported bool) string {
	if s.jellyfin == nil || strings.TrimSpace(s.jellyfinURL) == "" {
		return ""
	}
	if !imported {
		return ""
	}
	if tmdbID == nil {
		return jellyfin.WebSearchURL(s.jellyfinURL, title)
	}

	// The lookup carries its own deadline inside internal/jellyfin, because
	// this one is in front of a person waiting for a page rather than behind a
	// poller. Measured against the real 10.10.7 over a LAN: 5.5 ms.
	//
	// The media type picks the query, and it has to: FindMovie pins
	// IncludeItemTypes=Movie, so a show asked for through it misses every time
	// and — because ErrNotFound is deliberately not logged below — the
	// downgraded link would be permanent and invisible (T92).
	item, err := s.findOnMediaServer(ctx, mediaType, int(*tmdbID), year)
	if err == nil {
		return jellyfin.WebItemURL(s.jellyfinURL, item)
	}

	// A miss is ordinary — Jellyfin scans on its own schedule, so a film
	// imported a minute ago is legitimately not there yet — and it is not worth
	// a line. Anything else is the operator's business: a revoked key and a
	// Jellyfin that is off both silently downgrade the link, and "why is Open
	// in Jellyfin always a search" is otherwise a question with no evidence.
	if !errors.Is(err, jellyfin.ErrNotFound) {
		s.log.Warn("jellyfin: could not look this title up, so the link is a search instead",
			"tmdb_id", *tmdbID, "media_type", mediaType, "err", err)
	}
	return jellyfin.WebSearchURL(s.jellyfinURL, title)
}

// findOnMediaServer asks the media server for one title, by media type.
func (s *Server) findOnMediaServer(ctx context.Context, mediaType string, tmdbID, year int) (jellyfin.Item, error) {
	if mediaType == store.MediaTypeTV {
		return s.jellyfin.FindSeries(ctx, tmdbID, year)
	}
	return s.jellyfin.FindMovie(ctx, tmdbID, year)
}

// errTMDBUnconfigured is the no-key state, phrased the way ErrUnconfigured is
// for downloads: it names the variable, because that is the whole remedy.
var errTMDBUnconfigured = errors.New("tmdb is not configured: set TMDB_API_KEY")

// failTMDB maps a catalogue failure onto a status that says whose problem it is.
func (s *Server) failTMDB(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errTMDBUnconfigured):
		s.fail(w, http.StatusServiceUnavailable, err)
	case errors.Is(err, tmdb.ErrNotFound):
		// The id came from a URL a human can type.
		s.fail(w, http.StatusNotFound, err)
	default:
		// 502 for both, and NOT 503. In this codebase 503 means "you did not set
		// the variable", which would be a lie when it is set and simply wrong —
		// and it would send someone to check a variable that is already there.
		s.failCause(w, http.StatusBadGateway, tmdbSentence(err), err)
	}
}

// tmdbSentence is the one place a TMDB failure becomes words.
//
// One function rather than two branches because the catalogue answers this
// failure at two different envelopes — 502 for a whole request, and a named rail
// inside an otherwise fine 200 on the home screen — and a reader should not have
// to learn two vocabularies for one dependency being unwell.
//
// The distinction it carries is the one the status cannot: a rejected key is not
// an unset one, and 502 says neither. The chain that says `tmdb <what>: ` and
// quotes TMDB's own status_message is the operator's half, and it goes to the log.
func tmdbSentence(err error) string {
	if errors.Is(err, tmdb.ErrUnauthorized) {
		return "TMDB rejected curator's API key, so the catalogue cannot be read — the key is set, it is being refused"
	}
	// Everything else TMDB can do: a 500, a timeout, a body that is not the JSON
	// the client expects. One sentence covers them because there is one remedy,
	// and none of it is the reader's to fix.
	return "TMDB did not answer in a way curator understands, so the catalogue cannot be read just now — this is TMDB's end, not yours or curator's"
}
