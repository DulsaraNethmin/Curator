package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DulsaraNethmin/curator/internal/api"
	"github.com/DulsaraNethmin/curator/internal/config"
	"github.com/DulsaraNethmin/curator/internal/download"
	"github.com/DulsaraNethmin/curator/internal/settings"
	"github.com/DulsaraNethmin/curator/internal/store"
)

// clearSettingsEnv unsets every registry variable for the duration of a test.
//
// A shell that has sourced .env really does have TMDB_API_KEY and
// VPN_CONFIG_FILE set, and the second would have Load read a file no other
// checkout has. internal/settings has its own clearEnv for the same reason.
func clearSettingsEnv(t *testing.T) {
	t.Helper()
	for _, item := range settings.All() {
		t.Setenv(item.Env, "")
		if item.FileEnv != "" {
			t.Setenv(item.FileEnv, "")
		}
	}
}

// effective() is the one place a setting's live value is spelled out by hand,
// and a registry row with no line in it would report "" for ever — a screen
// showing an empty box for something the process is running on. This is what
// turns that into a failing build rather than a puzzle in phase 8.
func TestEverySettingHasAnEffectiveValue(t *testing.T) {
	clearSettingsEnv(t)

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	live := effective(cfg)

	for _, item := range settings.All() {
		if item.Immediate {
			// The two Access settings apply on the next request rather than at
			// the next start, so they have no *config.Config field to read and
			// settingsStates takes them off the resolution (docs/decisions.md D29).
			continue
		}
		if _, ok := live[item.Key]; !ok {
			t.Errorf("%s is in the registry with no effective value: add it to effective()", item.Key)
		}
	}
	for key := range live {
		if _, ok := settings.Get(key); !ok {
			t.Errorf("effective() reports %q, which is not a setting", key)
		}
	}

	// Zero means unset, not zero. internal/config leaves TORRENT_MAX_CONNS at 0
	// when the variable is absent and internal/engine substitutes its own
	// documented default, so reporting "0" would name a value nothing runs on.
	if got := live["torrent_max_conns"]; got != "" {
		t.Errorf("torrent_max_conns = %q with the variable unset, want empty", got)
	}
}

// The two comparison paths, and the escape hatch that is not a feature but the
// precedence rule itself (docs/decisions.md D25, D28).
//
// A password saved from the screen is bcrypt by the time it reaches the table,
// so the holder gets a Hash. AUTH_PASSWORD in the environment is what somebody
// typed into a `docker run -e`, so it is carried in clear and never hashed —
// and it beats the stored hash, exactly as AUTH_ENABLED=false beats a stored
// true. That is the whole of the lockout recovery: one -e, no rescue mode.
func TestAuthCredentialTakesTheEnvironmentFirst(t *testing.T) {
	clearSettingsEnv(t)

	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "curator.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	// What settingsWriter would have written: a bcrypt hash, not ciphertext.
	// The password is hashed and the four secrets are encrypted, and that
	// asymmetry is the design (D28).
	const stored = "$2a$10$MgKX4S3LNGtaOA6Z7hM9O.7dQ0Jf3nOqiF9wUcQ0DdVJmC7YV6kR2"
	if err := db.SetSettings(ctx, map[string]string{"auth_enabled": "true", "auth_password": stored}, nil); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	// A nil codec, because nothing here is encrypted: a password never is.
	source := authCredential(db, nil)

	got, err := source(ctx)
	if err != nil {
		t.Fatalf("read credential: %v", err)
	}
	if want := (api.Credential{Enabled: true, Hash: stored}); got != want {
		t.Errorf("stored only = %+v, want %+v", got, want)
	}

	t.Setenv("AUTH_ENABLED", "false")
	if got, err = source(ctx); err != nil {
		t.Fatalf("read credential: %v", err)
	}
	if got.Enabled {
		t.Error("AUTH_ENABLED=false did not beat a stored true: that is the whole lockout escape hatch")
	}

	t.Setenv("AUTH_ENABLED", "true")
	t.Setenv("AUTH_PASSWORD", "typed into a docker run")
	if got, err = source(ctx); err != nil {
		t.Fatalf("read credential: %v", err)
	}
	if want := (api.Credential{Enabled: true, Password: "typed into a docker run"}); got != want {
		t.Errorf("with the environment set = %+v, want %+v — the stored hash must not survive it", got, want)
	}
}

// Nothing configured is the state every existing install is in, and it has to
// read as off rather than as a lockout.
func TestAuthCredentialIsOffWithNothingConfigured(t *testing.T) {
	clearSettingsEnv(t)

	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "curator.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	got, err := authCredential(db, nil)(ctx)
	if err != nil {
		t.Fatalf("read credential: %v", err)
	}
	if (got != api.Credential{}) {
		t.Errorf("an empty settings table = %+v, want authentication off with no credential", got)
	}
}

// The image's HEALTHCHECK is `curator -healthcheck` in the same binary, because
// a FROM scratch image has no shell and no curl (docs/tasks/T47-image.md). The
// property being tested is that it finds the port the server bound rather than
// assuming 8090: PORT is read from the environment alone (D28), so a check that
// hard-coded the default would report a working curator on any other port as
// unhealthy — and compose would then refuse to start whatever waited on it.
func TestHealthcheckReadsThePortTheServerBound(t *testing.T) {
	clearSettingsEnv(t)

	// A real listener rather than httptest's, because the port has to be known
	// before the check runs and PORT is what carries it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	// Atomic because the handler runs on its own goroutine and the second half of
	// this test changes the answer under it — a plain int is a data race that
	// `make check`'s -race would find, and finding it here would be a puzzle
	// about the test rather than about curator.
	var status atomic.Int32
	status.Store(http.StatusOK)

	// The error is the one http.Serve returns when ln closes, which is what the
	// deferred Close is for.
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:errcheck
		if r.URL.Path != "/healthz" {
			t.Errorf("healthcheck asked for %q, want /healthz", r.URL.Path)
		}
		w.WriteHeader(int(status.Load()))
	}))

	port := ln.Addr().(*net.TCPAddr).Port
	t.Setenv("PORT", strconv.Itoa(port))

	// The same variable the server reads, through the same function — which is
	// the whole reason config.Port is exported.
	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != port {
		t.Fatalf("the server would bind %d and the check probes %d", cfg.Port, port)
	}

	if err := healthcheck(); err != nil {
		t.Errorf("healthcheck against a listening /healthz: %v", err)
	}

	// A 401 is the case that matters: /healthz sits outside the authentication
	// middleware on purpose (D25, T41), so a credential prompt here is a
	// regression in the middleware — and either way the container is not healthy.
	status.Store(http.StatusUnauthorized)
	if err := healthcheck(); err == nil {
		t.Error("healthcheck accepted a 401; anything but 200 is unhealthy")
	}
}

// Nothing listening is the ordinary state for the first ten seconds of a
// container's life, and it has to be an error rather than a panic or a hang.
func TestHealthcheckFailsWithNothingListening(t *testing.T) {
	clearSettingsEnv(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	t.Setenv("PORT", strconv.Itoa(port))

	if err := healthcheck(); err == nil {
		t.Errorf("healthcheck reported %s healthy with nothing on it", fmt.Sprintf("127.0.0.1:%d", port))
	}
}

// Every Register* on api.Server is actually called here.
//
// Written after a handler was built, unit-tested and then never mounted: the
// package's own tests register the routes themselves, so a missing line in this
// file is invisible to all of them and shows up only as a 404 in a browser. The
// route table lives in one function and this asserts the function names it.
//
// Reflection over the methods rather than a list, so a Register* added in a
// later phase is covered the moment it exists rather than when somebody
// remembers to extend a slice.
func TestEveryRegisterIsWiredIntoMain(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}

	server := reflect.TypeOf(&api.Server{})
	found := 0
	for i := range server.NumMethod() {
		name := server.Method(i).Name
		if !strings.HasPrefix(name, "Register") {
			continue
		}
		found++
		if !strings.Contains(string(source), "."+name+"(mux)") {
			t.Errorf("api.Server has %s and main.go never calls it — the handlers exist and nothing serves them", name)
		}
	}
	if found == 0 {
		t.Fatal("found no Register* methods on api.Server; this test has stopped checking anything")
	}
}

// TestNoTunnelAndVpnRequiredBuildsNoEngineAtAll is the state a fresh install is
// in, and it used to be the worst one in the process.
//
// startEngine left `network` nil and called engine.New anyway. engine.New runs
// bindConfig only when it has a Network, so DisableTCP, DisableUTP and NoDHT
// were all false and anacrolix opened host sockets and ran a DHT node on the
// household connection — announcing this machine, on every boot, before anybody
// had dispatched anything, while the settings screen showed the kill switch on.
// The guard refuses dispatches. It never touched that.
//
// Reachable by starting with VPN_CONFIG unset, which is every first run.
func TestNoTunnelAndVpnRequiredBuildsNoEngineAtAll(t *testing.T) {
	clearSettingsEnv(t)

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.DownloadsDir = t.TempDir()
	if !cfg.VPNRequired {
		t.Fatal("VPN_REQUIRED did not default to true, which is the whole premise (docs/decisions.md D27)")
	}

	engine, guard, err := startEngine(cfg, nil, http.DefaultClient, quietLogger())
	if err != nil {
		t.Fatalf("startEngine: %v", err)
	}
	if engine != nil {
		t.Error("an engine was built with no tunnel and VPN_REQUIRED on: with a nil Network it has " +
			"DisableTCP, DisableUTP and NoDHT all false, which is a DHT node on the real connection")
	}
	if guard == nil {
		t.Fatal("no guard, so dispatch would not be refused either")
	}
	if err := guard(context.Background()); err == nil {
		t.Error("the guard passed; with no VPN configured it must refuse and name VPN_CONFIG")
	} else if !strings.Contains(err.Error(), "VPN_CONFIG") {
		t.Errorf("the refusal does not name the variable that would fix it: %v", err)
	}

	// The other half, and the one a `torrents != nil` check cannot see. run()
	// assigns this into a download.TorrentClient, and a nil *engine.Engine put
	// into an interface is a NON-nil interface holding a nil pointer — so the
	// poller starts, the resume runs, and both dereference it.
	var client download.TorrentClient
	if engine != nil {
		client = engine
	}
	if client != nil {
		t.Error("the nil engine reached the interface as a non-nil value; run() must guard the assignment")
	}
}

// TestAnEngineIsStillBuiltWithoutATunnelWhenEnforcementIsOff pins the escape
// hatch, so the fix above cannot quietly become "the embedded backend needs a
// VPN, full stop". VPN_REQUIRED=false is documented (D27) and has to keep
// working on a laptop that is already behind one.
func TestAnEngineIsStillBuiltWithoutATunnelWhenEnforcementIsOff(t *testing.T) {
	clearSettingsEnv(t)

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.DownloadsDir = t.TempDir()
	cfg.VPNRequired = false

	engine, guard, err := startEngine(cfg, nil, http.DefaultClient, quietLogger())
	if err != nil {
		t.Fatalf("startEngine: %v", err)
	}
	if engine == nil {
		t.Fatal("no engine with VPN_REQUIRED=false, so the documented escape hatch downloads nothing")
	}
	t.Cleanup(func() { _ = engine.Close() })
	if guard != nil {
		t.Error("a guard was installed with VPN_REQUIRED=false; there is nothing for it to check")
	}
}

// quietLogger keeps these two tests' output off the terminal. They deliberately
// take the branches that log a warning.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
