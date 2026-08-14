package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DulsaraNethmin/curator/internal/library"
	"github.com/DulsaraNethmin/curator/internal/store"
)

// The playback endpoints: hand a film in the library to a player.
//
// There is no logic here to isolate in a package of its own — the whole of it
// is a lookup, a containment check, a file open and http.ServeContent, which
// already does ranges, 206, 416, HEAD and conditional requests correctly. What
// this file owns that is not free is three things: which file in the folder is
// the film, what Content-Type to call it, and the URL to hand something that
// cannot carry a cookie.

// ticketLifetime is how long a minted playback URL works for.
//
// Twelve hours: comfortably longer than a film plus an interruption, and far
// shorter than the thirty days a session cookie gets. A ticket is a bearer
// credential in a URL — it lands in shell history, in VLC's recent-files list
// and in any proxy's access log — so it gets the shortest life that still does
// its job (docs/decisions.md D31).
const ticketLifetime = 12 * time.Hour

// videoContentTypes is curator's own MIME table, and its existence is the
// point: this package must NEVER call mime.TypeByExtension and must never let
// http.ServeContent sniff.
//
// Measured, and the reason this is not the obvious one-liner. Go's builtin MIME
// table (mime/type.go:53-70) contains no video extension at all. On a Mac
// mime.TypeByExtension(".mkv") answers "video/x-matroska", and only because Go
// read /etc/apache2/mime.types — one of the four files mime/type_unix.go looks
// in. Phase 9's image is FROM scratch, where none of the four exists, every
// answer becomes "" and ServeContent falls back to sniffing the first 512
// bytes. Sniffing an MKV gives "video/webm" — also measured — because Matroska
// and WebM share the EBML magic. So the obvious version works perfectly on the
// laptop it was written on and mislabels every file in the shipped image.
//
// These four are exactly library's videoExtensions, which is what FindFeature
// will return, so the table is total over its own input.
var videoContentTypes = map[string]string{
	".mkv": "video/x-matroska",
	".mp4": "video/mp4",
	".m4v": "video/x-m4v",
	".avi": "video/x-msvideo",
}

// Tickets mints a bearer credential for one URL path. Nil when authentication
// is off, which is the default and is why it is optional.
type Tickets interface {
	Ticket(path string, ttl time.Duration) (value string, expires time.Time, ok bool)
}

// WithTickets attaches the minter. *Auth satisfies it.
func (s *Server) WithTickets(t Tickets) *Server {
	s.tickets = t
	return s
}

// RegisterStream mounts phase 8's routes.
//
// Separate from Register, beside RegisterMovieDelete and for the same reason:
// phase 1's route set keeps its shape, and a deployment could omit playback
// entirely.
func (s *Server) RegisterStream(mux *http.ServeMux) {
	// GET also matches HEAD in Go 1.22 routing, which matters here rather than
	// being a detail: players probe with HEAD for the length before they ask
	// for a byte of it.
	mux.HandleFunc("GET /api/movies/{id}/stream", s.handleStream)
	mux.HandleFunc("POST /api/movies/{id}/playback", s.handlePlayback)
}

// streamPath is the URL a movie's bytes are at. One function, because it is
// both what playback hands out and what a ticket is signed for, and the two
// disagreeing would mint credentials that never work.
func streamPath(id int64) string {
	return "/api/movies/" + strconv.FormatInt(id, 10) + "/stream"
}

// handleStream serves the film itself.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.libraryFolder(w, r)
	if !ok {
		return
	}

	// library_path is a FOLDER, not a file: the importer stores the directory
	// because it is the scanner's identity key, and a row holding the .mkv path
	// is a row no future scan would match (importer.go, MarkImported). This is
	// the same picker the importer used to put the file there, which brings its
	// 50 MiB floor and its sample/extras/subs skip — so a folder with a 6 MB
	// sample.mkv beside the film cannot stream the sample. Two answers to
	// "which file is the film" would disagree exactly on the folders where it
	// matters.
	//
	// No cache. It is one ReadDir of a folder with two or three entries, and a
	// cached answer would go stale the moment a file is replaced.
	feature, err := library.FindFeature(folder, library.FeatureOpts{})
	if err != nil {
		// A missing folder, an unreadable one and one with no film in it are
		// all the same answer to the browser and all worth a line in the log:
		// the film is in the database and not on the disk, which is the
		// operator's problem and not this request's.
		s.log.Warn("stream: no feature file", "library_path", folder, "err", err)
		s.fail(w, http.StatusNotFound, errors.New("this film has no playable file in the library"))
		return
	}
	if feature.Others > 0 {
		s.passedOver(folder, feature)
	}

	file, err := os.Open(feature.Path)
	if err != nil {
		if os.IsNotExist(err) {
			// It was there for FindFeature and gone by the open. A race with a
			// delete, not a server fault.
			s.fail(w, http.StatusNotFound, errors.New("this film has no playable file in the library"))
			return
		}
		s.fail(w, http.StatusInternalServerError, fmt.Errorf("open feature: %w", err))
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		s.fail(w, http.StatusInternalServerError, fmt.Errorf("stat feature: %w", err))
		return
	}

	// Set before ServeContent, which only picks a type when the header is
	// absent. This is the one line the trap above is about.
	w.Header().Set("Content-Type", contentType(feature.Path))

	// Everything a player needs and none of it written here: byte ranges, 206,
	// a 416 carrying Content-Range: bytes */size, multipart ranges, HEAD, and
	// If-Modified-Since off the ModTime. The same call internal/web makes for
	// the embedded UI. A hand-rolled version gets the parts no test here would
	// have covered wrong.
	http.ServeContent(w, r, filepath.Base(feature.Path), info.ModTime(), file)
}

// playbackBody answers the UI's one question — how do I play this film — in one
// round trip.
type playbackBody struct {
	// StreamURL is relative and never carries a ticket. It is what the page's
	// own <video> uses, and a <video src> is a same-origin subresource that
	// sends the session cookie without being asked. A credential in a URL that
	// did not need one ends up in the DOM, in error messages and in whatever
	// gets pasted into a bug report.
	StreamURL string `json:"stream_url"`

	// ExternalURL is absolute, because VLC needs a host, and carries a ticket
	// when there is a password to carry. It is built from the request's own
	// Host: the browser asking is on the network that will play it.
	ExternalURL string `json:"external_url"`

	// ExpiresAt is absent with authentication off, because nothing was minted.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	// remux_url is T44's field and is absent until it exists.
}

// handlePlayback hands back the URLs this film can be played with, and mints a
// ticket only if there is a password for one to stand in for.
func (s *Server) handlePlayback(w http.ResponseWriter, r *http.Request) {
	movie, _, ok := s.libraryFolder(w, r)
	if !ok {
		return
	}

	path := streamPath(movie.ID)
	body := playbackBody{StreamURL: path, ExternalURL: s.absolute(r, path)}

	// With authentication off — the default — nothing is minted and the UI
	// still has one code path (docs/decisions.md D31).
	if s.tickets != nil {
		if value, expires, minted := s.tickets.Ticket(path, ticketLifetime); minted {
			body.ExternalURL += "?" + url.Values{ticketParam: {value}}.Encode()
			body.ExpiresAt = &expires
		}
	}

	s.respond(w, http.StatusOK, body)
}

// absolute turns a path into something that can be pasted into a player.
//
// The scheme comes from whether this connection is TLS and never from
// X-Forwarded-Proto, for the reason auth.go gives for not trusting
// X-Forwarded-For: the header is written by whoever is being served. curator
// has no TLS of its own (D25 says so where the password is set), so this is
// http on every install that exists today and https only behind a proxy that
// terminated it and spoke to curator over one.
func (s *Server) absolute(r *http.Request, path string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + path
}

// libraryFolder resolves the id in the URL to a folder inside the library, and
// writes the failure itself when there isn't one.
//
// Both endpoints ask the same four questions in the same order, so they have
// one implementation: a playback response that handed out a URL the stream
// would 404 is worse than a 404 from playback. Neither touches the disk here —
// that is the stream's own step, and doing it twice would be a race as well as
// a second ReadDir.
func (s *Server) libraryFolder(w http.ResponseWriter, r *http.Request) (store.Movie, string, bool) {
	// The route takes curator's movies.id, NOT the TMDB id the movie screen's
	// URL carries (docs/decisions.md D21). The detail body already carries
	// library.movie_id beside it, for exactly the films this can serve.
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, http.StatusBadRequest, errors.New("id must be a whole number"))
		return store.Movie{}, "", false
	}

	movie, err := s.store.GetMovie(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.fail(w, http.StatusNotFound, fmt.Errorf("no movie with id %d", id))
			return store.Movie{}, "", false
		}
		s.fail(w, http.StatusInternalServerError, err)
		return store.Movie{}, "", false
	}

	// A wanted film that has never been imported. Only imported rows have a
	// library_path, which is also why this endpoint can never read a partial
	// download.
	if movie.LibraryPath == nil || strings.TrimSpace(*movie.LibraryPath) == "" {
		s.fail(w, http.StatusNotFound, fmt.Errorf("movie %d is not in the library", id))
		return store.Movie{}, "", false
	}
	folder := *movie.LibraryPath

	// What this catches is a ROW pointing outside the library, not a traversal:
	// the id above is a strconv.ParseInt, so nothing traverses in from the
	// request. A row written when LIBRARY_MOVIES pointed elsewhere, a database
	// restored beside a different library, a row edited by hand. 404 to the
	// caller because it is not their problem, Warn with both paths because it
	// is the operator's and the log is where it gets diagnosed.
	if err := library.AssertInside(s.libraryRoot, folder); err != nil {
		s.log.Warn("stream: library_path is outside the library root; refusing to serve it",
			"movie_id", id, "library_path", folder, "library_root", s.libraryRoot)
		s.fail(w, http.StatusNotFound, fmt.Errorf("movie %d is not in the library", id))
		return store.Movie{}, "", false
	}

	return movie, folder, true
}

// contentType is curator's answer and never the standard library's.
func contentType(path string) string {
	if known, ok := videoContentTypes[strings.ToLower(filepath.Ext(path))]; ok {
		return known
	}
	// Unreachable while FindFeature only returns the four extensions this table
	// holds. If that ever changes, an honest "some bytes" is better than a
	// confident wrong label: the browser refuses it and fires the error event,
	// which is the codec authority this phase already defers to.
	return "application/octet-stream"
}

// passedOver says once that a folder holds more than one film.
//
// The importer logs its equivalent once per import. This one would log once per
// REQUEST, and a browser seeking in a film issues a new range request every
// time — which would push every real line out of the tail /api/logs serves
// (docs/decisions.md D18 made that buffer a product surface). Once per folder
// per process is the same information and none of the flood.
func (s *Server) passedOver(folder string, feature library.Feature) {
	if _, seen := s.streamWarned.LoadOrStore(folder, struct{}{}); seen {
		return
	}
	s.log.Warn("stream: more than one film in this folder; serving the largest",
		"library_path", folder, "serving", feature.Path, "others", feature.Others)
}
