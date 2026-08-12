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
}

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

// DownloadsConfigured reports whether qBittorrent has credentials to work with.
//
// An unset QBIT_USER is a supported state, not a broken one — the same posture as
// an unset TMDB_API_KEY. The library still scans and search still works; only
// dispatch reports itself unconfigured, and the poller is never started. A service
// that refuses to boot because one of five integrations is unconfigured is worse
// at being partially useful.
func (c *Config) DownloadsConfigured() bool {
	return c.QBitURL != "" && c.QBitUser != ""
}

// Defaults. LibraryMovies points at the fixture so `go run ./cmd/curator` does
// something useful on a fresh clone, with no config file and no mounted library.
const (
	defaultPort          = 8090
	defaultDBPath        = "./curator.db"
	defaultLibraryMovies = "./testdata/library/movies"
	defaultLogLevel      = "info"

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

		// DOWNLOADS_PATH is read raw rather than through env(): it has no
		// default, and empty is a meaningful value — "the path qBittorrent
		// reports is already the path curator sees".
		DownloadsPath:     os.Getenv("DOWNLOADS_PATH"),
		QBitDownloadsPath: env("QBIT_DOWNLOADS_PATH", defaultQBitDownloadsPath),
		JellyfinURL:       env("JELLYFIN_URL", defaultJellyfinURL),
		JellyfinAPIKey:    os.Getenv("JELLYFIN_API_KEY"),
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

	return cfg, nil
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
