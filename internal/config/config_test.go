package config

import (
	"log/slog"
	"testing"
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
}

func TestLoadReadsEnvironment(t *testing.T) {
	t.Setenv("PORT", "9999")
	t.Setenv("DB_PATH", "/tmp/x.db")
	t.Setenv("LIBRARY_MOVIES", "/media/storage/media/movies")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
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
