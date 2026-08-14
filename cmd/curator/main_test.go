package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/DulsaraNethmin/curator/internal/api"
	"github.com/DulsaraNethmin/curator/internal/config"
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
