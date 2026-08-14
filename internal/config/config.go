// Package config holds the environment-driven settings, read once at startup.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the whole of curator's configuration. It is read once, at startup,
// and passed down explicitly — nothing reads the environment again later.
type Config struct {
	Port          int
	DBPath        string
	LibraryMovies string
	TMDBAPIKey    string
	LogLevel      slog.Level

	// LogBufferLines is how much of the process log is kept in memory for
	// GET /api/logs and the Logs screen. It is a ring: the oldest line is
	// dropped, and the API reports how many were dropped rather than serving a
	// log with a silent gap in it.
	LogBufferLines int

	// Phase 2: indexers.
	MinterURL      string
	SearchTimeout  time.Duration
	SearchCacheTTL time.Duration

	// Phase 7: which of them are asked at all. All three are on unless somebody
	// turns one off, so an unset value keeps exactly the behaviour phases 2-6
	// shipped — and turning 1337x off is also what stops minter being probed for
	// a service nobody asked for (docs/tasks/T40-settings-api.md).
	IndexerYTS   bool
	IndexerTPB   bool
	IndexerX1337 bool

	// Phase 3: downloads.
	QBitURL              string
	QBitUser             string
	QBitPass             string
	QBitCategory         string
	DownloadPollInterval time.Duration

	// Phase 4: import.
	DownloadsPath     string
	QBitDownloadsPath string
	JellyfinURL       string
	JellyfinAPIKey    string

	// Phase 6: curator's own engine, and the tunnel under it.
	//
	// TorrentBackend chooses which client downloads. `embedded` is the default
	// and is the only one curator can promise a VPN for, because it is the only
	// one whose socket curator owns (docs/decisions.md D22, D27).
	TorrentBackend    string
	DownloadsDir      string
	TorrentMaxConns   int
	TorrentPort       int
	TorrentStallAfter time.Duration

	VPNConfig     string
	VPNRequired   bool
	VPNIPCheckURL string
}

// The two torrent backends. An unknown value is a startup error naming both,
// not a silent fallback to either.
const (
	BackendEmbedded    = "embedded"
	BackendQBittorrent = "qbittorrent"
)

// Embedded reports whether curator downloads in this process.
func (c *Config) Embedded() bool { return c.TorrentBackend == BackendEmbedded }

// VPNConfigured reports whether there is a tunnel to bring up.
func (c *Config) VPNConfigured() bool { return strings.TrimSpace(c.VPNConfig) != "" }

// JellyfinConfigured reports whether the library refresh has somewhere to go.
//
// An unset JELLYFIN_API_KEY disables the refresh and is not a startup error —
// the same posture as an unset TMDB_API_KEY and an unset QBIT_USER, and the
// state curator ships in today, because the Pi's Jellyfin has no API key yet.
// A refresh is the softest step of an import: the file is already hardlinked
// into the library and Jellyfin rescans on its own schedule regardless
// (decisions.md D15).
func (c *Config) JellyfinConfigured() bool {
	return c.JellyfinURL != "" && c.JellyfinAPIKey != ""
}

// DownloadsConfigured reports whether there is something to download with.
//
// The embedded engine always has: it is in this binary. qBittorrent needs
// credentials, and an unset QBIT_USER is a supported state rather than a broken
// one — the library still scans, search still works, and only dispatch reports
// itself unconfigured.
//
// An unset QBIT_USER is a supported state, not a broken one — the same posture as
// an unset TMDB_API_KEY. The library still scans and search still works; only
// dispatch reports itself unconfigured, and the poller is never started. A service
// that refuses to boot because one of five integrations is unconfigured is worse
// at being partially useful.
func (c *Config) DownloadsConfigured() bool {
	if c.Embedded() {
		return true
	}
	return c.QBitURL != "" && c.QBitUser != ""
}

// Defaults. LibraryMovies points at the fixture so `go run ./cmd/curator` does
// something useful on a fresh clone, with no config file and no mounted library.
const (
	defaultPort          = 8090
	defaultDBPath        = "./curator.db"
	defaultLibraryMovies = "./testdata/library/movies"
	defaultLogLevel      = "info"

	// defaultLogBufferLines is a thousand lines — a few hundred kilobytes, and
	// enough to cover either hours of an idle poller or the whole of a busy
	// import. Large enough to be useful without being a memory leak with a
	// friendly name.
	defaultLogBufferLines = 1000

	// defaultMinterURL is the literal IPv4 address, not "localhost": minter binds
	// IPv4 only, so "localhost" resolves to ::1 first and the connection fails
	// while 127.0.0.1 works. Inside Docker the name "minter" resolves correctly
	// and the point is moot — this default is for running on a laptop, which is
	// where it gets typed most.
	defaultMinterURL = "http://127.0.0.1:8191"

	// defaultSearchTimeout bounds a whole search. 1337x through minter measures
	// ~9-15 s because a real browser has to clear the challenge, so this has to
	// leave room for that; a straggler past it is omitted, not waited for.
	defaultSearchTimeout = 30 * time.Second

	// defaultSearchCacheTTL is how long 1337x results are reused. An hour is far
	// longer than the seconds a manual pick takes, which is what makes a repeat
	// search free without serving anything meaningfully stale.
	defaultSearchCacheTTL = time.Hour

	// defaultQBitURL is a laptop default. On the Pi, qBittorrent shares gluetun's
	// network namespace and has no ports of its own, so it is reached at
	// http://gluetun:8080 from inside Docker and 192.168.1.26:8080 from outside.
	defaultQBitURL = "http://127.0.0.1:8080"

	// defaultQBitCategory scopes everything curator adds. It is a category rather
	// than only a tag because a category also sets the save path and gives
	// torrents/info a filter — which is what makes phase 4's importer incapable of
	// touching a torrent somebody added by hand. See D13.
	defaultQBitCategory = "curator"

	// defaultDownloadPollInterval reconciles qBittorrent into the downloads table.
	// One request per tick covers every download at once, so this is cheap; ten
	// seconds is well inside what a human watching a progress bar expects.
	defaultDownloadPollInterval = 10 * time.Second

	// defaultQBitDownloadsPath is the downloads root inside qBittorrent's own
	// namespace: its mount on the Pi is host /media/storage/media/downloads →
	// container /downloads, so every path it reports starts here. It pairs with
	// DOWNLOADS_PATH, which has no default at all — empty means "use the path
	// qBittorrent reported verbatim", which is correct on a laptop and correct
	// whenever curator shares qBittorrent's mount, and those are the two cases
	// that should need no configuration.
	defaultQBitDownloadsPath = "/downloads"

	// defaultJellyfinURL is a laptop default, like defaultQBitURL. On the Pi
	// Jellyfin is 10.10.7 at 192.168.1.26:8096, and http://jellyfin:8096 inside
	// Docker.
	defaultJellyfinURL = "http://127.0.0.1:8096"

	// defaultDownloadsDir is where the embedded engine writes payloads. It is
	// relative for the same reason defaultDBPath is: `go run ./cmd/curator` on
	// a fresh clone should do something rather than ask for configuration.
	defaultDownloadsDir = "./downloads"

	// defaultTorrentStallAfter is how long a torrent may gain nothing before it
	// says so. Metadata arrived in 3.2 s in phase 6's spike and a healthy
	// download moves every second, so five minutes is long enough that
	// "stalled" means it.
	defaultTorrentStallAfter = 5 * time.Minute

	// defaultSecretKeyName is the key that decrypts the stored settings, placed
	// beside the database so it rides in the same volume and survives the same
	// restart. Anything that copies the volume copies both, which is stated
	// plainly in D28 rather than implied otherwise — what it defends against is
	// a database that travels alone.
	defaultSecretKeyName = "curator.key"
)

// Boot is the part of the configuration that has to exist before there is a
// database to read the rest from.
//
// It is a subset, not a separate configuration: Load reads every one of these
// again, so *Config stays the single thing passed down and nothing downstream
// has to know that start-up happens in two steps.
type Boot struct {
	DBPath         string
	LogLevel       slog.Level
	LogBufferLines int

	// SecretKey and SecretKeyFile are where the key that decrypts the stored
	// settings comes from. It cannot itself be a stored setting, for the same
	// reason DBPath cannot: a key readable from the store it protects is not a
	// key (docs/decisions.md D28).
	SecretKey     string
	SecretKeyFile string
}

// Bootstrap reads the environment-only settings.
//
// These five are not in the settings registry and are deliberately unsettable
// from the settings screen. The rule is one sentence — anything needed in order
// to reach the settings screen is not settable from the settings screen — and
// they are read through os.Getenv here and in Load, never through the resolved
// map, so a row written into the settings table by hand cannot move the
// database or silence the log.
func Bootstrap() (Boot, error) {
	boot := Boot{
		DBPath:         env("DB_PATH", defaultDBPath),
		LogBufferLines: defaultLogBufferLines,
		SecretKey:      os.Getenv("SECRET_KEY"),
		SecretKeyFile:  os.Getenv("SECRET_KEY_FILE"),
	}

	level, err := parseLevel(env("LOG_LEVEL", defaultLogLevel))
	if err != nil {
		return Boot{}, err
	}
	boot.LogLevel = level

	if raw := os.Getenv("LOG_BUFFER_LINES"); raw != "" {
		lines, err := strconv.Atoi(raw)
		if err != nil || lines < 1 {
			return Boot{}, fmt.Errorf("LOG_BUFFER_LINES %q: want a positive whole number", raw)
		}
		boot.LogBufferLines = lines
	}

	// The key lives beside the database, so it rides in the same volume and
	// survives the same restart. It is a separate file rather than a row
	// because the thing that leaks a database — a copy, a backup, a dump pasted
	// into an issue — takes the rows and not the neighbour.
	if boot.SecretKeyFile == "" {
		boot.SecretKeyFile = filepath.Join(filepath.Dir(boot.DBPath), defaultSecretKeyName)
	}
	return boot, nil
}

// Load reads the configuration, applying defaults for anything unset. It
// returns an error rather than panicking so main can report the problem and
// exit cleanly.
//
// resolved is the settings map with environment precedence ALREADY applied —
// internal/settings.Resolve produces it, keyed by setting key. Passing nil
// means "the environment and the defaults", which is exactly what phases 1-6
// did and is still what every test and every caller outside cmd/curator wants.
//
// Precedence lives in one place, in internal/settings, rather than being
// implemented again here: this reads the resolved value first and falls through
// to the environment, which is the same answer when Resolve ran and the only
// answer when it did not.
//
// An empty TMDB_API_KEY is deliberately not an error: scanning the library works
// without it, only metadata matching does not.
func Load(resolved map[string]string) (*Config, error) {
	r := source(resolved)

	cfg := &Config{
		Port:          defaultPort,
		DBPath:        env("DB_PATH", defaultDBPath),
		LibraryMovies: r.get("LIBRARY_MOVIES", defaultLibraryMovies),
		TMDBAPIKey:    r.get("TMDB_API_KEY", ""),
		MinterURL:     r.get("MINTER_URL", defaultMinterURL),
		QBitURL:       r.get("QBIT_URL", defaultQBitURL),
		QBitUser:      r.get("QBIT_USER", ""),
		QBitPass:      r.get("QBIT_PASS", ""),
		QBitCategory:  r.get("QBIT_CATEGORY", defaultQBitCategory),

		LogBufferLines: defaultLogBufferLines,

		// DOWNLOADS_PATH has no default, and empty is a meaningful value — "the
		// path qBittorrent reports is already the path curator sees".
		DownloadsPath:     r.get("DOWNLOADS_PATH", ""),
		QBitDownloadsPath: r.get("QBIT_DOWNLOADS_PATH", defaultQBitDownloadsPath),
		JellyfinURL:       r.get("JELLYFIN_URL", defaultJellyfinURL),
		JellyfinAPIKey:    r.get("JELLYFIN_API_KEY", ""),

		DownloadsDir:  r.get("DOWNLOADS_DIR", defaultDownloadsDir),
		VPNIPCheckURL: r.get("VPN_IP_CHECK_URL", ""),

		// Mandatory by default, and it has to be typed to turn off. A VPN that
		// defaults to optional is a slogan (docs/decisions.md D27).
		VPNRequired: true,

		// Every indexer on by default: these three variables are new in phase 7,
		// so an unset value has to mean what curator did before they existed.
		IndexerYTS:   true,
		IndexerTPB:   true,
		IndexerX1337: true,
	}

	// Parsed from the raw value rather than from a pre-lowered copy, so the
	// message quotes what was actually typed — and so it is the same message
	// internal/settings produces when the same value is refused on its way into
	// the database, which a test in that package asserts.
	backend, err := ParseBackend(r.get("TORRENT_BACKEND", BackendEmbedded))
	if err != nil {
		return nil, err
	}
	cfg.TorrentBackend = backend

	cfg.VPNConfig, err = r.vpnConfig()
	if err != nil {
		return nil, err
	}
	// Every boolean defaults to something deliberate above, so an unset variable
	// is left alone rather than parsed into a false.
	for _, b := range []struct {
		key   string
		field *bool
	}{
		{"VPN_REQUIRED", &cfg.VPNRequired},
		{"INDEXER_YTS", &cfg.IndexerYTS},
		{"INDEXER_TPB", &cfg.IndexerTPB},
		{"INDEXER_1337X", &cfg.IndexerX1337},
	} {
		raw := r.get(b.key, "")
		if raw == "" {
			continue
		}
		value, parseErr := ParseBool(b.key, raw)
		if parseErr != nil {
			return nil, parseErr
		}
		*b.field = value
	}

	for _, n := range []struct {
		key   string
		field *int
		min   int
		max   int
	}{
		{"TORRENT_MAX_CONNS", &cfg.TorrentMaxConns, 1, 500},
		{"TORRENT_PORT", &cfg.TorrentPort, 0, 65535},
	} {
		raw := r.get(n.key, "")
		if raw == "" {
			continue
		}
		value, parseErr := ParseInt(n.key, raw, n.min, n.max)
		if parseErr != nil {
			return nil, parseErr
		}
		*n.field = value
	}

	// PORT, LOG_LEVEL and LOG_BUFFER_LINES are read from the environment
	// directly and never from the resolved map. They are Bootstrap's, and the
	// rule is that anything needed in order to reach the settings screen is not
	// settable from it.
	if raw := os.Getenv("PORT"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("PORT %q: not a number", raw)
		}
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("PORT %d: out of range 1-65535", port)
		}
		cfg.Port = port
	}

	if raw := os.Getenv("LOG_BUFFER_LINES"); raw != "" {
		lines, err := strconv.Atoi(raw)
		if err != nil || lines < 1 {
			return nil, fmt.Errorf("LOG_BUFFER_LINES %q: want a positive whole number", raw)
		}
		cfg.LogBufferLines = lines
	}

	level, err := parseLevel(env("LOG_LEVEL", defaultLogLevel))
	if err != nil {
		return nil, err
	}
	cfg.LogLevel = level

	for _, d := range []struct {
		key      string
		field    *time.Duration
		fallback time.Duration
	}{
		{"SEARCH_TIMEOUT", &cfg.SearchTimeout, defaultSearchTimeout},
		{"SEARCH_CACHE_TTL", &cfg.SearchCacheTTL, defaultSearchCacheTTL},
		{"DOWNLOAD_POLL_INTERVAL", &cfg.DownloadPollInterval, defaultDownloadPollInterval},
		{"TORRENT_STALL_AFTER", &cfg.TorrentStallAfter, defaultTorrentStallAfter},
	} {
		raw := r.get(d.key, "")
		if raw == "" {
			*d.field = d.fallback
			continue
		}
		value, err := ParseDuration(d.key, raw)
		if err != nil {
			return nil, err
		}
		*d.field = value
	}

	return cfg, nil
}

// source reads a value out of the resolved settings, then out of the
// environment, then out of the caller's default.
type source map[string]string

func (r source) get(key, fallback string) string {
	// The resolved map is keyed by the lower-case form of the variable, which
	// is the invariant internal/settings is built on and asserts. Lower-casing
	// here rather than importing that package is what keeps this one importing
	// nothing of curator's.
	if v := r[strings.ToLower(key)]; v != "" {
		return v
	}
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// vpnConfig returns the tunnel's wg-quick file.
//
// The resolved value already accounts for VPN_CONFIG and VPN_CONFIG_FILE, so
// the two branches below only run when Load was given nil — which is every
// caller that is not cmd/curator, including every test.
//
// Both variables exist because both are how it arrives: a provider hands out a
// file, and a container is configured with a variable. A path that cannot be
// read is an error rather than a silent "no VPN" — a mandatory tunnel that
// disappears because of a typo in a path is the failure this whole design is
// trying not to have.
func (r source) vpnConfig() (string, error) {
	if v := r["vpn_config"]; strings.TrimSpace(v) != "" {
		return v, nil
	}
	if inline := strings.TrimSpace(os.Getenv("VPN_CONFIG")); inline != "" {
		return inline, nil
	}
	path := strings.TrimSpace(os.Getenv("VPN_CONFIG_FILE"))
	if path == "" {
		return "", nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("VPN_CONFIG_FILE %s: %w", path, err)
	}
	return string(body), nil
}

// ParseBackend checks a TORRENT_BACKEND value.
//
// Exported, like the three parsers below it, so that internal/settings can
// validate a value on its way *into* the database with the function that will
// reject it at the next start rather than with a second one that agrees today.
// A settings screen accepting a value the next boot refuses is a settings
// screen that bricks a container (docs/tasks/T39-settings-store.md).
func ParseBackend(raw string) (string, error) {
	backend := strings.ToLower(strings.TrimSpace(raw))
	switch backend {
	case BackendEmbedded, BackendQBittorrent:
		return backend, nil
	default:
		return "", fmt.Errorf("TORRENT_BACKEND %q: want %q or %q", raw, BackendEmbedded, BackendQBittorrent)
	}
}

// ParseBool reads a boolean setting.
func ParseBool(key, raw string) (bool, error) {
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, fmt.Errorf("%s %q: want true or false", key, raw)
	}
	return value, nil
}

// ParseInt reads a whole number and bounds it.
func ParseInt(key, raw string, min, max int) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < min || value > max {
		return 0, fmt.Errorf("%s %q: want a number between %d and %d", key, raw, min, max)
	}
	return value, nil
}

// ParseDuration reads a Go duration string ("30s", "1h").
//
// A non-positive value is an error rather than a silent disable: a zero
// SEARCH_TIMEOUT would cancel every search the instant it started, and a zero
// TTL would quietly reinstate the browser launch on every repeat search that
// the cache exists to prevent.
func ParseDuration(key, raw string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("%s %q: not a duration (want e.g. \"30s\", \"1h\")", key, raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s %q: must be positive", key, raw)
	}
	return d, nil
}

// Addr is the listen address for the HTTP server.
func (c *Config) Addr() string {
	return fmt.Sprintf(":%d", c.Port)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("LOG_LEVEL %q: want debug, info, warn or error", raw)
	}
}
