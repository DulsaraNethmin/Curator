package main

import (
	"testing"

	"github.com/DulsaraNethmin/curator/internal/config"
	"github.com/DulsaraNethmin/curator/internal/settings"
)

// effective() is the one place a setting's live value is spelled out by hand,
// and a registry row with no line in it would report "" for ever — a screen
// showing an empty box for something the process is running on. This is what
// turns that into a failing build rather than a puzzle in phase 8.
func TestEverySettingHasAnEffectiveValue(t *testing.T) {
	// Cleared explicitly: a shell that has sourced .env really does have
	// TMDB_API_KEY and VPN_CONFIG_FILE set, and the second would have Load read
	// a file that no other checkout has.
	for _, item := range settings.All() {
		t.Setenv(item.Env, "")
		if item.FileEnv != "" {
			t.Setenv(item.FileEnv, "")
		}
	}

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
