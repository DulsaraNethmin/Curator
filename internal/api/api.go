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
	"strconv"

	"github.com/DulsaraNethmin/curator/internal/library"
	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/tmdb"
)

// Store is the persistence the handlers need.
type Store interface {
	UpsertMovieByPath(ctx context.Context, m store.ScannedMovie) (store.Movie, bool, error)
	SetTMDBMetadata(ctx context.Context, id int64, match store.TMDBMatch) error
	ListMovies(ctx context.Context) ([]store.Movie, error)
	GetMovie(ctx context.Context, id int64) (store.Movie, error)
	MoviesMissingMetadata(ctx context.Context) ([]store.Movie, error)
}

// Scanner walks the library root and reports what is on disk.
type Scanner interface {
	Scan(root string) ([]library.Movie, error)
}

// ScannerFunc adapts a plain function — library.Scan is one — to Scanner.
type ScannerFunc func(root string) ([]library.Movie, error)

// Scan calls f.
func (f ScannerFunc) Scan(root string) ([]library.Movie, error) { return f(root) }

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
	Scanned   int `json:"scanned"`
	Added     int `json:"added"`
	Matched   int `json:"matched"`
	Unmatched int `json:"unmatched"`
}

// handleScan walks the library, upserts every folder, then tries to match
// whatever still has no tmdb_id.
//
// Synchronous by design: 29 folders and a handful of TMDB calls. A background job
// would be more code and less honest about when the work has actually finished.
func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	found, err := s.scanner.Scan(s.libraryRoot)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, fmt.Errorf("scan library: %w", err))
		return
	}

	out := scanResponse{Scanned: len(found)}
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
			"scanned", out.Scanned, "added", out.Added, "unmatched", out.Unmatched)
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
		"matched", out.Matched, "unmatched", out.Unmatched)
	s.respond(w, http.StatusOK, out)
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
	s.respond(w, http.StatusOK, movie)
}

func (s *Server) respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already sent, so this cannot become an error
		// response; log it rather than pretending the write succeeded.
		s.log.Error("write response", "err", err)
	}
}

// fail writes {"error": "..."} with a real status code. minter once returned 200
// carrying a failure, which made every failure look like success; not here.
func (s *Server) fail(w http.ResponseWriter, status int, err error) {
	if status >= http.StatusInternalServerError {
		s.log.Error("request failed", "status", status, "err", err)
	}
	s.respond(w, status, map[string]string{"error": err.Error()})
}
