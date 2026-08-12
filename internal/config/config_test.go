package config

import (
	"log/slog"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != defaultPort {
		t.Errorf("Port = %d, want %d", cfg.Port, defaultPort)
	}
	if cfg.DBPath != defaultDBPath {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, defaultDBPath)
	}
	if cfg.LibraryMovies != defaultLibraryMovies {
		t.Errorf("LibraryMovies = %q, want %q", cfg.LibraryMovies, defaultLibraryMovies)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
	// 127.0.0.1, not localhost: minter binds IPv4 only, so localhost resolves to
	// ::1 first and fails. Asserted because it is the kind of default that gets
	// "tidied" back to localhost by someone who has not hit it.
	if cfg.MinterURL != "http://127.0.0.1:8191" {
		t.Errorf("MinterURL = %q, want http://127.0.0.1:8191", cfg.MinterURL)
	}
	if cfg.SearchTimeout != 30*time.Second {
		t.Errorf("SearchTimeout = %v, want 30s", cfg.SearchTimeout)
	}
	if cfg.SearchCacheTTL != time.Hour {
		t.Errorf("SearchCacheTTL = %v, want 1h", cfg.SearchCacheTTL)
	}
}

func TestLoadReadsEnvironment(t *testing.T) {
	t.Setenv("PORT", "9999")
	t.Setenv("DB_PATH", "/tmp/x.db")
	t.Setenv("LIBRARY_MOVIES", "/media/storage/media/movies")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("MINTER_URL", "http://minter:8191")
	t.Setenv("SEARCH_TIMEOUT", "45s")
	t.Setenv("SEARCH_CACHE_TTL", "15m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MinterURL != "http://minter:8191" {
		t.Errorf("MinterURL = %q", cfg.MinterURL)
	}
	if cfg.SearchTimeout != 45*time.Second {
		t.Errorf("SearchTimeout = %v, want 45s", cfg.SearchTimeout)
	}
	if cfg.SearchCacheTTL != 15*time.Minute {
		t.Errorf("SearchCacheTTL = %v, want 15m", cfg.SearchCacheTTL)
	}
	if cfg.Port != 9999 {
		t.Errorf("Port = %d, want 9999", cfg.Port)
	}
	if cfg.Addr() != ":9999" {
		t.Errorf("Addr = %q, want \":9999\"", cfg.Addr())
	}
	if cfg.DBPath != "/tmp/x.db" {
		t.Errorf("DBPath = %q", cfg.DBPath)
	}
	if cfg.LibraryMovies != "/media/storage/media/movies" {
		t.Errorf("LibraryMovies = %q", cfg.LibraryMovies)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want debug", cfg.LogLevel)
	}
}

// An empty key is a normal state: the library still scans, only matching stops.
func TestEmptyTMDBKeyIsNotAnError(t *testing.T) {
	t.Setenv("TMDB_API_KEY", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TMDBAPIKey != "" {
		t.Errorf("TMDBAPIKey = %q, want empty", cfg.TMDBAPIKey)
	}
}

func TestLoadInvalid(t *testing.T) {
	tests := []struct {
		name, key, value string
	}{
		{"non-numeric port", "PORT", "eight thousand"},
		{"port out of range", "PORT", "70000"},
		{"port zero", "PORT", "0"},
		{"unknown log level", "LOG_LEVEL", "chatty"},
		{"non-duration search timeout", "SEARCH_TIMEOUT", "30 seconds"},
		{"bare number search timeout", "SEARCH_TIMEOUT", "30"},
		// Zero is rejected rather than treated as "no limit": it would cancel every
		// search the instant it started.
		{"zero search timeout", "SEARCH_TIMEOUT", "0s"},
		{"negative search timeout", "SEARCH_TIMEOUT", "-5s"},
		{"non-duration cache ttl", "SEARCH_CACHE_TTL", "one hour"},
		// A zero TTL would silently reinstate the browser launch on every repeat
		// search, which is the one cost phase 2 exists to remove.
		{"zero cache ttl", "SEARCH_CACHE_TTL", "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%q: want error, got nil", tt.key, tt.value)
			}
		})
	}
}
