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
	"strconv"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

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
	"github.com/DulsaraNethmin/curator/internal/remux"
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
	// The image's HEALTHCHECK, and the one argument curator takes. A `FROM
	// scratch` image has no shell and no curl, so the only thing that can make an
	// HTTP request inside it is curator itself (docs/tasks/T47-image.md). Handled
	// before run() because it must open no database, read no settings and write
	// no log — it runs every thirty seconds beside a server holding both.
	if len(os.Args) > 1 && (os.Args[1] == "-healthcheck" || os.Args[1] == "--healthcheck") {
		if err := healthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("curator exited", "err", err)
		os.Exit(1)
	}
}

// healthcheckTimeout bounds the probe. It is well under the HEALTHCHECK's own
// --timeout so that a slow answer is reported as a failed check by curator
// rather than as a killed process by Docker, which says nothing in the log.
const healthcheckTimeout = 2 * time.Second

// healthcheck asks the running curator whether it is up, and is what
// `curator -healthcheck` does.
//
// 127.0.0.1 rather than the address the server bound: this only ever runs inside
// the same container, and a health check that reached across a network would be
// testing the network. The port comes from config.Port so that it is read out of
// the same variable the server read it from, rather than guessed.
func healthcheck() error {
	port, err := config.Port()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), healthcheckTimeout)
	defer cancel()

	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer resp.Body.Close()
	// /healthz is outside the authentication middleware on purpose (D25, T41), so
	// a 401 here would be a regression in the middleware rather than a missing
	// credential — and either way it is not healthy.
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: %s answered %s", url, resp.Status)
	}
	return nil
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

	resolution, codec, unreadable, err := resolveSettings(ctx, db, boot)
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

	// This is the line that honours the three indexer toggles, and until phase 7
	// nothing did: they were stored settings with no reader, which is a switch
	// that looks like it works. An indexer that is off is never constructed, so
	// turning 1337x off is also what stops minter being launched for a service
	// nobody asked for (docs/tasks/T40-settings-api.md step 9).
	//
	// 1337x is the only indexer wrapped in the cache: its pages cost ~9 s and a
	// browser each, while YTS and TPB answer in under a second and would only be
	// made stale by caching.
	var indexers []indexer.Indexer
	if cfg.IndexerYTS {
		indexers = append(indexers, indexer.NewYTS(indexerHTTP))
	}
	if cfg.IndexerTPB {
		indexers = append(indexers, indexer.NewTPB(indexerHTTP))
	}
	if cfg.IndexerX1337 {
		indexers = append(indexers, indexer.NewCache(indexer.NewX1337(indexer.NewMinter(cfg.MinterURL)), cfg.SearchCacheTTL))
	}
	if len(indexers) == 0 {
		// Not a startup failure, the same posture as every other unconfigured
		// integration: the library still scans and the screen that turns one
		// back on is still reachable.
		log.Warn("every indexer is disabled: search will find nothing until one is enabled in Settings")
	}
	aggregator := indexer.NewAggregator(indexers, cfg.SearchTimeout, cfg.SearchCacheTTL)

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
	// mediaServer is the same client the refresher is, kept so that phase 8's
	// Open in Jellyfin link can use the one connection pool rather than opening
	// a second. It is declared as the api interface for the same reason
	// refresher is declared as the importer one: a nil must stay a nil
	// interface and not one holding a nil pointer.
	var (
		refresher   importer.LibraryRefresher
		mediaServer api.MediaServer
	)
	if cfg.JellyfinConfigured() {
		client := jellyfin.New(cfg.JellyfinURL, cfg.JellyfinAPIKey, indexerHTTP)
		refresher, mediaServer = client, client
	} else {
		log.Warn("JELLYFIN_API_KEY is unset: imports will happen, but Jellyfin will not be told to " +
			"refresh, and a film's page will show no Open in Jellyfin link")
	}

	// The library root is the import destination as well as the scan source. An
	// importer writing anywhere else would produce movies the scanner never sees.
	imports := importer.New(db, cfg.LibraryMovies, refresher, log)

	// ffmpeg, probed ONCE and here rather than per request. A miss is the fourth
	// optional dependency with this shape — direct play still works, and the
	// only thing lost is the fallback for a container the browser refuses
	// (docs/decisions.md D24, D15). It is logged either way, because "why is
	// there no Play fallback on this install" is otherwise a question with no
	// evidence anywhere.
	var remuxer *remux.Remuxer
	ffmpegPath, err := remux.Find(cfg.FFmpegPath)
	if err != nil {
		log.Info("no ffmpeg: playback is direct play only, and a container this browser refuses "+
			"will offer VLC instead of a remux. Set FFMPEG_PATH, or put ffmpeg on PATH", "err", err)
	} else {
		remuxer = remux.New(ffmpegPath, remux.DefaultConcurrent)
		log.Info("ffmpeg found: a container the browser refuses will be remuxed, never transcoded",
			"path", ffmpegPath, "concurrent", remux.DefaultConcurrent)
	}

	downloads := download.NewService(torrents, db, aggregator, cfg.QBitCategory, log).
		WithImporter(imports).
		WithGuard(guard)

	// The password, read per request through one atomic holder. It is the one
	// exception to "a written setting applies at the next start": a password
	// that took effect after a restart would leave you unprotected until then,
	// which inverts the point of setting one (docs/decisions.md D29). Attaching
	// it to the settings view is what makes a write apply — T40 already calls
	// Reload after a write that touched an Immediate setting.
	//
	// The session key is a subkey of the one that encrypts settings, so the
	// cookie survives a restart without a session table, and the holder cannot
	// decrypt a WireGuard config. A database restored without its key file has
	// no codec, and the honest cost is that sessions then last until the
	// process does.
	var sessionKey []byte
	if codec != nil {
		sessionKey = codec.Derive(api.SessionLabel)
	}
	auth := api.NewAuth(sessionKey, authCredential(db, codec), log)
	if err := auth.Reload(ctx); err != nil {
		return err
	}

	// The read-only view phase 5 built, plus the three parts phase 7 adds: what
	// every setting currently is, where a change goes, and the one change that
	// does not wait for a restart. All three are wiring and none is a handler,
	// which is why they are built here and passed in — internal/api still knows
	// nothing about where a setting comes from.
	view := settingsView(cfg, matcher, torrents, tunnel, indexerHTTP, ffmpegPath)
	view.States = settingsStates(cfg, db, codec, log)
	view.Writer = settingsWriter{db: db, codec: codec}
	view.Access = auth

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	apiSrv := api.New(db, api.ScannerFunc(library.Scan), matcher, cfg.LibraryMovies, log).
		WithSearch(aggregator).
		WithDownloads(downloads).
		WithSettings(view).
		WithLogs(logBuffer).
		WithBrowser(browser).
		// *Auth is the minter: a ticket is the session cookie's own machinery
		// pointed at a URL, so the thing that signs one signs the other with
		// the same key (docs/decisions.md D31). It is passed even with
		// authentication off — Ticket answers ok=false in that state, which is
		// how POST .../playback returns a plain URL and the UI keeps one code
		// path.
		WithTickets(auth).
		// Nil when there is no ffmpeg, which is not a degraded mode: the stream
		// endpoint is untouched and only the fallback is missing.
		WithRemux(remuxer).
		// The link a browser follows, which is NOT cfg.JellyfinURL whenever the
		// two are in Docker together: curator reaches Jellyfin by a container
		// name and a browser cannot resolve one (docs/phase-8.md).
		WithJellyfin(mediaServer, cfg.JellyfinLink()).
		// Phase 9's Playback screen, and the only thing in the process that can
		// construct a Provisioner. Deliberately separate from WithJellyfin
		// above: that one takes a read-only client the poller and the importer
		// also hold, and this one takes the type that can rewrite a media
		// server (docs/decisions.md D34).
		WithJellyfinSetup(api.JellyfinSetup{
			URL:         provisionURL(cfg),
			LibraryPath: cfg.LibraryMovies,
			New: func(baseURL string) api.Provisioner {
				return jellyfin.NewProvisioner(baseURL, Version, indexerHTTP, log)
			},
			// The READ-ONLY client, built fresh for the key adoption just
			// minted — not mediaServer above, which holds whatever key this
			// process started with and is nil on an install that has none. It
			// is how adoption finishes by proving the new key with the call
			// that will use it (docs/tasks/T66-adopt-jellyfin.md).
			Reader: func(baseURL, apiKey string) api.MediaServer {
				return jellyfin.New(baseURL, apiKey, indexerHTTP)
			},
		})
	apiSrv.Register(mux)
	apiSrv.RegisterSearch(mux)
	apiSrv.RegisterDownloads(mux)
	apiSrv.RegisterSettings(mux)
	apiSrv.RegisterLogs(mux)
	apiSrv.RegisterMovieDelete(mux)
	apiSrv.RegisterBrowse(mux)
	apiSrv.RegisterStream(mux)
	apiSrv.RegisterJellyfin(mux)
	auth.Register(mux)

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
		Addr: cfg.Addr(),
		// The whole mux, so the check runs BEFORE routing: a protected route
		// with no credential answers 401 whether or not it exists, and nobody
		// without the password can map the API by watching which one they get.
		// /healthz and the static UI pass straight through — the middleware
		// knows which paths it is in front of (docs/tasks/T41-auth.md).
		Handler:           auth.Middleware(mux),
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
//
// It also returns the codec, because the settings screen writes through the
// same one: a secret typed into the form is encrypted with the key this call
// found or generated, and a second Open on the write path could generate a
// second key.
func resolveSettings(ctx context.Context, db *store.Store, boot config.Boot) (settings.Resolution, *secret.Codec, []string, error) {
	raw, err := db.Settings(ctx)
	if err != nil {
		return settings.Resolution{}, nil, nil, err
	}

	// A key is generated only when there is nothing it could orphan. With
	// ciphertext already in the table and no key to read it, inventing one
	// would turn "I restored the database without its key" from recoverable
	// into invisible (docs/decisions.md D28).
	codec, err := secret.Open(boot.SecretKey, boot.SecretKeyFile, !secret.AnyEncrypted(raw))
	if err != nil && !errors.Is(err, secret.ErrNoKey) {
		return settings.Resolution{}, nil, nil, err
	}

	values, unreadable := secret.Reveal(codec, raw)
	resolution, err := settings.Resolve(values)
	return resolution, codec, unreadable, err
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
// ffmpeg is the resolved binary or "" — the answer remux.Find already gave at
// start-up, passed in rather than looked up again, so this screen cannot
// disagree with the process about whether there is a fallback.
func settingsView(cfg *config.Config, matcher api.Matcher, torrents download.TorrentClient, tunnel *vpn.Tunnel, httpClient *http.Client, ffmpeg string) api.Settings {
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
		// Only when 1337x is on. minter exists for that one indexer, so with it
		// disabled there is nothing to be configured for and nothing to probe —
		// probing anyway would launch a browser for a service nobody asked for.
		Name:       "minter",
		Env:        "MINTER_URL",
		Configured: cfg.IndexerX1337 && cfg.MinterURL != "",
		Detail:     minterDetail(cfg),
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
	}, {
		// ffmpeg. Configured means one was actually FOUND, not that a path was
		// typed — the difference is the whole of what this row is for, and it is
		// why the resolved path comes in rather than cfg.FFmpegPath.
		//
		// No Probe: the binary was run at start-up in the only sense that
		// matters, which is that it exists and is executable, and a probe that
		// spawned an ffmpeg to render a settings page would be a subprocess per
		// page load for an answer already known.
		Name:       "ffmpeg",
		Env:        "FFMPEG_PATH",
		Configured: ffmpeg != "",
		Detail:     ffmpegDetail(ffmpeg),
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
	if cfg.IndexerX1337 && cfg.MinterURL != "" {
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

// effective is what this process is actually running on, by setting key.
//
// A map literal, beside the paths and intervals ones above it, and deliberately
// boring. It cannot drift from what curator is doing because it IS what curator
// is doing — read straight off the *config.Config every other line in this file
// was built from — which is why internal/settings' registry holds no defaults
// of its own to disagree with it (docs/phase-7.md).
//
// The two Access settings are not here. They have no *config.Config field
// because nothing at start-up uses them: the password applies on the next
// request rather than at the next start (docs/decisions.md D29), so what is live
// for those two is what is stored, and settingsStates reads them from the
// resolution instead.
func effective(cfg *config.Config) map[string]string {
	return map[string]string{
		"library_movies": cfg.LibraryMovies,
		"tmdb_api_key":   cfg.TMDBAPIKey,

		"torrent_backend":        cfg.TorrentBackend,
		"downloads_dir":          cfg.DownloadsDir,
		"torrent_max_conns":      number(cfg.TorrentMaxConns),
		"torrent_port":           number(cfg.TorrentPort),
		"torrent_stall_after":    cfg.TorrentStallAfter.String(),
		"download_poll_interval": cfg.DownloadPollInterval.String(),

		"qbit_url":            cfg.QBitURL,
		"qbit_user":           cfg.QBitUser,
		"qbit_pass":           cfg.QBitPass,
		"qbit_category":       cfg.QBitCategory,
		"downloads_path":      cfg.DownloadsPath,
		"qbit_downloads_path": cfg.QBitDownloadsPath,

		"vpn_config":       cfg.VPNConfig,
		"vpn_required":     strconv.FormatBool(cfg.VPNRequired),
		"vpn_ip_check_url": cfg.VPNIPCheckURL,

		"indexer_yts":      strconv.FormatBool(cfg.IndexerYTS),
		"indexer_tpb":      strconv.FormatBool(cfg.IndexerTPB),
		"indexer_1337x":    strconv.FormatBool(cfg.IndexerX1337),
		"minter_url":       cfg.MinterURL,
		"search_timeout":   cfg.SearchTimeout.String(),
		"search_cache_ttl": cfg.SearchCacheTTL.String(),

		// What was CONFIGURED, and empty is meaningful — "look on PATH". The
		// binary that was actually resolved is the ffmpeg integration's detail,
		// beside whether one was found at all.
		"ffmpeg_path": cfg.FFmpegPath,

		// Empty is meaningful here too, and it is the state every install
		// starts in: the question has not been put yet, which is what makes the
		// Playback screen a first-run destination rather than a nag.
		"playback_target": cfg.PlaybackTarget,

		"jellyfin_url":     cfg.JellyfinURL,
		"jellyfin_api_key": cfg.JellyfinAPIKey,

		// What was CONFIGURED, so empty is meaningful here too — "the browser
		// reaches Jellyfin at jellyfin_url". Reporting cfg.JellyfinLink() would
		// show a value nobody typed and make the field look already set.
		"jellyfin_public_url": cfg.JellyfinPublicURL,
	}
}

// number renders an int setting, with zero meaning unset rather than zero.
//
// TORRENT_MAX_CONNS is the case that decides this: config leaves it at 0 when
// the variable is absent and internal/engine substitutes its own documented
// default, so reporting "0" would name a value nothing is running on. An empty
// string is the honest answer and the one the form wants — an empty box, with
// source `default` beside it.
func number(value int) string {
	if value == 0 {
		return ""
	}
	return strconv.Itoa(value)
}

// settingsStates reads what every setting currently is, per request.
//
// Per request, not once: a PUT changes the answer, and the GET a moment later
// has to report what is now saved and waiting. It reads the table, decrypts,
// resolves against the environment — which wins — and then answers the question
// D29 makes the interesting one, "what would the next start use", by calling the
// function that will actually run at the next start rather than a second one
// that agrees with it today.
//
// No secret value leaves this closure. internal/api never receives one, which
// is what makes "no secret in the response" a property of the wiring as well as
// of the handler.
func settingsStates(cfg *config.Config, db *store.Store, codec *secret.Codec, log *slog.Logger) func(context.Context) (map[string]api.SettingState, error) {
	live := effective(cfg)

	return func(ctx context.Context) (map[string]api.SettingState, error) {
		raw, err := db.Settings(ctx)
		if err != nil {
			return nil, err
		}
		values, unreadable := secret.Reveal(codec, raw)
		resolution, err := settings.Resolve(values)
		if err != nil {
			return nil, err
		}

		next := live
		if loaded, err := config.Load(resolution.Values); err == nil {
			next = effective(loaded)
		} else {
			// A stored value that will not load cannot be reported as pending,
			// but it must not take the settings screen down with it — that is
			// the screen it gets fixed from. It can only happen to a row written
			// by hand, because every write through this API is validated by the
			// parser that would reject it here.
			log.Warn("the stored settings do not load; nothing is reported as pending", "err", err)
		}

		unreadableKeys := make(map[string]bool, len(unreadable))
		for _, key := range unreadable {
			unreadableKeys[key] = true
		}

		out := make(map[string]api.SettingState, len(raw))
		for _, item := range settings.All() {
			state := api.SettingState{
				Source:     resolution.Sources[item.Key],
				Configured: resolution.Values[item.Key] != "",
				Unreadable: unreadableKeys[item.Key],
			}

			value, pending := live[item.Key], next[item.Key]
			if item.Immediate {
				// Applied on the next request, so what is stored is already
				// live and there is never anything pending about it.
				value, pending = resolution.Values[item.Key], resolution.Values[item.Key]
			}
			state.Value = value
			if value != pending {
				state.PendingChange = true
				state.Pending = pending
			}

			if item.Secret {
				// Stripped before they cross the boundary. The handler omits
				// them again; this is the half that means a handler bug has
				// nothing to leak (docs/decisions.md D17, D28).
				state.Value, state.Pending = "", ""
			}
			out[item.Key] = state
		}
		return out, nil
	}
}

// authCredential reads the live authentication settings, per call.
//
// It is where the TWO comparison paths are decided, and they are two because of
// the precedence rule rather than by choice: AUTH_PASSWORD in the environment
// is what somebody typed, so it is compared in clear and is never hashed or
// stored, while a password saved from the settings screen is already bcrypt by
// the time settingsWriter has finished with it. internal/settings has decided
// which of the two won before this function reads either — all this does is
// name it, so the holder never has to guess whether a string is a hash.
//
// A stored auth_password is NOT encrypted and so is never unreadable: it is
// hashed, which is D28's asymmetry — curator must be able to use a VPN key and
// must never be able to read a password back.
func authCredential(db *store.Store, codec *secret.Codec) api.CredentialSource {
	enabledKey, passwordKey := settings.Key("AUTH_ENABLED"), settings.Key("AUTH_PASSWORD")

	return func(ctx context.Context) (api.Credential, error) {
		raw, err := db.Settings(ctx)
		if err != nil {
			return api.Credential{}, err
		}
		values, _ := secret.Reveal(codec, raw)
		resolution, err := settings.Resolve(values)
		if err != nil {
			return api.Credential{}, err
		}

		// An unparseable stored value reads as off, which is the safe reading
		// of a boolean that should not exist: every write through the API is
		// checked by config.ParseBool before it lands, so this can only be a
		// row written by hand.
		enabled, _ := config.ParseBool(enabledKey, resolution.Values[enabledKey])
		credential := api.Credential{Enabled: enabled}

		switch password := resolution.Values[passwordKey]; {
		case password == "":
		case resolution.Sources[passwordKey] == settings.SourceEnv:
			credential.Password = password
		default:
			credential.Hash = password
		}
		return credential, nil
	}
}

// settingsWriter turns a validated settings change into rows.
//
// It is where D28's two asymmetries live: a secret is encrypted because curator
// has to be able to *use* it, and the password is hashed because curator only
// ever has to *compare* one. internal/api does neither and knows about neither —
// it hands over plaintext, and this decides what actually reaches the table.
type settingsWriter struct {
	db *store.Store

	// codec is nil only when the database holds ciphertext and its key is gone.
	// Writing a secret then is refused rather than re-keyed: generating a key on
	// top of values that were already there is what would turn "restored without
	// the key file" from recoverable into invisible.
	codec *secret.Codec
}

func (w settingsWriter) WriteSettings(ctx context.Context, set map[string]string, clear []string) error {
	out := make(map[string]string, len(set))
	for key, value := range set {
		item, ok := settings.Get(key)
		if !ok {
			// The handler refuses these already; this is the second door on the
			// same room, because a row nothing reads is a row nobody finds again.
			return fmt.Errorf("%s: not a setting", key)
		}

		switch {
		case item.Kind == settings.KindPassword:
			// bcrypt, one-way, and the only thing in this file that is not
			// reversible on purpose. T41 owns the comparison and the cost
			// measurement on the Pi; storing it any other way would leave that
			// task a migration to write.
			hashed, err := bcrypt.GenerateFromPassword([]byte(value), bcrypt.DefaultCost)
			if err != nil {
				// Names the key and never the value: this is on its way to a log.
				return fmt.Errorf("hash %s: %w", key, err)
			}
			out[key] = string(hashed)

		case item.Secret:
			if w.codec == nil {
				return fmt.Errorf("%s: there is no secret key to encrypt it with — "+
					"restore the key file beside the database, or set SECRET_KEY", key)
			}
			sealed, err := w.codec.Encrypt(key, value)
			if err != nil {
				return err
			}
			out[key] = sealed

		default:
			out[key] = value
		}
	}
	return w.db.SetSettings(ctx, out, clear)
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

func minterDetail(cfg *config.Config) string {
	if !cfg.IndexerX1337 {
		return "not used: 1337x is disabled, and YTS and TPB need no browser"
	}
	return "1337x only; YTS and TPB need no browser"
}

func jellyfinDetail(configured bool) string {
	if !configured {
		return "the library refresh is disabled: set JELLYFIN_API_KEY"
	}
	return "not probed: the only call available is a refresh, which would queue a library scan"
}

// provisionURL is where the Playback screen looks for a Jellyfin.
//
// Not simply cfg.JellyfinURL, and the reason is a decision two tasks apart.
// compose.yaml sets no JELLYFIN_URL on purpose: the environment beats the store
// (docs/decisions.md D28), so a value there would permanently shadow the one
// provisioning is about to write. That leaves cfg.JellyfinURL sitting at its
// laptop default inside the bundle, pointing at a 127.0.0.1 where nothing is —
// so an unconfigured install looks for Jellyfin at compose's service name,
// which is the only address the bundle guarantees.
//
// A configured one is left alone. Somebody who set JELLYFIN_URL, or saved one,
// meant it: that is the NAS case, and T66 adopts it at whatever address they
// gave.
func provisionURL(cfg *config.Config) string {
	if cfg.JellyfinURL != "" && cfg.JellyfinURL != config.DefaultJellyfinURL {
		return cfg.JellyfinURL
	}
	return bundleJellyfinURL
}

// bundleJellyfinURL is compose.yaml's `jellyfin` service on its own network.
//
// The name is the service name and nothing else — not a container_name, which
// compose.yaml deliberately does not set, because DNS on a compose network is
// keyed on the service.
const bundleJellyfinURL = "http://jellyfin:8096"

// ffmpegDetail says what playback can and cannot do on this install.
//
// The path is reported rather than hidden: it is a location on the server's own
// disk and not a credential, and "which ffmpeg is this" is the question somebody
// asks when the remux behaves differently from the one on their laptop.
func ffmpegDetail(path string) string {
	if path == "" {
		return "no ffmpeg found: direct play only, and a container the browser refuses offers VLC"
	}
	return path + " — remuxed, never transcoded"
}
