package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DulsaraNethmin/curator/internal/config"
)

// clearEnv makes a test mean the defaults rather than the developer's machine.
// A shell that has sourced .env really does have TMDB_API_KEY and
// VPN_CONFIG_FILE set, and an inherited value would make half of these assert
// nothing.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, s := range All() {
		t.Setenv(s.Env, "")
		if s.FileEnv != "" {
			t.Setenv(s.FileEnv, "")
		}
	}
	for _, key := range []string{"PORT", "DB_PATH", "LOG_LEVEL", "LOG_BUFFER_LINES"} {
		t.Setenv(key, "")
	}
}

// The invariant the whole package is built on: two ways to configure one
// service is already one more than ideal, and two vocabularies would be the
// version of this that rots.
func TestEveryStoredKeyIsItsVariableLowerCased(t *testing.T) {
	seen := map[string]string{}
	for _, s := range All() {
		if s.Key != strings.ToLower(s.Env) {
			t.Errorf("%s: key = %q, want %q", s.Env, s.Key, strings.ToLower(s.Env))
		}
		if s.Env == "" || s.Group == "" || s.Kind == "" {
			t.Errorf("%s: an incomplete registry entry: %+v", s.Env, s)
		}
		if first, ok := seen[s.Key]; ok {
			t.Errorf("%s: duplicate key %q, already used by %s", s.Env, s.Key, first)
		}
		seen[s.Key] = s.Env
	}
}

// The registry is the second place this list exists; the first is
// docs/phase-7.md#the-settings-catalogue. A setting added to one and not the
// other is a field nobody can save, so it fails here instead.
func TestTheRegistryMatchesTheCatalogue(t *testing.T) {
	const documented = 30
	if len(All()) != documented {
		t.Errorf("the registry has %d settings and docs/phase-7.md documents %d — "+
			"update both, or neither is the catalogue", len(All()), documented)
	}
}

func TestResolvePrefersTheEnvironmentThenTheStoreThenNothing(t *testing.T) {
	clearEnv(t)
	t.Setenv("QBIT_USER", "from-the-environment")

	res, err := Resolve(map[string]string{
		"qbit_user":      "from-the-store",
		"search_timeout": "45s",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if res.Values["qbit_user"] != "from-the-environment" {
		t.Errorf("qbit_user = %q, want the environment's value", res.Values["qbit_user"])
	}
	if res.Sources["qbit_user"] != SourceEnv {
		t.Errorf("qbit_user source = %q, want env", res.Sources["qbit_user"])
	}

	if res.Values["search_timeout"] != "45s" {
		t.Errorf("search_timeout = %q, want the stored value", res.Values["search_timeout"])
	}
	if res.Sources["search_timeout"] != SourceStored {
		t.Errorf("search_timeout source = %q, want stored", res.Sources["search_timeout"])
	}

	// Set nowhere: no value, and a source that says so. A screen needs the
	// third state to render "using the built-in default" rather than "empty".
	if _, ok := res.Values["jellyfin_url"]; ok {
		t.Error("an unset setting has a value")
	}
	if res.Sources["jellyfin_url"] != SourceDefault {
		t.Errorf("jellyfin_url source = %q, want default", res.Sources["jellyfin_url"])
	}

	// Every setting has a source, including the ones with no value.
	if len(res.Sources) != len(All()) {
		t.Errorf("Sources covers %d of %d settings", len(res.Sources), len(All()))
	}
}

// The sharp edge of "the environment wins": a stored value under a set variable
// is silently ignored. Sources is what stops it being silent — the settings
// screen renders that field read-only rather than letting somebody type into
// something it would throw away.
func TestAShadowedStoredValueIsReportedAsTheEnvironments(t *testing.T) {
	clearEnv(t)
	t.Setenv("TMDB_API_KEY", "the-one-in-the-environment")

	res, err := Resolve(map[string]string{"tmdb_api_key": "the-one-somebody-typed"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Values["tmdb_api_key"] != "the-one-in-the-environment" {
		t.Errorf("value = %q", res.Values["tmdb_api_key"])
	}
	if res.Sources["tmdb_api_key"] != SourceEnv {
		t.Error("a shadowed setting must report source env, or the screen cannot say why")
	}
}

func TestAFileVariableCountsAsTheEnvironment(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "wg0.conf")
	if err := os.WriteFile(path, []byte("[Interface]\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("VPN_CONFIG_FILE", path)

	res, err := Resolve(map[string]string{"vpn_config": "[Interface] # the stored one"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Values["vpn_config"] != "[Interface]\n" {
		t.Errorf("vpn_config = %q, want the file's contents", res.Values["vpn_config"])
	}
	if res.Sources["vpn_config"] != SourceEnv {
		t.Errorf("source = %q, want env", res.Sources["vpn_config"])
	}
}

// A path that cannot be read is an error rather than a quiet fall-through to
// the stored value. A mandatory tunnel that becomes a *different* tunnel
// because of a typo in a path is the failure D27 exists to avoid.
func TestAnUnreadableFileVariableIsFatal(t *testing.T) {
	clearEnv(t)
	t.Setenv("VPN_CONFIG_FILE", filepath.Join(t.TempDir(), "absent.conf"))

	if _, err := Resolve(map[string]string{"vpn_config": "[Interface] # the stored one"}); err == nil {
		t.Fatal("an unreadable VPN_CONFIG_FILE fell through to the stored value")
	}
}

// Validation is start-up's, called early. The assertion is against the error
// config.Load produces for the same input rather than against a literal, so the
// two cannot drift into a settings screen that accepts what the next boot
// rejects.
func TestValidationSaysWhatTheNextStartWouldSay(t *testing.T) {
	for _, tc := range []struct{ key, env, value string }{
		{"search_timeout", "SEARCH_TIMEOUT", "half an hour"},
		{"search_timeout", "SEARCH_TIMEOUT", "-5s"},
		{"torrent_max_conns", "TORRENT_MAX_CONNS", "9000"},
		{"torrent_port", "TORRENT_PORT", "not-a-port"},
		{"vpn_required", "VPN_REQUIRED", "maybe"},
		{"torrent_backend", "TORRENT_BACKEND", "transmission"},
		// Mixed case, because both sides normalise and only one of them may
		// quote the normalised form back at you.
		{"torrent_backend", "TORRENT_BACKEND", "QBittorrent-ish"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			fromWrite := Validate(tc.key, tc.value)
			if fromWrite == nil {
				t.Fatalf("Validate(%q, %q) accepted it", tc.key, tc.value)
			}

			clearEnv(t)
			t.Setenv(tc.env, tc.value)
			_, fromStart := config.Load(nil)
			if fromStart == nil {
				t.Fatalf("config.Load accepted %s=%q", tc.env, tc.value)
			}

			if fromWrite.Error() != fromStart.Error() {
				t.Errorf("the two messages have drifted:\n  write: %v\n  start: %v", fromWrite, fromStart)
			}
		})
	}
}

func TestValidationAcceptsWhatStartUpAccepts(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"search_timeout", "45s"},
		{"torrent_max_conns", "40"},
		{"torrent_port", "0"},
		{"vpn_required", "false"},
		{"torrent_backend", "qbittorrent"},
		{"qbit_url", "http://192.168.1.26:8080"},
		{"library_movies", "/media/storage/media/movies"},
		{"tmdb_api_key", "anything at all"},
	} {
		if err := Validate(tc.key, tc.value); err != nil {
			t.Errorf("Validate(%q, %q) = %v", tc.key, tc.value, err)
		}
	}
}

// A key that is not in the registry is not a setting. Storing one would be a
// row nothing ever reads, which is worse than a refusal because it looks like
// it worked.
func TestValidationRefusesAKeyThatIsNotASetting(t *testing.T) {
	if err := Validate("db_path", "/tmp/somewhere-else.db"); err == nil {
		t.Fatal("db_path was accepted as a setting")
	}
	if err := Validate("tmdb_api_kye", "typo"); err == nil {
		t.Fatal("a misspelled key was accepted")
	}
}

// The WireGuard config is checked by the parser that will bring the tunnel up,
// not by a second one that agrees with it today.
func TestAWireGuardConfigIsCheckedByTheRealParser(t *testing.T) {
	const good = `[Interface]
PrivateKey = qJPsm5m1lPXBLSbBHhBz4iHRUmBRPKZ5o5s4hMcQO1Y=
Address = 10.5.0.2/32
DNS = 10.5.0.1

[Peer]
PublicKey = qJPsm5m1lPXBLSbBHhBz4iHRUmBRPKZ5o5s4hMcQO1Y=
Endpoint = sg701.nordvpn.com:51820
AllowedIPs = 0.0.0.0/0
`
	if err := Validate("vpn_config", good); err != nil {
		t.Fatalf("a valid config was refused: %v", err)
	}

	withoutKey := strings.ReplaceAll(good, "PrivateKey = qJPsm5m1lPXBLSbBHhBz4iHRUmBRPKZ5o5s4hMcQO1Y=\n", "")
	err := Validate("vpn_config", withoutKey)
	if err == nil {
		t.Fatal("a config with no PrivateKey was accepted, and would fail at the next start instead")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "privatekey") {
		t.Errorf("the message does not name the missing field: %v", err)
	}
}

// The scrub list. A whole .conf will never appear in a log line, so scrubbing
// the blob protects nothing; the two keys inside it are the strings that could
// leak, and they are what T37 asked for and never got.
func TestSecretsCarryTheTunnelsKeysRatherThanItsFile(t *testing.T) {
	clearEnv(t)
	const key = "qJPsm5m1lPXBLSbBHhBz4iHRUmBRPKZ5o5s4hMcQO1Y="
	conf := "[Interface]\nPrivateKey = " + key + "\nAddress = 10.5.0.2/32\n\n" +
		"[Peer]\nPublicKey = " + key + "\nEndpoint = sg701.nordvpn.com:51820\nAllowedIPs = 0.0.0.0/0\n"

	res, err := Resolve(map[string]string{
		"vpn_config":   conf,
		"tmdb_api_key": "a-tmdb-key",
		"qbit_user":    "nethmin",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	secrets := res.Secrets()
	var sawKey, sawTMDB bool
	for _, s := range secrets {
		if s == key {
			sawKey = true
		}
		if s == "a-tmdb-key" {
			sawTMDB = true
		}
		if s == conf {
			t.Error("the whole .conf is in the scrub list; no log line will ever contain it")
		}
		if s == "nethmin" {
			t.Error("a value that is not a secret is in the scrub list")
		}
	}
	if !sawKey {
		t.Error("the tunnel's private key is not in the scrub list")
	}
	if !sawTMDB {
		t.Error("TMDB_API_KEY is not in the scrub list")
	}
}
