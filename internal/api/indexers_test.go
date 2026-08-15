package api

// The Indexers screen's probe, against a fake minter.
//
// Hermetic for the same reason the Jellyfin tests are: internal/indexer already
// proves /health against a real HTTP server, and proving it twice from here
// would test that package's wire format from a place that cannot see it. What
// is asserted here is what this package owns — which of the three worlds is
// reported, that every one of them is a 200, that the pasted command is always
// present, and that whether 1337x is live is read off the RUNNING process
// rather than off the form.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DulsaraNethmin/curator/internal/indexer"
)

const testMinterURL = "http://minter:8191"

// fakeMinter answers whatever the test says minter would.
type fakeMinter struct {
	health indexer.Health
	err    error

	// probes counts calls, so "do not report a stale probe" can be asserted as
	// a fact about the handler rather than hoped for.
	probes int
}

func (f *fakeMinter) URL() string { return testMinterURL }

func (f *fakeMinter) Probe(context.Context) (indexer.Health, error) {
	f.probes++
	return f.health, f.err
}

func minterServer(t *testing.T, setup IndexerSetup) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, quiet()).
		WithIndexers(setup).
		RegisterIndexers(mux)
	return mux
}

func minterProbe(t *testing.T, h http.Handler) (*httptest.ResponseRecorder, minterProbeBody) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/indexers/minter/probe", nil))
	var body minterProbeBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("probe body %q: %v", rec.Body.String(), err)
	}
	return rec, body
}

// The three worlds, and the rule that they are all 200.
func TestMinterProbeReportsWhichWorldMinterIsIn(t *testing.T) {
	const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:151.0) Gecko/20100101 Firefox/151.0"

	for _, tc := range []struct {
		name   string
		health indexer.Health
		err    error
		want   string
		wantUA string
	}{
		{"up and ready", indexer.Health{OK: true, UserAgent: ua}, nil, minterReady, ua},
		{"answering but not ready", indexer.Health{OK: false}, nil, minterUnhealthy, ""},
		{"nothing listening", indexer.Health{}, indexer.ErrUnreachable, minterUnreachable, ""},
		{"something else on the port", indexer.Health{},
			errors.New("minter at http://minter:8191 answered 404 Not Found to /health"), minterUnhealthy, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := minterServer(t, IndexerSetup{
				Minter: &fakeMinter{health: tc.health, err: tc.err},
				X1337:  true,
			})
			rec, body := minterProbe(t, h)

			// Always 200: the state IS the answer. Most of this screen's life
			// is spent in "the container is not up yet", and a 503 makes the
			// expected state look like a broken curator in a network tab.
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 — the state is the answer, not the failure", rec.Code)
			}
			if body.State != tc.want {
				t.Errorf("state = %q, want %q", body.State, tc.want)
			}
			if body.URL != testMinterURL {
				t.Errorf("url = %q, want the address that was actually tried", body.URL)
			}
			// Present in every state, including the ready one: the screen shows
			// what to run, and a command that appeared only on failure would be
			// missing from the page that explains what minter is for.
			if !strings.Contains(body.Command, "--profile 1337x") {
				t.Errorf("command = %q, want the pasted line naming compose.yaml's profile", body.Command)
			}
			if body.UserAgent != tc.wantUA {
				t.Errorf("user_agent = %q, want %q", body.UserAgent, tc.wantUA)
			}
			if body.Detail == "" {
				t.Error("detail is empty — every state owes the screen a sentence")
			}
		})
	}
}

// The command has to match compose.yaml's profile exactly. A profile is
// invisible rather than stopped, so a misspelling here is a service that never
// starts and never errors — the one typo in the flow with no symptom.
func TestMinterCommandMatchesTheComposeProfile(t *testing.T) {
	if minterCommand != "docker compose --profile 1337x up -d" {
		t.Errorf("minterCommand = %q, which must be compose.yaml's 1337x profile verbatim", minterCommand)
	}
}

// Enabled is the running process's answer, not the stored setting's. A setting
// written through the API applies at the next start (D29), so the ordinary path
// through this screen — switch 1337x on, save, paste the command — leaves
// curator running with it off, and the screen has to be able to say so.
func TestMinterProbeReportsWhetherThisProcessSearches1337x(t *testing.T) {
	for _, live := range []bool{true, false} {
		h := minterServer(t, IndexerSetup{
			Minter: &fakeMinter{health: indexer.Health{OK: true}},
			X1337:  live,
		})
		_, body := minterProbe(t, h)
		if body.Enabled != live {
			t.Errorf("enabled = %v, want %v — it is cfg.IndexerX1337 and not the stored value",
				body.Enabled, live)
		}
	}
}

// Nothing is cached. A minter that was up when the page loaded and is down now
// must not be reported as up, so every request is a fresh probe.
func TestMinterProbeIsNeverStale(t *testing.T) {
	fake := &fakeMinter{health: indexer.Health{OK: true}}
	h := minterServer(t, IndexerSetup{Minter: fake, X1337: true})

	if _, body := minterProbe(t, h); body.State != minterReady {
		t.Fatalf("state = %q, want %q", body.State, minterReady)
	}

	// minter goes away between one poll and the next, which is what happens
	// when somebody stops the container with the page still open.
	fake.health, fake.err = indexer.Health{}, indexer.ErrUnreachable

	if _, body := minterProbe(t, h); body.State != minterUnreachable {
		t.Errorf("state = %q, want %q — the answer was cached", body.State, minterUnreachable)
	}
	if fake.probes != 2 {
		t.Errorf("probes = %d, want 2 — one per request", fake.probes)
	}
}

// A process with no minter at all answers the honest 503 rather than inventing
// a state. This is the same posture as the Jellyfin endpoints with a nil New.
func TestMinterProbeWithoutAMinter(t *testing.T) {
	rec := httptest.NewRecorder()
	minterServer(t, IndexerSetup{}).ServeHTTP(
		rec, httptest.NewRequest(http.MethodGet, "/api/indexers/minter/probe", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}
