package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/DulsaraNethmin/curator/internal/download"
	"github.com/DulsaraNethmin/curator/internal/library"
	"github.com/DulsaraNethmin/curator/internal/store"
)

// handleImport runs an import now, without waiting for the next poll tick.
//
// It exists for the case a tick cannot serve: an import that failed for a reason
// since fixed — an unmounted library root, a full disk — where waiting up to
// DOWNLOAD_POLL_INTERVAL to find out whether the fix worked is worse than
// asking. It is the identical code path; there is deliberately no second
// importer and no second set of rules.
//
// The hash is the whole request. content_path is qBittorrent's to report and is
// fetched fresh, because a client naming a path curator will hardlink from is
// the same class of mistake D10 refused when it kept detail page URLs
// server-side.
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	hash := strings.TrimSpace(r.PathValue("hash"))
	if hash == "" {
		s.fail(w, http.StatusBadRequest, errors.New("hash is required"))
		return
	}

	movie, err := s.dispatcher.Import(r.Context(), hash)
	if err != nil {
		s.failImport(w, hash, err)
		return
	}
	s.respond(w, http.StatusOK, movie)
}

// failImport maps an import failure onto a status code that says whose problem
// it is and whether trying again could help. The ordering of these cases is not
// arbitrary: the specific sentinels come before ErrClient, because an import
// error can legitimately carry more than one.
func (s *Server) failImport(w http.ResponseWriter, hash string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		// No download with that hash. Not a 502: qBittorrent was never asked.
		s.fail(w, http.StatusNotFound, fmt.Errorf("no download with hash %s", hash))
	case errors.Is(err, download.ErrUnconfigured):
		s.fail(w, http.StatusServiceUnavailable, err)
	case errors.Is(err, download.ErrNotCompleted):
		// The request is well-formed and will be fine later, which is exactly
		// what 409 means and what 500 would not.
		s.fail(w, http.StatusConflict, err)
	case errors.Is(err, library.ErrNoVideo), errors.Is(err, library.ErrBadTitle):
		// 422, not 500: curator did nothing wrong, and retrying unchanged cannot
		// help. The download row stays `completed` either way — a torrent that
		// holds no film has not failed, it just is not importable.
		s.fail(w, http.StatusUnprocessableEntity, err)
	case errors.Is(err, download.ErrClient):
		s.fail(w, http.StatusBadGateway, err)
	default:
		s.fail(w, http.StatusInternalServerError, err)
	}
}
