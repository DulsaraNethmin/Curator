package jellyfin

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
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

// TestLiveFindMovie runs against the real Jellyfin, and unlike the refresh above
// it is NOT skipped.
//
// That is the whole difference between the two calls: a refresh queues a scan of
// every library on a media server the household is using, and this one is a GET
// that changes nothing. T45 required the query to be established against the
// real 10.10.7 before the client was written, precisely because an API shape
// nobody has run is not a fact — and what it established is that the shape the
// task file sketched does not exist (docs/decisions.md D32).
//
// The assertion is deliberately library-independent. A TMDB id that cannot be in
// anybody's library must come back ErrNotFound, and that single answer proves
// the whole chain: the key was accepted, the path and parameters were right, the
// server answered 200, and the body decoded into items with provider ids. Every
// other outcome — a 401, a 404, a transport failure, a decode error — is a
// different error, so this cannot go green on a broken query.
func TestLiveFindMovie(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live Jellyfin check in -short mode")
	}
	baseURL, apiKey := liveConfig(t)
	if apiKey == "" {
		t.Skip("no JELLYFIN_API_KEY in the environment or ../../.env; skipping live check")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client := New(baseURL, apiKey, nil)

	// No film has this id, so a well-formed query can only answer ErrNotFound.
	const noSuchFilm = 999999999
	switch _, err := client.FindMovie(ctx, noSuchFilm, 2026); {
	case errors.Is(err, ErrNotFound):
		t.Logf("live lookup answered against %s", baseURL)
	case errors.Is(err, ErrUnauthorized):
		t.Fatalf("Jellyfin rejected the key: %v", err)
	case err != nil && isUnreachable(err):
		t.Skipf("live Jellyfin unreachable: %v", err)
	case err != nil:
		t.Fatalf("live lookup failed: %v", err)
	default:
		t.Fatalf("Jellyfin claims to hold tmdb %d", noSuchFilm)
	}

	// The positive half, when somebody names a film this Jellyfin actually has.
	// Optional because the library's contents are the household's business and
	// not this repo's, and a test that hard-codes them fails the day a film is
	// deleted.
	id, year := os.Getenv("JELLYFIN_LIVE_TMDB_ID"), os.Getenv("JELLYFIN_LIVE_YEAR")
	if id == "" {
		return
	}
	tmdbID, err := strconv.Atoi(id)
	if err != nil {
		t.Fatalf("JELLYFIN_LIVE_TMDB_ID = %q: %v", id, err)
	}
	releaseYear, _ := strconv.Atoi(year)

	item, err := client.FindMovie(ctx, tmdbID, releaseYear)
	if err != nil {
		t.Fatalf("live lookup for tmdb %d: %v", tmdbID, err)
	}
	if item.ID == "" {
		t.Fatalf("tmdb %d matched an item with no id", tmdbID)
	}
	t.Logf("tmdb %d is jellyfin item %s on server %s → %s",
		tmdbID, item.ID, item.ServerID, WebItemURL(baseURL, item))
}

// --- provisioning, against a throwaway and never anything else ---------------

// TestLiveProvision runs the whole measured sequence against a real Jellyfin and
// then proves the result with curator's own two calls.
//
// It is off unless JELLYFIN_PROVISION_URL names a server, and that variable is
// deliberately NOT the JELLYFIN_URL every other live test reads. This test
// completes a startup wizard, creates a library and mints a key: run against the
// household's server it would be the exact damage D34 exists to prevent, so
// there is no default, no fallback to .env, and pointing it at the configured
// Jellyfin is a failure rather than a surprise.
//
// Start the target with a FRESH config volume:
//
//	docker run -d --name jf-throwaway -p 8097:8096 \
//	  -v "$PWD/jf-config":/config -v "$PWD/jf-cache":/cache \
//	  -v "$HOME/curator-local/movies":/media/movies:ro \
//	  jellyfin/jellyfin:10.10.7
//	JELLYFIN_PROVISION_URL=http://127.0.0.1:8097 go test ./internal/jellyfin/ -run TestLiveProvision -v
//
// A REUSED config volume is the trap this test refuses to walk into: the wizard
// would already be complete and the run would silently exercise the adopt branch
// while reporting that it provisioned something.
func TestLiveProvision(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live Jellyfin provisioning in -short mode")
	}
	target := strings.TrimSpace(os.Getenv("JELLYFIN_PROVISION_URL"))
	if target == "" {
		t.Skip("no JELLYFIN_PROVISION_URL; skipping live provisioning (it needs a throwaway container)")
	}

	// The guard on the guard. A throwaway is something nobody is watching, and
	// the only way to be sure of that here is to refuse the one server we know
	// somebody is.
	configured, _ := liveConfig(t)
	if sameServer(target, configured) {
		t.Fatalf("JELLYFIN_PROVISION_URL is %s, which is the configured JELLYFIN_URL. "+
			"This test completes a startup wizard and mints keys; point it at a throwaway container.", target)
	}
	if strings.Contains(target, "192.168.1.26") {
		t.Fatalf("JELLYFIN_PROVISION_URL is %s, which is the Pi. Nothing on the Pi changes before phase 10.", target)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := NewProvisioner(target, "0.1.0", nil, logger)

	// Measured cold start on arm64: 17.6 s from docker run to a 200 here, so
	// this waits rather than fails.
	var status ServerStatus
	start := time.Now()
	for {
		var err error
		status, err = p.Status(ctx)
		if err == nil {
			break
		}
		// Both states mean "ask again": nothing listening yet, and listening
		// but still loading. Measured, a starting 10.10.7 spends most of its
		// cold start in the second one.
		if !errors.Is(err, ErrUnreachable) && !errors.Is(err, ErrNotReady) {
			t.Fatalf("live status: %v", err)
		}
		if ctx.Err() != nil {
			t.Fatalf("no Jellyfin answered at %s within %v", target, time.Since(start))
		}
		time.Sleep(time.Second)
	}
	t.Logf("jellyfin %s answered after %v (wizard completed: %t)",
		status.Version, time.Since(start).Round(100*time.Millisecond), status.WizardCompleted)

	if status.WizardCompleted {
		t.Fatalf("%s has already finished its wizard. Its config volume was reused, and this run "+
			"would test the adopt branch while claiming to test provisioning. Delete the volume.", target)
	}

	const (
		user        = "curator-live-test"
		password    = "provision-me-please"
		libraryName = "Movies"
	)
	libraryPath := strings.TrimSpace(os.Getenv("JELLYFIN_PROVISION_PATH"))
	if libraryPath == "" {
		libraryPath = "/media/movies"
	}

	if err := p.Configure(ctx, StartupConfiguration{}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := p.CreateAdmin(ctx, user, password); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	if err := p.EnableRemoteAccess(ctx); err != nil {
		t.Fatalf("EnableRemoteAccess: %v", err)
	}

	session, err := p.Authenticate(ctx, user, password)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if session.Token == "" || session.UserID == "" {
		t.Fatalf("session = %+v, want a token and a user id", session)
	}
	// The distinction that must survive a real server: a wrong password is 401
	// and never the 400 a missing header produces.
	if _, err := p.Authenticate(ctx, user, "not-the-password"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("a wrong password gave %v, want ErrBadCredentials", err)
	}

	if err := p.AddLibrary(ctx, session, libraryName, libraryPath, OnlyIfUnconfigured); err != nil {
		t.Fatalf("AddLibrary(%s): %v", libraryPath, err)
	}

	key, err := p.MintKey(ctx, session, KeyAppName, OnlyIfUnconfigured)
	if err != nil {
		t.Fatalf("MintKey: %v", err)
	}
	if key == "" {
		t.Fatal("MintKey returned an empty key with no error")
	}
	// Not idempotent on Jellyfin's side, so calling twice must reuse rather
	// than accumulate: two keys named curator, both with Id 0, is the measured
	// failure this defends against.
	again, err := p.MintKey(ctx, session, KeyAppName, OnlyIfUnconfigured)
	if err != nil {
		t.Fatalf("MintKey a second time: %v", err)
	}
	if again != key {
		t.Errorf("a second MintKey produced a different key; the server is now holding two")
	}

	if err := p.CompleteSetup(ctx); err != nil {
		t.Fatalf("CompleteSetup: %v", err)
	}

	// The guard, against a server that really did just finish its wizard.
	if err := p.CreateAdmin(ctx, "attacker", "x"); !errors.Is(err, ErrAlreadyConfigured) {
		t.Fatalf("CreateAdmin on a configured server gave %v, want ErrAlreadyConfigured — "+
			"measured, Jellyfin itself answers 204 and renames the admin", err)
	}
	after, err := p.Status(ctx)
	if err != nil {
		t.Fatalf("status after setup: %v", err)
	}
	if !after.WizardCompleted {
		t.Error("the wizard is still open after CompleteSetup")
	}
	// And the original credentials still work, which is the property the guard
	// is actually protecting.
	if _, err := p.Authenticate(ctx, user, password); err != nil {
		t.Errorf("the admin curator created can no longer sign in: %v", err)
	}

	// The minted key, through the code that will consume it: curator's only two
	// existing Jellyfin calls.
	client := New(target, key, nil)
	if err := client.RefreshLibrary(ctx); err != nil {
		t.Fatalf("RefreshLibrary with the minted key: %v", err)
	}
	const noSuchFilm = 999999999
	if _, err := client.FindMovie(ctx, noSuchFilm, 2026); !errors.Is(err, ErrNotFound) {
		t.Fatalf("FindMovie with the minted key: %v, want ErrNotFound", err)
	}
	t.Logf("the minted key works against RefreshLibrary and FindMovie")

	// The positive half — D32's key surviving a library curator created itself.
	// Optional, and it waits: the scan is queued, not immediate.
	id := os.Getenv("JELLYFIN_LIVE_TMDB_ID")
	if id == "" {
		t.Log("set JELLYFIN_LIVE_TMDB_ID to also check that the scan writes ProviderIds.Tmdb")
		return
	}
	tmdbID, err := strconv.Atoi(id)
	if err != nil {
		t.Fatalf("JELLYFIN_LIVE_TMDB_ID = %q: %v", id, err)
	}
	releaseYear, _ := strconv.Atoi(os.Getenv("JELLYFIN_LIVE_YEAR"))

	scanStart := time.Now()
	for {
		item, err := client.FindMovie(ctx, tmdbID, releaseYear)
		if err == nil {
			t.Logf("after %v the scan produced tmdb %d as item %s → %s",
				time.Since(scanStart).Round(time.Second), tmdbID, item.ID, WebItemURL(target, item))
			return
		}
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("lookup while waiting for the scan: %v", err)
		}
		if ctx.Err() != nil {
			t.Fatalf("tmdb %d never appeared: the library curator created scanned but wrote no ProviderIds.Tmdb, "+
				"which is what Open in Jellyfin is keyed on (D32)", tmdbID)
		}
		time.Sleep(2 * time.Second)
	}
}

// TestLiveAdopt runs the adopt branch against a throwaway whose wizard is
// ALREADY complete, which is the only honest way to test it without touching a
// server somebody is watching.
//
// It is off unless JELLYFIN_ADOPT_URL names one, and it refuses the configured
// Jellyfin and the Pi for the same reason TestLiveProvision does — more so, in
// fact: this branch exists precisely for servers in use, so the temptation to
// point it at a real one is the failure mode.
//
// Set one up with T64's own sequence and then reuse it:
//
//	docker run -d --name adopt-me -p 8097:8096 jellyfin/jellyfin:10.10.7
//	JELLYFIN_PROVISION_URL=http://127.0.0.1:8097 go test ./internal/jellyfin/ -run TestLiveProvision
//	JELLYFIN_ADOPT_URL=http://127.0.0.1:8097 \
//	  JELLYFIN_ADOPT_USER=curator-live-test JELLYFIN_ADOPT_PASSWORD=provision-me-please \
//	  go test ./internal/jellyfin/ -run TestLiveAdopt -v
//
// **The assertion this test exists for is the last one**: the credentials that
// worked before adoption still work after it. Everything else here is a feature
// working; that one is a household not being locked out of their media server.
func TestLiveAdopt(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live Jellyfin adoption in -short mode")
	}
	target := strings.TrimSpace(os.Getenv("JELLYFIN_ADOPT_URL"))
	if target == "" {
		t.Skip("no JELLYFIN_ADOPT_URL; skipping live adoption (it needs a throwaway container)")
	}
	user := strings.TrimSpace(os.Getenv("JELLYFIN_ADOPT_USER"))
	password := os.Getenv("JELLYFIN_ADOPT_PASSWORD")
	if user == "" || password == "" {
		t.Fatal("JELLYFIN_ADOPT_URL is set without JELLYFIN_ADOPT_USER and JELLYFIN_ADOPT_PASSWORD")
	}

	configured, _ := liveConfig(t)
	if sameServer(target, configured) {
		t.Fatalf("JELLYFIN_ADOPT_URL is %s, which is the configured JELLYFIN_URL. "+
			"This test mints a key and may add a library; point it at a throwaway container.", target)
	}
	if strings.Contains(target, "192.168.1.26") {
		t.Fatalf("JELLYFIN_ADOPT_URL is %s, which is the Pi. Nothing on the Pi changes before phase 10.", target)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := NewProvisioner(target, "0.1.0", nil, logger)

	status, err := p.Status(ctx)
	if err != nil {
		t.Fatalf("live status: %v", err)
	}
	if !status.WizardCompleted {
		t.Fatalf("%s has not finished its wizard, so this would not be adoption. "+
			"Run TestLiveProvision against it first.", target)
	}

	// Every startup endpoint, refused, against a real server that really would
	// have accepted them. This is the measurement D34 is built on: Jellyfin
	// itself answers 204 to POST /Startup/User here and renames the admin.
	for name, call := range map[string]func() error{
		"Configure":          func() error { return p.Configure(ctx, StartupConfiguration{}) },
		"CreateAdmin":        func() error { return p.CreateAdmin(ctx, "attacker", "x") },
		"EnableRemoteAccess": func() error { return p.EnableRemoteAccess(ctx) },
		"CompleteSetup":      func() error { return p.CompleteSetup(ctx) },
	} {
		if err := call(); !errors.Is(err, ErrAlreadyConfigured) {
			t.Fatalf("%s against a configured server gave %v, want ErrAlreadyConfigured", name, err)
		}
	}

	session, err := p.Authenticate(ctx, user, password)
	if err != nil {
		t.Fatalf("Authenticate as the account that already exists: %v", err)
	}
	if _, err := p.Authenticate(ctx, user, "not-the-password"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("a wrong password gave %v, want ErrBadCredentials — this is the branch where a "+
			"real person's typo arrives", err)
	}

	before, err := p.Libraries(ctx, session)
	if err != nil {
		t.Fatalf("Libraries: %v", err)
	}
	t.Logf("that server holds %d libraries: %v", len(before), libraryNames(before))

	// The path question, against a real filesystem. "/" is the control: a server
	// that 404s it has no such endpoint and every answer here is unknown.
	for _, tc := range []struct {
		path string
		want PathAnswer
	}{
		{"/", PathPresent},
		{"/media", PathPresent},
		{"/no/such/path/on/any/jellyfin", PathAbsent},
	} {
		got, err := p.PathExists(ctx, session, tc.path)
		if err != nil {
			t.Fatalf("PathExists(%s): %v", tc.path, err)
		}
		if got != tc.want {
			t.Errorf("PathExists(%s) = %v, want %v", tc.path, got, tc.want)
		}
	}

	key, err := p.MintKey(ctx, session, KeyAppName, AdoptConfigured)
	if err != nil {
		t.Fatalf("MintKey with the adopt opt-in: %v", err)
	}
	if key == "" {
		t.Fatal("MintKey returned an empty key with no error")
	}
	// Curator's own read, with the key that was just minted — the check that
	// separates a working setup screen from a broken Open in Jellyfin link.
	if _, err := New(target, key, nil).FindMovie(ctx, 999999999, 2026); !errors.Is(err, ErrNotFound) {
		t.Fatalf("FindMovie with the adopted key: %v, want ErrNotFound", err)
	}

	// Adding a library is additive and does not disturb the ones already there.
	if path := strings.TrimSpace(os.Getenv("JELLYFIN_ADOPT_PATH")); path != "" {
		if err := p.AddLibrary(ctx, session, "curator", path, AdoptConfigured); err != nil {
			t.Fatalf("AddLibrary(%s) with the adopt opt-in: %v", path, err)
		}
		after, err := p.Libraries(ctx, session)
		if err != nil {
			t.Fatalf("Libraries after adding one: %v", err)
		}
		if len(after) != len(before)+1 {
			t.Errorf("libraries went from %v to %v; adding one may not disturb the others",
				libraryNames(before), libraryNames(after))
		}
	}

	// **The single check this task exists for.** Everything above can work and
	// still leave somebody locked out of their own media server.
	if _, err := p.Authenticate(ctx, user, password); err != nil {
		t.Fatalf("the credentials that existed before adoption no longer work: %v — "+
			"this is exactly the damage D34's guard exists to prevent", err)
	}
	t.Log("the admin account that existed before adoption still signs in")
}

func libraryNames(libraries []Library) []string {
	out := make([]string, 0, len(libraries))
	for _, library := range libraries {
		out = append(out, library.Name)
	}
	return out
}

// sameServer compares two base URLs the way a careless copy-paste would differ:
// a trailing slash, a case difference in the scheme or host.
func sameServer(a, b string) bool {
	normalise := func(s string) string {
		return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "/"))
	}
	return normalise(a) != "" && normalise(a) == normalise(b)
}
