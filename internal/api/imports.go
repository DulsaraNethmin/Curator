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
		s.fail(w, http.StatusConflict, errors.New(notCompletedSentence(err)))
	case errors.Is(err, library.ErrNoVideo):
		// 422, not 500: curator did nothing wrong, and retrying unchanged cannot
		// help. The download row stays `completed` either way — a torrent that
		// holds no film has not failed, it just is not importable.
		//
		// The path the chain names is on curator's download disk and is not the
		// reader's to act on, so the sentence names the size rule instead: the
		// usual cause is a folder holding only a sample.
		s.fail(w, http.StatusUnprocessableEntity, errors.New(
			"that download holds no video file big enough to be the feature, so there is nothing to import"))
	case errors.Is(err, library.ErrBadTitle):
		// Its own case and its own sentence. Sharing one with ErrNoVideo is what
		// D39 found: two situations arriving at one banner under one wrong
		// sentence, and the status is 422 for both, so the split has to be here.
		s.fail(w, http.StatusUnprocessableEntity, errors.New(badTitleSentence(err)))
	case errors.Is(err, download.ErrClient):
		s.fail(w, http.StatusBadGateway, err)
	default:
		s.fail(w, http.StatusInternalServerError, err)
	}
}

// notCompletedSentence says the import was declined rather than that it broke,
// and names the state when the error carried it — "stalled" and "downloading"
// are different things to be told to wait for.
func notCompletedSentence(err error) string {
	var pending download.NotCompleted
	if errors.As(err, &pending) {
		return fmt.Sprintf(
			"this download has not finished yet — the torrent client reports it as %q — so there is nothing to import",
			pending.State)
	}
	return "this download has not finished yet, so there is nothing to import"
}

// badTitleSentence names the title and why it was refused. Both come off the
// error, and both are already written: library.BadTitle carries the six reasons
// that used to be format strings.
func badTitleSentence(err error) string {
	var bad library.BadTitle
	if errors.As(err, &bad) && bad.Title != "" {
		return fmt.Sprintf(
			"curator has nowhere to put this film: %q cannot be a folder name, because %s",
			bad.Title, bad.Reason)
	}
	return "curator has nowhere to put this film: its title cannot be a folder name"
}
