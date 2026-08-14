// Command curator serves the movie library API.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/DulsaraNethmin/curator/internal/api"
	"github.com/DulsaraNethmin/curator/internal/config"
	"github.com/DulsaraNethmin/curator/internal/download"
	"github.com/DulsaraNethmin/curator/internal/engine"
	"github.com/DulsaraNethmin/curator/internal/importer"
	"github.com/DulsaraNethmin/curator/internal/indexer"
	"github.com/DulsaraNethmin/curator/internal/jellyfin"
	"github.com/DulsaraNethmin/curator/internal/library"
	"github.com/DulsaraNethmin/curator/internal/logs"
	"github.com/DulsaraNethmin/curator/internal/qbit"
	"github.com/DulsaraNethmin/curator/internal/secret"
	"github.com/DulsaraNethmin/curator/internal/settings"
	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/tmdb"
	"github.com/DulsaraNethmin/curator/internal/vpn"
	"github.com/DulsaraNethmin/curator/internal/web"
)

// shutdownTimeout bounds the wait for in-flight requests. A scan holds the
// request open while it walks the disk and calls TMDB, so this is not instant —
// but it is bounded, because an abrupt exit mid-write is how SQLite files get
// corrupted.
const shutdownTimeout = 15 * time.Second

// indexerHTTPTimeout bounds a single YTS or TPB request. Both are plain JSON APIs
// answering in well under a second, so this is a ceiling for a hung connection,
// not a budget. The whole-search deadline is SEARCH_TIMEOUT and is enforced by the
// aggregator; this only stops one socket holding a goroutine open past it.
const indexerHTTPTimeout = 15 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("curator exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	// Start-up happens in two steps since phase 7, and the order is the whole
	// of it. The settings that say where the database is have to be read before
	// the database; everything else is read out of it, decrypted, and resolved
	// against the environment — which wins (docs/decisions.md D28).
	boot, err := config.Bootstrap()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Opened before there is a logger, which is legal only because store.Open
	// returns its errors and logs nothing. Check that is still true before
	// moving anything above this line.
	db, err := store.Open(ctx, boot.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	resolution, unreadable, err := resolveSettings(ctx, db, boot)
	if err != nil {
		return err
	}
	cfg, err := config.Load(resolution.Values)
	if err != nil {
		return err
	}

	// Every record still goes to stderr exactly as before; the buffer keeps a
	// copy for GET /api/logs and the Logs screen, so nobody has to SSH into the
	// Pi to see what curator is doing.
	//
	// The secrets are handed over so they can be scrubbed on the way in. That is
	// the second line of defence, not the first — internal/tmdb already strips
	// the API key out of transport errors at the source — but a log line is now
	// a log line on the network, and the guarantee has to hold for log calls
	// nobody has written yet (docs/decisions.md D18).
	//
	// The list comes off the resolution rather than being spelled out here,
	// because a hand-written list gains a member and not a line: T37 said to
	// hand over the tunnel's keys and nothing did, so for two commits the one
	// secret this phase exists to protect was the one that was not scrubbed.
	logBuffer := logs.NewBuffer(boot.LogBufferLines, resolution.Secrets()...)
	log := slog.New(logBuffer.Handler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: boot.LogLevel})))
	slog.SetDefault(log)

	for _, key := range unreadable {
		// Not fatal, and never silently treated as unset: a database restored
		// without its key file is recoverable by typing the value again, and
		// the only thing that makes it unrecoverable is not being told.
		log.Error("a stored setting cannot be decrypted and is being ignored: enter it again. "+
			"The secret key is missing, or is not the one this value was written with",
			"key", key)
	}

	// A nil Matcher is a supported state: with no key the library still scans and
	// everything is reported unmatched. Declared as the interface so the nil stays
	// a nil interface rather than a non-nil interface holding a nil pointer.
	// One client behind two interfaces: Matcher is phase 1's scan-time lookup and
	// Browser is the catalogue the browsing screens read. Both stay nil
	// interfaces when there is no key — not interfaces holding a nil pointer.
	var (
		matcher api.Matcher
		browser api.Browser
	)
	if cfg.TMDBAPIKey == "" {
		log.Warn("TMDB_API_KEY is unset: the library will scan but nothing will be matched, " +
			"Discover is unavailable, and Search falls back to release names")
	} else {
		client := tmdb.New(cfg.TMDBAPIKey, nil)
		matcher, browser = client, client
	}

	// One HTTP client, shared by the indexers that speak plain JSON. minter gets
	// its own inside NewMinter, because it has to outlast a real browser clearing
	// a Cloudflare challenge and cannot share a ten-second timeout.
	indexerHTTP := &http.Client{Timeout: indexerHTTPTimeout}

	// 1337x is the only indexer wrapped in the cache: its pages cost ~9 s and a
	// browser each, while YTS and TPB answer in under a second and would only be
	// made stale by caching.
	x1337 := indexer.NewCache(indexer.NewX1337(indexer.NewMinter(cfg.MinterURL)), cfg.SearchCacheTTL)
	aggregator := indexer.NewAggregator(
		[]indexer.Indexer{indexer.NewYTS(indexerHTTP), indexer.NewTPB(indexerHTTP), x1337},
		cfg.SearchTimeout,
		cfg.SearchCacheTTL,
	)

	// The download backend, and the tunnel under it if there is one.
	//
	// A nil torrent client is a supported state, exactly like a nil Matcher: the
	// library still scans and search still works, and only dispatch reports
	// itself unconfigured. Declared as the interface so the nil stays a nil
	// interface rather than one holding a nil pointer.
	var (
		torrents download.TorrentClient
		guard    download.Guard
		tunnel   *vpn.Tunnel
		engine   *engine.Engine
	)
	if cfg.VPNConfigured() {
		tunnel, err = startTunnel(cfg, log)
		if err != nil {
			// Fatal, and deliberately so: a tunnel that was configured and did
			// not come up must not silently become no tunnel at all.
			return err
		}
		defer tunnel.Close()
	}

	switch {
	case cfg.Embedded():
		if engine, guard, err = startEngine(cfg, tunnel, indexerHTTP, log); err != nil {
			return err
		}
		defer engine.Close()
		torrents = engine

	case cfg.DownloadsConfigured():
		client := qbit.New(cfg.QBitURL, cfg.QBitUser, cfg.QBitPass, indexerHTTP).
			WithPaths(qbit.Paths{Curator: cfg.DownloadsPath, QBit: cfg.QBitDownloadsPath})
		torrents = client
		// curator does not route this client's traffic, so the guarantee
		// becomes a check: refuse when it leaves by the same route curator does
		// (docs/decisions.md D27). VPN_REQUIRED=false means the same thing here
		// as it does for the embedded engine — proceed, and keep saying what
		// could not be verified.
		check := vpn.External(client.ExternalAddress, cfg.VPNIPCheckURL, indexerHTTP, log)
		if !cfg.VPNRequired {
			check = vpn.Advisory(check, log)
		}
		guard = download.Guard(check)
		log.Info("using an external qBittorrent; curator cannot route its traffic, only check it",
			"url", cfg.QBitURL)

	default:
		log.Warn("TORRENT_BACKEND=qbittorrent and QBIT_USER is unset: search works but nothing can be downloaded")
	}

	// A nil refresher is a supported state, the third of the same shape: with no
	// key the import still happens and only the "tell Jellyfin" step is skipped
	// (decisions.md D15). Declared as the interface so the nil stays a nil
	// interface rather than one holding a nil pointer.
	var refresher importer.LibraryRefresher
	if cfg.JellyfinConfigured() {
		refresher = jellyfin.New(cfg.JellyfinURL, cfg.JellyfinAPIKey, indexerHTTP)
	} else {
		log.Warn("JELLYFIN_API_KEY is unset: imports will happen, but Jellyfin will not be told to refresh")
	}

	// The library root is the import destination as well as the scan source. An
	// importer writing anywhere else would produce movies the scanner never sees.
	imports := importer.New(db, cfg.LibraryMovies, refresher, log)

	downloads := download.NewService(torrents, db, aggregator, cfg.QBitCategory, log).
		WithImporter(imports).
		WithGuard(guard)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	apiSrv := api.New(db, api.ScannerFunc(library.Scan), matcher, cfg.LibraryMovies, log).
		WithSearch(aggregator).
		WithDownloads(downloads).
		WithSettings(settingsView(cfg, matcher, torrents, tunnel, indexerHTTP)).
		WithLogs(logBuffer).
		WithBrowser(browser)
	apiSrv.Register(mux)
	apiSrv.RegisterSearch(mux)
	apiSrv.RegisterDownloads(mux)
	apiSrv.RegisterSettings(mux)
	apiSrv.RegisterLogs(mux)
	apiSrv.RegisterMovieDelete(mux)
	apiSrv.RegisterBrowse(mux)

	// The UI is mounted last and at "/", so it can never shadow an API pattern.
	// Go 1.22 routing prefers the more specific pattern anyway, but the order is
	// worth being deliberate about rather than lucky.
	mux.Handle("/", web.Handler())
	if web.IsPlaceholder() {
		// Said once, loudly. Silence here is how somebody ships a binary that
		// serves a "run npm" page to the household (docs/decisions.md D16).
		log.Warn("the UI has not been built into this binary: run `npm --prefix web run build` " +
			"before `go build ./...`; the API is complete and working")
	}

	// Resume before the poller starts, so the first tick already sees whatever
	// was in flight when curator last stopped. It is not fatal: a torrent client
	// that is down at boot means downloads resume when it comes back, and the
	// library, search and the whole UI have nothing to do with it.
	if torrents != nil {
		if err := downloads.Resume(ctx); err != nil {
			log.Warn("could not resume downloads; the poller will still reconcile what the client has", "err", err)
		}
	}

	// The poller takes the same context that shuts the server down, so it stops on
	// SIGTERM with everything else and nothing outlives main. It is not started at
	// all when downloads are unconfigured: a loop logging an authentication
	// failure every ten seconds for ever is worse than no loop.
	if torrents != nil {
		poller := download.NewPoller(torrents, db, cfg.QBitCategory, cfg.DownloadPollInterval, log).
			WithImporter(imports)
		go poller.Run(ctx)
	}

	srv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", srv.Addr, "version", Version, "library", cfg.LibraryMovies)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
			return
		}
		errc <- nil
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	return <-errc
}

// resolveSettings reads the settings table, decrypts what is encrypted, and
// resolves the lot against the environment.
//
// It returns the keys it could not decrypt rather than failing on them. A
// missing key file is a state to report and carry on from — the library still
// scans, search still works, and the settings screen can be used to type the
// values in again — while a malformed SECRET_KEY is a typo in something
// somebody set deliberately, and starting with it ignored would encrypt the
// next write under a key nobody meant.
func resolveSettings(ctx context.Context, db *store.Store, boot config.Boot) (settings.Resolution, []string, error) {
	raw, err := db.Settings(ctx)
	if err != nil {
		return settings.Resolution{}, nil, err
	}

	// A key is generated only when there is nothing it could orphan. With
	// ciphertext already in the table and no key to read it, inventing one
	// would turn "I restored the database without its key" from recoverable
	// into invisible (docs/decisions.md D28).
	codec, err := secret.Open(boot.SecretKey, boot.SecretKeyFile, !secret.AnyEncrypted(raw))
	if err != nil && !errors.Is(err, secret.ErrNoKey) {
		return settings.Resolution{}, nil, err
	}

	values, unreadable := secret.Reveal(codec, raw)
	resolution, err := settings.Resolve(values)
	return resolution, unreadable, err
}

// healthz is deliberately cheap: no database, no disk. It answers "is the process
// up", which is the only question a container healthcheck should ask.
func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "version": Version})
}

// settingsView describes the integrations to the settings screen.
//
// No credential is passed in, only whether one is present: there is no
// authentication in front of GET /api/settings and there is not going to be, so
// the only safe design is one where the secret is never in the payload to leak
// (docs/decisions.md D17).
//
// Every probe here is READ-ONLY. That is the constraint that decides which of
// them exist at all.
func settingsView(cfg *config.Config, matcher api.Matcher, torrents download.TorrentClient, tunnel *vpn.Tunnel, httpClient *http.Client) api.Settings {
	integrations := []api.Integration{{
		Name:       "tmdb",
		Env:        "TMDB_API_KEY",
		Configured: cfg.TMDBAPIKey != "",
		Detail:     detailIf(cfg.TMDBAPIKey == "", "the library scans but nothing is matched"),
	}, {
		// The backend, whichever it is. `embedded` needs no credentials — it is
		// this binary — so what "configured" reports is whether anything can
		// download at all.
		Name:       "torrents",
		Env:        "TORRENT_BACKEND",
		Configured: cfg.DownloadsConfigured(),
		Detail:     backendDetail(cfg),
	}, {
		// The tunnel. No address is ever reported, only whether it is up: this
		// endpoint has no authentication in front of it (docs/decisions.md D17
		// and D18), and where traffic leaves from is the one fact the tunnel
		// exists to keep.
		Name:       "vpn",
		Env:        "VPN_CONFIG",
		Configured: cfg.VPNConfigured(),
		Detail:     vpnDetail(cfg, tunnel),
	}, {
		Name:       "minter",
		Env:        "MINTER_URL",
		Configured: cfg.MinterURL != "",
		Detail:     "1337x only; YTS and TPB need no browser",
	}, {
		Name:       "jellyfin",
		Env:        "JELLYFIN_API_KEY",
		Configured: cfg.JellyfinConfigured(),
		// Deliberately no Probe. The only call internal/jellyfin makes is
		// POST /Library/Refresh, which MUTATES — rendering a settings page must
		// not queue a scan of every library on a media server the household is
		// watching. A read-only probe needs an endpoint that package does not
		// have, and adding one to satisfy a status page is the wrong trade.
		Detail: jellyfinDetail(cfg.JellyfinConfigured()),
	}}

	if matcher != nil {
		// A real search, which is the only read-only call the TMDB client has.
		// It costs one request against a free quota, and only when asked for.
		integrations[0].Probe = func(ctx context.Context) error {
			_, err := matcher.SearchMovie(ctx, "Interstellar", 2014)
			return err
		}
	}
	if torrents != nil {
		// Lists our own category and nothing else — the same call the poller
		// makes every tick, and it cannot disturb the *arr stack's torrents.
		integrations[1].Probe = func(ctx context.Context) error {
			_, err := torrents.Torrents(ctx, cfg.QBitCategory)
			return err
		}
	}
	if tunnel != nil {
		// Read-only, and local: the device's own counters, not a round trip.
		integrations[2].Probe = func(context.Context) error {
			status, err := tunnel.Status()
			if err != nil {
				return err
			}
			if !status.Handshaken() {
				return fmt.Errorf("no handshake with %s yet", status.Endpoint)
			}
			return nil
		}
	}
	if cfg.MinterURL != "" {
		integrations[3].Probe = func(ctx context.Context) error {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.MinterURL, nil)
			if err != nil {
				return err
			}
			resp, err := httpClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			// Any answer proves the process is listening. minter's root is not a
			// documented health endpoint, so its status is not worth asserting.
			return nil
		}
	}

	return api.Settings{
		Version:      Version,
		Integrations: integrations,
		Paths: map[string]string{
			"library_movies":      cfg.LibraryMovies,
			"downloads_dir":       cfg.DownloadsDir,
			"downloads_path":      cfg.DownloadsPath,
			"qbit_downloads_path": cfg.QBitDownloadsPath,
			"database":            cfg.DBPath,
		},
		Intervals: map[string]string{
			"download_poll":    cfg.DownloadPollInterval.String(),
			"search_timeout":   cfg.SearchTimeout.String(),
			"search_cache_ttl": cfg.SearchCacheTTL.String(),
		},
	}
}

// startTunnel brings up the WireGuard device the engine will be bound to.
func startTunnel(cfg *config.Config, log *slog.Logger) (*vpn.Tunnel, error) {
	parsed, err := vpn.ParseConfig(cfg.VPNConfig)
	if err != nil {
		return nil, err
	}
	return vpn.New(parsed, log)
}

// startEngine builds curator's own torrent client, and the check that runs
// before anything is dispatched through it.
//
// With a tunnel, the engine is built so that it cannot open a socket of its
// own; without one it is refused outright unless VPN_REQUIRED=false has been
// typed. That is the whole of "mandatory": there is no configuration in which
// the embedded engine quietly downloads unprotected (docs/decisions.md D27).
func startEngine(cfg *config.Config, tunnel *vpn.Tunnel, httpClient *http.Client, log *slog.Logger) (*engine.Engine, download.Guard, error) {
	var network engine.Network
	var guard download.Guard

	switch {
	case tunnel != nil:
		network = tunnel
		guard = download.Guard(vpn.Tunnelled(tunnel, cfg.VPNIPCheckURL, httpClient, 0, log))
	case cfg.VPNRequired:
		// Not a startup failure: the library, search and the whole UI have
		// nothing to do with the tunnel, and the screen that will configure one
		// in phase 7 has to be reachable. Only dispatch is refused, and it says
		// which variable would fix it.
		guard = download.Guard(vpn.Required())
		log.Warn("no VPN is configured and VPN_REQUIRED is true: downloads are refused until VPN_CONFIG is set, " +
			"or VPN_REQUIRED=false")
	default:
		log.Warn("VPN_REQUIRED=false: the embedded engine will download over this machine's own connection")
	}

	built, err := engine.New(engine.Config{
		DataDir:    cfg.DownloadsDir,
		Category:   cfg.QBitCategory,
		MaxConns:   cfg.TorrentMaxConns,
		ListenPort: cfg.TorrentPort,
		StallAfter: cfg.TorrentStallAfter,
		Network:    network,
		Log:        log,
	})
	if err != nil {
		return nil, nil, err
	}
	return built, guard, nil
}

func backendDetail(cfg *config.Config) string {
	switch {
	case cfg.Embedded():
		return "curator downloads it itself, in this process"
	case !cfg.DownloadsConfigured():
		return "downloads are disabled: set QBIT_USER and QBIT_PASS"
	default:
		return "an external qBittorrent: curator can check its exit address but cannot route it"
	}
}

func vpnDetail(cfg *config.Config, tunnel *vpn.Tunnel) string {
	// The external backend is a different promise, so it gets a different
	// sentence: curator cannot route that traffic, and a tunnel configured here
	// would not carry it. What protects it is the exit-address check, and
	// saying so is the point (docs/decisions.md D27).
	if !cfg.Embedded() {
		return "not used by an external qBittorrent: curator cannot route that traffic, " +
			"and refuses to dispatch when its exit address is curator's own"
	}
	switch {
	case tunnel == nil && cfg.VPNRequired:
		return "no tunnel: downloads are refused until VPN_CONFIG is set, or VPN_REQUIRED=false"
	case tunnel == nil:
		return "no tunnel, and VPN_REQUIRED=false: downloads leave from this machine's own address"
	default:
		return "every peer byte goes through the tunnel; a dead tunnel is a failed dial, not a leak"
	}
}

func detailIf(cond bool, detail string) string {
	if cond {
		return detail
	}
	return ""
}

func jellyfinDetail(configured bool) string {
	if !configured {
		return "the library refresh is disabled: set JELLYFIN_API_KEY"
	}
	return "not probed: the only call available is a refresh, which would queue a library scan"
}
