package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	// Test-only, and one-way: internal/config imports nothing of curator's, so
	// the shared "/downloads" default cannot drift between the two places that
	// have to agree on it.
	"github.com/DulsaraNethmin/curator/internal/qbit"
)

func TestLoadDefaults(t *testing.T) {
	// Cleared explicitly so the test still means something in a shell that has
	// sourced .env — where QBIT_USER really is set, and where an inherited value
	// would otherwise make this assert the developer's machine, not the defaults.
	t.Setenv("QBIT_URL", "")
	t.Setenv("QBIT_USER", "")
	t.Setenv("QBIT_CATEGORY", "")
	t.Setenv("DOWNLOAD_POLL_INTERVAL", "")

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
	if cfg.QBitURL != "http://127.0.0.1:8080" {
		t.Errorf("QBitURL = %q", cfg.QBitURL)
	}
	if cfg.QBitCategory != "curator" {
		t.Errorf("QBitCategory = %q, want curator", cfg.QBitCategory)
	}
	if cfg.DownloadPollInterval != 10*time.Second {
		t.Errorf("DownloadPollInterval = %v, want 10s", cfg.DownloadPollInterval)
	}
	// Phase 6 changed what the default means: the embedded engine is in this
	// binary, so downloads are configured out of the box. What is NOT
	// configured by default is the VPN, and that is what stops a dispatch.
	if cfg.TorrentBackend != BackendEmbedded {
		t.Errorf("TorrentBackend = %q, want %q", cfg.TorrentBackend, BackendEmbedded)
	}
	if !cfg.DownloadsConfigured() {
		t.Error("DownloadsConfigured() = false with the embedded engine, which needs no credentials")
	}
	if cfg.VPNConfigured() {
		t.Error("VPNConfigured() = true with nothing set")
	}
	if !cfg.VPNRequired {
		t.Error("VPNRequired = false by default; a mandatory VPN that defaults to off is a slogan")
	}
	if cfg.TorrentStallAfter != 5*time.Minute {
		t.Errorf("TorrentStallAfter = %v, want 5m", cfg.TorrentStallAfter)
	}
}

// TestQBittorrentBackendStillNeedsCredentials: choosing the other backend puts
// the phase 3 posture back, unchanged.
func TestQBittorrentBackendStillNeedsCredentials(t *testing.T) {
	t.Setenv("TORRENT_BACKEND", "qbittorrent")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Embedded() {
		t.Error("Embedded() = true for TORRENT_BACKEND=qbittorrent")
	}
	if cfg.DownloadsConfigured() {
		t.Error("DownloadsConfigured() = true with no QBIT_USER")
	}
}

// An unknown backend fails at startup naming both valid values, rather than
// silently falling back to either. Falling back would mean a typo in a compose
// file quietly downloads without the tunnel.
func TestUnknownBackendIsAStartupError(t *testing.T) {
	t.Setenv("TORRENT_BACKEND", "transmission")

	_, err := Load()
	if err == nil {
		t.Fatal("Load accepted an unknown TORRENT_BACKEND")
	}
	for _, want := range []string{BackendEmbedded, BackendQBittorrent} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name %q", err, want)
		}
	}
}

// VPN_REQUIRED is the one escape hatch, and it has to be typed.
func TestVPNRequiredCanBeTurnedOff(t *testing.T) {
	t.Setenv("VPN_REQUIRED", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.VPNRequired {
		t.Error("VPN_REQUIRED=false was ignored")
	}

	t.Setenv("VPN_REQUIRED", "perhaps")
	if _, err := Load(); err == nil {
		t.Fatal("Load accepted VPN_REQUIRED=perhaps")
	}
}

// A VPN_CONFIG_FILE that cannot be read is an error, not a silent "no VPN".
func TestVPNConfigFileMustBeReadable(t *testing.T) {
	t.Setenv("VPN_CONFIG_FILE", "/nowhere/wg0.conf")
	if _, err := Load(); err == nil {
		t.Fatal("Load accepted an unreadable VPN_CONFIG_FILE")
	}

	path := filepath.Join(t.TempDir(), "wg0.conf")
	if err := os.WriteFile(path, []byte("[Interface]\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("VPN_CONFIG_FILE", path)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.VPNConfigured() {
		t.Error("VPNConfigured() = false after reading a file")
	}
}

// Downloads are unconfigured without a user, exactly as metadata is without a
// TMDB key: the rest of the service still works.
func TestDownloadsConfigured(t *testing.T) {
	t.Setenv("QBIT_USER", "nethmin")
	t.Setenv("QBIT_PASS", "secret")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.DownloadsConfigured() {
		t.Error("DownloadsConfigured() = false with a user set")
	}
	if cfg.QBitUser != "nethmin" || cfg.QBitPass != "secret" {
		t.Errorf("credentials not read: %q/%q", cfg.QBitUser, cfg.QBitPass)
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
	t.Setenv("QBIT_URL", "http://gluetun:8080")
	t.Setenv("QBIT_CATEGORY", "curator-test")
	t.Setenv("DOWNLOAD_POLL_INTERVAL", "5s")

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
	if cfg.QBitURL != "http://gluetun:8080" {
		t.Errorf("QBitURL = %q", cfg.QBitURL)
	}
	if cfg.QBitCategory != "curator-test" {
		t.Errorf("QBitCategory = %q", cfg.QBitCategory)
	}
	if cfg.DownloadPollInterval != 5*time.Second {
		t.Errorf("DownloadPollInterval = %v, want 5s", cfg.DownloadPollInterval)
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
		{"non-duration poll interval", "DOWNLOAD_POLL_INTERVAL", "ten seconds"},
		// A zero interval would spin the poller as fast as the CPU allows.
		{"zero poll interval", "DOWNLOAD_POLL_INTERVAL", "0s"},
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

// Phase 4's four. DOWNLOADS_PATH is the odd one: it has no default, and empty
// is a meaningful value rather than an unset one.
func TestImportDefaults(t *testing.T) {
	t.Setenv("DOWNLOADS_PATH", "")
	t.Setenv("QBIT_DOWNLOADS_PATH", "")
	t.Setenv("JELLYFIN_URL", "")
	t.Setenv("JELLYFIN_API_KEY", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DownloadsPath != "" {
		t.Errorf("DownloadsPath = %q, want empty — empty means use content_path verbatim, which is the laptop case", cfg.DownloadsPath)
	}
	if cfg.QBitDownloadsPath != defaultQBitDownloadsPath {
		t.Errorf("QBitDownloadsPath = %q, want %q", cfg.QBitDownloadsPath, defaultQBitDownloadsPath)
	}
	if cfg.JellyfinURL != defaultJellyfinURL {
		t.Errorf("JellyfinURL = %q, want %q", cfg.JellyfinURL, defaultJellyfinURL)
	}
	if cfg.JellyfinAPIKey != "" {
		t.Errorf("JellyfinAPIKey = %q, want empty", cfg.JellyfinAPIKey)
	}
	// The state curator ships in today: the Pi's Jellyfin has no API key yet.
	if cfg.JellyfinConfigured() {
		t.Error("JellyfinConfigured() = true with no key")
	}
}

// The default has to agree with the constant the qBittorrent adapter falls back
// to, or a deployment that sets DOWNLOADS_PATH and not QBIT_DOWNLOADS_PATH
// would translate against a different root than it reads about.
func TestQBitDownloadsPathDefaultMatchesTheAdapter(t *testing.T) {
	if defaultQBitDownloadsPath != qbit.DefaultDownloadsPath {
		t.Errorf("config default %q != qbit.DefaultDownloadsPath %q",
			defaultQBitDownloadsPath, qbit.DefaultDownloadsPath)
	}
}

func TestImportReadsEnvironment(t *testing.T) {
	t.Setenv("DOWNLOADS_PATH", "/media/storage/media/downloads")
	t.Setenv("QBIT_DOWNLOADS_PATH", "/data/torrents")
	t.Setenv("JELLYFIN_URL", "http://jellyfin:8096")
	t.Setenv("JELLYFIN_API_KEY", "abc123")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DownloadsPath != "/media/storage/media/downloads" {
		t.Errorf("DownloadsPath = %q", cfg.DownloadsPath)
	}
	if cfg.QBitDownloadsPath != "/data/torrents" {
		t.Errorf("QBitDownloadsPath = %q", cfg.QBitDownloadsPath)
	}
	if cfg.JellyfinURL != "http://jellyfin:8096" {
		t.Errorf("JellyfinURL = %q", cfg.JellyfinURL)
	}
	if !cfg.JellyfinConfigured() {
		t.Error("JellyfinConfigured() = false with a URL and a key")
	}
}
