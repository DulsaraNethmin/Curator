package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/DulsaraNethmin/curator/internal/download"
	"github.com/DulsaraNethmin/curator/internal/indexer"
	"github.com/DulsaraNethmin/curator/internal/store"
)

// Dispatcher turns a picked release into a running torrent and reports what is
// running. Declared here rather than taken as a concrete type, like Store,
// Scanner, Matcher and Searcher, so the handlers are tested against a fake
// instead of a live qBittorrent.
type Dispatcher interface {
	Dispatch(ctx context.Context, req download.Request) (store.Download, error)
	Downloads(ctx context.Context) ([]store.Download, error)

	// Import is phase 4's, on this interface rather than a second one because it
	// is the same service, reached from the same handler set, and a second
	// interface over one implementation would only be more names.
	Import(ctx context.Context, hash string) (store.Movie, error)
}

// WithDownloads attaches release dispatch and returns the server.
func (s *Server) WithDownloads(d Dispatcher) *Server {
	s.dispatcher = d
	return s
}

// RegisterDownloads mounts phase 3's routes, and phase 4's manual import, which
// belongs here because it is a download's own verb rather than a movie's.
func (s *Server) RegisterDownloads(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/downloads", s.handleDispatch)
	mux.HandleFunc("GET /api/downloads", s.handleListDownloads)
	mux.HandleFunc("POST /api/downloads/{hash}/import", s.handleImport)
}

// dispatchRequest is the body of POST /api/downloads.
//
// Title and Year are required because a release id says nothing about which film
// it is for — the caller picked it out of a search it made and is the only party
// that knows. The release's own name and indexer are deliberately absent: the
// server holds those already, and accepting them would let a request describe
// itself.
type dispatchRequest struct {
	ReleaseID string `json:"release_id"`
	Title     string `json:"title"`
	Year      int    `json:"year"`
	TMDBID    *int64 `json:"tmdb_id"`
}

func (s *Server) handleDispatch(w http.ResponseWriter, r *http.Request) {
	var body dispatchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		// A malformed year lands here as a decode failure, which is the same
		// answer for the same reason: the request could not be understood.
		s.fail(w, http.StatusBadRequest, fmt.Errorf("request body: %w", err))
		return
	}
	if strings.TrimSpace(body.ReleaseID) == "" {
		s.fail(w, http.StatusBadRequest, errors.New("release_id is required"))
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		s.fail(w, http.StatusBadRequest, errors.New("title is required"))
		return
	}
	if body.Year < 0 {
		s.fail(w, http.StatusBadRequest, fmt.Errorf("year %d: not a year", body.Year))
		return
	}

	saved, err := s.dispatcher.Dispatch(r.Context(), download.Request{
		ReleaseID: strings.TrimSpace(body.ReleaseID),
		Title:     strings.TrimSpace(body.Title),
		Year:      body.Year,
		TMDBID:    body.TMDBID,
	})
	if err != nil {
		s.failDispatch(w, err)
		return
	}

	s.respond(w, http.StatusCreated, saved)
}

// failDispatch maps a dispatch failure onto a status code that says whose problem
// it is. Getting this wrong in either direction is the dishonesty this repo keeps
// legislating against: a 500 blames curator for qBittorrent's bad day, and a 200
// would hide the failure entirely.
func (s *Server) failDispatch(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, indexer.ErrReleaseExpired):
		// The id was almost certainly real; the search that issued it has aged out.
		s.fail(w, http.StatusGone, fmt.Errorf("%w — search again and pick from the new results", err))
	case errors.Is(err, download.ErrUnconfigured):
		s.fail(w, http.StatusServiceUnavailable, err)
	case errors.Is(err, download.ErrClient):
		s.fail(w, http.StatusBadGateway, err)
	default:
		s.fail(w, http.StatusInternalServerError, err)
	}
}

func (s *Server) handleListDownloads(w http.ResponseWriter, r *http.Request) {
	downloads, err := s.dispatcher.Downloads(r.Context())
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	if downloads == nil {
		downloads = []store.Download{} // nothing downloading is [], never null
	}
	s.respond(w, http.StatusOK, downloads)
}
