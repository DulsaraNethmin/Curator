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
