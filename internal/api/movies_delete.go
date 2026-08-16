package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/DulsaraNethmin/curator/internal/download"
	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// RegisterMovieDelete mounts the one destructive route curator has.
//
// It is registered separately from Register so that phase 1's route set keeps
// its shape, and so that a deployment could omit it entirely.
func (s *Server) RegisterMovieDelete(mux *http.ServeMux) {
	mux.HandleFunc("DELETE /api/movies/{id}", s.handleDeleteMovie)
}

// handleDeleteMovie removes a film from the library and from the disk.
//
// This is the first request curator has ever had that destroys something, and
// there is no authentication in front of it — the same posture as the rest, and
// as the *arr stack it replaces (docs/decisions.md D19). The UI is what says
// precisely what will go and how much disk it frees, before it asks for this.
func (s *Server) handleDeleteMovie(w http.ResponseWriter, r *http.Request) {
	if s.dispatcher == nil {
		s.fail(w, http.StatusServiceUnavailable, errors.New("deleting is not configured"))
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, http.StatusBadRequest, errors.New("id must be a whole number"))
		return
	}

	report, err := s.dispatcher.DeleteMovie(r.Context(), id)
	if err != nil {
		s.failDelete(w, id, err)
		return
	}
	s.respond(w, http.StatusOK, report)
}

func (s *Server) failDelete(w http.ResponseWriter, id int64, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.fail(w, http.StatusNotFound, errors.New("no movie with id "+strconv.FormatInt(id, 10)))
	case errors.Is(err, torrent.ErrWrongCategory):
		// The guard fired: something asked curator to delete a torrent that is
		// not ours. 409, because the request is well-formed and the refusal is
		// deliberate — the *arr stack shares that qBittorrent until the cutover.
		//
		// The sentence is written here rather than passed through, because
		// `err` is three packages of prefixes ending in the info hash twice in
		// two different cases. The category is the only word a reader can act
		// on, so it is recovered — and the sentence is true without it, which
		// is what a wrapped sentinel with no WrongCategory in it gets.
		s.fail(w, http.StatusConflict, errors.New(wrongCategorySentence(err)))
	case errors.Is(err, download.ErrUnconfigured):
		s.fail(w, http.StatusServiceUnavailable, err)
	case errors.Is(err, download.ErrClient):
		// The torrent could not be removed, so nothing else was touched. The
		// film is still in the library and still on disk — and saying so is the
		// whole sentence, because a failed delete that does not say what
		// survived reads as a half-finished one.
		//
		// Which client, and which of its endpoints, is `qbit torrents/delete: `
		// or `engine: ` and belongs in the log: the answer used to depend on
		// TORRENT_BACKEND, which is the defect torrent.WrongCategory closed for
		// the 409 one line up and this closes for the 502.
		s.failCause(w, http.StatusBadGateway,
			"curator could not reach the torrent client, so nothing was deleted — the film is still in your library and the download is still there",
			err)
	default:
		s.fail(w, http.StatusInternalServerError, err)
	}
}

// wrongCategorySentence says what was refused and what it means for the film,
// naming the owning category when the error carried it.
//
// Nothing was deleted when this fires — not the torrent, not the row, not the
// files — and saying so is the point: the guard is silent about consequences and
// a bare "not in the required category" reads like a partial delete.
func wrongCategorySentence(err error) string {
	var wrong torrent.WrongCategory
	if errors.As(err, &wrong) {
		return fmt.Sprintf(
			"that torrent is in the %q category, not curator's %q — removing it belongs to whatever put it there, so curator deleted nothing and the film is still in your library",
			wrong.Actual, wrong.Required)
	}
	return "that torrent belongs to another application — removing it belongs to whatever put it there, so curator deleted nothing and the film is still in your library"
}
