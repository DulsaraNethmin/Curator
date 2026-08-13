// Command curator serves the movie library API.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/DulsaraNethmin/curator/internal/api"
	"github.com/DulsaraNethmin/curator/internal/config"
	"github.com/DulsaraNethmin/curator/internal/download"
	"github.com/DulsaraNethmin/curator/internal/importer"
	"github.com/DulsaraNethmin/curator/internal/indexer"
	"github.com/DulsaraNethmin/curator/internal/jellyfin"
	"github.com/DulsaraNethmin/curator/internal/library"
	"github.com/DulsaraNethmin/curator/internal/logs"
	"github.com/DulsaraNethmin/curator/internal/qbit"
	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/tmdb"
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
	cfg, err := config.Load()
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
	logBuffer := logs.NewBuffer(cfg.LogBufferLines, cfg.TMDBAPIKey, cfg.QBitPass, cfg.JellyfinAPIKey)
	log := slog.New(logBuffer.Handler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel})))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

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

	// A nil torrent client is a supported state, exactly like a nil Matcher: with
	// no credentials the library still scans and search still works, and only
	// dispatch reports itself unconfigured. Declared as the interface so the nil
	// stays a nil interface rather than one holding a nil pointer.
	var torrents download.TorrentClient
	if cfg.DownloadsConfigured() {
		torrents = qbit.New(cfg.QBitURL, cfg.QBitUser, cfg.QBitPass, indexerHTTP)
	} else {
		log.Warn("QBIT_USER is unset: search works but nothing can be downloaded")
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
	imports := importer.New(db, cfg.LibraryMovies, importer.Paths{
		Curator: cfg.DownloadsPath,
		QBit:    cfg.QBitDownloadsPath,
	}, refresher, log)

	downloads := download.NewService(torrents, db, aggregator, cfg.QBitCategory, log).
		WithImporter(imports)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	apiSrv := api.New(db, api.ScannerFunc(library.Scan), matcher, cfg.LibraryMovies, log).
		WithSearch(aggregator).
		WithDownloads(downloads).
		WithSettings(settingsView(cfg, matcher, torrents, indexerHTTP)).
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
func settingsView(cfg *config.Config, matcher api.Matcher, torrents download.TorrentClient, httpClient *http.Client) api.Settings {
	integrations := []api.Integration{{
		Name:       "tmdb",
		Env:        "TMDB_API_KEY",
		Configured: cfg.TMDBAPIKey != "",
		Detail:     detailIf(cfg.TMDBAPIKey == "", "the library scans but nothing is matched"),
	}, {
		Name:       "qbittorrent",
		Env:        "QBIT_USER",
		Configured: cfg.DownloadsConfigured(),
		Detail:     detailIf(!cfg.DownloadsConfigured(), "downloads are disabled: set QBIT_USER and QBIT_PASS"),
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
	if cfg.MinterURL != "" {
		integrations[2].Probe = func(ctx context.Context) error {
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
