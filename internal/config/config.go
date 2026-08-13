// Package config holds the environment-driven settings, read once at startup.
package config

import (
	"fmt"
	"log/slog"
	"os"
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
)

// Load reads the configuration from the environment, applying defaults for
// anything unset. It returns an error rather than panicking so main can report
// the problem and exit cleanly.
//
// An empty TMDB_API_KEY is deliberately not an error: scanning the library works
// without it, only metadata matching does not.
func Load() (*Config, error) {
	cfg := &Config{
		Port:          defaultPort,
		DBPath:        env("DB_PATH", defaultDBPath),
		LibraryMovies: env("LIBRARY_MOVIES", defaultLibraryMovies),
		TMDBAPIKey:    os.Getenv("TMDB_API_KEY"),
		MinterURL:     env("MINTER_URL", defaultMinterURL),
		QBitURL:       env("QBIT_URL", defaultQBitURL),
		QBitUser:      os.Getenv("QBIT_USER"),
		QBitPass:      os.Getenv("QBIT_PASS"),
		QBitCategory:  env("QBIT_CATEGORY", defaultQBitCategory),

		LogBufferLines: defaultLogBufferLines,

		// DOWNLOADS_PATH is read raw rather than through env(): it has no
		// default, and empty is a meaningful value — "the path qBittorrent
		// reports is already the path curator sees".
		DownloadsPath:     os.Getenv("DOWNLOADS_PATH"),
		QBitDownloadsPath: env("QBIT_DOWNLOADS_PATH", defaultQBitDownloadsPath),
		JellyfinURL:       env("JELLYFIN_URL", defaultJellyfinURL),
		JellyfinAPIKey:    os.Getenv("JELLYFIN_API_KEY"),

		TorrentBackend: strings.ToLower(strings.TrimSpace(env("TORRENT_BACKEND", BackendEmbedded))),
		DownloadsDir:   env("DOWNLOADS_DIR", defaultDownloadsDir),
		VPNIPCheckURL:  os.Getenv("VPN_IP_CHECK_URL"),

		// Mandatory by default, and it has to be typed to turn off. A VPN that
		// defaults to optional is a slogan (docs/decisions.md D27).
		VPNRequired: true,
	}

	switch cfg.TorrentBackend {
	case BackendEmbedded, BackendQBittorrent:
	default:
		return nil, fmt.Errorf("TORRENT_BACKEND %q: want %q or %q", cfg.TorrentBackend, BackendEmbedded, BackendQBittorrent)
	}

	vpn, err := vpnConfig()
	if err != nil {
		return nil, err
	}
	cfg.VPNConfig = vpn
	if raw := os.Getenv("VPN_REQUIRED"); raw != "" {
		required, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			return nil, fmt.Errorf("VPN_REQUIRED %q: want true or false", raw)
		}
		cfg.VPNRequired = required
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
		raw := os.Getenv(n.key)
		if raw == "" {
			continue
		}
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil || value < n.min || value > n.max {
			return nil, fmt.Errorf("%s %q: want a number between %d and %d", n.key, raw, n.min, n.max)
		}
		*n.field = value
	}

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

	cfg.SearchTimeout, err = duration("SEARCH_TIMEOUT", defaultSearchTimeout)
	if err != nil {
		return nil, err
	}
	cfg.SearchCacheTTL, err = duration("SEARCH_CACHE_TTL", defaultSearchCacheTTL)
	if err != nil {
		return nil, err
	}
	cfg.DownloadPollInterval, err = duration("DOWNLOAD_POLL_INTERVAL", defaultDownloadPollInterval)
	if err != nil {
		return nil, err
	}
	cfg.TorrentStallAfter, err = duration("TORRENT_STALL_AFTER", defaultTorrentStallAfter)
	if err != nil {
		return nil, err
	}

	return cfg, nil
}

// vpnConfig reads the tunnel's wg-quick file, from VPN_CONFIG directly or from
// the path in VPN_CONFIG_FILE.
//
// Both exist because both are how it arrives: a provider hands out a file, and
// a container is configured with an environment variable. A path that cannot be
// read is an error rather than a silent "no VPN" — a mandatory tunnel that
// disappears because of a typo in a path is the failure this whole design is
// trying not to have.
func vpnConfig() (string, error) {
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

// duration reads a Go duration string ("30s", "1h"). A non-positive value is an
// error rather than a silent disable: a zero SEARCH_TIMEOUT would cancel every
// search the instant it started, and a zero TTL would quietly reinstate the
// browser launch on every repeat search that the cache exists to prevent.
func duration(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s %q: not a duration (want e.g. \"30s\", \"1h\")", key, raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s %q: must be positive", key, raw)
	}
	return d, nil
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
