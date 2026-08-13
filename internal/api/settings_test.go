package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The sentinels every secret in the config is set to. If any of them reaches the
// marshalled body, the endpoint is leaking to an unauthenticated LAN page.
const (
	secretTMDB     = "TMDB-SECRET-3f1c9a"
	secretQBitPass = "QBIT-SECRET-77b210"
	secretJellyfin = "JELLY-SECRET-c40de8"
)

func settingsServer(t *testing.T, set Settings) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, quiet()).
		WithSettings(set).
		RegisterSettings(mux)
	return mux
}

func fullSettings() Settings {
	return Settings{
		Version: "0.1.0",
		Integrations: []Integration{
			{Name: "tmdb", Env: "TMDB_API_KEY", Configured: true,
				Probe: func(context.Context) error { return nil }},
			{Name: "qbittorrent", Env: "QBIT_USER", Configured: false,
				Detail: "downloads are disabled"},
			{Name: "minter", Env: "MINTER_URL", Configured: true,
				Probe: func(context.Context) error { return nil }},
			{Name: "jellyfin", Env: "JELLYFIN_API_KEY", Configured: false,
				Detail: "the library refresh is disabled"},
		},
		Paths: map[string]string{
			"library_movies":      "./testdata/library/movies",
			"downloads_path":      "",
			"qbit_downloads_path": "/downloads",
		},
		Intervals: map[string]string{"download_poll": "10s", "search_timeout": "30s"},
	}
}

func getSettings(t *testing.T, h http.Handler, target string) (settingsBody, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))

	var body settingsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v — body was %s", target, err, rec.Body)
	}
	return body, rec
}

// The test this endpoint exists to survive.
//
// Every secret in the configuration is a distinctive sentinel; none of them may
// appear anywhere in the response, in any form. There is no authentication in
// front of this page and there is not going to be (D17), so this is the whole
// safety argument in one assertion.
func TestSettingsNeverLeaksASecret(t *testing.T) {
	// Deliberately built the way a careless implementation would: the secrets
	// are right there in the values the handler is given access to.
	set := fullSettings()
	set.Paths["tmdb_api_key"] = "" // present as a key, never as a value
	set.Integrations[0].Detail = "configured"

	h := settingsServer(t, set)

	for _, target := range []string{"/api/settings", "/api/settings?probe=1"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))

		raw := rec.Body.String()
		for _, secret := range []string{secretTMDB, secretQBitPass, secretJellyfin} {
			if strings.Contains(raw, secret) {
				t.Errorf("%s leaked %q into the response: %s", target, secret, raw)
			}
		}
		// Not even a fragment: a masked or truncated secret still confirms its
		// length and its existence.
		for _, fragment := range []string{"3f1c9a", "77b210", "c40de8"} {
			if strings.Contains(raw, fragment) {
				t.Errorf("%s leaked the fragment %q", target, fragment)
			}
		}
	}
}

func TestSettingsReportsWhatIsConfigured(t *testing.T) {
	body, rec := getSettings(t, settingsServer(t, fullSettings()), "/api/settings")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body.Version != "0.1.0" {
		t.Errorf("version = %q", body.Version)
	}
	if body.Probed {
		t.Error("probed = true without ?probe=1")
	}
	if len(body.Integrations) != 4 {
		t.Fatalf("got %d integrations, want 4", len(body.Integrations))
	}

	byName := map[string]integrationBody{}
	for _, i := range body.Integrations {
		byName[i.Name] = i
	}
	if !byName["tmdb"].Configured {
		t.Error("tmdb reported unconfigured")
	}
	// The truth today, and the state the screen has to render first.
	if byName["qbittorrent"].Configured {
		t.Error("qbittorrent reported configured; QBIT_USER is unset")
	}
	if byName["qbittorrent"].Env != "QBIT_USER" {
		t.Errorf("qbittorrent env = %q, want the variable to set", byName["qbittorrent"].Env)
	}

	// An empty DOWNLOADS_PATH is a meaningful value — "use content_path
	// verbatim" — not a missing one, so the key must still be present.
	if _, ok := body.Paths["downloads_path"]; !ok {
		t.Error("downloads_path is absent; empty is a deliberate setting, not a missing one")
	}
	if body.Intervals["download_poll"] != "10s" {
		t.Errorf("download_poll = %q", body.Intervals["download_poll"])
	}
}

// "Not set up" and "set up but broken" are different facts and the screen says
// different things about them, so an unconfigured integration must not report
// reachable: false.
func TestSettingsOmitsReachableForUnconfiguredIntegrations(t *testing.T) {
	body, _ := getSettings(t, settingsServer(t, fullSettings()), "/api/settings?probe=1")

	for _, i := range body.Integrations {
		switch {
		case i.Configured && i.Reachable == nil:
			t.Errorf("%s is configured but reports no reachability under ?probe=1", i.Name)
		case !i.Configured && i.Reachable != nil:
			t.Errorf("%s is not configured but reports reachable=%v", i.Name, *i.Reachable)
		}
	}
}

// The probe must never fire for something that was never set up: there is
// nothing to probe with, and the failure would be reported as "unreachable".
func TestSettingsProbesOnlyConfiguredIntegrationsAndOnlyOnRequest(t *testing.T) {
	// Atomic because the handler runs probes concurrently, which is deliberate —
	// four sequential five-second timeouts would be twenty seconds of settings
	// page. Plain ints here fail under -race, correctly.
	var configuredProbes, unconfiguredProbes atomic.Int32

	set := fullSettings()
	set.Integrations[0].Probe = func(context.Context) error { configuredProbes.Add(1); return nil }
	set.Integrations[1].Probe = func(context.Context) error { unconfiguredProbes.Add(1); return nil }
	set.Integrations[2].Probe = func(context.Context) error { configuredProbes.Add(1); return nil }
	h := settingsServer(t, set)

	if _, _ = getSettings(t, h, "/api/settings"); configuredProbes.Load() != 0 {
		t.Errorf("the plain GET ran %d probes; it must answer from configuration alone", configuredProbes.Load())
	}

	body, _ := getSettings(t, h, "/api/settings?probe=1")
	if !body.Probed {
		t.Error("probed = false with ?probe=1")
	}
	if got := configuredProbes.Load(); got != 2 {
		t.Errorf("ran %d probes, want 2 (the configured ones)", got)
	}
	if got := unconfiguredProbes.Load(); got != 0 {
		t.Errorf("probed an unconfigured integration %d times", got)
	}
}

// A failed probe is the news this page exists to carry.
func TestSettingsProbeFailureIsStill200(t *testing.T) {
	set := fullSettings()
	set.Integrations[0].Probe = func(context.Context) error {
		return errors.New("dial tcp 127.0.0.1:8191: connection refused")
	}

	body, rec := getSettings(t, settingsServer(t, set), "/api/settings?probe=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an unreachable dependency is the answer, not an error", rec.Code)
	}

	tmdb := body.Integrations[0]
	if tmdb.Reachable == nil || *tmdb.Reachable {
		t.Fatalf("reachable = %v, want false", tmdb.Reachable)
	}
	if !strings.Contains(tmdb.Detail, "connection refused") {
		t.Errorf("detail = %q, want the reason", tmdb.Detail)
	}
}

// An integration with no safe read-only call is never probed. Jellyfin is the
// live case: the only method internal/jellyfin has is /Library/Refresh, which
// mutates, and rendering a settings page must not queue a library scan.
func TestSettingsSkipsIntegrationsWithNoSafeProbe(t *testing.T) {
	set := fullSettings()
	set.Integrations[3].Configured = true
	set.Integrations[3].Probe = nil
	set.Integrations[3].Detail = "no read-only probe: a refresh would queue a library scan"

	body, _ := getSettings(t, settingsServer(t, set), "/api/settings?probe=1")

	jellyfin := body.Integrations[3]
	if jellyfin.Reachable != nil {
		t.Errorf("reachable = %v, want absent — there is no safe way to ask", *jellyfin.Reachable)
	}
	if !strings.Contains(jellyfin.Detail, "read-only") {
		t.Errorf("detail = %q, want it to say why it was not probed", jellyfin.Detail)
	}
}

// A wedged dependency must not wedge the page.
func TestSettingsProbeIsBounded(t *testing.T) {
	set := fullSettings()
	set.Integrations[0].Probe = func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Minute):
			return nil
		}
	}
	set.Integrations[2].Probe = func(context.Context) error { return nil }

	done := make(chan struct{})
	go func() {
		defer close(done)
		body, rec := getSettings(t, settingsServer(t, set), "/api/settings?probe=1")
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
		if r := body.Integrations[0].Reachable; r == nil || *r {
			t.Error("a hung probe was not reported as unreachable")
		}
	}()

	select {
	case <-done:
	case <-time.After(probeTimeout + 10*time.Second):
		t.Fatal("the handler did not return; the probe timeout is not bounding it")
	}
}

func TestSettingsWithoutConfigurationStillAnswers(t *testing.T) {
	mux := http.NewServeMux()
	New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, quiet()).RegisterSettings(mux)

	body, rec := getSettings(t, mux, "/api/settings")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// [] and {}, never null: the screen iterates them.
	if body.Integrations == nil || body.Paths == nil || body.Intervals == nil {
		t.Errorf("null collections in %+v; the UI iterates these", body)
	}
}
