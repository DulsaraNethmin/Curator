package jellyfin

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

// TestLiveRefreshLibrary is the one test that would talk to the real Jellyfin.
// It exists for the same reason internal/tmdb's does: every other test here
// proves only that we handle a recorded shape correctly, and this would prove
// the request we build is one Jellyfin accepts.
//
// It is DELIBERATELY DISABLED for phase 4. Unlike a TMDB search, a refresh
// MUTATES Jellyfin — it queues a scan of every library on a media server the
// household is using — and the Pi is read-only until phase 6 cutover
// (docs/phase-4.md). There is also no API key yet.
//
// Phase 6 enables this by deleting exactly one statement, the t.Skip below.
// Everything under it is written to run, and follows internal/tmdb/live_test.go's
// contract: skip under -short, skip when the URL or key is absent, skip when
// the host is unreachable — but FAIL on a bad status, so a dead URL cannot sit
// there green.
func TestLiveRefreshLibrary(t *testing.T) {
	t.Skip("phase 4 does not touch the Pi: a refresh mutates Jellyfin, and the *arr stack keeps serving until phase 6 cutover. Delete this line to enable.")

	if testing.Short() {
		t.Skip("skipping live Jellyfin check in -short mode")
	}
	baseURL, apiKey := liveConfig(t)
	if apiKey == "" {
		t.Skip("no JELLYFIN_API_KEY in the environment or ../../.env; skipping live check")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	err := New(baseURL, apiKey, nil).RefreshLibrary(ctx)
	switch {
	case errors.Is(err, ErrUnauthorized):
		// A rejected key is a real configuration fault, not an absent one.
		t.Fatalf("Jellyfin rejected the key: %v", err)
	case err != nil && isUnreachable(err):
		t.Skipf("live Jellyfin unreachable: %v", err)
	case err != nil:
		// A bad status is a failure, not a skip. This is the line that stops a
		// dead base URL from staying green for months.
		t.Fatalf("live refresh failed: %v", err)
	}
	t.Logf("live refresh queued against %s", baseURL)
}

// isUnreachable distinguishes "there is no Jellyfin here" — worth skipping —
// from "Jellyfin answered something wrong", which is worth failing. A status
// error is already formatted with the word "answered", so anything else is a
// transport failure.
func isUnreachable(err error) bool {
	return !strings.Contains(err.Error(), "answered")
}

// liveConfig reads the URL and key from the environment, falling back to the
// gitignored .env at the repo root so the check runs from a plain `go test`
// without the developer exporting anything. The key is never logged.
func liveConfig(t *testing.T) (baseURL, apiKey string) {
	t.Helper()

	baseURL = strings.TrimSpace(os.Getenv("JELLYFIN_URL"))
	apiKey = strings.TrimSpace(os.Getenv("JELLYFIN_API_KEY"))
	if baseURL == "" {
		baseURL = dotEnv(t, "JELLYFIN_URL")
	}
	if apiKey == "" {
		apiKey = dotEnv(t, "JELLYFIN_API_KEY")
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return baseURL, apiKey
}

// dotEnv reads one value out of the repo-root .env. Tests run in the package
// directory: internal/jellyfin -> repo root.
func dotEnv(t *testing.T, key string) string {
	t.Helper()

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
		if !found || strings.TrimSpace(name) != key {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}
