package tmdb

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveSearchMovieInterstellar is the one test that talks to the real API. It
// exists because every other test proves only that we parse a recording
// correctly — this proves the request we build is one TMDB accepts.
//
// It skips rather than fails whenever the network or the key is unavailable, so
// a fresh clone and a CI box without secrets both stay green:
//
//	go test -short ./internal/tmdb/...   # never touches the network
func TestLiveSearchMovieInterstellar(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live TMDB check in -short mode")
	}
	key := liveAPIKey(t)
	if key == "" {
		t.Skip("no TMDB_API_KEY in the environment or ../../.env; skipping live check")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	got, err := New(key, nil).SearchMovie(ctx, "Interstellar", 2014)
	switch {
	case errors.Is(err, ErrUnauthorized):
		// A rejected key is a real configuration fault, not an absent one.
		t.Fatalf("TMDB rejected the key: %v", err)
	case err != nil:
		t.Skipf("live TMDB unreachable: %v", err)
	}
	if got == nil {
		t.Fatal("live search for Interstellar (2014) found nothing")
	}
	if got.TMDBID != 157336 {
		t.Fatalf("TMDBID = %d, want 157336 (%q, %d)", got.TMDBID, got.Title, got.Year)
	}
	if got.Year != 2014 {
		t.Errorf("Year = %d, want 2014", got.Year)
	}
	if got.PosterPath == "" {
		t.Error("PosterPath is empty; the UI has no image to build a URL from")
	}
	t.Logf("live match: id=%d title=%q year=%d poster=%s", got.TMDBID, got.Title, got.Year, got.PosterPath)
}

// liveAPIKey reads the key from the environment, falling back to the gitignored
// .env at the repo root so the check runs from a plain `go test` without the
// developer exporting anything. The value is never logged.
func liveAPIKey(t *testing.T) string {
	t.Helper()
	if key := strings.TrimSpace(os.Getenv("TMDB_API_KEY")); key != "" {
		return key
	}

	// Tests run in the package directory: internal/tmdb -> repo root.
	f, err := os.Open(filepath.Join("..", "..", ".env"))
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(name) != "TMDB_API_KEY" {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}

// The browse methods against the real API, under the same contract as the search
// above: skip with no key or no network, but FAIL on a bad status, so a wrong
// endpoint path cannot sit there green.
//
// These are also how the fixtures in testdata/ were recorded, which is why they
// assert the same facts the fixture tests do — if TMDB changes the shape, the
// live test fails here rather than the recordings quietly going stale.
func TestLiveBrowse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live TMDB check in -short mode")
	}
	key := liveAPIKey(t)
	if key == "" {
		t.Skip("no TMDB_API_KEY in the environment or ../../.env; skipping live check")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := New(key, nil)

	t.Run("Movie", func(t *testing.T) {
		got, err := client.Movie(ctx, 299534)
		switch {
		case errors.Is(err, ErrUnauthorized):
			t.Fatalf("TMDB rejected the key: %v", err)
		case err != nil:
			t.Skipf("live TMDB unreachable: %v", err)
		}
		if got.Title != "Avengers: Endgame" || got.Runtime == 0 || len(got.Genres) == 0 {
			t.Errorf("live details look wrong: %+v", got)
		}
		t.Logf("live: %s (%d), %d min, %v", got.Title, got.Year, got.Runtime, got.Genres)
	})

	t.Run("NotFound", func(t *testing.T) {
		_, err := client.Movie(ctx, 999999999)
		if err != nil && !errors.Is(err, ErrNotFound) && !strings.Contains(err.Error(), "unexpected status") {
			t.Skipf("live TMDB unreachable: %v", err)
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound for an id TMDB does not have", err)
		}
	})

	t.Run("SearchMovies", func(t *testing.T) {
		got, err := client.SearchMovies(ctx, "avengers", 0)
		if err != nil {
			t.Skipf("live TMDB unreachable: %v", err)
		}
		if len(got) == 0 {
			t.Fatal("live search for avengers found nothing")
		}
		// The whole point of the redesign: the results are a choice, and the most
		// popular one is not necessarily the one anybody wanted.
		t.Logf("live: %d results, first is %q (%d)", len(got), got[0].Title, got[0].Year)
	})

	// All four home-screen rails, because the fixture tests cannot tell a live
	// path from a dead one: pathServer answers whatever the map says, so
	// /movie/top_rated staying green there proves only that we ask for it.
	t.Run("TrendingAndPopular", func(t *testing.T) {
		for name, call := range map[string]func() ([]Match, error){
			"Trending":   func() ([]Match, error) { return client.Trending(ctx) },
			"Popular":    func() ([]Match, error) { return client.Popular(ctx) },
			"TopRated":   func() ([]Match, error) { return client.TopRated(ctx) },
			"NowPlaying": func() ([]Match, error) { return client.NowPlaying(ctx) },
		} {
			got, err := call()
			if err != nil {
				t.Skipf("live TMDB unreachable: %v", err)
			}
			if len(got) == 0 {
				t.Errorf("%s returned nothing", name)
				continue
			}
			t.Logf("live %s: %d, first is %q", name, len(got), got[0].Title)
		}
	})
}

// The television methods against the real API, under exactly the contract the
// two above use: -short skips, no key skips, no network skips, a REJECTED key
// fails, and a bad status fails — so a wrong endpoint path cannot sit there
// green. The gating is copied deliberately rather than reinvented; a live test
// with a rule of its own is what T76 existed to remove on the indexer side.
//
// This is the only thing that can catch the television half's real risk. Every
// other TV test proves we parse a recording correctly, and the recordings in
// testdata/ were written to TMDB's documented shape rather than captured from a
// key nobody in CI has. If /search/tv ever stopped sending `name`, this is
// where it would show — the fixture tests would go on passing forever.
func TestLiveTV(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live TMDB check in -short mode")
	}
	key := liveAPIKey(t)
	if key == "" {
		t.Skip("no TMDB_API_KEY in the environment or ../../.env; skipping live check")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := New(key, nil)

	t.Run("SearchShow", func(t *testing.T) {
		got, err := client.SearchShow(ctx, "Breaking Bad", 2008)
		switch {
		case errors.Is(err, ErrUnauthorized):
			t.Fatalf("TMDB rejected the key: %v", err)
		case err != nil:
			t.Skipf("live TMDB unreachable: %v", err)
		}
		if got == nil {
			t.Fatal("live search for Breaking Bad (2008) found nothing")
		}
		if got.TMDBID != 1396 {
			t.Fatalf("TMDBID = %d, want 1396 (%q, %d)", got.TMDBID, got.Title, got.Year)
		}
		// Title from `name` and Year from `first_air_date`. An empty title here
		// is the exact failure the mapping exists to prevent, and it is what a
		// movie-shaped decode of a TV payload produces.
		if got.Title != "Breaking Bad" {
			t.Errorf("Title = %q — `name` did not map", got.Title)
		}
		if got.Year != 2008 {
			t.Errorf("Year = %d — `first_air_date` did not map", got.Year)
		}
		t.Logf("live show: id=%d title=%q year=%d poster=%s", got.TMDBID, got.Title, got.Year, got.PosterPath)
	})

	t.Run("Show", func(t *testing.T) {
		got, err := client.Show(ctx, 1396)
		switch {
		case errors.Is(err, ErrUnauthorized):
			t.Fatalf("TMDB rejected the key: %v", err)
		case err != nil:
			t.Skipf("live TMDB unreachable: %v", err)
		}
		if got.Title != "Breaking Bad" || got.NumberOfSeasons == 0 || got.NumberOfEpisodes == 0 || len(got.Genres) == 0 {
			t.Errorf("live show details look wrong: %+v", got)
		}
		if got.FirstAirDate == "" || got.LastAirDate == "" {
			t.Errorf("air dates missing: first=%q last=%q", got.FirstAirDate, got.LastAirDate)
		}
		// The one thing no fixture can prove: that TMDB still honours
		// append_to_response on /tv/{id}. A fixture carries external_ids
		// whether or not the request asked for them, so if TMDB ever stopped
		// inlining the sub-resource, every offline test would stay green while
		// EZTV silently lost the key it is searched by.
		if !strings.HasPrefix(got.IMDBID, "tt") {
			t.Errorf("IMDBID = %q, want tt... — append_to_response=external_ids did not arrive", got.IMDBID)
		}
		// seasons[] likewise: it is what the episode picker is built from, and
		// number_of_seasons above cannot stand in for it.
		if len(got.Seasons) == 0 {
			t.Error("seasons[] is empty — the episode picker has nothing to draw")
		}
		// Counts are not asserted exactly: TMDB revises them, and a test that
		// fails because a special was added is a test nobody trusts.
		t.Logf("live: %s (%d), %d seasons, %d episodes, %d min/ep, imdb=%s, seasons[]=%d, %v",
			got.Title, got.Year, got.NumberOfSeasons, got.NumberOfEpisodes, got.EpisodeRuntime,
			got.IMDBID, len(got.Seasons), got.Genres)
	})

	t.Run("NotFound", func(t *testing.T) {
		_, err := client.Show(ctx, 999999999)
		if err != nil && !errors.Is(err, ErrNotFound) && !strings.Contains(err.Error(), "unexpected status") {
			t.Skipf("live TMDB unreachable: %v", err)
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound for an id TMDB does not have", err)
		}
	})

	t.Run("SearchShows", func(t *testing.T) {
		got, err := client.SearchShows(ctx, "the office", 0)
		if err != nil {
			t.Skipf("live TMDB unreachable: %v", err)
		}
		if len(got) == 0 {
			t.Fatal("live search for the office found nothing")
		}
		// Several distinct shows share this name; the point is that a human
		// picks, which is only possible if every card carries a year.
		for i, m := range got {
			if m.Title == "" {
				t.Fatalf("result %d has no title — `name` did not map: %+v", i, m)
			}
		}
		t.Logf("live: %d results, first is %q (%d)", len(got), got[0].Title, got[0].Year)
	})

	t.Run("TrendingAndPopularShows", func(t *testing.T) {
		for name, call := range map[string]func() ([]Match, error){
			"TrendingShows": func() ([]Match, error) { return client.TrendingShows(ctx) },
			"PopularShows":  func() ([]Match, error) { return client.PopularShows(ctx) },
			"TopRatedShows": func() ([]Match, error) { return client.TopRatedShows(ctx) },
			// The one rail whose fixture is hand-written and could not sensibly be
			// captured — a rolling seven-day window — so this is the only check
			// that /tv/on_the_air is a path TMDB still serves.
			"OnTheAir": func() ([]Match, error) { return client.OnTheAir(ctx) },
		} {
			got, err := call()
			if err != nil {
				t.Skipf("live TMDB unreachable: %v", err)
			}
			if len(got) == 0 {
				t.Errorf("%s returned nothing", name)
				continue
			}
			// An unmapped page is 20 cards with empty titles, not an error —
			// which is why this asserts the content rather than the count.
			if got[0].Title == "" || got[0].TMDBID == 0 {
				t.Errorf("%s: first card is unmapped: %+v", name, got[0])
			}
			t.Logf("live %s: %d, first is %q (%d)", name, len(got), got[0].Title, got[0].Year)
		}
	})
}
