// Package settings is the catalogue of everything curator can be configured
// with, and the rule that decides which source wins.
//
// It exists because phase 7 has two ways to configure one service — the
// environment and a database — and two ways is already one more than ideal.
// Two *vocabularies* would be the version of this that rots, so there is one
// registry: every setting's stored key is the lower-case form of its
// environment variable, asserted in a test rather than remembered
// (docs/decisions.md D28).
//
// It does not hold defaults. The default of SEARCH_TIMEOUT is a documented
// constant in internal/config, next to the reasoning for its value, and a copy
// here would be a second answer that drifts. What a screen shows as the current
// value is what this process actually resolved, read off *config.Config — which
// cannot disagree with the running program, because it is the running program.
package settings

import (
	"fmt"
	"strings"

	"github.com/DulsaraNethmin/curator/internal/config"
	"github.com/DulsaraNethmin/curator/internal/vpn"
)

// Kind is what a value is, which decides how it is validated and what the
// settings screen renders for it.
type Kind string

const (
	KindText      Kind = "text"
	KindInt       Kind = "int"
	KindBool      Kind = "bool"
	KindDuration  Kind = "duration"
	KindURL       Kind = "url"
	KindPath      Kind = "path"
	KindEnum      Kind = "enum"
	KindMultiline Kind = "multiline"

	// KindPassword is write-only and one-way. It is not KindText with Secret
	// set, because the difference is the whole design: a secret is encrypted
	// because curator has to be able to use it, and a password is hashed
	// because curator only ever has to compare one (docs/decisions.md D28).
	KindPassword Kind = "password"
)

// Setting is one configurable thing.
type Setting struct {
	// Key is how it is stored, and it is always Env lower-cased.
	Key string

	// Env is the environment variable that overrides it. Every setting has
	// exactly one, including the three this phase invented, so `docker run -e`
	// stays a promise rather than a suggestion.
	Env string

	// FileEnv is a second variable naming a *file* whose contents are the
	// value. Only VPN_CONFIG_FILE has one: a provider hands out a .conf and a
	// container hands out a variable, and both have to work.
	FileEnv string

	Group string
	Kind  Kind

	// Secret means the value is never sent to a browser and is encrypted at
	// rest. Not masked, not truncated, not its length — a masked secret still
	// confirms an existence to anyone on the LAN (docs/decisions.md D17, which
	// survives phase 7 with its threat model intact).
	Secret bool

	// Options is the permitted set for KindEnum.
	Options []string

	// Min and Max bound a KindInt.
	Min, Max int

	// Immediate means a write takes effect on the next request rather than at
	// the next start. Only the password does, and it has to: one that took
	// effect after a restart would leave you unprotected until then
	// (docs/decisions.md D29).
	Immediate bool

	// validate overrides the default check for the kind. It exists so that the
	// two settings with a real parser behind them — the backend enum and the
	// WireGuard config — are checked by *that* parser rather than by a second
	// one that agrees with it today.
	validate func(value string) error

	// expand names the substrings that must be scrubbed out of a log line when
	// the value itself is not the secret. Only the WireGuard config has one: it
	// is a whole file, and no log line will ever contain a whole file, while
	// the two keys inside it are exactly the strings that could leak.
	expand func(value string) []string
}

// registry is the catalogue, in the order a settings screen shows it.
//
// It is the second place this list exists; the first is
// docs/phase-7.md#the-settings-catalogue, and a test asserts they agree in
// count so a setting added to one and not the other fails a build rather than
// becoming a field nobody can save.
var registry = []Setting{
	{Env: "LIBRARY_MOVIES", Group: "library", Kind: KindPath},
	{Env: "TMDB_API_KEY", Group: "library", Kind: KindText, Secret: true},

	{Env: "TORRENT_BACKEND", Group: "downloads", Kind: KindEnum,
		Options:  []string{config.BackendEmbedded, config.BackendQBittorrent},
		validate: func(v string) error { _, err := config.ParseBackend(v); return err }},
	{Env: "DOWNLOADS_DIR", Group: "downloads", Kind: KindPath},
	{Env: "TORRENT_MAX_CONNS", Group: "downloads", Kind: KindInt, Min: 1, Max: 500},
	{Env: "TORRENT_PORT", Group: "downloads", Kind: KindInt, Min: 0, Max: 65535},
	{Env: "TORRENT_STALL_AFTER", Group: "downloads", Kind: KindDuration},
	{Env: "DOWNLOAD_POLL_INTERVAL", Group: "downloads", Kind: KindDuration},

	{Env: "QBIT_URL", Group: "qbittorrent", Kind: KindURL},
	{Env: "QBIT_USER", Group: "qbittorrent", Kind: KindText},
	{Env: "QBIT_PASS", Group: "qbittorrent", Kind: KindText, Secret: true},
	{Env: "QBIT_CATEGORY", Group: "qbittorrent", Kind: KindText},
	{Env: "DOWNLOADS_PATH", Group: "qbittorrent", Kind: KindPath},
	{Env: "QBIT_DOWNLOADS_PATH", Group: "qbittorrent", Kind: KindPath},

	{Env: "VPN_CONFIG", FileEnv: "VPN_CONFIG_FILE", Group: "vpn", Kind: KindMultiline, Secret: true,
		validate: func(v string) error { _, err := vpn.ParseConfig(v); return err },
		expand:   tunnelKeys},
	{Env: "VPN_REQUIRED", Group: "vpn", Kind: KindBool},
	{Env: "VPN_IP_CHECK_URL", Group: "vpn", Kind: KindURL},

	// Updating. curator reads a version number and asks somebody else to
	// install it (docs/decisions.md D44), so these three are the whole of it:
	// whether to look, where to look, and who to ask.
	{Env: "UPDATE_CHECK", Group: "updates", Kind: KindBool},
	{Env: "UPDATE_CHECK_URL", Group: "updates", Kind: KindURL},
	{Env: "UPDATER_URL", Group: "updates", Kind: KindURL},
	{Env: "UPDATER_TOKEN", Group: "updates", Kind: KindText, Secret: true},

	{Env: "INDEXER_YTS", Group: "indexers", Kind: KindBool},
	{Env: "INDEXER_TPB", Group: "indexers", Kind: KindBool},
	{Env: "INDEXER_1337X", Group: "indexers", Kind: KindBool},
	{Env: "MINTER_URL", Group: "indexers", Kind: KindURL},
	{Env: "SEARCH_TIMEOUT", Group: "indexers", Kind: KindDuration},
	{Env: "SEARCH_CACHE_TTL", Group: "indexers", Kind: KindDuration},

	// Phase 7 promised the registry would make playback one entry rather than a
	// screen (docs/phase-7.md, "Playback is not in this table"). This is that
	// entry. There is deliberately no "prefer direct play" toggle beside it: it
	// would be a row nothing reads, because direct play is always tried first
	// and the browser decides. The only switch anybody needs is whether the
	// remux exists at all, and this is it.
	{Env: "FFMPEG_PATH", Group: "playback", Kind: KindPath},

	// Phase 9's one new row, and it is a record of an answer rather than a
	// switch. Nothing reads it except the screen that asked — it does not gate
	// the Play button, hide the Jellyfin link or change what any endpoint does,
	// because that would be the "prefer direct play" toggle above being
	// reintroduced under a new name (docs/tasks/T65-playback-screen.md).
	//
	// Empty is a legal value and the one every install starts in: it means the
	// question has not been put yet, which is what makes the Playback screen a
	// first-run destination instead of a nag.
	{Env: "PLAYBACK_TARGET", Group: "playback", Kind: KindEnum,
		Options:  []string{config.PlaybackBrowser, config.PlaybackJellyfin},
		validate: func(v string) error { _, err := config.ParsePlaybackTarget(v); return err }},

	{Env: "JELLYFIN_URL", Group: "jellyfin", Kind: KindURL},
	{Env: "JELLYFIN_API_KEY", Group: "jellyfin", Kind: KindText, Secret: true},

	// The second of phase 8's two rows, and the one that is not obviously
	// needed until it is: JELLYFIN_URL is what curator uses to reach Jellyfin,
	// and inside Docker that is http://jellyfin:8096 — a name a browser cannot
	// resolve. Open in Jellyfin is clicked by a browser. One URL cannot be
	// both, and empty falls back to JELLYFIN_URL (docs/phase-8.md).
	{Env: "JELLYFIN_PUBLIC_URL", Group: "jellyfin", Kind: KindURL},

	{Env: "AUTH_ENABLED", Group: "access", Kind: KindBool, Immediate: true},
	{Env: "AUTH_PASSWORD", Group: "access", Kind: KindPassword, Secret: true, Immediate: true},
}

// index is the registry by key, built once.
var index = func() map[string]Setting {
	m := make(map[string]Setting, len(registry))
	for i := range registry {
		// The key is derived rather than typed, so the invariant this package
		// is built on cannot be broken by a typo in the table above.
		registry[i].Key = Key(registry[i].Env)
		m[registry[i].Key] = registry[i]
	}
	return m
}()

// Key is the stored key for an environment variable.
func Key(env string) string { return strings.ToLower(env) }

// All returns the catalogue in screen order.
func All() []Setting {
	out := make([]Setting, len(registry))
	copy(out, registry)
	return out
}

// Get returns one setting. A key that is not in the registry is not a setting:
// callers refuse the write rather than storing something nothing will ever read
// (docs/tasks/T40-settings-api.md).
func Get(key string) (Setting, bool) {
	s, ok := index[key]
	return s, ok
}

// Validate checks a value that is about to be stored.
//
// It is not called for a clear — an empty value is a deletion, handled above
// this package — so every check here may assume something was typed.
//
// The message is the one the next start-up would have printed, because for
// everything with a real parser it comes from that parser. A settings screen
// that accepts a value the next boot rejects is a settings screen that bricks a
// container.
func Validate(key, value string) error {
	s, ok := Get(key)
	if !ok {
		return fmt.Errorf("%s: not a setting", key)
	}
	if s.validate != nil {
		return s.validate(value)
	}
	switch s.Kind {
	case KindInt:
		_, err := config.ParseInt(s.Env, value, s.Min, s.Max)
		return err
	case KindBool:
		_, err := config.ParseBool(s.Env, value)
		return err
	case KindDuration:
		_, err := config.ParseDuration(s.Env, value)
		return err
	}

	// Text, paths, URLs and passwords are taken as typed. A URL is a Kind so a
	// screen can render the right input, not so this can be stricter than
	// start-up: config.Load has never validated QBIT_URL, and a value the
	// environment accepts while the UI refuses it would be the same drift this
	// package exists to prevent, pointing the other way.
	return nil
}

// tunnelKeys pulls the two secrets out of a wg-quick config.
//
// A config that does not parse yields nothing rather than an error: this is the
// scrub list being built at start-up, and a tunnel that cannot be parsed is
// about to be reported by the code that brings it up. Failing here would turn a
// bad .conf into "curator will not start" with a message about logging.
func tunnelKeys(value string) []string {
	parsed, err := vpn.ParseConfig(value)
	if err != nil {
		return nil
	}
	return []string{parsed.PrivateKey, parsed.PresharedKey}
}
