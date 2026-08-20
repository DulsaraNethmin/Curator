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

	// DeleteMovie removes a film from the library and the disk (D19). It is the
	// only destructive thing on this interface, and the only one in the API.
	DeleteMovie(ctx context.Context, id int64) (download.Deletion, error)
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

	if s.alreadyHave(w, r, body.TMDBID) {
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

// alreadyHave refuses a dispatch for a film that is already in the library, and
// writes the refusal itself. It reports whether it answered the request.
//
// It runs BEFORE the dispatcher, so a refusal costs nothing and leaves nothing
// behind: no indexer is asked to resolve the release id and no torrent is added.
//
// **The gate is `imported`, and deliberately nothing else.** A film mid-download
// is not "already downloaded", and leaving that path open is right on its own
// merits — when a torrent stalls, searching for and dispatching a DIFFERENT
// release is precisely what you want to do. A non-nil library_path is required
// alongside the status so that a half-written row refuses nothing rather than
// refusing wrongly.
//
// LibraryByTMDBID rather than a targeted lookup, and that is the point: it is the
// same query behind "already in your library" on a poster, so the badge and the
// server's refusal cannot drift apart. It costs one query with no placeholders on
// a request that is about to launch a torrent.
//
// **The hole is deliberate and worth naming.** tmdb_id is optional on this body:
// /search's release-name mode dispatches without one, so nothing here can fire
// for it. With no TMDB id there is no reliable identity for a film, and matching
// on title and year would be TMDB matching done in the wrong place — the same
// line UpsertWantedMovie already draws.
func (s *Server) alreadyHave(w http.ResponseWriter, r *http.Request, tmdbID *int64) bool {
	if tmdbID == nil {
		return false
	}

	library, err := s.store.LibraryByTMDBID(r.Context(), store.MediaTypeMovie)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return true
	}

	state, ok := library[*tmdbID]
	if !ok || state.Status != store.StatusImported || state.LibraryPath == nil {
		return false
	}

	// A request that deliberately did nothing has to say so somewhere a human
	// looks, and /api/logs is a screen. Info, not Warn: nothing is wrong.
	s.log.Info("refused a download: curator already has this film",
		"tmdb_id", *tmdbID, "movie_id", state.MovieID, "library_path", *state.LibraryPath)
	// 409 for the reason failDelete's ErrWrongCategory is one: the request is
	// well-formed, curator is working, and the refusal is deliberate.
	s.fail(w, http.StatusConflict, fmt.Errorf(
		"curator already has this film, at %s — delete it first to replace it", *state.LibraryPath))
	return true
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
		// Arrives bare from the service and its own text names the two variables
		// that are the whole remedy, so there is no chain here to strip.
		s.fail(w, http.StatusServiceUnavailable, err)
	case errors.Is(err, download.ErrUnprotected):
		// Unprotected is 503 for the same reason unconfigured is: the request
		// was fine, curator is fine, and something about the deployment means
		// this cannot be served right now.
		//
		// Its own case since T72. It shared unconfigured's arm while both simply
		// passed `err` through, and that hid the difference: unconfigured
		// arrives bare, and this one arrived behind `dispatch <releaseID>: ` and
		// a second sentinel in front of the sentence worth reading.
		s.failCause(w, http.StatusServiceUnavailable, unprotectedSentence(err), err)
	case errors.Is(err, download.ErrClient):
		// Refused before the release was resolved or anything was written, so
		// the sentence can promise that nothing was started.
		s.failCause(w, http.StatusBadGateway,
			"curator could not reach the torrent client, so this release was not started — nothing was added and nothing was recorded",
			err)
	default:
		s.fail(w, http.StatusInternalServerError, err)
	}
}

// unprotectedSentence leads with the refusal and lets internal/vpn finish it.
//
// The guard's own sentences are good — they name VPN_CONFIG, or VPN_REQUIRED,
// or the exit address that matched curator's own — so this adds only the thing
// they leave out, which is that nothing was started.
func unprotectedSentence(err error) string {
	var unprotected download.Unprotected
	if errors.As(err, &unprotected) && unprotected.Reason != "" {
		return "curator did not start this download, because it would not have been protected: " + unprotected.Reason
	}
	return "curator did not start this download, because it would not have been protected by a VPN"
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
