package tmdb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// pathServer serves a recorded body per request PATH, which is what the browse
// endpoints need — newFixtureServer keys on the `query` parameter, because
// SearchMovie only ever hits one path.
func pathServer(t *testing.T, byPath map[string]string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := byPath[r.URL.Path]
		if !ok {
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(name, "notfound") {
			w.WriteHeader(http.StatusNotFound)
		}
		w.Write(readFixture(t, name))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func browseClient(t *testing.T, byPath map[string]string) *Client {
	t.Helper()
	return New("k", nil, WithBaseURL(pathServer(t, byPath).URL))
}

// THE test of this file, and the reason `get` exists.
//
// The query string carries api_key=, and *url.Error stringifies the whole URL,
// so a transport failure prints the key into the logs unless it is scrubbed.
// Every endpoint is another chance to forget, and T89 doubled the count by
// adding the television half. This is table-driven over every exported method
// so the next one cannot quietly skip it: an endpoint missing from this table
// is an endpoint that can leak the key, and nothing else in the suite would
// catch it.
func TestEveryEndpointScrubsTheAPIKey(t *testing.T) {
	const key = "super-secret-key"

	// A server that never answers, so every call ends as a transport error with
	// the URL in it — which is exactly the case scrubURL exists for.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	client := New(key, nil, WithBaseURL(srv.URL))

	calls := map[string]func(context.Context) error{
		"SearchMovie": func(ctx context.Context) error {
			_, err := client.SearchMovie(ctx, "Interstellar", 2014)
			return err
		},
		"SearchMovies": func(ctx context.Context) error {
			_, err := client.SearchMovies(ctx, "avengers", 0)
			return err
		},
		"Movie": func(ctx context.Context) error {
			_, err := client.Movie(ctx, 299534)
			return err
		},
		"Trending": func(ctx context.Context) error {
			_, err := client.Trending(ctx)
			return err
		},
		"Popular": func(ctx context.Context) error {
			_, err := client.Popular(ctx)
			return err
		},
		"TopRated": func(ctx context.Context) error {
			_, err := client.TopRated(ctx)
			return err
		},
		"NowPlaying": func(ctx context.Context) error {
			_, err := client.NowPlaying(ctx)
			return err
		},
		"SearchShow": func(ctx context.Context) error {
			_, err := client.SearchShow(ctx, "Breaking Bad", 2008)
			return err
		},
		"SearchShows": func(ctx context.Context) error {
			_, err := client.SearchShows(ctx, "the office", 0)
			return err
		},
		"Show": func(ctx context.Context) error {
			_, err := client.Show(ctx, 1396)
			return err
		},
		"TrendingShows": func(ctx context.Context) error {
			_, err := client.TrendingShows(ctx)
			return err
		},
		"PopularShows": func(ctx context.Context) error {
			_, err := client.PopularShows(ctx)
			return err
		},
		"TopRatedShows": func(ctx context.Context) error {
			_, err := client.TopRatedShows(ctx)
			return err
		},
		"OnTheAir": func(ctx context.Context) error {
			_, err := client.OnTheAir(ctx)
			return err
		},
	}

	for name, call := range calls {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		err := call(ctx)
		cancel()

		if err == nil {
			t.Errorf("%s: no error from a server that never answers", name)
			continue
		}
		if strings.Contains(err.Error(), key) {
			t.Errorf("%s LEAKED THE API KEY: %v", name, err)
		}
		// And the cause survives the scrub, so callers can still branch on it.
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: err = %v, want it to wrap context.DeadlineExceeded", name, err)
		}
	}
}

// SearchMovies is not SearchMovie: it returns everything, in TMDB's order, and
// never rejects a year.
func TestSearchMoviesReturnsEveryResult(t *testing.T) {
	client := browseClient(t, map[string]string{"/search/movie": "search_avengers.json"})

	got, err := client.SearchMovies(context.Background(), "avengers", 0)
	if err != nil {
		t.Fatalf("SearchMovies: %v", err)
	}
	if len(got) != 20 {
		t.Fatalf("got %d results, want the whole page of 20", len(got))
	}

	// The point of the whole redesign, pinned: TMDB's first result for "avengers"
	// is Avengers: Doomsday (2026). The old flow took position one and recorded
	// the wrong film. Here it is one card among twenty, and a human picks.
	if got[0].Title != "Avengers: Doomsday" {
		t.Errorf("first result = %q, want Avengers: Doomsday — the fixture pins why this exists", got[0].Title)
	}
	var endgame *Match
	for i := range got {
		if got[i].TMDBID == 299534 {
			endgame = &got[i]
		}
	}
	if endgame == nil {
		t.Fatal("Avengers: Endgame is not in the results")
	}
	if endgame.Year != 2019 || endgame.Title != "Avengers: Endgame" {
		t.Errorf("endgame = %+v", endgame)
	}
	// Both fields were in the fixtures all along and merely dropped before.
	if endgame.BackdropPath == "" {
		t.Error("BackdropPath is empty; the movie screen has no banner")
	}
	if endgame.VoteAverage == 0 {
		t.Error("VoteAverage is zero")
	}
}

func TestSearchMoviesRejectsAnEmptyQuery(t *testing.T) {
	// No server: it must fail before making a request.
	if _, err := New("k", nil).SearchMovies(context.Background(), "   ", 0); err == nil {
		t.Error("an empty query was accepted")
	}
}

func TestMovieDecodesEverythingTheScreenShows(t *testing.T) {
	client := browseClient(t, map[string]string{"/movie/299534": "movie_299534.json"})

	got, err := client.Movie(context.Background(), 299534)
	if err != nil {
		t.Fatalf("Movie: %v", err)
	}

	if got.TMDBID != 299534 || got.Title != "Avengers: Endgame" || got.Year != 2019 {
		t.Errorf("identity = %d %q %d", got.TMDBID, got.Title, got.Year)
	}
	if got.Runtime != 181 {
		t.Errorf("Runtime = %d, want 181", got.Runtime)
	}
	if got.Tagline != "Avenge the fallen." {
		t.Errorf("Tagline = %q", got.Tagline)
	}
	if got.Status != "Released" {
		t.Errorf("Status = %q", got.Status)
	}
	if got.ReleaseDate != "2019-04-24" {
		t.Errorf("ReleaseDate = %q — the full date, not just the year", got.ReleaseDate)
	}
	if got.OriginalLanguage != "en" {
		t.Errorf("OriginalLanguage = %q", got.OriginalLanguage)
	}
	if len(got.Genres) == 0 || got.Genres[0] != "Adventure" {
		t.Errorf("Genres = %v, want them in TMDB's own order", got.Genres)
	}
	if len(got.Studios) == 0 || got.Studios[0] != "Marvel Studios" {
		t.Errorf("Studios = %v", got.Studios)
	}
	if got.BackdropPath == "" || got.PosterPath == "" {
		t.Error("the screen has no images")
	}
}

// An id comes from a URL a human can type, so "no such film" must be tellable
// from "TMDB is having a bad day".
func TestMovieNotFound(t *testing.T) {
	client := browseClient(t, map[string]string{"/movie/999999999": "movie_notfound.json"})

	_, err := client.Movie(context.Background(), 999999999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	// TMDB's own words survive, because they are better than ours.
	if !strings.Contains(err.Error(), "could not be found") {
		t.Errorf("err = %q, want TMDB's message", err)
	}
}

func TestMovieRejectsANonID(t *testing.T) {
	for _, id := range []int{0, -1} {
		if _, err := New("k", nil).Movie(context.Background(), id); err == nil {
			t.Errorf("Movie(%d) was accepted", id)
		}
	}
}

// The film lists, and the PATHS they ask for.
//
// pathServer fails the test on any path not in this map, so the map is the
// assertion: TMDB puts the media type in a different position for trending
// (/trending/movie/week) than for everything else (/movie/popular,
// /movie/top_rated, /movie/now_playing), and a method reaching for the wrong
// shape is caught here rather than against the live API.
//
// top_rated and now_playing are served the popular fixture on purpose. The
// envelope is identical — TMDB returns the same paged `results` of the same
// movie objects for all four — so a third and fourth captured page would assert
// nothing the first two do not, at the cost of two more files to keep. What is
// NOT shared is the path each method asks for, and that is what this pins.
func TestTrendingAndPopular(t *testing.T) {
	client := browseClient(t, map[string]string{
		"/trending/movie/week": "trending_week.json",
		"/movie/popular":       "popular.json",
		"/movie/top_rated":     "popular.json",
		"/movie/now_playing":   "popular.json",
	})
	ctx := context.Background()

	for name, call := range map[string]func() ([]Match, error){
		"Trending":   func() ([]Match, error) { return client.Trending(ctx) },
		"Popular":    func() ([]Match, error) { return client.Popular(ctx) },
		"TopRated":   func() ([]Match, error) { return client.TopRated(ctx) },
		"NowPlaying": func() ([]Match, error) { return client.NowPlaying(ctx) },
	} {
		got, err := call()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(got) != 20 {
			t.Errorf("%s: got %d, want a page of 20", name, len(got))
			continue
		}
		if got[0].TMDBID == 0 || got[0].Title == "" {
			t.Errorf("%s: first card is empty: %+v", name, got[0])
		}
	}
}

// Every browse method goes through the same transport, so one 401 test covers
// the switch rather than four.
func TestBrowseReportsARejectedKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write(readFixture(t, "unauthorized.json"))
	}))
	defer srv.Close()

	client := New("wrong", nil, WithBaseURL(srv.URL))
	ctx := context.Background()

	for name, err := range map[string]error{
		"SearchMovies":  second(client.SearchMovies(ctx, "avengers", 0)),
		"Movie":         second(client.Movie(ctx, 299534)),
		"Trending":      second(client.Trending(ctx)),
		"Popular":       second(client.Popular(ctx)),
		"TopRated":      second(client.TopRated(ctx)),
		"NowPlaying":    second(client.NowPlaying(ctx)),
		"SearchShow":    second(client.SearchShow(ctx, "Breaking Bad", 2008)),
		"SearchShows":   second(client.SearchShows(ctx, "the office", 0)),
		"Show":          second(client.Show(ctx, 1396)),
		"TrendingShows": second(client.TrendingShows(ctx)),
		"PopularShows":  second(client.PopularShows(ctx)),
		"TopRatedShows": second(client.TopRatedShows(ctx)),
		"OnTheAir":      second(client.OnTheAir(ctx)),
	} {
		if !errors.Is(err, ErrUnauthorized) {
			t.Errorf("%s: err = %v, want ErrUnauthorized", name, err)
		}
	}
}

// second discards a call's value and keeps its error, so the table above reads
// as a list rather than four near-identical blocks.
func second[T any](_ T, err error) error { return err }

// An empty page is [] and never nil, because the UI iterates it.
func TestBrowseEmptyPageIsNotNil(t *testing.T) {
	client := browseClient(t, map[string]string{"/movie/popular": "empty.json"})

	got, err := client.Popular(context.Background())
	if err != nil {
		t.Fatalf("Popular: %v", err)
	}
	if got == nil {
		t.Error("an empty page came back nil")
	}
	if len(got) != 0 {
		t.Errorf("got %d results, want 0", len(got))
	}
}
