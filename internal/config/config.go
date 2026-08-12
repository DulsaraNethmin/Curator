// Package config holds the environment-driven settings, read once at startup.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// Config is the whole of curator's configuration. It is read once, at startup,
// and passed down explicitly — nothing reads the environment again later.
type Config struct {
	Port          int
	DBPath        string
	LibraryMovies string
	TMDBAPIKey    string
	LogLevel      slog.Level
}

// Defaults. LibraryMovies points at the fixture so `go run ./cmd/curator` does
// something useful on a fresh clone, with no config file and no mounted library.
const (
	defaultPort          = 8090
	defaultDBPath        = "./curator.db"
	defaultLibraryMovies = "./testdata/library/movies"
	defaultLogLevel      = "info"
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
