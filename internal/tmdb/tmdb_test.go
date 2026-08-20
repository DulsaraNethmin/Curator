package tmdb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fixtures in testdata/ are real /search/movie responses recorded from the
// live API, so the parsing under test meets the shape TMDB actually returns.

// fixtureServer serves a recorded body per search query and records every query
// it was asked for, in order. Counting requests is how the fallback tests prove
// a second call did or did not happen.
type fixtureServer struct {
	*httptest.Server

	mu      sync.Mutex
	queries []string
}

// newFixtureServer maps a `query` parameter to a testdata filename. A query the
// map does not mention is a test failure, not an empty result: it means the
// client searched for something the test did not expect.
func newFixtureServer(t *testing.T, byQuery map[string]string) *fixtureServer {
	t.Helper()
	return newSearchServer(t, "/search/movie", byQuery)
}

// newSearchServer is newFixtureServer with the path spelled out, because
// /search/tv answers a differently shaped payload at the same envelope and its
// tests need the identical query-recording machinery. Asserting the path is
// half the point: it is what catches a TV method sent to the movie endpoint,
// which would otherwise decode to a page of empty names rather than fail.
func newSearchServer(t *testing.T, path string, byQuery map[string]string) *fixtureServer {
	t.Helper()

	fs := &fixtureServer{}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			t.Errorf("unexpected path %q, want %q", r.URL.Path, path)
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query().Get("query")

		fs.mu.Lock()
		fs.queries = append(fs.queries, query)
		fs.mu.Unlock()

		name, ok := byQuery[query]
		if !ok {
			t.Errorf("unexpected query %q", query)
			http.Error(w, "unexpected query", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(readFixture(t, name))
	}))
	t.Cleanup(fs.Close)
	return fs
}

func (fs *fixtureServer) seen() []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]string(nil), fs.queries...)
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

func newTestClient(t *testing.T, fs *fixtureServer) *Client {
	t.Helper()
	return New("test-key", fs.Client(), WithBaseURL(fs.URL))
}

func TestSearchMovieNormalTitle(t *testing.T) {
	fs := newFixtureServer(t, map[string]string{
		"Interstellar": "interstellar_2014.json",
	})

	got, err := newTestClient(t, fs).SearchMovie(context.Background(), "Interstellar", 2014)
	if err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	if got == nil {
		t.Fatal("SearchMovie returned no match, want Interstellar")
	}
	if got.TMDBID != 157336 {
		t.Errorf("TMDBID = %d, want 157336", got.TMDBID)
	}
	if got.Title != "Interstellar" {
		t.Errorf("Title = %q, want %q", got.Title, "Interstellar")
	}
	if got.Year != 2014 {
		t.Errorf("Year = %d, want 2014", got.Year)
	}
	if got.Overview == "" {
		t.Error("Overview is empty")
	}
	if got.PosterPath != "/yQvGrMoipbRoddT0ZR8tPoR7NfX.jpg" {
		t.Errorf("PosterPath = %q", got.PosterPath)
	}
	if n := len(fs.seen()); n != 1 {
		t.Errorf("made %d requests, want 1", n)
	}
}

func TestSearchMovieSendsQueryParams(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		w.Write(readFixture(t, "empty.json"))
	}))
	defer srv.Close()

	c := New("secret-key", srv.Client(), WithBaseURL(srv.URL))
	if _, err := c.SearchMovie(context.Background(), "Jaws", 1975); err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	for key, want := range map[string]string{
		"api_key":              "secret-key",
		"query":                "Jaws",
		"primary_release_year": "1975",
	} {
		if v := got.Get(key); v != want {
			t.Errorf("%s = %q, want %q", key, v, want)
		}
	}
}

// The important half of D9: TMDB's fuzzy search resolves the raw folder title,
// so a " - " title must never reach the fallback. Verified against the live API
// for all 8 hyphenated titles in testdata/library/movies.
func TestSearchMovieSeparatorTitleMatchesOnFirstQuery(t *testing.T) {
	fs := newFixtureServer(t, map[string]string{
		"Avengers - Infinity War": "avengers_infinity_war_2018.json",
		// "Avengers Infinity War" is deliberately absent: if the client falls
		// back, the server reports an unexpected query and the test fails.
	})

	got, err := newTestClient(t, fs).SearchMovie(context.Background(), "Avengers - Infinity War", 2018)
	if err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	if got == nil || got.TMDBID != 299536 {
		t.Fatalf("match = %+v, want TMDBID 299536", got)
	}
	if got.Title != "Avengers: Infinity War" {
		t.Errorf("Title = %q, want the colon restored by TMDB itself", got.Title)
	}
	queries := fs.seen()
	if len(queries) != 1 {
		t.Fatalf("made %d requests (%q), want exactly 1 — no fallback", len(queries), queries)
	}
	if queries[0] != "Avengers - Infinity War" {
		t.Errorf("first query = %q, want the raw folder title", queries[0])
	}
}

func TestSearchMovieFallbackAfterCollapse(t *testing.T) {
	fs := newFixtureServer(t, map[string]string{
		"Captain America - The First Avenger": "empty.json",
		"Captain America The First Avenger":   "captain_america_2011.json",
	})

	got, err := newTestClient(t, fs).SearchMovie(context.Background(), "Captain America - The First Avenger", 2011)
	if err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	if got == nil || got.TMDBID != 1771 {
		t.Fatalf("match = %+v, want TMDBID 1771", got)
	}
	want := []string{"Captain America - The First Avenger", "Captain America The First Avenger"}
	if queries := fs.seen(); !equal(queries, want) {
		t.Errorf("queries = %q, want %q", queries, want)
	}
}

func TestSearchMovieZeroResults(t *testing.T) {
	t.Run("plain title makes one request", func(t *testing.T) {
		fs := newFixtureServer(t, map[string]string{"Backrooms": "empty.json"})

		got, err := newTestClient(t, fs).SearchMovie(context.Background(), "Backrooms", 2026)
		if err != nil {
			t.Fatalf("SearchMovie: %v, want nil — not found is not a failure", err)
		}
		if got != nil {
			t.Fatalf("match = %+v, want nil", got)
		}
		if n := len(fs.seen()); n != 1 {
			t.Errorf("made %d requests, want 1 — no collapse is possible", n)
		}
	})

	t.Run("separator title gives up after the fallback", func(t *testing.T) {
		fs := newFixtureServer(t, map[string]string{
			"In the Grey - Nothing": "empty.json",
			"In the Grey Nothing":   "empty.json",
		})

		got, err := newTestClient(t, fs).SearchMovie(context.Background(), "In the Grey - Nothing", 2026)
		if err != nil {
			t.Fatalf("SearchMovie: %v, want nil", err)
		}
		if got != nil {
			t.Fatalf("match = %+v, want nil", got)
		}
		if n := len(fs.seen()); n != 2 {
			t.Errorf("made %d requests, want 2 — one attempt plus one fallback", n)
		}
	})
}

// A 2026 release that TMDB happens to know under an older year must come back as
// "not found" rather than as the wrong film.
func TestSearchMovieWrongYearRejected(t *testing.T) {
	fs := newFixtureServer(t, map[string]string{
		"Interstellar": "interstellar_2014.json",
	})

	got, err := newTestClient(t, fs).SearchMovie(context.Background(), "Interstellar", 1999)
	if err != nil {
		t.Fatalf("SearchMovie: %v, want nil", err)
	}
	if got != nil {
		t.Fatalf("match = %+v, want nil for a year that disagrees", got)
	}
}

// TMDB orders by popularity, so the year decides — not the position.
func TestSearchMoviePicksYearNotFirstResult(t *testing.T) {
	fs := newFixtureServer(t, map[string]string{
		"Supergirl": "supergirl_all_years.json",
	})

	got, err := newTestClient(t, fs).SearchMovie(context.Background(), "Supergirl", 1984)
	if err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	if got == nil || got.TMDBID != 9651 {
		t.Fatalf("match = %+v, want the 1984 Supergirl (9651), not the leading 2026 entry", got)
	}
	if got.Year != 1984 {
		t.Errorf("Year = %d, want 1984", got.Year)
	}
}

func TestSearchMovieUnauthorized(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write(readFixture(t, "unauthorized.json"))
	}))
	defer srv.Close()

	c := New("bad-key", srv.Client(), WithBaseURL(srv.URL))
	got, err := c.SearchMovie(context.Background(), "Avengers - Infinity War", 2018)
	if got != nil {
		t.Errorf("match = %+v, want nil", got)
	}
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	// The operator must be able to tell a bad key from an unknown film.
	if !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("err = %v, want TMDB's own explanation included", err)
	}
	if requests != 1 {
		t.Errorf("made %d requests, want 1 — a 401 is not retried", requests)
	}
}

func TestSearchMovieUnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"status_message":"upstream is unwell"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New("test-key", srv.Client(), WithBaseURL(srv.URL))
	_, err := c.SearchMovie(context.Background(), "Jaws", 1975)
	if err == nil {
		t.Fatal("err = nil, want a 500 to surface")
	}
	if errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want a 500 to be distinct from a bad key", err)
	}
}

func TestSearchMovieRespectsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	c := New("super-secret-key", srv.Client(), WithBaseURL(srv.URL))
	_, err := c.SearchMovie(ctx, "Jaws", 1975)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	// A transport error stringifies the request URL unless we scrub it, and the
	// URL carries api_key= — this guards against leaking the key into logs.
	if strings.Contains(err.Error(), "super-secret-key") {
		t.Errorf("err = %v, leaks the API key", err)
	}
}

// The trap, pinned: only " - " is a substitution. Every other hyphen is real.
func TestCollapseSeparator(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Avengers - Infinity War", "Avengers Infinity War"},
		{"Spider-Man - No Way Home", "Spider-Man No Way Home"},
		{"X-Men Origins - Wolverine", "X-Men Origins Wolverine"},
		{"Spider-Man - Across the Spider-Verse", "Spider-Man Across the Spider-Verse"},
		{"Tom Clancy's Jack Ryan - Ghost War", "Tom Clancy's Jack Ryan Ghost War"},
		{"Deadpool & Wolverine", "Deadpool & Wolverine"},
		{"Iron Man 2", "Iron Man 2"},
	}
	for _, tc := range cases {
		if got := collapseSeparator(tc.in); got != tc.want {
			t.Errorf("collapseSeparator(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestReleaseYear(t *testing.T) {
	cases := map[string]int{
		"2014-11-05": 2014,
		"1975":       1975,
		"":           0,
		"n/a":        0,
		"abcd-01-01": 0,
	}
	for in, want := range cases {
		if got := releaseYear(in); got != want {
			t.Errorf("releaseYear(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestSearchMovieEmptyTitle(t *testing.T) {
	c := New("test-key", http.DefaultClient, WithBaseURL("http://127.0.0.1:0"))
	if _, err := c.SearchMovie(context.Background(), "  ", 2018); err == nil {
		t.Fatal("err = nil, want an error rather than a pointless request")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
