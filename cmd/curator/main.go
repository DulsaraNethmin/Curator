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
	"github.com/DulsaraNethmin/curator/internal/qbit"
	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/tmdb"
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

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
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
	var matcher api.Matcher
	if cfg.TMDBAPIKey == "" {
		log.Warn("TMDB_API_KEY is unset: the library will scan but nothing will be matched")
	} else {
		matcher = tmdb.New(cfg.TMDBAPIKey, nil)
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
		WithDownloads(downloads)
	apiSrv.Register(mux)
	apiSrv.RegisterSearch(mux)
	apiSrv.RegisterDownloads(mux)

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
