// Package api is curator's HTTP surface: the handlers, and the wiring that turns
// a library scan plus a TMDB lookup into rows.
//
// The store, the scanner and the metadata client are reached through interfaces
// declared here rather than their concrete types, so handlers can be exercised
// against fakes instead of a live database and network.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DulsaraNethmin/curator/internal/library"
	"github.com/DulsaraNethmin/curator/internal/remux"
	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/tmdb"
)

// Store is the persistence the handlers need.
type Store interface {
	UpsertMovieByPath(ctx context.Context, m store.ScannedMovie) (store.Movie, bool, error)
	SetTMDBMetadata(ctx context.Context, id int64, match store.TMDBMatch) error

	// MatchMovie is SetTMDBMetadata's request-facing twin: it refuses a row that
	// is already matched and an id another row holds, where the scan's write
	// overwrites because it selected on tmdb_id IS NULL in the first place (T67).
	//
	// CorrectMatch is the one write that may replace a match, and it does it in a
	// single transaction rather than clearing the row first — a row with a NULL
	// tmdb_id is on MoviesMissingMetadata's list, so a clear would hand it back to
	// the scan to re-match from the folder name that was wrong to begin with (T69).
	MatchMovie(ctx context.Context, id int64, match store.TMDBMatch) (store.Movie, error)
	CorrectMatch(ctx context.Context, id int64, match store.TMDBMatch) (store.Movie, error)

	// The media type is a required argument on all three, and there is no value
	// meaning "both". Since T88 a show is a row in the same table, so a read that
	// did not say which kind it wanted would put shows on the film grid and — far
	// worse — put every show on the matching pass's work list, where TMDB's
	// /search/movie would answer confidently for Fargo and Watchmen.
	ListMovies(ctx context.Context, mediaType string) ([]store.Movie, error)
	GetMovie(ctx context.Context, id int64) (store.Movie, error)
	MoviesMissingMetadata(ctx context.Context, mediaType string) ([]store.Movie, error)

	// MoviesMissingArtwork is the OTHER list, and the two are disjoint: rows
	// TMDB has already matched that still carry no poster. They exist because
	// UpsertWanted knows an id and nothing else, so the pass above — which
	// selects `<tmdbcol> IS NULL` — has never seen one.
	MoviesMissingArtwork(ctx context.Context, mediaType string) ([]store.Movie, error)

	// SetTMDBArtwork is that list's write half, and it is COALESCE where
	// SetTMDBMetadata is assignment: this caller fills gaps in a row whose
	// identity is already settled, so a title TMDB has no overview for must not
	// blank one the row already carries.
	SetTMDBArtwork(ctx context.Context, id int64, overview, posterPath *string) error

	// LibraryByTMDBID annotates a TMDB card with what curator already has, within
	// one media type's id space — TMDB's movie and tv ids overlap, so one map
	// keyed on a bare number would badge the wrong poster.
	LibraryByTMDBID(ctx context.Context, mediaType string) (map[int64]store.LibraryState, error)

	// MoviesOnDisk and DeleteMovie are the scan's cleanup: a row for a folder
	// with no film in it loses its row, and only its row (docs/decisions.md D33).
	// This is store.DeleteMovie — rows and nothing else — and NOT the Dispatcher's
	// DeleteMovie, which removes files and talks to a torrent client. The two
	// names sitting on two interfaces in one package is a hazard worth naming
	// here: a scan must never reach the destructive one.
	MoviesOnDisk(ctx context.Context) ([]store.OnDisk, error)
	DeleteMovie(ctx context.Context, id int64) (store.Deleted, error)
}

// Scanner walks the library root and reports what is on disk: the folders that
// hold a film, and the folders that do not and why.
//
// The skipped list is not diagnostics. handleScan joins it against the database to
// decide which rows no longer describe anything, so it is half the contract.
type Scanner interface {
	Scan(root string) ([]library.Movie, []library.Skipped, error)
}

// ScannerFunc adapts a plain function — library.Scan is one — to Scanner.
type ScannerFunc func(root string) ([]library.Movie, []library.Skipped, error)

// Scan calls f.
func (f ScannerFunc) Scan(root string) ([]library.Movie, []library.Skipped, error) {
	return f(root)
}

// ShowScanner is Scanner for the other media type: it walks LIBRARY_TV and
// reports the folders that hold at least one episode, and the folders that do
// not and why.
//
// A second interface rather than a second method on Scanner, for Browser's
// reason: Scanner is phase 1's and every phase 1 fake implements it, and a
// television method on it would force each of them to grow one for a feature
// they do not test. The Skipped half is the SAME type, deliberately — the prune
// joins both roots' skips in one pass, and two skip types would be two joins.
type ShowScanner interface {
	ScanShows(root string) ([]library.Show, []library.Skipped, error)
}

// ShowScannerFunc adapts a plain function to ShowScanner. library.ScanShows
// takes a FeatureOpts as well as a root, so cmd/curator closes over the zero
// value — which production must pass, exactly as it must to library.ScanWith.
type ShowScannerFunc func(root string) ([]library.Show, []library.Skipped, error)

// ScanShows calls f.
func (f ShowScannerFunc) ScanShows(root string) ([]library.Show, []library.Skipped, error) {
	return f(root)
}

// Matcher resolves a folder title and year to TMDB metadata. A nil Matcher is a
// supported state, not a broken one: with no API key configured the library still
// scans and everything is reported unmatched.
type Matcher interface {
	SearchMovie(ctx context.Context, title string, year int) (*tmdb.Match, error)
}

// ShowMatcher is Matcher for television, and it is a separate interface for a
// reason that has already cost this project a paragraph in three documents:
// a show's row must never be looked up against TMDB's /search/movie. Fargo,
// Watchmen, Hannibal, Westworld, Dune and Snowpiercer all match a FILM there,
// and SetTMDBMetadata overwrites unconditionally — so the show would acquire a
// film's id, overview and poster with no error and no log, on every scan
// (docs/decisions.md D48).
//
// Two interfaces make that a compile-time property rather than a rule to
// remember: there is no way to hand the television pass a client that only
// knows how to search films.
type ShowMatcher interface {
	SearchShow(ctx context.Context, title string, year int) (*tmdb.Match, error)
}

// TV is television's half of the server's dependencies, attached with WithTV.
//
// It is a struct rather than three With* calls because the three are one
// feature and are switched on together — the shape JellyfinSetup, IndexerSetup
// and VPNSetup already have in this package.
type TV struct {
	// Root is LIBRARY_TV, and **empty means television is off**: no shows are
	// scanned, no TV rows are pruned, and every television route answers 503
	// naming the variable (docs/decisions.md D40, D48). There is no default,
	// deliberately — see config.Config.LibraryTV.
	Root string

	// Scanner walks Root. Set beside it by cmd/curator; a Root with no scanner
	// could only be a wiring mistake, and tvConfigured reads that pair as off
	// because the alternative is a scan that nil-panics on the request that
	// walks the library.
	Scanner ShowScanner

	// Matcher is nil when TMDB_API_KEY is unset, exactly as Matcher is, and it
	// means shows are recorded and reported unmatched rather than guessed at.
	Matcher ShowMatcher
}

// Server holds the dependencies the handlers close over.
type Server struct {
	store       Store
	scanner     Scanner
	matcher     Matcher // nil when TMDB_API_KEY is unset
	libraryRoot string
	log         *slog.Logger

	// searcher is phase 2's release search. It is attached with WithSearch rather
	// than passed to New so that phase 1's constructor — and every call to it —
	// keeps its shape.
	searcher Searcher

	// dispatcher is phase 3's downloads, attached with WithDownloads for the same
	// reason.
	dispatcher Dispatcher

	// settings is phase 5's read-only status view, attached with WithSettings.
	// It is a plain struct built by cmd/curator rather than a *config.Config, so
	// this package still knows nothing about where configuration comes from.
	settings *Settings

	// logs is the in-memory tail of the process log, attached with WithLogs.
	logs LogTail

	// browser is the TMDB catalogue behind the browsing screens, attached with
	// WithBrowser. Nil when TMDB_API_KEY is unset, which is a supported state.
	browser Browser

	// rails caches the TMDB half of the Discover screen. Its zero value works;
	// see discoverCache.
	rails discoverCache

	// remux is phase 8's ffmpeg fallback, attached with WithRemux. Nil is the
	// state of every install without an ffmpeg on it, and it is what makes
	// POST .../playback omit remux_url and GET .../remux answer 404.
	remux *remux.Remuxer

	// jellyfin is the media server the movie screen links into, attached with
	// WithJellyfin. Nil when JELLYFIN_API_KEY is unset, which is the state
	// curator ships in, and it means the screen draws no link at all.
	//
	// jellyfinURL beside it is what a BROWSER can reach — jellyfin_public_url,
	// falling back to jellyfin_url — and never the address curator itself uses,
	// which inside Docker is a container name no browser can resolve.
	jellyfin    MediaServer
	jellyfinURL string

	// jellyfinSetup is phase 9's Playback screen, attached with
	// WithJellyfinSetup. It is separate from the two fields above and holds the
	// WRITING half of internal/jellyfin: a Provisioner reaches it, the media
	// server above cannot, and only the setup flow is ever handed one
	// (docs/decisions.md D34).
	jellyfinSetup *JellyfinSetup

	// indexers is phase 9's Indexers screen, attached with WithIndexers. It
	// holds minter's probe and nothing that can fetch a page through it.
	indexers *IndexerSetup

	// updates is the Updates screen's dependencies, attached with WithUpdates.
	// Nil in any process that was built without them, which is every test that
	// does not care.
	updates *UpdateSetup

	// vpn is T84's VPN screen, attached with WithVPN. Nil in every process
	// built without it, which is every test that does not care.
	vpn *VPNSetup

	// vpnLastCheck is when POST /api/vpn/check last ran, and its lock. Here
	// rather than in VPNSetup because a Setup is passed to With* by value, so a
	// mutex in one would be copied and a limiter that resets per copy is not a
	// limiter. Same reason streamWarned is here.
	vpnCheckMu   sync.Mutex
	vpnLastCheck time.Time

	// tickets mints phase 8's playback credential, attached with WithTickets.
	// Nil is the state every install without a password is in, and it is what
	// makes POST .../playback answer with a plain URL.
	tickets Tickets

	// streamWarned is the set of folders the stream endpoint has already said
	// hold more than one film. It is here rather than beside its one use in
	// stream.go so that it is per-Server and not per-process; see passedOver
	// for why the line is deduplicated at all.
	streamWarned sync.Map

	// tv is television, attached with WithTV. The zero value is television off,
	// which is what every install that has not set LIBRARY_TV runs, and it is
	// the state phase 11 keeps working exactly as it did before.
	tv TV
}

// WithTV attaches television and returns the server.
func (s *Server) WithTV(tv TV) *Server {
	s.tv = tv
	return s
}

// tvConfigured reports whether this curator does television at all.
//
// One place to ask, mirroring config.TVConfigured, so that the scan, the
// routes and the refusal cannot come to different conclusions about whether a
// TV root exists.
func (s *Server) tvConfigured() bool {
	return strings.TrimSpace(s.tv.Root) != "" && s.tv.Scanner != nil
}

// errTVUnconfigured is the television-is-off state, phrased exactly as
// errTMDBUnconfigured is: it names the variable, because that is the whole
// remedy (docs/decisions.md D40).
var errTVUnconfigured = errors.New("television is not configured: set LIBRARY_TV")

// televisionOff answers the request itself when television is off, and reports
// whether it did.
//
// 503 and not 404: the route exists, the request was fine, and this install has
// simply not turned television on — the same posture an unset QBIT_USER and an
// unset TMDB_API_KEY already have. A 404 would read as "curator cannot do
// this", which is the one thing it is not.
func (s *Server) televisionOff(w http.ResponseWriter) bool {
	if s.tvConfigured() {
		return false
	}
	s.fail(w, http.StatusServiceUnavailable, errTVUnconfigured)
	return true
}

// New builds a Server. matcher may be nil; log may be nil, in which case the
// default logger is used.
func New(st Store, sc Scanner, matcher Matcher, libraryRoot string, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{store: st, scanner: sc, matcher: matcher, libraryRoot: libraryRoot, log: log}
}

// Register mounts the API routes on mux. /healthz belongs to main, not here.
//
// The two television routes are mounted whether or not television is
// configured, and that is the point rather than an oversight: they answer 503
// naming LIBRARY_TV, and a route that did not exist would answer 404 — which
// says "curator cannot do this" instead of "you have not turned this on"
// (docs/decisions.md D40).
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/scan", s.handleScan)
	mux.HandleFunc("GET /api/movies", s.handleListMovies)
	mux.HandleFunc("GET /api/movies/{id}", s.handleGetMovie)
	mux.HandleFunc("GET /api/shows", s.handleListShows)
	mux.HandleFunc("GET /api/shows/{id}", s.handleGetShow)
}

// scanResponse is what POST /api/scan reports.
//
// unmatched counts every row still lacking a tmdb_id after the pass, not just the
// ones this scan added — a folder that failed to match last time is still
// unmatched, and hiding that would defeat the point of recording it (D6).
type scanResponse struct {
	// Scanned is folders that hold a FILM, which is narrower than it used to be:
	// a folder with nothing playable in it is no longer a movie (D33). Empty is
	// how many of those there were, and it ships in the same response rather than
	// later precisely so that a library of 29 folders reporting 2 films explains
	// itself instead of looking like a scanner that broke.
	Scanned   int `json:"scanned"`
	Added     int `json:"added"`
	Matched   int `json:"matched"`
	Unmatched int `json:"unmatched"`

	// Artwork is rows that were ALREADY matched and had no poster until this
	// pass fetched one — which is a different number from Matched and is not a
	// subset of it. It is a backfill, so on a healthy library it is 0 forever;
	// the run that first sees a dispatched row is the one that reports it.
	Artwork int `json:"artwork"`

	Empty int `json:"empty"` // folders on disk with no film in them

	// Removed and Missing count BOTH media types, and they are not split the
	// way the four above are. There is ONE prune over both roots (see prune),
	// so a second pair of counters would imply two passes that could disagree —
	// and the two numbers a reader acts on are "how many rows went" and "how
	// many could not be accounted for", not which root they came from. The log
	// carries the media type on every line for the reader who needs it.
	Removed int `json:"removed"` // rows dropped — the folders were left where they are
	Missing int `json:"missing"` // rows kept because this scan could not account for them

	// Television, counted separately so that every key above keeps exactly the
	// meaning it had before phase 11 — a screen reading `scanned` gets films,
	// as it always did. All four are zero when LIBRARY_TV is unset, which is
	// the honest report of a root that was never walked.
	Shows          int `json:"shows"`           // folders that hold at least one episode
	ShowsAdded     int `json:"shows_added"`     // of those, rows that did not exist before
	ShowsMatched   int `json:"shows_matched"`   // shows this pass resolved against TMDB's tv ids
	ShowsUnmatched int `json:"shows_unmatched"` // shows still carrying no tmdb_tv_id
	ShowsArtwork   int `json:"shows_artwork"`   // matched shows that had no poster until this pass
	ShowsEmpty     int `json:"shows_empty"`     // folders under LIBRARY_TV with no episode in them
	Episodes       int `json:"episodes"`        // distinct episodes across every show found
}

// mediaWalk is what one scan learned about one media type's root.
//
// It is the whole input to the prune, and it is built ONLY for a root this scan
// actually walked. A media type absent from the map is a media type this scan
// has no finding about — which is a reason to keep every row under it and never
// a reason to delete one.
type mediaWalk struct {
	// root is the directory this media type's rows must live under. Kept per
	// media type because the containment question is asked per row: a show is
	// not outside the library for sitting outside LIBRARY_MOVIES.
	root string

	recorded map[string]bool // folders that hold a film, or at least one episode
	noMedia  map[string]bool // folders read successfully with nothing in them
}

// handleScan walks the library, upserts every folder that holds a film or an
// episode, removes the rows that no longer describe one, then tries to match
// whatever still has no TMDB id.
//
// The order is not arbitrary. The upserts run first so the pruner reads a database
// that already knows about everything found this run; the prune runs before the
// TMDB pass so no lookup is ever spent on a row that is about to go.
//
// **Both roots are walked in ONE pass and pruned ONCE**, and that is the whole
// design rather than an implementation detail. Two scoped prunes is the tempting
// shape and it loses the television library: `prune`'s switch puts the
// containment check before the "did this scan find it" check, so a show row met
// by a movie-only prune is not merely unfound — it is affirmatively deleted for
// sitting outside LIBRARY_MOVIES, taking its downloads with it through the
// foreign key (docs/decisions.md D48, and phase-11.md's first trap).
//
// Synchronous by design: 29 folders and a handful of TMDB calls. A background job
// would be more code and less honest about when the work has actually finished.
func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// One entry per root this scan actually walked. Television is absent from
	// it when LIBRARY_TV is unset, which is what leaves every TV row in the
	// prune's "kept" arm.
	walks := make(map[string]mediaWalk, 2)

	found, skipped, err := s.scanner.Scan(s.libraryRoot)
	if err != nil {
		// Nothing is pruned on this path, and that is the guard rather than an
		// omission: a library disk that is not mounted reads as zero folders, and
		// a prune that ran anyway would delete the row for every film on it.
		s.fail(w, http.StatusInternalServerError, fmt.Errorf("scan library: %w", err))
		return
	}

	out := scanResponse{Scanned: len(found)}
	for _, sk := range skipped {
		if sk.NoMedia {
			out.Empty++
		}
	}
	films := mediaWalk{root: s.libraryRoot, recorded: map[string]bool{}, noMedia: noMediaKeys(skipped)}
	for _, m := range found {
		size := m.SizeBytes
		_, inserted, err := s.store.UpsertMovieByPath(ctx, store.ScannedMovie{
			// Required since T88, and stated at every construction site rather
			// than defaulted. UpsertMovieByPath REWRITES media_type from this
			// field on every pass, so a site that left it out would relabel a
			// show as a film — and the prune below would then delete it for
			// sitting outside LIBRARY_MOVIES.
			MediaType:   store.MediaTypeMovie,
			LibraryPath: m.LibraryPath,
			Title:       m.Title,
			Year:        m.Year,
			Status:      m.Status,
			SizeBytes:   &size,
		})
		if err != nil {
			// Unlike a TMDB failure, a failing write means the database is not
			// usable; continuing would report a success that did not happen.
			s.fail(w, http.StatusInternalServerError, err)
			return
		}
		if inserted {
			out.Added++
		}
		addPathKey(films.recorded, m.LibraryPath)
	}
	walks[store.MediaTypeMovie] = films

	if s.tvConfigured() {
		if err := s.scanShows(ctx, walks, &out); err != nil {
			// A television root that cannot be read stops the WHOLE scan, films
			// included, and that is deliberate: there is one prune over both
			// roots, so letting the film half proceed would run it with the
			// television half's findings missing — which is the movie-only
			// prune this design exists to avoid. D33's rule is that a root that
			// cannot be read prunes nothing, and here that is both of them.
			s.fail(w, http.StatusInternalServerError, err)
			return
		}
	}

	if err := s.prune(ctx, walks, &out); err != nil {
		// Same reasoning as the upsert above: a failing write means the database
		// is not usable, and a 200 carrying a partial cleanup would report a
		// success that did not happen.
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	if out.Matched, out.Unmatched, err = s.matchPass(ctx, store.MediaTypeMovie); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	if s.tvConfigured() {
		if out.ShowsMatched, out.ShowsUnmatched, err = s.matchPass(ctx, store.MediaTypeTV); err != nil {
			s.fail(w, http.StatusInternalServerError, err)
			return
		}
	}

	// After the matching pass, not before: a row matched a moment ago already
	// carries its poster, so running this first would ask TMDB about it twice.
	// The television half is NOT behind tvConfigured — a show row exists whether
	// or not LIBRARY_TV is set today, because a dispatch creates one, and a root
	// that is switched off is a reason not to SCAN it rather than a reason to
	// leave its artwork blank.
	if out.Artwork, err = s.artworkPass(ctx, store.MediaTypeMovie); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	if out.ShowsArtwork, err = s.artworkPass(ctx, store.MediaTypeTV); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	// The disk is the source of truth and metadata is an enrichment, so a missing
	// key is reported, not fatal: everything on disk is already recorded by now.
	message := "scan complete"
	if s.matcher == nil {
		message = "scan complete without metadata: no TMDB key configured"
	}
	s.log.Info(message, "scanned", out.Scanned, "added", out.Added,
		"matched", out.Matched, "unmatched", out.Unmatched,
		"artwork", out.Artwork+out.ShowsArtwork,
		"empty", out.Empty, "removed", out.Removed, "missing", out.Missing,
		"shows", out.Shows, "shows_added", out.ShowsAdded, "episodes", out.Episodes)
	s.respond(w, http.StatusOK, out)
}

// scanShows is handleScan's television half: walk LIBRARY_TV, record every
// folder that holds at least one episode, and hand the prune what it found.
//
// Split out rather than inlined because the two halves are the same four steps
// over two different types, and interleaving them in one function is how the
// media type on a ScannedMovie gets copied from the line above it.
func (s *Server) scanShows(ctx context.Context, walks map[string]mediaWalk, out *scanResponse) error {
	shows, skipped, err := s.tv.Scanner.ScanShows(s.tv.Root)
	if err != nil {
		return fmt.Errorf("scan television library: %w", err)
	}

	out.Shows = len(shows)
	for _, sk := range skipped {
		if sk.NoMedia {
			out.ShowsEmpty++
		}
	}

	walk := mediaWalk{root: s.tv.Root, recorded: map[string]bool{}, noMedia: noMediaKeys(skipped)}
	for _, show := range shows {
		size := show.SizeBytes
		_, inserted, err := s.store.UpsertMovieByPath(ctx, store.ScannedMovie{
			// The line the whole media-type argument is about. It is stated
			// here for the same reason MediaTypeMovie is stated above: this
			// field REWRITES media_type on every pass, so an omission would
			// relabel every show as a film — and the prune would then delete
			// each of them for sitting outside LIBRARY_MOVIES.
			MediaType:   store.MediaTypeTV,
			LibraryPath: show.LibraryPath,
			Title:       show.Title,
			Year:        show.Year,
			Status:      show.Status,
			// The SUMMED size of the episodes on disk, which is what
			// library.ScanShows already computed. It is the scan that owns this
			// column for television: the importer writes one file's size per
			// import, so season 2 would otherwise report season 2's bytes.
			SizeBytes: &size,
		})
		if err != nil {
			return err
		}
		if inserted {
			out.ShowsAdded++
		}
		out.Episodes += show.Episodes
		addPathKey(walk.recorded, show.LibraryPath)
	}
	walks[store.MediaTypeTV] = walk
	return nil
}

// matchPass looks up every row of one media type that still has no TMDB id, and
// reports how many it matched and how many it left alone.
//
// The media type decides which of TMDB's two searches is used, and there is no
// path by which a show reaches /search/movie: the television lookup is a
// different interface entirely (ShowMatcher), so the wrong one is a compile
// error rather than six shows quietly acquiring films' posters.
func (s *Server) matchPass(ctx context.Context, mediaType string) (matched, unmatched int, err error) {
	missing, err := s.store.MoviesMissingMetadata(ctx, mediaType)
	if err != nil {
		return 0, 0, err
	}
	if len(missing) == 0 {
		return 0, 0, nil
	}
	if !s.canMatch(mediaType) {
		// No key for this media type: everything on disk is already recorded,
		// and the rows are reported unmatched rather than guessed at.
		return 0, len(missing), nil
	}

	for _, m := range missing {
		// A row with no year is not matched, it is surfaced. TMDB's search is
		// fuzzy by design, and an unconstrained query for a bare title returns a
		// confident wrong answer: "avengers" with no year matches
		// Avengers: Doomsday (2026). D9 is explicit that the fallback is to
		// record NULL and surface it rather than guess, and it names 2026
		// releases as exactly where a plausible wrong match lives. A folder on
		// disk always has a year; a row wanted from a yearless search does not.
		if m.Year <= 0 {
			s.log.Warn("not matched: no year, and TMDB would guess without one",
				"title", m.Title, "movie_id", m.ID, "media_type", mediaType)
			unmatched++
			continue
		}

		if err := s.match(ctx, m); err != nil {
			// One failure must not abort the scan. The row keeps its TMDB id
			// NULL and is surfaced by the unmatched count.
			s.log.Warn("tmdb match failed", "title", m.Title, "year", m.Year,
				"media_type", mediaType, "err", err)
			unmatched++
			continue
		}
		matched++
	}
	return matched, unmatched, nil
}

// artworkPass fills in the overview and poster of rows TMDB has ALREADY matched
// and that have neither, and reports how many it filled.
//
// It is not matchPass with a wider predicate, and the rows it exists for are the
// ones curator wrote for itself. store.UpsertWanted records a dispatch as
// (tmdb_id, title, year, media_type, status, added_at) — at that moment the id
// is known and the artwork is not — and matchPass's work list is
// `<tmdbcol> IS NULL`, so those rows were never revisited by any scan. Measured
// on the Pi 2026-08-22: five of five rows carried an id and no poster, which was
// every row curator had ever created, and the library grid drew the `noposter`
// fallback for all of them permanently.
//
// **The lookup is by id, so D9 does not apply and a yearless row is fine.**
// matchPass refuses to search without a year because TMDB's fuzzy search would
// answer confidently and wrongly. There is nothing fuzzy here: the id came from
// the row, it is the title itself, and /movie/{id} either knows it or 404s.
//
// A failure is one row, not the scan. The pass is idempotent and runs again on
// the next scan, so a TMDB outage costs a delay rather than a permanent hole.
func (s *Server) artworkPass(ctx context.Context, mediaType string) (filled int, err error) {
	if s.browser == nil {
		// Same posture as canMatch: no key for this catalogue, so the rows stay
		// as they are and nothing is guessed.
		return 0, nil
	}

	missing, err := s.store.MoviesMissingArtwork(ctx, mediaType)
	if err != nil {
		return 0, err
	}

	for _, m := range missing {
		// The id is read from the column this row's own media type owns. The
		// list is already scoped, so a nil here would be a store that answered
		// the wrong question — worth saying rather than dereferencing.
		id := m.TMDBID
		if mediaType == store.MediaTypeTV {
			id = m.TMDBTVID
		}
		if id == nil {
			s.log.Warn("a row on the artwork list carries no id for its own media type",
				"title", m.Title, "movie_id", m.ID, "media_type", mediaType)
			continue
		}
		if err := s.fillArtwork(ctx, m.ID, mediaType, *id); err != nil {
			s.log.Warn("could not fetch artwork for a matched row",
				"title", m.Title, "movie_id", m.ID, "media_type", mediaType, "err", err)
			continue
		}
		filled++
	}
	return filled, nil
}

// fillArtwork reads one already-identified title off TMDB by id and records its
// overview and poster.
//
// **One function on both paths deliberately** — the dispatch that stops the
// hole opening and the scan pass that heals what already fell in it. The
// healing path is the one that gets exercised in anger, so sharing the code is
// what keeps the prevention path honest.
//
// The title is left alone, exactly as match does: TMDB's canonical spelling
// would undo the " - " substitution that identifies the folder (D9), and
// library_path is the row's identity. tmdb_year is left alone too — it belongs
// to the hand-match path (D37).
func (s *Server) fillArtwork(ctx context.Context, movieID int64, mediaType string, tmdbID int64) error {
	found, err := s.details(ctx, mediaType, tmdbID)
	if err != nil {
		return err
	}

	var overview, poster *string
	if found.Overview != "" {
		overview = &found.Overview
	}
	if found.PosterPath != "" {
		poster = &found.PosterPath
	}
	if overview == nil && poster == nil {
		// TMDB knows this id and holds neither. Writing would be a no-op that
		// leaves the row on the list, so say so rather than report a fill.
		return fmt.Errorf("tmdb has no overview and no poster for %d", tmdbID)
	}
	return s.store.SetTMDBArtwork(ctx, movieID, overview, poster)
}

// details is the by-id lookup, chosen from the media type of the row asking.
//
// **Browser spans both id spaces, so this is the one place the split that
// Matcher/ShowMatcher makes a compile error has to be made by hand.** TMDB
// numbers films and shows independently — 95396 is both Severance's tv id and
// some film's movie id — so asking /movie/{id} with a tv id does not miss, it
// LANDS on an unrelated film and would write its poster onto the show (D48).
func (s *Server) details(ctx context.Context, mediaType string, tmdbID int64) (*tmdb.Details, error) {
	if mediaType == store.MediaTypeTV {
		return s.browser.Show(ctx, int(tmdbID))
	}
	return s.browser.Movie(ctx, int(tmdbID))
}

// canMatch reports whether there is a TMDB client for this media type.
func (s *Server) canMatch(mediaType string) bool {
	if mediaType == store.MediaTypeTV {
		return s.tv.Matcher != nil
	}
	return s.matcher != nil
}

// noMediaKeys is the set of folders a scan READ and found nothing in.
//
// The NoMedia bool rather than the sentence, because it is the one skip that
// removes a row and a caller deciding that on a prose match is one reworded
// message away from deleting the wrong thing (docs/decisions.md D33).
func noMediaKeys(skipped []library.Skipped) map[string]bool {
	keys := make(map[string]bool, len(skipped))
	for _, sk := range skipped {
		if !sk.NoMedia {
			continue
		}
		addPathKey(keys, sk.Path)
	}
	return keys
}

// addPathKey records a path in its comparable form, and records nothing when it
// will not resolve.
func addPathKey(set map[string]bool, path string) {
	if key, ok := pathKey(path); ok {
		set[key] = true
	}
}

// prune removes the rows this scan proved no longer describe a film, and counts
// the ones it deliberately kept.
//
// It is a pure join over what the scan already returned: the walk has just been
// done, so every fact is in hand and nothing here touches the disk again. That
// also removes a time-of-check window — a re-stat could disagree with the walk
// that produced the classification.
//
// **Only two conditions delete, and both are positive findings.** A row is removed
// when its folder can never be served (it is outside the library root) or when the
// scan READ that folder and found no film in it. Everything else is kept: absent,
// unreadable, unparseable, or simply not mentioned. That asymmetry is the whole
// safety argument (docs/decisions.md D33) — a library that mounts empty produces no
// movies and no skips, every row falls through to "kept", and nothing is lost to a
// loose cable.
//
// The deletion is store.DeleteMovie: rows only, downloads then movies for the
// foreign key, and no disk and no torrent client. Emphatically not the download
// service's method of the same name.
//
// **It runs once, over both media types, and `outside` is computed against the
// root that owns each row.** store.MoviesOnDisk deliberately returns films and
// shows together with the media type on the ROW rather than a filter on the
// query, because this list is what a prune may consider — and a row filtered
// out of it is a row that cannot be kept either. A row whose root this scan did
// not walk falls through to kept, which is the same asymmetry as everything
// else here: only a positive finding removes anything.
func (s *Server) prune(ctx context.Context, walks map[string]mediaWalk, out *scanResponse) error {
	rows, err := s.store.MoviesOnDisk(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}

	// Rows kept because nothing walked their root, counted per media type and
	// logged once at the end. One line per show per scan would be the shape a
	// movies-only prune produced for ever, and /api/logs is a screen (D18) —
	// the fact is about the ROOT, so it is stated once about the root.
	unwalked := map[string]int{}

	for _, row := range rows {
		// Checked before anything else, so it overrides every reason to delete.
		// A film mid-import has a folder that legitimately holds nothing yet.
		if row.Downloading {
			continue
		}

		// Both sides of the join go through filepath.Abs, because "./library/X"
		// and "library/X" are one folder and joining on the raw strings would
		// call the second absent — flagging a row for a film that is right there.
		key, ok := pathKey(row.LibraryPath)
		walk, walked := walks[row.MediaType]

		var reason string
		switch {
		case !ok:
			// A path that will not resolve is one nothing can conclude about.
			s.log.Warn("scan: keeping a row whose library_path cannot be resolved",
				"movie_id", row.ID, "title", row.Title, "library_path", row.LibraryPath)
			out.Missing++
			continue
		case !walked:
			// **Before the containment check, and that order is load-bearing.**
			// With LIBRARY_TV unset there is no television root to be inside of,
			// and library.AssertInside("", path) resolves the empty root to the
			// working directory — so asking it here would find every show
			// "outside" and delete the television library on the first movie
			// scan. A root nobody walked produces no finding at all.
			unwalked[row.MediaType]++
			out.Missing++
			continue
		case library.AssertInside(walk.root, row.LibraryPath) != nil:
			reason = "its library_path is outside " + rootVariable(row.MediaType) + ", so it can never be served"
		case walk.recorded[key]:
			continue
		case walk.noMedia[key]:
			reason = emptyFolderReason(row.MediaType)
		default:
			// Absent, unreadable, or a name that no longer parses. The folder may
			// still hold the film, so the row stays and says so in the log.
			s.log.Warn("scan: keeping a row this scan could not account for",
				"movie_id", row.ID, "title", row.Title, "library_path", row.LibraryPath,
				"media_type", row.MediaType)
			out.Missing++
			continue
		}

		if _, err := s.store.DeleteMovie(ctx, row.ID); err != nil {
			return fmt.Errorf("scan: removing movie %d: %w", row.ID, err)
		}
		// One line per removal, because /api/logs is a screen and "nothing
		// vanishes silently" has to be true there as well as in the response.
		s.log.Info("scan: removed a row, and left the folder on disk",
			"movie_id", row.ID, "title", row.Title, "year", row.Year,
			"media_type", row.MediaType, "library_path", row.LibraryPath, "why", reason)
		out.Removed++
	}

	for mediaType, count := range unwalked {
		s.log.Info("scan: rows kept without being considered, because nothing walked their library root",
			"media_type", mediaType, "rows", count, "why", unwalkedReason(mediaType))
	}
	return nil
}

// rootVariable names the environment variable that owns a media type's root. It
// is what a removal's reason quotes, because the variable is the remedy.
func rootVariable(mediaType string) string {
	if mediaType == store.MediaTypeTV {
		return "LIBRARY_TV"
	}
	return "LIBRARY_MOVIES"
}

// emptyFolderReason is the sentence for a folder that was read and holds
// nothing. The two media types are two different emptinesses — a film or an
// episode — and saying which is what makes the line actionable.
func emptyFolderReason(mediaType string) string {
	if mediaType == store.MediaTypeTV {
		return "there is no episode in that folder"
	}
	return "there is no film in that folder"
}

// unwalkedReason says why a media type's root produced no findings at all.
func unwalkedReason(mediaType string) string {
	if mediaType == store.MediaTypeTV {
		return "LIBRARY_TV is unset, so television is off and nothing under it was scanned"
	}
	return "curator does not know that media type, so it has no root to scan"
}

// pathKey is the comparable form of a library path. It reports false when the
// path cannot be resolved at all, which is a reason to keep a row rather than a
// reason to conclude anything about it.
func pathKey(path string) (string, bool) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	return absolute, true
}

// errNoMatch reports that TMDB had nothing for this title — a normal outcome, and
// deliberately distinct from a lookup that failed.
var errNoMatch = errors.New("no tmdb match")

// match looks one row up and records what came back.
//
// The stored title stays the one parsed off disk. TMDB's canonical title would
// undo the very substitution that identifies the folder — "Avengers - Infinity
// War" becoming "Avengers: Infinity War" — and library_path, not title, is the
// row's identity. The UI can show either once the TMDB id is known.
//
// **The row decides which search runs, and which column the answer lands in.**
// The lookup is chosen from m.MediaType here, and store.SetTMDBMetadata writes
// through a CASE over the row's own media_type — so there is no argument by
// which a caller could put a tv id in the movie column (D48).
func (s *Server) match(ctx context.Context, m store.Movie) error {
	found, err := s.lookup(ctx, m.MediaType, m.Title, m.Year)
	if err != nil {
		return err
	}
	if found == nil {
		return errNoMatch
	}

	match := store.TMDBMatch{TMDBID: int64(found.TMDBID)}
	if found.Overview != "" {
		match.Overview = &found.Overview
	}
	if found.PosterPath != "" {
		match.PosterPath = &found.PosterPath
	}

	// Both TMDB id columns are UNIQUE, so two folders resolving to one entry
	// collide here. That is the correct outcome — the second is left unmatched
	// and visible rather than silently merged — and it must not stop the rest
	// of the scan.
	return s.store.SetTMDBMetadata(ctx, m.ID, match)
}

// lookup is the TMDB search for one media type.
//
// A show goes to /search/tv and a film to /search/movie, and the two are
// reached through two different interfaces so that the wrong one cannot be
// passed. That is not ceremony: a show searched as a film matches Fargo,
// Watchmen, Hannibal, Westworld, Dune and Snowpiercer, and the write that
// followed would overwrite the row with a film's identity, silently, on every
// scan (docs/decisions.md D48).
func (s *Server) lookup(ctx context.Context, mediaType, title string, year int) (*tmdb.Match, error) {
	if mediaType == store.MediaTypeTV {
		return s.tv.Matcher.SearchShow(ctx, title, year)
	}
	return s.matcher.SearchMovie(ctx, title, year)
}

func (s *Server) handleListMovies(w http.ResponseWriter, r *http.Request) {
	movies, err := s.store.ListMovies(r.Context(), store.MediaTypeMovie)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	if movies == nil {
		movies = []store.Movie{} // an empty library is [], never null
	}
	s.respond(w, http.StatusOK, movies)
}

func (s *Server) handleGetMovie(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, http.StatusBadRequest, fmt.Errorf("id %q: not a number", r.PathValue("id")))
		return
	}

	movie, err := s.store.GetMovie(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.fail(w, http.StatusNotFound, fmt.Errorf("no movie with id %d", id))
			return
		}
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	// A show is not a film, whether or not television is configured on this
	// install — so this is a 404 and never the 503 the /api/shows routes
	// answer. Tested for a POSITIVE television finding rather than "not a
	// movie", which is the same asymmetry the prune is built on: a row whose
	// media type says nothing is served as what this route serves.
	if movie.MediaType == store.MediaTypeTV {
		s.fail(w, http.StatusNotFound, fmt.Errorf("no movie with id %d", id))
		return
	}

	body := movieBody{Movie: movie}
	body.JellyfinURL = s.jellyfinLinkFor(r.Context(), store.MediaTypeMovie,
		movie.TMDBID, movie.MatchYear(), movie.Title, movie.Status == store.StatusImported)
	s.respond(w, http.StatusOK, body)
}

// handleListShows is handleListMovies for the other media type, and it is a
// second handler rather than a ?media= on the first.
//
// The two lists are two screens with two URLs — /library/ toggles between them
// (T95) — and a query parameter that changed what a route returns would make
// "which one am I looking at" a question about a string rather than about a
// path. It also keeps GET /api/movies exactly what it has always been: films.
func (s *Server) handleListShows(w http.ResponseWriter, r *http.Request) {
	if s.televisionOff(w) {
		return
	}

	shows, err := s.store.ListMovies(r.Context(), store.MediaTypeTV)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	if shows == nil {
		shows = []store.Movie{} // an empty library is [], never null
	}
	s.respond(w, http.StatusOK, shows)
}

func (s *Server) handleGetShow(w http.ResponseWriter, r *http.Request) {
	if s.televisionOff(w) {
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, http.StatusBadRequest, fmt.Errorf("id %q: not a number", r.PathValue("id")))
		return
	}

	show, err := s.store.GetMovie(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.fail(w, http.StatusNotFound, fmt.Errorf("no show with id %d", id))
			return
		}
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	if show.MediaType != store.MediaTypeTV {
		// A film's id, on the television route. 404 rather than a redirect to
		// /api/movies/{id}: the two id spaces are one table, and a route that
		// quietly served the other kind is how a screen ends up drawing a film
		// as a show.
		s.fail(w, http.StatusNotFound, fmt.Errorf("no show with id %d", id))
		return
	}

	body := movieBody{Movie: show}
	// TMDBTVID, not TMDBID: a show's id lives in its own column because TMDB's
	// two id sequences overlap, and Jellyfin is asked for a Series with it.
	body.JellyfinURL = s.jellyfinLinkFor(r.Context(), store.MediaTypeTV,
		show.TMDBTVID, show.MatchYear(), show.Title, show.Status == store.StatusImported)
	s.respond(w, http.StatusOK, body)
}

// movieBody is a library row plus the one fact about it that is not in the
// database.
//
// **store.Movie is embedded, so the JSON keeps exactly the shape this endpoint
// has always had** and gains one optional key. encoding/json flattens an
// embedded struct, so every existing field stays at the top level and no client
// that already reads this route has to change.
//
// The LIST endpoint deliberately keeps `store.Movie` itself. jellyfin_url costs
// a lookup per film against a service that may be switched off, and a library
// screen asks for every row at once — the movie detail body has always paid
// that cost for one film at a time and this route now matches it.
type movieBody struct {
	store.Movie
	// Absent rather than empty when there is no link to draw, exactly as on the
	// catalogue page: no Jellyfin configured, or a film that is not on disk.
	JellyfinURL string `json:"jellyfin_url,omitempty"`
}

func (s *Server) respond(w http.ResponseWriter, status int, body any) {
	if err := writeJSON(w, status, body); err != nil {
		// The status line is already sent, so this cannot become an error
		// response; log it rather than pretending the write succeeded.
		s.log.Error("write response", "err", err)
	}
}

// writeJSON writes one JSON body with a status.
//
// Package-level rather than a method because the authentication middleware runs
// in front of the mux, with no Server to reach for — and a 401 that answered in
// a different shape from every other error would be a second envelope for
// clients to learn.
func writeJSON(w http.ResponseWriter, status int, body any) error {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(body)
}

// fail writes {"error": "..."} with a real status code. minter once returned 200
// carrying a failure, which made every failure look like success; not here.
func (s *Server) fail(w http.ResponseWriter, status int, err error) {
	if status >= http.StatusInternalServerError {
		s.log.Error("request failed", "status", status, "err", err)
	}
	s.respond(w, status, map[string]string{"error": err.Error()})
}

// failCause writes the sentence to the client and keeps the chain for the log.
//
// `fail` puts one error in both places, which is right for every status where
// the two readers want the same thing. A dependency's failure is where they
// stop wanting it: the chain names an upstream path, a base URL and — through
// `snippet` — up to 256 bytes of somebody else's response body, which is
// precisely what an operator needs and precisely what a browser must not show.
//
// This is a second function rather than a fourth parameter on `fail`. T71
// refused the overload because at 409 and 422 there was no log at all, so the
// second channel would have been dead. At 502 and 503 both channels are real,
// and `fail`'s one job is still one job.
func (s *Server) failCause(w http.ResponseWriter, status int, sentence string, cause error) {
	s.logCause(status, cause)
	s.respond(w, status, map[string]string{"error": sentence})
}

// logCause writes the chain the response body no longer carries.
//
// Gated at 500 for the same reason `fail` is, and the gate is the whole reason
// this is safe to call from the Jellyfin handlers: those answer 409 and 422 as
// well, and D40 left the 4xx unlogged deliberately — `failFields` never logs
// because a settings message can quote a rejected value.
func (s *Server) logCause(status int, cause error) {
	if status >= http.StatusInternalServerError {
		s.log.Error("request failed", "status", status, "err", cause)
	}
}
