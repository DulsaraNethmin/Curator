package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DulsaraNethmin/curator/internal/jellyfin"
	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/tmdb"
)

// fakeBrowser stands in for the TMDB client.
type fakeBrowser struct {
	search   []tmdb.Match
	details  *tmdb.Details
	trending []tmdb.Match
	popular  []tmdb.Match

	searchErr   error
	detailsErr  error
	trendingErr error
	popularErr  error

	gotQuery string
	gotYear  int
	gotID    int
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
	if len(body.Rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(body.Rows))
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

	gotTMDBID int
	gotYear   int
	calls     int
}

func (f *fakeMediaServer) FindMovie(_ context.Context, tmdbID, year int) (jellyfin.Item, error) {
	f.calls++
	f.gotTMDBID, f.gotYear = tmdbID, year
	return f.item, f.err
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
