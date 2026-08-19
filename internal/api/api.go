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

	ListMovies(ctx context.Context) ([]store.Movie, error)
	GetMovie(ctx context.Context, id int64) (store.Movie, error)
	MoviesMissingMetadata(ctx context.Context) ([]store.Movie, error)

	// LibraryByTMDBID annotates a TMDB card with what curator already has.
	LibraryByTMDBID(ctx context.Context) (map[int64]store.LibraryState, error)

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

// Matcher resolves a folder title and year to TMDB metadata. A nil Matcher is a
// supported state, not a broken one: with no API key configured the library still
// scans and everything is reported unmatched.
type Matcher interface {
	SearchMovie(ctx context.Context, title string, year int) (*tmdb.Match, error)
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
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/scan", s.handleScan)
	mux.HandleFunc("GET /api/movies", s.handleListMovies)
	mux.HandleFunc("GET /api/movies/{id}", s.handleGetMovie)
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

	Empty   int `json:"empty"`   // folders on disk with no film in them
	Removed int `json:"removed"` // rows dropped — the folders were left where they are
	Missing int `json:"missing"` // rows kept because this scan could not account for them
}

// handleScan walks the library, upserts every folder that holds a film, removes
// the rows that no longer describe one, then tries to match whatever still has no
// tmdb_id.
//
// The order is not arbitrary. The upsert runs first so the pruner reads a database
// that already knows about everything found this run; the prune runs before the
// TMDB pass so no lookup is ever spent on a row that is about to go.
//
// Synchronous by design: 29 folders and a handful of TMDB calls. A background job
// would be more code and less honest about when the work has actually finished.
func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

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
	for _, m := range found {
		size := m.SizeBytes
		_, inserted, err := s.store.UpsertMovieByPath(ctx, store.ScannedMovie{
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
	}

	if err := s.prune(ctx, found, skipped, &out); err != nil {
		// Same reasoning as the upsert above: a failing write means the database
		// is not usable, and a 200 carrying a partial cleanup would report a
		// success that did not happen.
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	missing, err := s.store.MoviesMissingMetadata(ctx)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	// The disk is the source of truth and metadata is an enrichment, so a missing
	// key is reported, not fatal: everything on disk is already recorded by now.
	if s.matcher == nil {
		out.Unmatched = len(missing)
		s.log.Info("scan complete without metadata: no TMDB key configured",
			"scanned", out.Scanned, "added", out.Added, "unmatched", out.Unmatched,
			"empty", out.Empty, "removed", out.Removed, "missing", out.Missing)
		s.respond(w, http.StatusOK, out)
		return
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
				"title", m.Title, "movie_id", m.ID)
			out.Unmatched++
			continue
		}

		if err := s.match(ctx, m); err != nil {
			// One failure must not abort the scan. The row keeps tmdb_id NULL and
			// is surfaced by the unmatched count.
			s.log.Warn("tmdb match failed", "title", m.Title, "year", m.Year, "err", err)
			out.Unmatched++
			continue
		}
		out.Matched++
	}

	s.log.Info("scan complete", "scanned", out.Scanned, "added", out.Added,
		"matched", out.Matched, "unmatched", out.Unmatched,
		"empty", out.Empty, "removed", out.Removed, "missing", out.Missing)
	s.respond(w, http.StatusOK, out)
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
func (s *Server) prune(ctx context.Context, found []library.Movie, skipped []library.Skipped, out *scanResponse) error {
	rows, err := s.store.MoviesOnDisk(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}

	// Both sides of the join go through filepath.Abs, because "./library/X" and
	// "library/X" are one folder and joining on the raw strings would call the
	// second absent — flagging a row for a film that is right there.
	recorded := make(map[string]bool, len(found))
	for _, m := range found {
		if key, ok := pathKey(m.LibraryPath); ok {
			recorded[key] = true
		}
	}
	noMedia := make(map[string]bool, len(skipped))
	for _, sk := range skipped {
		if !sk.NoMedia {
			continue
		}
		if key, ok := pathKey(sk.Path); ok {
			noMedia[key] = true
		}
	}

	for _, row := range rows {
		// Checked before anything else, so it overrides every reason to delete.
		// A film mid-import has a folder that legitimately holds nothing yet.
		if row.Downloading {
			continue
		}

		key, ok := pathKey(row.LibraryPath)
		outside := library.AssertInside(s.libraryRoot, row.LibraryPath) != nil

		var reason string
		switch {
		case !ok:
			// A path that will not resolve is one nothing can conclude about.
			s.log.Warn("scan: keeping a row whose library_path cannot be resolved",
				"movie_id", row.ID, "title", row.Title, "library_path", row.LibraryPath)
			out.Missing++
			continue
		case outside:
			reason = "its library_path is outside LIBRARY_MOVIES, so it can never be served"
		case recorded[key]:
			continue
		case noMedia[key]:
			reason = "there is no film in that folder"
		default:
			// Absent, unreadable, or a name that no longer parses. The folder may
			// still hold the film, so the row stays and says so in the log.
			s.log.Warn("scan: keeping a row this scan could not account for",
				"movie_id", row.ID, "title", row.Title, "library_path", row.LibraryPath)
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
			"library_path", row.LibraryPath, "why", reason)
		out.Removed++
	}
	return nil
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

// match looks one movie up and records what came back.
//
// The stored title stays the one parsed off disk. TMDB's canonical title would
// undo the very substitution that identifies the folder — "Avengers - Infinity
// War" becoming "Avengers: Infinity War" — and library_path, not title, is the
// row's identity. The UI can show either once tmdb_id is known.
func (s *Server) match(ctx context.Context, m store.Movie) error {
	found, err := s.matcher.SearchMovie(ctx, m.Title, m.Year)
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

	// tmdb_id is UNIQUE, so two folders resolving to one TMDB entry collide here.
	// That is the correct outcome — the second is left unmatched and visible
	// rather than silently merged — and it must not stop the rest of the scan.
	return s.store.SetTMDBMetadata(ctx, m.ID, match)
}

func (s *Server) handleListMovies(w http.ResponseWriter, r *http.Request) {
	movies, err := s.store.ListMovies(r.Context())
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

	body := movieBody{Movie: movie}
	body.JellyfinURL = s.jellyfinLinkFor(
		r.Context(), movie.TMDBID, movie.MatchYear(), movie.Title, movie.Status == store.StatusImported)
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
