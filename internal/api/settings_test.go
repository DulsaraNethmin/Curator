package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/logs"
	"github.com/DulsaraNethmin/curator/internal/settings"
)

// The sentinels every secret in the config is set to. If any of them reaches the
// marshalled body, the endpoint is leaking to an unauthenticated LAN page.
const (
	secretTMDB     = "TMDB-SECRET-3f1c9a"
	secretQBitPass = "QBIT-SECRET-77b210"
	secretJellyfin = "JELLY-SECRET-c40de8"
	secretVPNKey   = "VPNKEY-SECRET-91ab04"
	secretPassword = "PASSWORD-SECRET-6de5f2"
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
	if body.Settings == nil {
		t.Error("settings = null with no state source; the screen iterates it")
	}
}

// ---------------------------------------------------------------------------
// T40: the settings array, and the write.

// settingStates builds a state for every registry row, overriding the ones a
// test cares about.
//
// Every key is present because cmd/curator's reader answers for every key, and
// a fixture that filled in three would be asserting against a shape the real
// wiring never produces.
func settingStates(t *testing.T, overrides map[string]SettingState) map[string]SettingState {
	t.Helper()
	out := map[string]SettingState{}
	for _, item := range settings.All() {
		out[item.Key] = SettingState{Source: settings.SourceDefault}
	}
	for key, state := range overrides {
		if _, ok := settings.Get(key); !ok {
			t.Fatalf("%q is not a setting: the override is a typo", key)
		}
		out[key] = state
	}
	return out
}

// fakeWriter is the store behind a write, as this package sees it: one method,
// and a record of exactly what it was handed.
type fakeWriter struct {
	calls int
	set   map[string]string
	clear []string
	err   error
}

func (f *fakeWriter) WriteSettings(_ context.Context, set map[string]string, clear []string) error {
	f.calls++
	f.set, f.clear = set, clear
	return f.err
}

// fakeAccess stands in for T41's live password holder.
type fakeAccess struct {
	reloads int
	err     error
}

func (f *fakeAccess) Reload(context.Context) error {
	f.reloads++
	return f.err
}

// writableServer is the view cmd/curator builds: phase 5's fields, a state for
// every registry row, and somewhere for a write to go.
func writableServer(t *testing.T, states map[string]SettingState) (http.Handler, *fakeWriter) {
	t.Helper()
	writer := &fakeWriter{}
	set := fullSettings()
	set.States = func(context.Context) (map[string]SettingState, error) { return states, nil }
	set.Writer = writer
	return settingsServer(t, set), writer
}

func putSettings(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body)))
	return rec
}

// rows decodes the settings array as raw JSON objects rather than as
// settingBody, so an assertion can ask whether a field is *there* — which is
// the only way to tell a withheld secret from an empty string.
func rows(t *testing.T, rec *httptest.ResponseRecorder) map[string]map[string]json.RawMessage {
	t.Helper()
	var body struct {
		Settings []map[string]json.RawMessage `json:"settings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v — body was %s", err, rec.Body)
	}
	out := map[string]map[string]json.RawMessage{}
	for _, row := range body.Settings {
		var key string
		if err := json.Unmarshal(row["key"], &key); err != nil {
			t.Fatalf("a settings row has no key: %v", err)
		}
		out[key] = row
	}
	return out
}

func failure(t *testing.T, rec *httptest.ResponseRecorder) (string, map[string]string) {
	t.Helper()
	var body struct {
		Error  string            `json:"error"`
		Fields map[string]string `json:"fields"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v — body was %s", err, rec.Body)
	}
	return body.Error, body.Fields
}

func field(t *testing.T, row map[string]json.RawMessage, name string) string {
	t.Helper()
	raw, ok := row[name]
	if !ok {
		t.Fatalf("no %q in %v", name, row)
	}
	return string(raw)
}

// GET grows one field and changes none. The five phase 5 verified are what T42's
// own screen still reads, so this is a golden-ish assertion rather than a
// spot check.
func TestSettingsGetIsAdditive(t *testing.T) {
	h, _ := writableServer(t, settingStates(t, nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))

	var top map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, name := range []string{"version", "probed", "integrations", "paths", "intervals", "settings"} {
		if _, ok := top[name]; !ok {
			t.Errorf("%q is missing from the response", name)
		}
	}
	if len(top) != 6 {
		t.Errorf("the response has %d top-level fields, want 6: %v", len(top), top)
	}

	// The phase 5 shapes, unchanged.
	body, _ := getSettings(t, h, "/api/settings")
	if body.Version != "0.1.0" || len(body.Integrations) != 4 {
		t.Errorf("the existing fields changed: %+v", body)
	}
	if len(body.Settings) != len(settings.All()) {
		t.Errorf("settings has %d entries, want one per registry row (%d)", len(body.Settings), len(settings.All()))
	}
}

// The test the array exists to survive. Every secret's plaintext is handed to
// the handler the way a careless wiring would hand it over — in both fields that
// could carry one — and none of them may appear in the bytes that go out.
//
// Asserted against the RAW JSON rather than against settingBody, so a field
// added in phase 8 cannot leak one past a test that only knows today's shape.
func TestSettingsArrayNeverCarriesASecretValue(t *testing.T) {
	states := settingStates(t, map[string]SettingState{
		"tmdb_api_key": {Source: settings.SourceStored, Configured: true,
			Value: secretTMDB, Pending: secretTMDB, PendingChange: true},
		"qbit_pass":        {Source: settings.SourceStored, Configured: true, Value: secretQBitPass},
		"jellyfin_api_key": {Source: settings.SourceEnv, Configured: true, Value: secretJellyfin},
		"vpn_config":       {Source: settings.SourceStored, Configured: true, Value: secretVPNKey},
		"auth_password":    {Source: settings.SourceStored, Configured: true, Value: secretPassword},
	})
	h, _ := writableServer(t, states)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))

	raw := rec.Body.String()
	for _, leaked := range []string{secretTMDB, secretQBitPass, secretJellyfin, secretVPNKey, secretPassword} {
		if strings.Contains(raw, leaked) {
			t.Errorf("the response carries %q", leaked)
		}
	}
	// Not even a fragment: a masked or truncated secret still confirms a length
	// and an existence to anyone on the LAN.
	for _, fragment := range []string{"3f1c9a", "77b210", "c40de8", "91ab04", "6de5f2"} {
		if strings.Contains(raw, fragment) {
			t.Errorf("the response carries the fragment %q", fragment)
		}
	}

	byKey := rows(t, rec)
	for _, item := range settings.All() {
		row, ok := byKey[item.Key]
		if !ok {
			t.Fatalf("%s is missing from the settings array", item.Key)
		}
		if !item.Secret {
			continue
		}
		if _, present := row["value"]; present {
			t.Errorf("%s is a secret and carries a value field: %v", item.Key, row)
		}
		if got := field(t, row, "pending"); got != "null" {
			t.Errorf("%s pending = %s, want null", item.Key, got)
		}
	}

	// configured is the whole of what a screen may know, and it must still be
	// the truth — withholding the value is not the same as reporting nothing.
	if got := field(t, byKey["tmdb_api_key"], "configured"); got != "true" {
		t.Errorf("tmdb_api_key configured = %s, want true", got)
	}
	// That a secret has changed is reportable; what it changed to is not.
	if got := field(t, byKey["tmdb_api_key"], "pending_change"); got != "true" {
		t.Errorf("tmdb_api_key pending_change = %s, want true", got)
	}
}

// The shadow case, which is the one that would otherwise be a silent failure:
// type into a field the environment owns, save, restart, and nothing changed.
func TestSettingsReportsSourceAndEditability(t *testing.T) {
	h, _ := writableServer(t, settingStates(t, map[string]SettingState{
		"library_movies": {Source: settings.SourceEnv, Configured: true, Value: "/media/movies"},
		"qbit_user":      {Source: settings.SourceStored, Configured: true, Value: "nethmin"},
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	byKey := rows(t, rec)

	for _, want := range []struct {
		key      string
		source   string
		editable string
	}{
		{"library_movies", `"env"`, "false"},
		{"qbit_user", `"stored"`, "true"},
		{"search_timeout", `"default"`, "true"},
	} {
		row := byKey[want.key]
		if got := field(t, row, "source"); got != want.source {
			t.Errorf("%s source = %s, want %s", want.key, got, want.source)
		}
		if got := field(t, row, "editable"); got != want.editable {
			t.Errorf("%s editable = %s, want %s", want.key, got, want.editable)
		}
	}

	// The variable is in the payload because the screen renders "set by
	// LIBRARY_MOVIES", and the UI carrying its own copy of the registry would be
	// the second vocabulary this phase exists to prevent.
	if got := field(t, byKey["library_movies"], "env"); got != `"LIBRARY_MOVIES"` {
		t.Errorf("library_movies env = %s", got)
	}
}

// pending is the honest half of "restart to apply".
func TestSettingsReportsWhatIsPending(t *testing.T) {
	h, _ := writableServer(t, settingStates(t, map[string]SettingState{
		"qbit_user":      {Source: settings.SourceStored, Configured: true, Value: "old", Pending: "new", PendingChange: true},
		"library_movies": {Source: settings.SourceStored, Configured: true, Value: "/media/movies"},
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	byKey := rows(t, rec)

	if got := field(t, byKey["qbit_user"], "pending"); got != `"new"` {
		t.Errorf("qbit_user pending = %s, want the saved value", got)
	}
	if got := field(t, byKey["qbit_user"], "value"); got != `"old"` {
		t.Errorf("qbit_user value = %s, want what this process is running on", got)
	}
	if got := field(t, byKey["library_movies"], "pending"); got != "null" {
		t.Errorf("library_movies pending = %s, want null when nothing differs", got)
	}

	// The password applies at once, so there is never a restart to wait for.
	if got := field(t, byKey["auth_password"], "restart_required"); got != "false" {
		t.Errorf("auth_password restart_required = %s, want false (D29)", got)
	}
	if got := field(t, byKey["qbit_user"], "restart_required"); got != "true" {
		t.Errorf("qbit_user restart_required = %s, want true", got)
	}
}

// Absent leaves alone, "" clears, and nothing else is touched.
func TestSettingsWriteIsAPartialUpdate(t *testing.T) {
	h, writer := writableServer(t, settingStates(t, map[string]SettingState{
		"qbit_user":      {Source: settings.SourceStored, Configured: true, Value: "old"},
		"downloads_path": {Source: settings.SourceStored, Configured: true, Value: "/downloads"},
		"library_movies": {Source: settings.SourceStored, Configured: true, Value: "/media/movies"},
	}))

	rec := putSettings(t, h, `{"qbit_user":"nethmin","downloads_path":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — body %s", rec.Code, rec.Body)
	}
	if writer.calls != 1 {
		t.Fatalf("the writer was called %d times, want exactly one transaction", writer.calls)
	}
	if len(writer.set) != 1 || writer.set["qbit_user"] != "nethmin" {
		t.Errorf("set = %v, want only qbit_user", writer.set)
	}
	if len(writer.clear) != 1 || writer.clear[0] != "downloads_path" {
		t.Errorf(`clear = %v, want only downloads_path — "" is a deletion`, writer.clear)
	}
	if _, touched := writer.set["library_movies"]; touched {
		t.Error("library_movies was written and it was not in the body")
	}

	// The answer is the body GET would give, and no probe ran to produce it.
	body, _ := getSettings(t, h, "/api/settings")
	var written settingsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &written); err != nil {
		t.Fatalf("decode the write response: %v", err)
	}
	if written.Probed {
		t.Error("the write probed; ?probe=1 is opt-in and stays opt-in")
	}
	if len(written.Settings) != len(body.Settings) || written.Version != body.Version {
		t.Errorf("the write answered a different shape from GET:\n%+v\n%+v", written, body)
	}
}

// One invalid field rejects the whole write, names the field, and leaves the
// store alone. Half a configuration is not a state anything downstream reasons
// about.
func TestSettingsWriteRejectsAnInvalidFieldAndWritesNothing(t *testing.T) {
	h, writer := writableServer(t, settingStates(t, nil))

	rec := putSettings(t, h, `{"search_timeout":"soon","qbit_user":"nethmin"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — body %s", rec.Code, rec.Body)
	}
	if writer.calls != 0 {
		t.Errorf("the writer ran %d times; nothing may be written when a field is refused", writer.calls)
	}

	message, fields := failure(t, rec)
	if message == "" {
		t.Error(`the {"error": ...} envelope is phase 1's and every existing client reads it`)
	}
	// The message the next start-up would have printed, because it is the same
	// function that would print it.
	if !strings.Contains(fields["search_timeout"], "not a duration") {
		t.Errorf("fields[search_timeout] = %q, want config.ParseDuration's own message", fields["search_timeout"])
	}
	if _, named := fields["qbit_user"]; named {
		t.Errorf("the valid field was reported as a failure too: %v", fields)
	}
}

// A typo that silently does nothing is the worst outcome a settings screen has.
func TestSettingsWriteRefusesAnUnknownKey(t *testing.T) {
	h, writer := writableServer(t, settingStates(t, nil))

	// DB_PATH is the live example: it says where the settings live, so it cannot
	// live there, and it is not in the registry to be written to.
	rec := putSettings(t, h, `{"db_path":"/tmp/curator.db"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if writer.calls != 0 {
		t.Error("an unknown key reached the store")
	}
	if _, fields := failure(t, rec); fields["db_path"] == "" {
		t.Errorf("the unknown key was not named: %v", fields)
	}
}

// 409 rather than 400: the value was fine, the destination was not.
func TestSettingsWriteRefusesAKeyTheEnvironmentOwns(t *testing.T) {
	h, writer := writableServer(t, settingStates(t, map[string]SettingState{
		"library_movies": {Source: settings.SourceEnv, Configured: true, Value: "/media/movies"},
		"vpn_config":     {Source: settings.SourceEnv, Configured: true},
	}))

	rec := putSettings(t, h, `{"library_movies":"/elsewhere"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — body %s", rec.Code, rec.Body)
	}
	if writer.calls != 0 {
		t.Error("a shadowed key was written; it would have been invisibly ignored at the next start")
	}
	_, fields := failure(t, rec)
	if !strings.Contains(fields["library_movies"], "LIBRARY_MOVIES") {
		t.Errorf("the variable that owns it was not named: %q", fields["library_movies"])
	}

	// The one setting with two variables: a provider hands out a file and a
	// container hands out a value, and the message must not send somebody to
	// unset the one they did not set.
	rec = putSettings(t, h, `{"vpn_config":"[Interface]"}`)
	_, fields = failure(t, rec)
	if !strings.Contains(fields["vpn_config"], "VPN_CONFIG_FILE") {
		t.Errorf("vpn_config = %q, want both variables named", fields["vpn_config"])
	}
}

// The one ordering mistake that locks somebody out of their own library.
func TestSettingsWriteRefusesAuthWithNoPassword(t *testing.T) {
	states := settingStates(t, nil)
	h, writer := writableServer(t, states)

	rec := putSettings(t, h, `{"auth_enabled":"true"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if writer.calls != 0 {
		t.Error("authentication was turned on with nothing to log in with")
	}
	if _, fields := failure(t, rec); fields["auth_enabled"] == "" {
		t.Errorf("the switch was not named: %v", fields)
	}

	// Both in one body is fine: it is one transaction, so there is no window in
	// which the switch is on and the password is not set.
	if rec := putSettings(t, h, `{"auth_enabled":"true","auth_password":"hunter2"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — body %s", rec.Code, rec.Body)
	}

	// And the same mistake spelled backwards: clearing the password out from
	// under a switch that is already on.
	on, _ := writableServer(t, settingStates(t, map[string]SettingState{
		"auth_enabled":  {Source: settings.SourceStored, Configured: true, Value: "true"},
		"auth_password": {Source: settings.SourceStored, Configured: true},
	}))
	if rec := putSettings(t, on, `{"auth_password":""}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("clearing the password under an enabled switch = %d, want 400", rec.Code)
	}
}

// A write is silent about values at every level, and the log buffer is where
// that would actually show up: the scrubber only knows the secrets it was handed
// at start-up, and a key being typed now is by definition not one of them.
func TestSettingsWriteLogsTheKeysAndNoValue(t *testing.T) {
	buffer := logs.NewBuffer(100)
	writer := &fakeWriter{}
	set := fullSettings()
	set.States = func(context.Context) (map[string]SettingState, error) { return settingStates(t, nil), nil }
	set.Writer = writer

	mux := http.NewServeMux()
	New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot,
		slog.New(buffer.Handler(slog.NewTextHandler(io.Discard, nil)))).
		WithSettings(set).
		RegisterSettings(mux)

	if rec := putSettings(t, mux, `{"tmdb_api_key":"`+secretTMDB+`","qbit_user":"nethmin"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — body %s", rec.Code, rec.Body)
	}

	entries, _, _ := buffer.Since(0, 100)
	var logged string
	for _, entry := range entries {
		logged += entry.Msg
		for key, value := range entry.Attrs {
			logged += " " + key + "=" + value
		}
	}
	if !strings.Contains(logged, "tmdb_api_key") {
		t.Errorf("the changed keys were not logged: %q", logged)
	}
	if strings.Contains(logged, secretTMDB) {
		t.Errorf("a write logged its own value: %q", logged)
	}
}

// A rejected secret is still a secret. vpn.ParseConfig quotes the line it could
// not parse, and for a wg-quick file that line can be the private key.
func TestSettingsWriteDoesNotEchoARejectedSecret(t *testing.T) {
	h, writer := writableServer(t, settingStates(t, nil))

	// A line with no `=` is what ParseConfig quotes back verbatim.
	rec := putSettings(t, h, `{"vpn_config":"[Interface]\nPrivateKey `+secretVPNKey+`\n"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — body %s", rec.Code, rec.Body)
	}
	if writer.calls != 0 {
		t.Error("an unparseable tunnel config was stored")
	}
	if strings.Contains(rec.Body.String(), secretVPNKey) {
		t.Errorf("the rejection carried the key back: %s", rec.Body)
	}
	if _, fields := failure(t, rec); !strings.Contains(fields["vpn_config"], "vpn config line") {
		t.Errorf("the message stopped being useful: %q", fields["vpn_config"])
	}
}

// And the other half of that, which is the harder one: the message has to
// survive the redaction.
//
// Found live rather than reasoned about — redacting anything eight characters
// or longer answered "vpn config: [redacted] [redacted] is missing", because
// `[Interface]` and `PrivateKey` appear in the submitted file as well as in the
// sentence explaining what is wrong with it.
func TestSettingsWriteKeepsARejectionReadable(t *testing.T) {
	h, _ := writableServer(t, settingStates(t, nil))

	// A well-formed private key and nothing else: the tunnel has no peer, which
	// is the mistake somebody pasting half a .conf actually makes.
	key := "uJ8kQ2mVv1PsGm4Bn0LxT7yWpQ3fRzKcE5dHaN9jXoU="
	rec := putSettings(t, h, `{"vpn_config":"[Interface]\nPrivateKey = `+key+`\nAddress = 10.5.0.2/32\n"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — body %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), key) {
		t.Errorf("the rejection carried the key back: %s", rec.Body)
	}
	_, fields := failure(t, rec)
	if !strings.Contains(fields["vpn_config"], "PublicKey is missing") {
		t.Errorf("vpn_config = %q, want the reason the next start-up would have printed", fields["vpn_config"])
	}
}

// The password is the exception D29 exists for: it applies to the next request,
// not the next restart. This handler writes it and calls one method; T41 owns
// what that method does.
func TestSettingsWriteAppliesAuthAtOnceAndNothingElse(t *testing.T) {
	access := &fakeAccess{}
	writer := &fakeWriter{}
	set := fullSettings()
	set.States = func(context.Context) (map[string]SettingState, error) {
		return settingStates(t, map[string]SettingState{
			"auth_password": {Source: settings.SourceStored, Configured: true},
		}), nil
	}
	set.Writer, set.Access = writer, access
	h := settingsServer(t, set)

	if rec := putSettings(t, h, `{"qbit_user":"nethmin"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d — body %s", rec.Code, rec.Body)
	}
	if access.reloads != 0 {
		t.Errorf("a setting that applies at the next start reloaded authentication %d times", access.reloads)
	}

	if rec := putSettings(t, h, `{"auth_enabled":"true"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d — body %s", rec.Code, rec.Body)
	}
	if access.reloads != 1 {
		t.Errorf("authentication reloaded %d times, want 1 — a password that waits for a restart is not a password", access.reloads)
	}
}

// Saved but not applied is a state to report loudly. Somebody who has just
// turned authentication on would otherwise believe it is on.
func TestSettingsWriteReportsAFailureToApply(t *testing.T) {
	access := &fakeAccess{err: errors.New("the password could not be installed")}
	writer := &fakeWriter{}
	set := fullSettings()
	set.States = func(context.Context) (map[string]SettingState, error) { return settingStates(t, nil), nil }
	set.Writer, set.Access = writer, access
	h := settingsServer(t, set)

	rec := putSettings(t, h, `{"auth_password":"hunter2"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if message, _ := failure(t, rec); !strings.Contains(message, "restart") {
		t.Errorf("error = %q, want it to say the value is saved and what to do", message)
	}
}

// A settings value is the same text its environment variable would carry. A
// number, a boolean or a null is a different vocabulary, and null in particular
// is neither "leave it alone" nor "clear it".
func TestSettingsWriteWantsStrings(t *testing.T) {
	h, writer := writableServer(t, settingStates(t, nil))

	for _, body := range []string{`{"vpn_required":true}`, `{"qbit_user":null}`, `{"torrent_port":51820}`} {
		rec := putSettings(t, h, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", body, rec.Code)
		}
	}
	if rec := putSettings(t, h, `["qbit_user"]`); rec.Code != http.StatusBadRequest {
		t.Errorf("a JSON array = %d, want 400", rec.Code)
	}
	if writer.calls != 0 {
		t.Errorf("the writer ran %d times for bodies that were all refused", writer.calls)
	}
}

// An empty body is a form nobody changed, not an error.
func TestSettingsWriteWithNothingInItIsANoOp(t *testing.T) {
	h, writer := writableServer(t, settingStates(t, nil))

	rec := putSettings(t, h, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if writer.calls != 0 {
		t.Errorf("an empty write opened %d transactions", writer.calls)
	}
}

// A process with no writer says so rather than accepting a body it will drop.
func TestSettingsWriteWithoutAWriterSaysSo(t *testing.T) {
	rec := putSettings(t, settingsServer(t, fullSettings()), `{"qbit_user":"nethmin"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}
