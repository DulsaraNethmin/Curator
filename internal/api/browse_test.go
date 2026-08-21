package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/jellyfin"
	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/tmdb"
)

// fakeBrowser stands in for the TMDB client.
type fakeBrowser struct {
	search     []tmdb.Match
	details    *tmdb.Details
	trending   []tmdb.Match
	popular    []tmdb.Match
	topRated   []tmdb.Match
	nowPlaying []tmdb.Match

	searchErr     error
	detailsErr    error
	trendingErr   error
	popularErr    error
	topRatedErr   error
	nowPlayingErr error

	// Television's six answer from their OWN fields, and that is the point of
	// them. A fake that served the film rails to a TV request would let a
	// handler that forgot to switch endpoints pass every test here and then ask
	// TMDB's /search/movie for a show in production — which is the exact defect
	// D48's contamination sweep exists to prevent, one layer up.
	showSearch   []tmdb.Match
	showDetails  *tmdb.Details
	showTrending []tmdb.Match
	showPopular  []tmdb.Match
	showTopRated []tmdb.Match
	showOnTheAir []tmdb.Match

	showSearchErr   error
	showDetailsErr  error
	showTrendingErr error
	showPopularErr  error
	showTopRatedErr error
	showOnTheAirErr error

	// The genre rails, keyed by TMDB genre id so a test can prove a rail asked
	// for ITS OWN genre rather than merely for some genre. A nil map answers
	// nil with no error, which is a rail that is ok and empty — the state every
	// test written before the genres existed expects.
	byGenre     map[int][]tmdb.Match
	showByGenre map[int][]tmdb.Match

	byGenreErr     error
	showByGenreErr error

	// The rails run concurrently, so anything they WRITE needs the mutex or
	// -race fails the package. Everything above is set before the request and
	// only read.
	mu            sync.Mutex
	gotGenres     []int
	gotShowGenres []int

	gotQuery string
	gotYear  int
	gotID    int

	// What television was actually asked, recorded separately so a test can
	// assert the show endpoints were reached rather than the film ones.
	gotShowQuery string
	gotShowYear  int
	gotShowID    int
}

func (f *fakeBrowser) SearchMovies(_ context.Context, query string, year int) ([]tmdb.Match, error) {
	f.gotQuery, f.gotYear = query, year
	return f.search, f.searchErr
}

func (f *fakeBrowser) Movie(_ context.Context, id int) (*tmdb.Details, error) {
	f.gotID = id
	if f.detailsErr != nil {
		return nil, f.detailsErr
	}
	return f.details, nil
}

func (f *fakeBrowser) Trending(context.Context) ([]tmdb.Match, error) {
	return f.trending, f.trendingErr
}

func (f *fakeBrowser) Popular(context.Context) ([]tmdb.Match, error) {
	return f.popular, f.popularErr
}

func (f *fakeBrowser) TopRated(context.Context) ([]tmdb.Match, error) {
	return f.topRated, f.topRatedErr
}

func (f *fakeBrowser) NowPlaying(context.Context) ([]tmdb.Match, error) {
	return f.nowPlaying, f.nowPlayingErr
}

func (f *fakeBrowser) SearchShows(_ context.Context, query string, year int) ([]tmdb.Match, error) {
	f.gotShowQuery, f.gotShowYear = query, year
	return f.showSearch, f.showSearchErr
}

func (f *fakeBrowser) Show(_ context.Context, id int) (*tmdb.Details, error) {
	f.gotShowID = id
	if f.showDetailsErr != nil {
		return nil, f.showDetailsErr
	}
	return f.showDetails, nil
}

func (f *fakeBrowser) TrendingShows(context.Context) ([]tmdb.Match, error) {
	return f.showTrending, f.showTrendingErr
}

func (f *fakeBrowser) PopularShows(context.Context) ([]tmdb.Match, error) {
	return f.showPopular, f.showPopularErr
}

func (f *fakeBrowser) TopRatedShows(context.Context) ([]tmdb.Match, error) {
	return f.showTopRated, f.showTopRatedErr
}

func (f *fakeBrowser) OnTheAir(context.Context) ([]tmdb.Match, error) {
	return f.showOnTheAir, f.showOnTheAirErr
}

func (f *fakeBrowser) MoviesByGenre(_ context.Context, genreID int, _ string) ([]tmdb.Match, error) {
	f.mu.Lock()
	f.gotGenres = append(f.gotGenres, genreID)
	f.mu.Unlock()
	return f.byGenre[genreID], f.byGenreErr
}

func (f *fakeBrowser) ShowsByGenre(_ context.Context, genreID int, _ string) ([]tmdb.Match, error) {
	f.mu.Lock()
	f.gotShowGenres = append(f.gotShowGenres, genreID)
	f.mu.Unlock()
	return f.showByGenre[genreID], f.showByGenreErr
}

// severance is television's endgame(): the fixture every show test in this
// package builds on. 95396 is Severance's real TMDB **tv** id, and it is the
// same number a film could hold as a **movie** id — which is the collision
// tmdb_tv_id exists for, and the reason these tests use it rather than a
// number chosen to be safe.
func severance() tmdb.Match {
	return tmdb.Match{
		TMDBID: 95396, Title: "Severance", Year: 2022,
		Overview: "Mark leads a team of office workers…", PosterPath: "/severance.jpg",
	}
}

func endgame() tmdb.Match {
	return tmdb.Match{
		TMDBID: 299534, Title: "Avengers: Endgame", Year: 2019,
		Overview: "After the devastating events…", PosterPath: "/poster.jpg",
		BackdropPath: "/backdrop.jpg", VoteAverage: 8.2,
	}
}

func browseServer(t *testing.T, b Browser, st *fakeStore) http.Handler {
	t.Helper()
	if st == nil {
		st = newFakeStore()
	}
	mux := http.NewServeMux()
	srv := New(st, ScannerFunc(nil), nil, fixtureRoot, quiet())
	if b != nil {
		srv = srv.WithBrowser(b)
	}
	srv.Register(mux)
	srv.RegisterBrowse(mux)
	return mux
}

// tvBrowseServer is browseServer with LIBRARY_TV set, which is what every
// media=tv request needs: s.media gates television BEFORE it looks at the
// browser, so without a root the catalogue routes answer 503 and the browser is
// never reached at all.
func tvBrowseServer(t *testing.T, b Browser) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	srv := New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, quiet()).
		WithTV(TV{Root: tvFixtureRoot, Scanner: tvFixtureScanner()})
	if b != nil {
		srv = srv.WithBrowser(b)
	}
	srv.Register(mux)
	srv.RegisterBrowse(mux)
	return mux
}

func getJSON(t *testing.T, h http.Handler, target string, into any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code == http.StatusOK && into != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
			t.Fatalf("decode %s: %v — body was %s", target, err, rec.Body)
		}
	}
	return rec
}

// A card for a film curator already has carries its library state; one it has
// never heard of carries null. That is the green check on a poster.
func TestSearchAnnotatesCardsWithTheLibrary(t *testing.T) {
	path := "/media/movies/Avengers - Endgame (2019)"
	st := newFakeStore()
	st.library = map[int64]store.LibraryState{
		299534: {MovieID: 7, Status: store.StatusImported, LibraryPath: &path},
	}
	browser := &fakeBrowser{search: []tmdb.Match{
		endgame(),
		{TMDBID: 1003596, Title: "Avengers: Doomsday", Year: 2026},
	}}

	var body struct {
		Query   string      `json:"query"`
		Year    int         `json:"year"`
		Results []movieCard `json:"results"`
	}
	rec := getJSON(t, browseServer(t, browser, st), "/api/tmdb/search?query=avengers", &body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if browser.gotQuery != "avengers" {
		t.Errorf("searched %q", browser.gotQuery)
	}
	if len(body.Results) != 2 {
		t.Fatalf("got %d cards, want 2", len(body.Results))
	}

	if body.Results[0].Library == nil {
		t.Fatal("the film in the library has no library state")
	}
	if body.Results[0].Library.State != store.StatusImported {
		t.Errorf("state = %q, want %q", body.Results[0].Library.State, store.StatusImported)
	}
	if body.Results[0].Library.LibraryPath != path {
		t.Errorf("library_path = %q", body.Results[0].Library.LibraryPath)
	}
	// The film that caused all this trouble, present as a choice rather than as
	// a silent guess — and correctly reported as not in the library.
	if body.Results[1].Title != "Avengers: Doomsday" || body.Results[1].Library != nil {
		t.Errorf("doomsday card = %+v, want library null", body.Results[1])
	}
}

// movies.status never says 'downloading', so the card has to get it from the
// downloads table. store.LibraryByTMDBID does that; this checks the API carries
// it through rather than echoing Status.
func TestCardReportsDownloadingRatherThanWanted(t *testing.T) {
	st := newFakeStore()
	st.library = map[int64]store.LibraryState{
		299534: {MovieID: 7, Status: store.StatusWanted, Downloading: true},
	}

	var body struct {
		Results []movieCard `json:"results"`
	}
	getJSON(t, browseServer(t, &fakeBrowser{search: []tmdb.Match{endgame()}}, st),
		"/api/tmdb/search?query=avengers", &body)

	if got := body.Results[0].Library.State; got != store.StatusDownloading {
		t.Errorf("state = %q, want %q — the row still says 'wanted' and that would be misleading",
			got, store.StatusDownloading)
	}
}

func TestMovieReturnsEverythingTheScreenShows(t *testing.T) {
	browser := &fakeBrowser{details: &tmdb.Details{
		Match: endgame(), Tagline: "Avenge the fallen.", Runtime: 181,
		Genres: []string{"Adventure", "Action"}, Status: "Released",
		ReleaseDate: "2019-04-24", OriginalLanguage: "en",
		Studios: []string{"Marvel Studios"}, SpokenLanguages: []string{"English"},
	}}

	var body movieDetailBody
	rec := getJSON(t, browseServer(t, browser, nil), "/api/tmdb/movies/299534", &body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if browser.gotID != 299534 {
		t.Errorf("fetched id %d", browser.gotID)
	}
	if body.Runtime != 181 || body.Tagline != "Avenge the fallen." || body.Status != "Released" {
		t.Errorf("details = %+v", body)
	}
	if body.Title != "Avengers: Endgame" || body.BackdropPath == "" {
		t.Errorf("the card half is wrong: %+v", body.movieCard)
	}
	if len(body.Genres) != 2 || body.Studios[0] != "Marvel Studios" {
		t.Errorf("genres/studios = %v %v", body.Genres, body.Studios)
	}
}

// One rail failing is a success carrying the other. A failing source that is
// invisible is how somebody concludes a film does not exist.
func TestDiscoverReportsAFailingRailWithoutLosingTheOther(t *testing.T) {
	browser := &fakeBrowser{
		trending:   []tmdb.Match{endgame()},
		popularErr: errors.New("dial tcp: connection refused"),
	}

	var body struct {
		Rows []discoverRow `json:"rows"`
	}
	rec := getJSON(t, browseServer(t, browser, nil), "/api/tmdb/discover", &body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a downed rail is not an error page", rec.Code)
	}
	// Four fixed rails plus one per genre. Derived rather than written as a
	// number, so adding a genre does not fail a test that is not about genres.
	if want := 4 + len(movieGenres); len(body.Rows) != want {
		t.Fatalf("got %d rows, want %d", len(body.Rows), want)
	}

	byID := map[string]discoverRow{}
	for _, row := range body.Rows {
		byID[row.ID] = row
	}
	if !byID["trending"].OK || len(byID["trending"].Results) != 1 {
		t.Errorf("trending = %+v, want its results", byID["trending"])
	}
	if byID["popular"].OK {
		t.Error("the failing rail reports ok")
	}
	if byID["popular"].Error == "" {
		t.Error("the failing rail does not say why")
	}
	// [] and never null, so the UI can iterate it without a guard.
	if byID["popular"].Results == nil {
		t.Error("a failed rail returned null results")
	}
}

// Every rail draws its OWN source, under its own heading.
//
// This is the test the parallel-slices version of handleDiscover could not have:
// with `rows` and `fetch` indexed in lockstep, a rail inserted into one and
// appended to the other keeps both lists the right length and every assertion
// about counts and failure envelopes still passes — while the screen puts
// top-rated films under "Trending this week". So each rail here is fed a card
// nothing else returns, and the id it arrives under is checked.
//
// It runs both media types from one table because the transposition can happen
// on either side, and the television half is the two places it is most likely:
// a rail whose TITLE differs, and a genre vocabulary where the same id means
// something else. /discover does not reject a film's genre id — it returns a
// plausible page of the wrong shows — so `genre_28` arriving with Action's cards
// on the television tab is a defect nothing else in this package would see.
func TestEachDiscoverRailDrawsItsOwnSource(t *testing.T) {
	// One card per rail, told apart by id. The numbers are arbitrary and only
	// have to be distinct.
	card := func(id int, title string) []tmdb.Match {
		return []tmdb.Match{{TMDBID: id, Title: title, Year: 2024}}
	}
	// Each genre is fed a card naming its own id, so a rail drawing another
	// genre's page says so in the failure message.
	genreCards := func(genres []struct {
		id    int
		title string
	}, noun string) (map[int][]tmdb.Match, map[string][2]string) {
		cards := map[int][]tmdb.Match{}
		want := map[string][2]string{}
		for _, g := range genres {
			label := fmt.Sprintf("the %s %s", strings.ToLower(g.title), noun)
			cards[g.id] = card(g.id, label)
			want[fmt.Sprintf("genre_%d", g.id)] = [2]string{label, g.title}
		}
		return cards, want
	}

	movieCards, movieWant := genreCards(movieGenres, "film")
	showCards, showWant := genreCards(showGenres, "show")

	for _, c := range []struct {
		media   string
		browser *fakeBrowser
		// want maps a rail id to the title of the card that rail must return,
		// and to the heading it must be drawn under.
		want map[string][2]string
	}{{
		media: "movie",
		browser: &fakeBrowser{
			trending:   card(1, "the trending film"),
			popular:    card(2, "the popular film"),
			topRated:   card(3, "the top rated film"),
			nowPlaying: card(4, "the film in cinemas"),
			byGenre:    movieCards,
		},
		want: merge(map[string][2]string{
			"trending":   {"the trending film", "Trending this week"},
			"popular":    {"the popular film", "Popular"},
			"top_rated":  {"the top rated film", "Top rated"},
			"in_release": {"the film in cinemas", "In cinemas now"},
		}, movieWant),
	}, {
		media: "tv",
		browser: &fakeBrowser{
			showTrending: card(5, "the trending show"),
			showPopular:  card(6, "the popular show"),
			showTopRated: card(7, "the top rated show"),
			showOnTheAir: card(8, "the show on the air"),
			showByGenre:  showCards,
		},
		want: merge(map[string][2]string{
			"trending":   {"the trending show", "Trending this week"},
			"popular":    {"the popular show", "Popular"},
			"top_rated":  {"the top rated show", "Top rated"},
			"in_release": {"the show on the air", "On the air this week"},
		}, showWant),
	}} {
		t.Run(c.media, func(t *testing.T) {
			var body struct {
				Rows []discoverRow `json:"rows"`
			}
			rec := getJSON(t, tvBrowseServer(t, c.browser), "/api/tmdb/discover?media="+c.media, &body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 — body was %s", rec.Code, rec.Body)
			}
			if len(body.Rows) != len(c.want) {
				t.Fatalf("got %d rails, want %d", len(body.Rows), len(c.want))
			}

			for _, row := range body.Rows {
				want, ok := c.want[row.ID]
				if !ok {
					t.Errorf("unexpected rail %q", row.ID)
					continue
				}
				if !row.OK {
					t.Errorf("%s: not ok — %s", row.ID, row.Error)
					continue
				}
				if len(row.Results) != 1 {
					t.Errorf("%s: got %d cards, want the one its source returned", row.ID, len(row.Results))
					continue
				}
				if row.Results[0].Title != want[0] {
					t.Errorf("%s drew %q, want %q — the rails are transposed",
						row.ID, row.Results[0].Title, want[0])
				}
				if row.Title != want[1] {
					t.Errorf("%s is headed %q, want %q", row.ID, row.Title, want[1])
				}
			}
		})
	}
}

// The cache exists because twelve rails is twelve TMDB requests, and the home
// screen is the most-reloaded page in the application.
//
// It counts CALLS rather than timing anything: a test that asserted the second
// request was faster would be a test about the machine it ran on.
func TestDiscoverAsksTMDBOncePerRailPerTTL(t *testing.T) {
	browser := &countingBrowser{fakeBrowser: fakeBrowser{trending: []tmdb.Match{endgame()}}}
	h := browseServer(t, browser, nil)

	for i := 1; i <= 3; i++ {
		if rec := getJSON(t, h, "/api/tmdb/discover", nil); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d", i, rec.Code)
		}
	}

	// One per rail, not three — and emphatically not one, which would mean the
	// rails were never fanned out at all.
	if want := 4 + len(movieGenres); browser.calls() != want {
		t.Errorf("TMDB was asked %d times over three requests, want %d — one per rail",
			browser.calls(), want)
	}
}

// The rails are cached; whether curator HAS the film is not.
//
// This is the whole reason the cache holds []tmdb.Match rather than the finished
// response body. A card carries `library: {...}`, which is what draws the badge
// on a poster, and it changes the moment somebody presses Download — so a cached
// response would leave the poster unbadged for fifteen minutes and make the
// button look broken.
func TestACachedRailStillPicksUpTheLibrary(t *testing.T) {
	st := newFakeStore()
	h := browseServer(t, &fakeBrowser{trending: []tmdb.Match{endgame()}}, st)

	var before struct {
		Rows []discoverRow `json:"rows"`
	}
	getJSON(t, h, "/api/tmdb/discover", &before)
	if card := findCard(t, before.Rows, "trending"); card.Library != nil {
		t.Fatalf("the film is in the library before it was imported: %+v", card.Library)
	}

	// The film arrives. Nothing invalidates the cache, and nothing should have
	// to — the rail's cards are the same twenty films either way.
	row := store.Movie{ID: 7, Title: "Avengers: Endgame", Year: 2019, MediaType: store.MediaTypeMovie}
	st.byID[row.ID] = &row
	st.library = map[int64]store.LibraryState{
		299534: {MovieID: row.ID, Status: store.StatusImported},
	}

	var after struct {
		Rows []discoverRow `json:"rows"`
	}
	getJSON(t, h, "/api/tmdb/discover", &after)
	card := findCard(t, after.Rows, "trending")
	if card.Library == nil {
		t.Fatal("a cached rail lost the library state — the poster would draw with no badge")
	}
	if card.Library.State != store.StatusImported {
		t.Errorf("state = %q, want %q", card.Library.State, store.StatusImported)
	}
}

// A failed rail is not remembered. Fifteen minutes of a cached transient 502 is
// how a blip becomes a screen somebody reports as broken.
func TestAFailedRailIsRetriedRatherThanCached(t *testing.T) {
	browser := &countingBrowser{fakeBrowser: fakeBrowser{
		trendingErr: errors.New("dial tcp: connection refused"),
	}}
	h := browseServer(t, browser, nil)

	for i := 1; i <= 2; i++ {
		var body struct {
			Rows []discoverRow `json:"rows"`
		}
		getJSON(t, h, "/api/tmdb/discover", &body)
		byID := map[string]discoverRow{}
		for _, row := range body.Rows {
			byID[row.ID] = row
		}
		if byID["trending"].OK {
			t.Fatalf("request %d: the failing rail reports ok", i)
		}
	}

	// Trending was asked both times; every other rail succeeded and was asked
	// once.
	if got, want := browser.trendingCalls(), 2; got != want {
		t.Errorf("the failed rail was asked %d times, want %d — a failure was cached", got, want)
	}
	if got, want := browser.popularCalls(), 1; got != want {
		t.Errorf("a rail that SUCCEEDED was asked %d times, want %d", got, want)
	}
}

// And it does expire. The clock is injected rather than slept through: fifteen
// real minutes is not a test.
func TestARailIsAskedAgainOnceTheTTLHasPassed(t *testing.T) {
	browser := &countingBrowser{fakeBrowser: fakeBrowser{trending: []tmdb.Match{endgame()}}}

	mux := http.NewServeMux()
	srv := New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, quiet()).WithBrowser(browser)
	now := time.Now()
	srv.rails.now = func() time.Time { return now }
	srv.Register(mux)
	srv.RegisterBrowse(mux)

	getJSON(t, mux, "/api/tmdb/discover", nil)
	getJSON(t, mux, "/api/tmdb/discover", nil)
	if got := browser.trendingCalls(); got != 1 {
		t.Fatalf("inside the TTL the rail was asked %d times, want 1", got)
	}

	// One second past, not one second before: the boundary is where an
	// off-by-one lives.
	now = now.Add(railTTL + time.Second)
	getJSON(t, mux, "/api/tmdb/discover", nil)
	if got := browser.trendingCalls(); got != 2 {
		t.Errorf("past the TTL the rail was asked %d times, want 2", got)
	}
}

// countingBrowser is fakeBrowser that counts what it was asked. The rails run
// concurrently, so every counter is atomic or -race fails the package.
type countingBrowser struct {
	fakeBrowser
	trendingN atomic.Int64
	popularN  atomic.Int64
	otherN    atomic.Int64
}

func (c *countingBrowser) Trending(ctx context.Context) ([]tmdb.Match, error) {
	c.trendingN.Add(1)
	return c.fakeBrowser.Trending(ctx)
}

func (c *countingBrowser) Popular(ctx context.Context) ([]tmdb.Match, error) {
	c.popularN.Add(1)
	return c.fakeBrowser.Popular(ctx)
}

func (c *countingBrowser) TopRated(ctx context.Context) ([]tmdb.Match, error) {
	c.otherN.Add(1)
	return c.fakeBrowser.TopRated(ctx)
}

func (c *countingBrowser) NowPlaying(ctx context.Context) ([]tmdb.Match, error) {
	c.otherN.Add(1)
	return c.fakeBrowser.NowPlaying(ctx)
}

func (c *countingBrowser) MoviesByGenre(ctx context.Context, id int, what string) ([]tmdb.Match, error) {
	c.otherN.Add(1)
	return c.fakeBrowser.MoviesByGenre(ctx, id, what)
}

func (c *countingBrowser) calls() int {
	return int(c.trendingN.Load() + c.popularN.Load() + c.otherN.Load())
}
func (c *countingBrowser) trendingCalls() int { return int(c.trendingN.Load()) }
func (c *countingBrowser) popularCalls() int  { return int(c.popularN.Load()) }

// findCard pulls one rail's first card, failing the test rather than panicking
// on a rail that is missing or empty.
func findCard(t *testing.T, rows []discoverRow, id string) movieCard {
	t.Helper()
	for _, row := range rows {
		if row.ID != id {
			continue
		}
		if len(row.Results) == 0 {
			t.Fatalf("rail %q is empty: %+v", id, row)
		}
		return row.Results[0]
	}
	t.Fatalf("no rail %q in %d rows", id, len(rows))
	return movieCard{}
}

// merge folds the genre expectations into the fixed ones. Both halves are built
// the same way and neither may quietly overwrite the other, so a collision is a
// failure rather than a last-write-wins.
func merge(into, from map[string][2]string) map[string][2]string {
	for k, v := range from {
		if _, clash := into[k]; clash {
			panic("two rails share the id " + k)
		}
		into[k] = v
	}
	return into
}

func TestBrowseStatusCodes(t *testing.T) {
	cases := []struct {
		name   string
		target string
		b      Browser
		want   int
	}{{
		name: "no key at all", target: "/api/tmdb/discover", b: nil,
		want: http.StatusServiceUnavailable,
	}, {
		name: "no key, search", target: "/api/tmdb/search?query=x", b: nil,
		want: http.StatusServiceUnavailable,
	}, {
		name: "no key, movie", target: "/api/tmdb/movies/1", b: nil,
		want: http.StatusServiceUnavailable,
	}, {
		// The id came from a URL a human can type.
		name: "unknown id", target: "/api/tmdb/movies/999999999",
		b:    &fakeBrowser{detailsErr: fmt.Errorf("tmdb movie 999999999: %w", tmdb.ErrNotFound)},
		want: http.StatusNotFound,
	}, {
		// 502 and NOT 503: 503 means "you did not set the variable", which would
		// send someone to check a variable that is already there.
		name: "wrong key", target: "/api/tmdb/search?query=x",
		b:    &fakeBrowser{searchErr: fmt.Errorf("tmdb search: %w: Invalid API key", tmdb.ErrUnauthorized)},
		want: http.StatusBadGateway,
	}, {
		name: "tmdb is unwell", target: "/api/tmdb/search?query=x",
		b:    &fakeBrowser{searchErr: errors.New("unexpected status 500")},
		want: http.StatusBadGateway,
	}, {
		name: "empty query", target: "/api/tmdb/search?query=", b: &fakeBrowser{},
		want: http.StatusBadRequest,
	}, {
		name: "blank query", target: "/api/tmdb/search?query=%20%20", b: &fakeBrowser{},
		want: http.StatusBadRequest,
	}, {
		name: "non-numeric id", target: "/api/tmdb/movies/not-a-number", b: &fakeBrowser{},
		want: http.StatusBadRequest,
	}, {
		name: "zero id", target: "/api/tmdb/movies/0", b: &fakeBrowser{},
		want: http.StatusBadRequest,
	}, {
		name: "bad year", target: "/api/tmdb/search?query=x&year=nineteen", b: &fakeBrowser{},
		want: http.StatusBadRequest,
	}}

	for _, c := range cases {
		rec := getJSON(t, browseServer(t, c.b, nil), c.target, nil)
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body)
		}
		var body struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
			t.Errorf("%s: body = %s, want the {\"error\": \"...\"} shape", c.name, rec.Body)
		}
	}
}

// Without a key the message has to name the variable, because that is the whole
// remedy — the same posture as QBIT_USER's 503.
func TestUnconfiguredNamesTheVariable(t *testing.T) {
	rec := getJSON(t, browseServer(t, nil, nil), "/api/tmdb/discover", nil)

	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if !strings.Contains(body.Error, "TMDB_API_KEY") {
		t.Errorf("error = %q, want it to name TMDB_API_KEY", body.Error)
	}
}

// The library endpoints are not TMDB's and must keep working without a key.
func TestLibraryRoutesAreUnaffectedByAMissingKey(t *testing.T) {
	h := browseServer(t, nil, nil)

	for _, target := range []string{"/api/movies"} {
		if rec := getJSON(t, h, target, nil); rec.Code != http.StatusOK {
			t.Errorf("%s = %d with no TMDB key, want 200 — it is the library, not the catalogue",
				target, rec.Code)
		}
	}
}

// --- the Open in Jellyfin link ----------------------------------------------

// fakeMediaServer stands in for internal/jellyfin's one read-only query. The
// three failure modes are the interesting part and all three are awkward to
// produce from a real client, which is why the seam is an interface.
type fakeMediaServer struct {
	item jellyfin.Item
	err  error

	// The show half answers from its own fields for the reason MediaServer's
	// own comment gives: asking Jellyfin for a Movie with a tv id can LAND on
	// an unrelated film rather than merely miss. A fake that returned `item` to
	// both would hide exactly that.
	series    jellyfin.Item
	seriesErr error

	gotTMDBID int
	gotYear   int
	calls     int

	gotSeriesTMDBID int
	gotSeriesYear   int
	seriesCalls     int
}

func (f *fakeMediaServer) FindMovie(_ context.Context, tmdbID, year int) (jellyfin.Item, error) {
	f.calls++
	f.gotTMDBID, f.gotYear = tmdbID, year
	return f.item, f.err
}

func (f *fakeMediaServer) FindSeries(_ context.Context, tmdbID, year int) (jellyfin.Item, error) {
	f.seriesCalls++
	f.gotSeriesTMDBID, f.gotSeriesYear = tmdbID, year
	return f.series, f.seriesErr
}

// importedEndgame is the fixture for these: the film is on disk, which is the
// only state that gets a link at all.
func importedEndgame(t *testing.T) *fakeStore {
	t.Helper()
	st := newFakeStore()
	path := "/library/Avengers - Endgame (2019)"
	st.library = map[int64]store.LibraryState{
		299534: {MovieID: 7, Status: store.StatusImported, LibraryPath: &path},
	}
	return st
}

func jellyfinServer(t *testing.T, st *fakeStore, m MediaServer, publicURL string) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	srv := New(st, ScannerFunc(nil), nil, fixtureRoot, quiet()).
		WithBrowser(&fakeBrowser{details: &tmdb.Details{Match: endgame()}}).
		WithJellyfin(m, publicURL)
	srv.RegisterBrowse(mux)
	return mux
}

// The happy path: a film on disk that Jellyfin has gets a deep link to the item
// itself, which is the whole point — landing on Jellyfin's home screen is the
// failure this task exists to remove.
func TestDetailBodyCarriesADeepJellyfinLink(t *testing.T) {
	media := &fakeMediaServer{item: jellyfin.Item{ID: "bbb", ServerID: "srv"}}

	var body movieDetailBody
	rec := getJSON(t, jellyfinServer(t, importedEndgame(t), media, "http://192.168.1.26:8096"),
		"/api/tmdb/movies/299534", &body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	const want = "http://192.168.1.26:8096/web/index.html#/details?id=bbb&serverId=srv"
	if body.JellyfinURL != want {
		t.Errorf("jellyfin_url = %q, want %q", body.JellyfinURL, want)
	}
	// The TMDB id and the year, which is what makes the query one small request
	// instead of the whole library (docs/decisions.md D32).
	if media.gotTMDBID != 299534 || media.gotYear != 2019 {
		t.Errorf("looked up tmdb %d year %d, want 299534 / 2019", media.gotTMDBID, media.gotYear)
	}
}

// Every way the lookup can fail lands on the same search link, because all four
// are the same thing to a person: the deep link is not available and a search
// always is. None of them may fail the page.
func TestAFailedLookupBecomesASearchLinkAndNeverAnError(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"jellyfin does not have the film", jellyfin.ErrNotFound},
		{"the key was revoked", fmt.Errorf("jellyfin answered 401: %w", jellyfin.ErrUnauthorized)},
		{"jellyfin is switched off", errors.New("dial tcp 192.168.1.26:8096: connection refused")},
		{"jellyfin answered rubbish", errors.New("decoding the item list: unexpected EOF")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			media := &fakeMediaServer{err: tc.err}

			var body movieDetailBody
			rec := getJSON(t, jellyfinServer(t, importedEndgame(t), media, "http://jf:8096"),
				"/api/tmdb/movies/299534", &body)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 — a Jellyfin problem is not the movie page's problem", rec.Code)
			}
			const want = "http://jf:8096/web/index.html#/search.html?query=Avengers%3A+Endgame"
			if body.JellyfinURL != want {
				t.Errorf("jellyfin_url = %q, want the search fallback %q", body.JellyfinURL, want)
			}
		})
	}
}

// "Carries nothing new for a film that is not on disk", and the reason is not
// tidiness: linking a film nobody owns into a media server that certainly does
// not have it is a link to a search for something that is not there.
func TestNoJellyfinLinkForAFilmThatIsNotOnDisk(t *testing.T) {
	notImported := map[string]*fakeStore{
		"never heard of it": newFakeStore(),
		"wanted":            {byPath: map[string]*store.Movie{}, byID: map[int64]*store.Movie{}, library: map[int64]store.LibraryState{299534: {MovieID: 7, Status: store.StatusWanted}}},
		"downloading":       {byPath: map[string]*store.Movie{}, byID: map[int64]*store.Movie{}, library: map[int64]store.LibraryState{299534: {MovieID: 7, Status: store.StatusWanted, Downloading: true}}},
	}
	for name, st := range notImported {
		t.Run(name, func(t *testing.T) {
			media := &fakeMediaServer{item: jellyfin.Item{ID: "bbb"}}

			var body movieDetailBody
			getJSON(t, jellyfinServer(t, st, media, "http://jf:8096"), "/api/tmdb/movies/299534", &body)

			if body.JellyfinURL != "" {
				t.Errorf("jellyfin_url = %q, want absent", body.JellyfinURL)
			}
			// And it did not go and ask, which would be a network round trip on
			// every page view of every film in the catalogue.
			if media.calls != 0 {
				t.Errorf("asked Jellyfin %d times about a film that is not in the library", media.calls)
			}
		})
	}
}

// No Jellyfin configured means no link — not a disabled button and not a
// tooltip explaining what you are missing, which is the rule the rest of the UI
// already follows for an unconfigured integration.
func TestNoJellyfinConfiguredMeansNoLink(t *testing.T) {
	for _, tc := range []struct {
		name      string
		media     MediaServer
		publicURL string
	}{
		{"no client", nil, "http://jf:8096"},
		{"no url", &fakeMediaServer{item: jellyfin.Item{ID: "bbb"}}, ""},
		{"a blank url", &fakeMediaServer{item: jellyfin.Item{ID: "bbb"}}, "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body movieDetailBody
			rec := getJSON(t, jellyfinServer(t, importedEndgame(t), tc.media, tc.publicURL),
				"/api/tmdb/movies/299534", &body)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if body.JellyfinURL != "" {
				t.Errorf("jellyfin_url = %q, want absent", body.JellyfinURL)
			}
		})
	}
}

// The key is absent from the JSON rather than present and empty, so a UI can
// branch on the field existing at all.
func TestTheJellyfinKeyIsOmittedRatherThanEmpty(t *testing.T) {
	rec := getJSON(t, jellyfinServer(t, newFakeStore(), nil, ""), "/api/tmdb/movies/299534", nil)
	if strings.Contains(rec.Body.String(), "jellyfin_url") {
		t.Errorf("the body carries an empty jellyfin_url: %s", rec.Body)
	}
}

// --- the same link, for a row with no catalogue entry (T61 / D35) ------------

// movieRowServer mounts the library routes beside a Jellyfin, which is what
// GET /api/movies/{id} now needs and Register alone does not give it.
func movieRowServer(t *testing.T, st *fakeStore, m MediaServer, publicURL string) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	srv := New(st, ScannerFunc(nil), nil, fixtureRoot, quiet()).WithJellyfin(m, publicURL)
	srv.Register(mux)
	return mux
}

// The point of T61: an unmatched row is on disk and playable, so it gets a
// Jellyfin link like any other film — and it can only ever be a search, because
// there is no TMDB id to look the film up by.
func TestAnUnmatchedRowGetsAJellyfinSearchLink(t *testing.T) {
	st := newFakeStore()
	row := st.seedOnDisk("/library/Some Home Video (2019)", "Some Home Video", 2019)
	if row.TMDBID != nil {
		t.Fatalf("fixture is matched; this test is about the unmatched row")
	}
	media := &fakeMediaServer{item: jellyfin.Item{ID: "bbb", ServerID: "srv"}}

	var body movieBody
	rec := getJSON(t, movieRowServer(t, st, media, "http://192.168.1.26:8096"),
		fmt.Sprintf("/api/movies/%d", row.ID), &body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	const want = "http://192.168.1.26:8096/web/index.html#/search.html?query=Some+Home+Video"
	if body.JellyfinURL != want {
		t.Errorf("jellyfin_url = %q, want %q", body.JellyfinURL, want)
	}
	// **Nothing was looked up.** A nil tmdb_id is D32's miss known in advance,
	// so spending a request to rediscover it would be a request that can only
	// fail — and on a Jellyfin that is switched off it would fail slowly.
	if media.calls != 0 {
		t.Errorf("FindMovie called %d times for a row with no tmdb_id, want 0", media.calls)
	}
}

// The embedding is the compatibility promise: this route answered a bare
// store.Movie before T61 and must still answer the same keys at the same level.
func TestTheRowBodyKeepsItsOldShape(t *testing.T) {
	st := newFakeStore()
	row := st.seedOnDisk("/library/Some Home Video (2019)", "Some Home Video", 2019)

	var raw map[string]any
	rec := getJSON(t, movieRowServer(t, st, nil, ""), fmt.Sprintf("/api/movies/%d", row.ID), &raw)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	for _, key := range []string{
		"id", "tmdb_id", "title", "year", "media_type", "overview", "poster_path",
		"status", "library_path", "quality", "size_bytes", "added_at", "imported_at",
	} {
		if _, ok := raw[key]; !ok {
			t.Errorf("key %q is missing: %s", key, rec.Body)
		}
	}
	// Absent, not empty, when there is no Jellyfin — the rule the whole UI reads
	// as "draw nothing at all".
	if _, ok := raw["jellyfin_url"]; ok {
		t.Errorf("jellyfin_url is present with no Jellyfin configured: %s", rec.Body)
	}
}

// A row that is not on disk gets no link, matched or not: linking a film nobody
// owns into a media server that certainly does not have it is a search for
// something that is not there.
func TestAWantedRowGetsNoJellyfinLink(t *testing.T) {
	st := newFakeStore()
	row := st.seedWanted("Some Film", 2019)
	media := &fakeMediaServer{item: jellyfin.Item{ID: "bbb"}}

	var body movieBody
	getJSON(t, movieRowServer(t, st, media, "http://192.168.1.26:8096"),
		fmt.Sprintf("/api/movies/%d", row.ID), &body)

	if body.JellyfinURL != "" {
		t.Errorf("jellyfin_url = %q for a film that is not on disk, want none", body.JellyfinURL)
	}
	if media.calls != 0 {
		t.Errorf("FindMovie called %d times for a wanted row, want 0", media.calls)
	}
}

// A matched row reached through this route still gets the deep link, so the two
// addressing modes agree about the film rather than each having their own idea
// of it.
func TestAMatchedRowStillGetsTheDeepLink(t *testing.T) {
	st := newFakeStore()
	row := st.seedOnDisk("/library/Avengers - Endgame (2019)", "Avengers: Endgame", 2019)
	tmdbID := int64(299534)
	row.TMDBID = &tmdbID
	media := &fakeMediaServer{item: jellyfin.Item{ID: "bbb", ServerID: "srv"}}

	var body movieBody
	getJSON(t, movieRowServer(t, st, media, "http://192.168.1.26:8096"),
		fmt.Sprintf("/api/movies/%d", row.ID), &body)

	const want = "http://192.168.1.26:8096/web/index.html#/details?id=bbb&serverId=srv"
	if body.JellyfinURL != want {
		t.Errorf("jellyfin_url = %q, want %q", body.JellyfinURL, want)
	}
	if media.gotTMDBID != 299534 || media.gotYear != 2019 {
		t.Errorf("looked up tmdb %d year %d, want 299534 / 2019", media.gotTMDBID, media.gotYear)
	}
}

// T68, and the whole reason the column exists: a row matched by hand is looked
// up by TMDB's year, not by the folder's.
//
// D32 narrows the lookup with `years=` because "both sides take the year from
// TMDB", and a hand-matched row is the only row that breaks that premise — the
// folder says one year and Jellyfin, which took its ProductionYear from TMDB,
// says another. Sent the folder's year the query narrows to a year the film is
// not in, Jellyfin answers nothing, and a perfectly good match silently
// degrades to a search link. Measured on the real 10.10.7 in T67: a 2011 folder
// matched to Jaws (1975) answered the search link.
func TestAHandMatchedRowIsLookedUpByTMDBsYear(t *testing.T) {
	st := newFakeStore()
	// The folder says 2011. Jaws is 1975, and Jellyfin holds it under 1975.
	row := st.seedOnDisk("/library/Some Home Video (2011)", "Some Home Video", 2011)
	tmdbID := int64(578)
	row.TMDBID = &tmdbID
	row.TMDBYear = ptrInt(1975)
	media := &fakeMediaServer{item: jellyfin.Item{ID: "jaws", ServerID: "srv"}}

	var body movieBody
	getJSON(t, movieRowServer(t, st, media, "http://192.168.1.26:8096"),
		fmt.Sprintf("/api/movies/%d", row.ID), &body)

	if media.gotYear != 1975 {
		t.Errorf("looked up year %d, want TMDB's 1975 — the folder's 2011 finds nothing", media.gotYear)
	}
	const want = "http://192.168.1.26:8096/web/index.html#/details?id=jaws&serverId=srv"
	if body.JellyfinURL != want {
		t.Errorf("jellyfin_url = %q, want the deep link %q", body.JellyfinURL, want)
	}
	// The row still reports the folder's year, because that is still what the
	// folder is called and what the importer would write.
	if body.Year != 2011 {
		t.Errorf("year = %d, want the folder's 2011", body.Year)
	}
}

// Both TMDB 502s, and the pair matters as much as either: the whole reason
// tmdb.ErrUnauthorized has its own arm is that a rejected key is a different
// situation from TMDB being unwell, and 502 cannot say which. If the two
// sentences were the same string, the arm would be decoration.
func TestTMDBFailuresAreWrittenForAHuman(t *testing.T) {
	said := map[string]string{}

	for _, tc := range []struct {
		name string
		err  error
		says string
	}{{
		// The key is SET and is being refused, which is exactly what 503 would
		// have lied about — so the sentence has to carry the distinction the
		// status cannot.
		name: "a key TMDB will not take",
		err:  fmt.Errorf("tmdb search: %w: Invalid API key: You must be granted a valid key.", tmdb.ErrUnauthorized),
		says: "rejected",
	}, {
		name: "TMDB is unwell",
		err:  errors.New("tmdb search: unexpected status 500 Internal Server Error: The server is down"),
		says: "TMDB did not answer",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			log, buffer := captured()
			mux := http.NewServeMux()
			srv := New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, log).
				WithBrowser(&fakeBrowser{searchErr: tc.err})
			srv.Register(mux)
			srv.RegisterBrowse(mux)

			rec := getJSON(t, mux, "/api/tmdb/search?query=x", nil)
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502 (%s)", rec.Code, rec.Body)
			}

			got := errorBody(t, rec)
			if !strings.Contains(got, tc.says) {
				t.Errorf("body does not say %q: %q", tc.says, got)
			}
			// `tmdb search: ` is curator's own prefix and the tail is TMDB's
			// status_message — a third party's prose in curator's banner.
			assertNoLeak(t, "body", got, []string{
				"tmdb search:", "unexpected status", "Invalid API key",
				"You must be granted", "The server is down",
			})
			said[tc.name] = got

			if logged := flattenLog(buffer); !strings.Contains(logged, "tmdb search:") {
				t.Errorf("the log lost the chain:\n%s", logged)
			}
		})
	}

	if len(said) == 2 {
		var got []string
		for _, sentence := range said {
			got = append(got, sentence)
		}
		if got[0] == got[1] {
			t.Errorf("both TMDB 502s say %q — one of them is false, and the arm is decoration", got[0])
		}
	}
}

// The thirteenth leak, and the one no grep for a status constant finds: a failed
// discover rail is named inside a 200, so the chain reached the home screen at a
// status nobody was auditing.
func TestAFailedRailIsWrittenForAHuman(t *testing.T) {
	log, buffer := captured()
	mux := http.NewServeMux()
	srv := New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, log).
		// Every rail fails, because that is what a rejected key actually does —
		// one key, one refusal, four rails. The loop below then holds for all of
		// them rather than for whichever two the fake happened to arm.
		WithBrowser(&fakeBrowser{
			trendingErr: fmt.Errorf("tmdb trending: %w: Invalid API key: You must be granted a valid key.",
				tmdb.ErrUnauthorized),
			popularErr: fmt.Errorf("tmdb popular: %w: Invalid API key: You must be granted a valid key.",
				tmdb.ErrUnauthorized),
			topRatedErr: fmt.Errorf("tmdb top rated: %w: Invalid API key: You must be granted a valid key.",
				tmdb.ErrUnauthorized),
			nowPlayingErr: fmt.Errorf("tmdb now playing: %w: Invalid API key: You must be granted a valid key.",
				tmdb.ErrUnauthorized),
			byGenreErr: fmt.Errorf("tmdb action films: %w: Invalid API key: You must be granted a valid key.",
				tmdb.ErrUnauthorized),
		})
	srv.Register(mux)
	srv.RegisterBrowse(mux)

	var body struct {
		Rows []struct {
			ID    string `json:"id"`
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"rows"`
	}
	rec := getJSON(t, mux, "/api/tmdb/discover", &body)
	// Still 200: the page is worth drawing, and that is the whole reason this
	// site went unnoticed.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a failed rail is not a failed page: %s", rec.Code, rec.Body)
	}
	if len(body.Rows) == 0 {
		t.Fatal("no rows at all")
	}

	for _, row := range body.Rows {
		if row.OK {
			t.Errorf("row %s reports ok with a failing browser", row.ID)
			continue
		}
		if !strings.Contains(row.Error, "rejected") {
			t.Errorf("row %s does not say the key was refused: %q", row.ID, row.Error)
		}
		assertNoLeak(t, "row "+row.ID, row.Error, []string{
			"tmdb trending:", "tmdb popular:", "tmdb top rated:", "tmdb now playing:",
			"api key rejected", "Invalid API key", "You must be granted",
		})
	}

	// The rail is the one 5xx-shaped failure inside a 200, so a gate on the
	// status would have dropped it. It has to reach the log some other way.
	logged := flattenLog(buffer)
	for _, want := range []string{"tmdb trending:", "Invalid API key"} {
		if !strings.Contains(logged, want) {
			t.Errorf("the log lost %q, so a rejected key is invisible to an operator:\n%s", want, logged)
		}
	}
}

// showBrowseServer is browseServer with television switched on, which is what
// an install with LIBRARY_TV set has. Without it every /api/tmdb/shows/ request
// answers 503 naming the variable (D48).
func showBrowseServer(t *testing.T, b Browser) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	srv := New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, quiet()).
		WithBrowser(b).
		WithTV(TV{Root: tvFixtureRoot, Scanner: tvFixtureScanner()})
	srv.Register(mux)
	srv.RegisterBrowse(mux)
	return mux
}

// silo is the show every T97 measurement was taken against, and its season list
// is the reason the count is not enough: TMDB reports four seasons and the
// fourth has no episodes in it.
func silo() tmdb.Match {
	return tmdb.Match{
		TMDBID: 125988, Title: "Silo", Year: 2023,
		Overview: "In a ruined and toxic future…", PosterPath: "/silo.jpg",
		BackdropPath: "/silo-wide.jpg", VoteAverage: 8.0,
	}
}

// The show body carries the two fields T97 added, and the season list is the
// half that is not obvious: it is not what `seasons` counts.
func TestShowCarriesItsIMDbIDAndItsSeasonList(t *testing.T) {
	browser := &fakeBrowser{showDetails: &tmdb.Details{
		Match: silo(), Status: "Returning Series", FirstAirDate: "2023-05-04",
		NumberOfSeasons: 4, NumberOfEpisodes: 30, IMDBID: "tt14688458",
		Seasons: []tmdb.Season{
			{Number: 0, Name: "Specials", EpisodeCount: 2},
			{Number: 1, Name: "Season 1", EpisodeCount: 10, AirDate: "2023-05-04"},
			{Number: 4, Name: "Season 4", EpisodeCount: 0},
		},
	}}

	var body showDetailBody
	rec := getJSON(t, showBrowseServer(t, browser), "/api/tmdb/shows/125988", &body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if browser.gotShowID != 125988 {
		t.Errorf("fetched show id %d — the tv endpoint was not the one reached", browser.gotShowID)
	}

	// Verbatim, prefix and all. The screen hands it straight to /api/search,
	// which refuses anything that is not tt-plus-digits.
	if body.IMDBID != "tt14688458" {
		t.Errorf("imdb_id = %q, want tt14688458", body.IMDBID)
	}

	// The count and the list are both sent and they disagree, which is exactly
	// why both are here: 4 against three listed, one of which has no episodes.
	if body.Seasons != 4 {
		t.Errorf("seasons = %d, want the count TMDB states", body.Seasons)
	}
	if len(body.SeasonList) != 3 {
		t.Fatalf("season_list has %d entries, want all three TMDB listed", len(body.SeasonList))
	}
	// Unfiltered, Specials and the empty season included. The API reports what
	// TMDB says; which of them a person may click is the screen's decision, and
	// it is a different decision for each of those two.
	if body.SeasonList[0].Number != 0 || body.SeasonList[0].Name != "Specials" {
		t.Errorf("season_list[0] = %+v, want Specials reported rather than dropped", body.SeasonList[0])
	}
	if body.SeasonList[1].EpisodeCount != 10 || body.SeasonList[1].AirDate != "2023-05-04" {
		t.Errorf("season_list[1] = %+v", body.SeasonList[1])
	}
	if body.SeasonList[2].Number != 4 || body.SeasonList[2].EpisodeCount != 0 {
		t.Errorf("season_list[2] = %+v, want the unaired season with its zero intact", body.SeasonList[2])
	}
}

// [] and never null, because the UI iterates it. A show TMDB lists no seasons
// for would otherwise crash the picker rather than draw nothing.
func TestShowSeasonListIsNeverNull(t *testing.T) {
	browser := &fakeBrowser{showDetails: &tmdb.Details{Match: silo()}}

	var body struct {
		SeasonList []seasonBody `json:"season_list"`
	}
	getJSON(t, showBrowseServer(t, browser), "/api/tmdb/shows/125988", &body)

	raw := httptest.NewRecorder()
	showBrowseServer(t, browser).ServeHTTP(raw, httptest.NewRequest(http.MethodGet, "/api/tmdb/shows/125988", nil))
	if !strings.Contains(raw.Body.String(), `"season_list":[]`) {
		t.Errorf("season_list did not serialise as []: %s", raw.Body)
	}
}
