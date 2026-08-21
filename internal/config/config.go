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

	// LibraryTV is where shows live, and **empty means television is off** — no
	// Shows tab, no TV rails, and the TV routes answering 503 naming this
	// variable. It has no default, unlike LibraryMovies, and that asymmetry is
	// the whole opt-in: a default would point curator at a directory nobody asked
	// it to write to, and phase 11 is additive to a product whose README says
	// "movies only" rather than a replacement for it.
	//
	// It is the same posture as QBIT_USER and JELLYFIN_URL — unconfigured is a
	// supported state, not a broken one.
	LibraryTV string

	TMDBAPIKey string
	LogLevel   slog.Level

	// LogBufferLines is how much of the process log is kept in memory for
	// GET /api/logs and the Logs screen. It is a ring: the oldest line is
	// dropped, and the API reports how many were dropped rather than serving a
	// log with a silent gap in it.
	LogBufferLines int

	// Phase 2: indexers.
	MinterURL      string
	SearchTimeout  time.Duration
	SearchCacheTTL time.Duration

	// Phase 7: which of them are asked at all. All of them are on unless
	// somebody turns one off — except 1337x, see the defaults — so an unset
	// value keeps exactly the behaviour that shipped, and turning 1337x off is
	// also what stops minter being probed for a service nobody asked for
	// (docs/tasks/T40-settings-api.md).
	IndexerYTS   bool
	IndexerTPB   bool
	IndexerX1337 bool
	IndexerEZTV  bool

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

	// JellyfinPublicURL is what a BROWSER can reach, and it is not redundant
	// with JellyfinURL: that one is what *curator* uses, which inside Docker is
	// http://jellyfin:8096 — a name that resolves on the container network and
	// nowhere a browser can follow. Empty falls back to JellyfinURL, which is
	// right on a laptop and wrong in the image; that gap is the whole reason
	// the setting exists (docs/phase-8.md). Use JellyfinLink, never this field.
	JellyfinPublicURL string

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

	VPNConfig   string
	VPNRequired bool

	// Updating. UpdateCheck is whether curator may ask the release feed at all:
	// it is one unauthenticated GET to a public endpoint, sending nothing about
	// this install, but it is still a request somebody may not want their media
	// server making and it is therefore a switch rather than an assumption.
	//
	// UpdaterURL and UpdaterToken point at something that holds the Docker
	// socket. curator never holds it (docs/decisions.md D44), so an empty
	// UpdaterURL is the normal state and means the screen offers a command to
	// paste instead of a button.
	UpdateCheck    bool
	UpdateCheckURL string
	UpdaterURL     string
	UpdaterToken   string
	VPNIPCheckURL  string

	// Phase 8: playback.
	//
	// PlaybackTarget records which answer was given to "how do you want to
	// watch" — `browser`, `jellyfin`, or empty for "nobody has been asked yet".
	//
	// It is a record of an answer rather than a switch that changes behaviour,
	// and that distinction is load-bearing: nothing reads it except the screen
	// that asks the question, so that it cannot become the "prefer direct play"
	// toggle phase 8 refused to build under a new name
	// (docs/tasks/T65-playback-screen.md). Empty is what makes the Playback
	// screen a first-run destination instead of a nag.
	PlaybackTarget string

	// FFmpegPath is where the remux's ffmpeg is. Empty means look on PATH, and
	// not finding one is not a startup error — it means direct play only, the
	// same posture an unset JELLYFIN_API_KEY already has (docs/decisions.md D24,
	// D15). What this holds is what was CONFIGURED; the binary that was actually
	// resolved is internal/remux's answer and is logged once at start-up.
	FFmpegPath string
}

// The two torrent backends. An unknown value is a startup error naming both,
// not a silent fallback to either.
const (
	BackendEmbedded    = "embedded"
	BackendQBittorrent = "qbittorrent"
)

// The two answers to "how do you want to watch", and the third state that is
// not a value at all.
//
// PlaybackUnasked is "" rather than a word because it has to be what an unset
// variable and an absent row both already are — a phase-9 install upgrading
// from phase 8 has never been asked, and inventing a spelling for that would
// mean writing a migration to apply it.
const (
	PlaybackUnasked  = ""
	PlaybackBrowser  = "browser"
	PlaybackJellyfin = "jellyfin"
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

// JellyfinLink is the base a BROWSER should use to reach Jellyfin.
//
// One function rather than the fallback written at each use, because there will
// be more than one use and two answers to "which URL does the link get" is
// exactly the bug the setting was added to prevent: inside Docker curator talks
// to http://jellyfin:8096 and a browser cannot, so a link built from the wrong
// one is the kind of failure that gets reported as "Jellyfin is down".
func (c *Config) JellyfinLink() string {
	if public := strings.TrimSpace(c.JellyfinPublicURL); public != "" {
		return public
	}
	return c.JellyfinURL
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

// TVConfigured reports whether curator has anywhere to put a television show.
//
// One place to ask, rather than `cfg.LibraryTV != ""` scattered through the
// handlers and the UI's feature flags — the same shape as DownloadsConfigured
// and JellyfinConfigured above. Every TV affordance hangs off this, so
// television is genuinely absent when it is unset rather than present and
// broken.
func (c *Config) TVConfigured() bool { return c.LibraryTV != "" }

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

	// DefaultJellyfinURL is a laptop default, like defaultQBitURL. On the Pi
	// Jellyfin is 10.10.7 at 192.168.1.26:8096, and http://jellyfin:8096 inside
	// Docker.
	//
	// Exported, unlike its neighbours, because phase 9's setup flow has to be
	// able to tell "somebody configured this" from "nobody has, and this is the
	// value a fresh clone gets". Inside the bundle those need different
	// answers — compose deliberately sets no JELLYFIN_URL, so that what T65
	// writes is not shadowed by an environment that always wins
	// (docs/decisions.md D28) — and the difference cannot be recovered from the
	// string alone (docs/tasks/T65-playback-screen.md).
	DefaultJellyfinURL = "http://127.0.0.1:8096"

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
		DBPath:        env("DB_PATH", defaultDBPath),
		LibraryMovies: r.get("LIBRARY_MOVIES", defaultLibraryMovies),
		// No default: empty means television is off. See Config.LibraryTV.
		LibraryTV:    r.get("LIBRARY_TV", ""),
		TMDBAPIKey:   r.get("TMDB_API_KEY", ""),
		MinterURL:    r.get("MINTER_URL", defaultMinterURL),
		QBitURL:      r.get("QBIT_URL", defaultQBitURL),
		QBitUser:     r.get("QBIT_USER", ""),
		QBitPass:     r.get("QBIT_PASS", ""),
		QBitCategory: r.get("QBIT_CATEGORY", defaultQBitCategory),

		LogBufferLines: defaultLogBufferLines,

		// DOWNLOADS_PATH has no default, and empty is a meaningful value — "the
		// path qBittorrent reports is already the path curator sees".
		DownloadsPath:     r.get("DOWNLOADS_PATH", ""),
		QBitDownloadsPath: r.get("QBIT_DOWNLOADS_PATH", defaultQBitDownloadsPath),
		JellyfinURL:       r.get("JELLYFIN_URL", DefaultJellyfinURL),
		JellyfinAPIKey:    r.get("JELLYFIN_API_KEY", ""),

		// No default, and empty is meaningful: "the browser can reach Jellyfin
		// at the same URL curator does". A default of DefaultJellyfinURL here
		// would hard-code 127.0.0.1 into a link somebody clicks from a phone.
		JellyfinPublicURL: r.get("JELLYFIN_PUBLIC_URL", ""),

		DownloadsDir:  r.get("DOWNLOADS_DIR", defaultDownloadsDir),
		VPNIPCheckURL: r.get("VPN_IP_CHECK_URL", ""),

		// Both empty by default. The check URL falls back to the release feed
		// inside internal/update rather than being spelled here, so there is
		// one place that knows where releases live.
		UpdateCheckURL: r.get("UPDATE_CHECK_URL", ""),
		UpdaterURL:     r.get("UPDATER_URL", ""),
		UpdaterToken:   r.get("UPDATER_TOKEN", ""),

		// No default, and empty is meaningful: "look on PATH". A default of
		// "ffmpeg" here would be the same behaviour spelled twice, and it would
		// make the settings screen show a value nobody typed.
		FFmpegPath: r.get("FFMPEG_PATH", ""),

		// On by default, and the opposite of the VPN switch below: knowing a
		// security fix exists is worth more than the one request it costs, and
		// an update nobody hears about is the failure this defaults against.
		UpdateCheck: true,

		// Mandatory by default, and it has to be typed to turn off. A VPN that
		// defaults to optional is a slogan (docs/decisions.md D27).
		VPNRequired: true,

		// The two that need nothing are on by default; the one that needs a
		// second container is not.
		//
		// All three were on in phase 7, so that an unset value meant what
		// curator did before these variables existed. Phase 9 changes what the
		// default has to be true of: the product is now a bundle a stranger
		// installs with one command, and 1337x cannot work in that install
		// until minter has been started by a second one
		// (docs/tasks/T49-minter-on-demand.md, which forbids this being on by
		// default in as many words). Leaving it on means every fresh install
		// reports an indexer it cannot use on every search, forever, for a
		// feature nobody asked for.
		//
		// YTS and TPB are plain JSON over the tunnel and need nothing, so they
		// keep the default they had. Nobody who explicitly set INDEXER_1337X —
		// in the environment or in the store — is affected either way.
		//
		// EZTV is on for the same reason those two are and not for 1337x's: it
		// is plain JSON with no browser, no companion container and no
		// credential, so a fresh install can use it on the first search. It
		// also cannot be reported as broken on a search it was not built for —
		// it declines a query with no IMDb id rather than failing one
		// (internal/indexer, QueryCapable), which is what makes "on by
		// default" safe for a source that only has television.
		IndexerYTS:   true,
		IndexerTPB:   true,
		IndexerX1337: false,
		IndexerEZTV:  true,
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

	// Same shape, and refused at start-up for the same reason: a value the
	// settings screen would reject must not be one the environment can smuggle
	// past it, or the two sources disagree about what is legal.
	cfg.PlaybackTarget, err = ParsePlaybackTarget(r.get("PLAYBACK_TARGET", PlaybackUnasked))
	if err != nil {
		return nil, err
	}

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
		{"UPDATE_CHECK", &cfg.UpdateCheck},
		{"INDEXER_YTS", &cfg.IndexerYTS},
		{"INDEXER_TPB", &cfg.IndexerTPB},
		{"INDEXER_1337X", &cfg.IndexerX1337},
		{"INDEXER_EZTV", &cfg.IndexerEZTV},
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
	cfg.Port, err = Port()
	if err != nil {
		return nil, err
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

// Port is the port the server listens on, read from the environment alone and
// never from the settings store: anything needed in order to reach the settings
// screen is not settable from it (docs/decisions.md D28).
//
// Exported, and called rather than repeated, because the image's HEALTHCHECK
// runs `curator -healthcheck` in the same binary — a `FROM scratch` image has no
// shell and no curl to run one with (docs/tasks/T47-image.md). A health check
// that guessed 8090 while the server read PORT would report a working curator as
// unhealthy, and compose would refuse to start anything waiting on it.
func Port() (int, error) {
	raw := os.Getenv("PORT")
	if raw == "" {
		return defaultPort, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("PORT %q: not a number", raw)
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("PORT %d: out of range 1-65535", port)
	}
	return port, nil
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

// ParsePlaybackTarget checks a PLAYBACK_TARGET value.
//
// Empty is accepted and means unasked, which is the one thing that separates
// this from ParseBackend: a backend must be one of two things because something
// has to download, and this may legitimately be nothing at all because the
// question may not have been put yet.
func ParsePlaybackTarget(raw string) (string, error) {
	target := strings.ToLower(strings.TrimSpace(raw))
	switch target {
	case PlaybackUnasked, PlaybackBrowser, PlaybackJellyfin:
		return target, nil
	default:
		return "", fmt.Errorf("PLAYBACK_TARGET %q: want %q or %q, or empty for not yet chosen",
			raw, PlaybackBrowser, PlaybackJellyfin)
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
