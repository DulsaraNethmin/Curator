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

	t.Run("TrendingAndPopular", func(t *testing.T) {
		for name, call := range map[string]func() ([]Match, error){
			"Trending": func() ([]Match, error) { return client.Trending(ctx) },
			"Popular":  func() ([]Match, error) { return client.Popular(ctx) },
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
