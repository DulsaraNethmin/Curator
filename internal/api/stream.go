package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DulsaraNethmin/curator/internal/library"
	"github.com/DulsaraNethmin/curator/internal/remux"
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

// startParam is the remux's seek offset, in seconds.
//
// It is `start` and not the obvious `t` because the ticket already owns that
// letter; the first draft of both had it (docs/phase-8.md, "?t= was going to
// mean two things"). It is a parameter rather than a Range header because a
// pipe has no byte-range semantics — you cannot answer bytes=500000000- without
// having produced the first 500 MB — so seeking outside the buffer is the
// player re-pointing <video src> at a new offset, which is T45's half of this.
const startParam = "start"

// remuxRetryAfter is what a refused remux suggests waiting.
//
// A hint and not a promise: a slot frees when somebody stops watching a film,
// and nothing here can predict that. Thirty seconds is short enough to be worth
// obeying and long enough not to be a retry loop.
const remuxRetryAfter = 30 * time.Second

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

// WithRemux attaches the ffmpeg fallback. A nil *remux.Remuxer is the ordinary
// state of an install with no ffmpeg on it: the route answers 404 and
// POST .../playback omits remux_url entirely, rather than handing out a URL
// that would always fail.
//
// The concrete type rather than an interface, unlike Store and Matcher above.
// There is nothing here worth faking — a fake ffmpeg on disk is what the tests
// use, because the invariants this depends on are the subprocess's and a stub
// would assert that a stub works.
func (s *Server) WithRemux(r *remux.Remuxer) *Server {
	s.remux = r
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
	mux.HandleFunc("GET /api/movies/{id}/remux", s.handleRemux)
	mux.HandleFunc("POST /api/movies/{id}/playback", s.handlePlayback)
}

// streamPath is the URL a movie's bytes are at. One function, because it is
// both what playback hands out and what a ticket is signed for, and the two
// disagreeing would mint credentials that never work.
func streamPath(id int64) string {
	return "/api/movies/" + strconv.FormatInt(id, 10) + "/stream"
}

// remuxPath is the same film through ffmpeg. A separate path and not a
// parameter on the one above, because the two responses have nothing in common:
// one has a length, ranges and a 206, and the other has none of the three.
func remuxPath(id int64) string {
	return "/api/movies/" + strconv.FormatInt(id, 10) + "/remux"
}

// handleStream serves the film itself.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	feature, ok := s.featureFile(w, r)
	if !ok {
		return
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

// featureFile resolves the request to the film's file on disk, and writes the
// failure itself when there isn't one.
//
// Both the stream and the remux ask this, and they ask it identically: two
// answers to "which file is the film" would disagree exactly on the folders
// where it matters, which is the whole reason the picker is shared with the
// importer rather than reimplemented.
func (s *Server) featureFile(w http.ResponseWriter, r *http.Request) (library.Feature, bool) {
	_, folder, ok := s.libraryFolder(w, r)
	if !ok {
		return library.Feature{}, false
	}

	// library_path is a FOLDER, not a file: the importer stores the directory
	// because it is the scanner's identity key, and a row holding the .mkv path
	// is a row no future scan would match (importer.go, MarkImported). This is
	// the same picker the importer used to put the file there, which brings its
	// 50 MiB floor and its sample/extras/subs skip — so a folder with a 6 MB
	// sample.mkv beside the film cannot stream the sample.
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
		return library.Feature{}, false
	}
	if feature.Others > 0 {
		s.passedOver(folder, feature)
	}
	return feature, true
}

// handleRemux serves the same film through ffmpeg, with its streams copied and
// nothing re-encoded.
//
// **This is not ServeContent and must not pretend to be.** The response is a
// subprocess writing into a pipe: no length, no byte ranges, 200 and never 206,
// and Accept-Ranges: none set explicitly so a player stops asking for something
// nothing here can answer. Seeking is ?start=<seconds>, which re-runs ffmpeg
// with -ss (docs/tasks/T44-remux.md, and docs/phase-8.md, "The remux endpoint is
// not the stream endpoint, and the difference is seeking").
func (s *Server) handleRemux(w http.ResponseWriter, r *http.Request) {
	if s.remux == nil {
		// No ffmpeg on this install. A 404 and not a 503, because there is
		// nothing here to come back for — and POST .../playback omits the URL
		// entirely, so nothing curator hands out ever leads here.
		s.fail(w, http.StatusNotFound, errors.New("this install has no ffmpeg: playback is direct play only"))
		return
	}

	feature, ok := s.featureFile(w, r)
	if !ok {
		return
	}

	start, err := seekOffset(r.URL.Query().Get(startParam))
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}

	// A HEAD is answered without starting anything. Players probe with one, and
	// the player in T45 probes specifically to tell a 401 from a codec failure
	// — an error event says neither. Spawning an ffmpeg to answer a question
	// about headers would burn one of three slots on a probe.
	if r.Method == http.MethodHead {
		setRemuxHeaders(w)
		w.WriteHeader(http.StatusOK)
		return
	}

	out := &remuxWriter{w: w}
	written, err := s.remux.Stream(r.Context(), out, feature.Path, start)

	switch {
	case err == nil:
	case errors.Is(err, remux.ErrBusy):
		// Refused, never queued: queueing would turn "the film is slow to
		// start" into "the film never starts". Nothing has been written yet, so
		// this can still be a status code.
		//
		// respond and a Warn of its own rather than fail, which logs anything
		// past 500 at ERROR. This is a DESIGNED refusal and not a server fault:
		// it took four people watching at once, it is worth exactly one line so
		// that "it told me to try again" is diagnosable, and an ERROR here would
		// put a red line in the tail /api/logs serves for a cap doing its job
		// (docs/decisions.md D18). Measured: this was an ERROR line first, and
		// it looked like a bug in the log before it looked like a cap.
		w.Header().Set("Retry-After", strconv.Itoa(int(remuxRetryAfter.Seconds())))
		s.log.Warn("remux: every slot is in use, so this one was refused rather than queued",
			"path", feature.Path, "concurrent", remux.DefaultConcurrent)
		s.respond(w, http.StatusServiceUnavailable, map[string]string{
			"error": "every remux is already in use; direct play still works, or try again shortly",
		})
	case errors.Is(err, context.Canceled):
		// A closed tab, which is the ordinary way one of these ends. Logging it
		// would fill the tail /api/logs serves with people finishing films.
	case written > 0:
		// **A failure mid-stream cannot become an error page.** The headers went
		// out with the first byte. Log it, end the response, and let the
		// browser's error event do what it already does — which is exactly the
		// next step of the fallback chain.
		s.log.Warn("remux: ffmpeg failed after the response had started", "path", feature.Path, "err", err)
	default:
		// It failed before a byte came out, so this one can still be a status.
		// The message to the caller is deliberately not ffmpeg's: the error
		// carries the captured stderr, which names a path on the server's disk.
		s.log.Warn("remux: ffmpeg failed", "path", feature.Path, "err", err)
		s.fail(w, http.StatusBadGateway, errors.New("this film could not be remuxed"))
	}
}

// remuxWriter is the response, and it commits the headers on the FIRST BYTE
// rather than up front.
//
// That is the whole of what makes the handler above able to answer 503 or 502
// with a real status: nothing is sent until ffmpeg has actually produced
// something, so a process that dies on startup is still a failure the caller
// can be told about properly, and one that dies at minute forty is not.
//
// It flushes every write. Without that, the first 4 KB of a fragmented MP4 sits
// in the server's buffer and the player waits for it — which looks exactly like
// a remux that does not work.
type remuxWriter struct {
	w       http.ResponseWriter
	written int64
}

// setRemuxHeaders describes a fragmented MP4 of unknown length.
//
// The Content-Type is curator's own, exactly as the stream endpoint's is, and
// for the same reason — except that here it is not even a lookup: the output
// container is -f mp4 because curator asked for it, whatever went in.
func setRemuxHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "video/mp4")
	// Explicit, and load-bearing. A pipe cannot answer bytes=500000000- without
	// having produced the first 500 MB, so a player has to be told to stop
	// asking; seeking is ?start= instead.
	h.Set("Accept-Ranges", "none")
	// It is generated on the fly from a subprocess and there is no length, no
	// ETag and no ModTime to validate it with. Nothing should keep it.
	h.Set("Cache-Control", "no-store")
}

func (rw *remuxWriter) Write(p []byte) (int, error) {
	if rw.written == 0 {
		setRemuxHeaders(rw.w)
		// 200 and never 206, with no Content-Length: Go sends this chunked,
		// which is what a stream of unknown length is.
		rw.w.WriteHeader(http.StatusOK)
	}
	n, err := rw.w.Write(p)
	rw.written += int64(n)
	if err == nil {
		// Best effort. A ResponseWriter that cannot flush would only mean the
		// film starts late, and there is nothing useful to do about it here.
		_ = http.NewResponseController(rw.w).Flush()
	}
	return n, err
}

// seekOffset reads ?start=<seconds>.
//
// Rubbish is a 400 rather than a flag pasted into an argv — though note that
// the parsed value is what internal/remux formats back into -ss, so this is the
// second guard and not the only one.
func seekOffset(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	seconds, err := strconv.ParseFloat(raw, 64)
	switch {
	case err != nil, math.IsNaN(seconds), math.IsInf(seconds, 0), seconds < 0:
		return 0, fmt.Errorf("start %q: want a number of seconds from the beginning of the film", raw)
	case seconds > math.MaxInt64/float64(time.Second):
		// A float too large for a Duration converts to an implementation-defined
		// value rather than to an error, so it is refused here instead.
		return 0, fmt.Errorf("start %q: further into the film than any film goes", raw)
	}
	return time.Duration(seconds * float64(time.Second)), nil
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

	// RemuxURL is where the same film arrives through ffmpeg, and it is ABSENT
	// when this install has no ffmpeg — omitted, not empty and not a URL that
	// answers 503. A URL that exists and never works is worse than one that
	// does not exist, and its absence is how the UI knows to say direct play
	// only rather than offering a fallback that cannot run.
	//
	// Relative and carrying no ticket, exactly like StreamURL and for the same
	// reason: this is what the page's own <video> re-points at when direct play
	// fires its error event, and that is a same-origin subresource with a
	// cookie. There is deliberately no external remux URL — the thing a ticket
	// is minted for is VLC, and VLC has never needed an MKV rewritten.
	RemuxURL string `json:"remux_url,omitempty"`

	// ExpiresAt is absent with authentication off, because nothing was minted.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
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
	if s.remux != nil {
		body.RemuxURL = remuxPath(movie.ID)
	}

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
