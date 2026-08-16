package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/tmdb"
)

// RegisterMovieMatch mounts the one route that writes a tmdb_id a human chose.
//
// It is registered separately from Register for the reason RegisterMovieDelete
// is: phase 1's route set keeps its shape, and a deployment could omit it.
func (s *Server) RegisterMovieMatch(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/movies/{id}/match", s.handleMatchMovie)
}

// matchRequest is the whole body. Only the id: everything else about the film is
// read from TMDB rather than accepted from the caller (see handleMatchMovie).
type matchRequest struct {
	TMDBID int64 `json:"tmdb_id"`
}

// handleMatchMovie points a library row at the film a human picked.
//
// **It lives under /api/movies/ although it needs the TMDB key**, which is worth
// stating because browse.go's prefix rule says everything TMDB-backed lives under
// /api/tmdb/. The subject of this request is a library row — it is addressed by
// curator's own movies.id, it writes to the library, and it answers a library
// row. TMDB is an input to it, not what it is about. What the prefix rule
// actually guarantees is that a keyless install gets a 503 naming the variable
// instead of a confusing failure, and this route keeps that by calling failTMDB
// with the same error the /api/tmdb/ handlers use.
//
// The catalogue is re-read rather than trusted. The body carries an id and
// nothing else, and Browser.Movie turns it into the overview and poster the scan
// would have written — so a row matched by hand and a row matched by the scanner
// are indistinguishable afterwards, and an id for a film TMDB does not have is a
// 404 instead of a row pointing at nothing.
func (s *Server) handleMatchMovie(w http.ResponseWriter, r *http.Request) {
	if s.browser == nil {
		s.failTMDB(w, errTMDBUnconfigured)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, http.StatusBadRequest, fmt.Errorf("id %q: not a number", r.PathValue("id")))
		return
	}

	var body matchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.fail(w, http.StatusBadRequest, fmt.Errorf("body: %w", err))
		return
	}
	if body.TMDBID <= 0 {
		// Zero is what a missing key decodes to, so this covers both the absent
		// field and a nonsense one. D6 keeps "unmatched" and "matched to 0"
		// distinguishable in the database and this is the same distinction
		// arriving over HTTP.
		s.fail(w, http.StatusBadRequest, errors.New("tmdb_id is required and must be a positive number"))
		return
	}

	// The row is read first so a request naming a row that does not exist, or one
	// that is already matched, does not spend a TMDB request finding that out.
	movie, err := s.store.GetMovie(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.fail(w, http.StatusNotFound, fmt.Errorf("no movie with id %d", id))
			return
		}
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	if movie.TMDBID != nil {
		s.failMatch(w, store.ErrAlreadyMatched)
		return
	}

	details, err := s.browser.Movie(r.Context(), int(body.TMDBID))
	if err != nil {
		s.failTMDB(w, err)
		return
	}

	// Title stays nil on purpose, exactly as the scan's match does: TMDB's
	// canonical title would undo the " - " substitution that identifies the
	// folder (D9), and library_path is the row's identity anyway.
	//
	// The year is not written either, and that one was tried and reverted: the
	// scan rewrites `year` from the folder on every pass, so a row matched to a
	// film of another year answers a Jellyfin deep link until the next scan and a
	// search afterwards. store.TMDBMatch carries the measurement. The cost is that
	// a folder whose year is wrong keeps D32's search fallback rather than gaining
	// a deep link — the poster, the overview and the catalogue page are fixed
	// either way.
	match := store.TMDBMatch{TMDBID: body.TMDBID}
	if details.Overview != "" {
		match.Overview = &details.Overview
	}
	if details.PosterPath != "" {
		match.PosterPath = &details.PosterPath
	}

	matched, err := s.store.MatchMovie(r.Context(), id, match)
	if err != nil {
		s.failMatch(w, err)
		return
	}

	s.log.Info("matched by hand", "movie_id", id, "tmdb_id", body.TMDBID, "title", matched.Title)

	// The same body GET /api/movies/{id} answers, so the page can swap the row it
	// is holding without a reload — and jellyfin_url really does change, because
	// jellyfinLinkFor stops taking the nil-tmdbID search branch and starts looking
	// the film up for a deep link.
	out := movieBody{Movie: matched}
	out.JellyfinURL = s.jellyfinLinkFor(
		r.Context(), matched.TMDBID, matched.Year, matched.Title, matched.Status == store.StatusImported)
	s.respond(w, http.StatusOK, out)
}

// failMatch maps the store's two refusals onto 409, and everything else onto 500.
//
// Both are 409 rather than 400 for the reason the delete handler's ErrWrongCategory
// is: the request is well-formed and the refusal is deliberate. They stay two
// distinct messages because the remedies differ — one says this is not the row to
// correct, the other says the film is already in the library somewhere else.
func (s *Server) failMatch(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.fail(w, http.StatusNotFound, err)
	case errors.Is(err, store.ErrAlreadyMatched):
		s.fail(w, http.StatusConflict,
			errors.New("this film is already matched to a TMDB film, so there is nothing to correct here"))
	case errors.Is(err, store.ErrTMDBIDTaken):
		s.fail(w, http.StatusConflict,
			errors.New("curator already has that film in the library under another folder"))
	case errors.Is(err, tmdb.ErrNotFound):
		s.fail(w, http.StatusNotFound, err)
	default:
		s.fail(w, http.StatusInternalServerError, err)
	}
}
