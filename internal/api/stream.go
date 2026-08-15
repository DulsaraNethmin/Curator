package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
// this file owns that is not free is four things: which file in the folder is
// the film, what Content-Type to call it, the URL to hand something that cannot
// carry a cookie, and — since T46 — the SubRip a `<track>` element will not take
// becoming the WebVTT it will, on the way out and never on the way in.

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
	mux.HandleFunc("GET /api/movies/{id}/subtitles/{name}", s.handleSubtitle)
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

// subtitlePath is where one of a film's sidecars is served from.
//
// The name is escaped because library names hold spaces and parentheses —
// "Interstellar (2014).en.srt" — and an unescaped space is not a URL. It is
// *decoded* again by the router before the handler compares it, so what is
// matched against the disk is the name and not its encoding.
func subtitlePath(id int64, name string) string {
	return "/api/movies/" + strconv.FormatInt(id, 10) + "/subtitles/" + url.PathEscape(name)
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

// --- subtitles ---------------------------------------------------------------

// trackFormats are the sidecar formats a <track> element can be given, and the
// map's value says whether one has to be converted on the way out.
//
// `.ass` and `.ssa` are deliberately absent. They are a STYLED format —
// positioning, fonts, colours, karaoke timing — and turning one into WebVTT
// means either throwing all of that away or reimplementing it, which is a
// subtitle renderer and not a route. They are still linked into the library, so
// Jellyfin and VLC both render them properly; the browser is simply not offered
// them. `.sub` is absent for a blunter reason: it is either MicroDVD, which is
// frame-numbered and needs the film's frame rate to convert at all, or the index
// half of a VobSub pair, which is a picture.
var trackFormats = map[string]bool{
	".srt": true,  // SubRip: converted below, because <track> takes WebVTT and nothing else
	".vtt": false, // already what the element wants
}

// maxSubtitleBytes is the most this endpoint will read into memory.
//
// A three-hour film's subtitles are about 100 KB, so this is forty times the
// largest real one. It exists because the file was named by a stranger and the
// extension is the only thing that says it is text: without a cap, a `.srt` that
// is actually a 12 GB video is an out-of-memory kill of the whole process on a
// Pi, and the conversion below has to hold the file to see one line ahead.
const maxSubtitleBytes = 4 << 20

// utf8BOM is what a Windows subtitle editor writes at the front of a file.
//
// **It is the one encoding detail that actually breaks players.** A WebVTT file
// must begin with the six bytes "WEBVTT"; a byte-order mark in front of them is
// permitted by the spec but a mark left in the MIDDLE of the output — which is
// what copying the SubRip through unchanged does, since curator writes the
// header itself — lands inside the first cue and shows up as a stray glyph on
// screen. It is stripped rather than tolerated.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// srtArrow separates a SubRip cue's two timestamps, and WebVTT's is spelled the
// same — which is the whole reason the conversion is as small as it is.
var srtArrow = []byte("-->")

// srtCue matches a SubRip timing line, tolerantly, so that it can be rewritten
// strictly.
//
// Tolerant on the way in because SubRip has no specification and files in the
// wild vary: `,` or `.` before the milliseconds, one or two digits of hour,
// missing spaces around the arrow. Strict on the way out because WebVTT does
// have one, and a browser that cannot parse a timing line silently drops the
// cue rather than reporting anything.
//
// The trailing group is WebVTT's cue settings, which SubRip files occasionally
// carry as the old X1/X2/Y1/Y2 coordinates. They are passed through: a setting
// a browser does not recognise is ignored, and dropping them would be a second
// thing to be wrong about.
var srtCue = regexp.MustCompile(`^\s*(\d{1,3}):([0-5]\d):([0-5]\d)[,.](\d{1,3})\s*-->\s*(\d{1,3}):([0-5]\d):([0-5]\d)[,.](\d{1,3})\s*(.*?)\s*$`)

// srtIndex matches the cue number SubRip puts in front of every timing line and
// WebVTT has no use for.
var srtIndex = regexp.MustCompile(`^\s*\d+\s*$`)

// subtitleTrack is one entry in the playback response's list.
type subtitleTrack struct {
	// Name is the file name in the library, and it is the last segment of URL.
	// It is here as well as in the URL because it is the thing the handler
	// matches against the disk, and a client that wants to say which track it
	// means says it with this.
	Name string `json:"name"`

	// Label is what a player draws in its track menu.
	Label string `json:"label"`

	// Language is an ISO 639-1 code for <track srclang>, or "" when the file
	// name carried nothing curator's table recognises. Empty rather than a
	// guess: srclang is what a browser uses to pick a default track, and a
	// wrong one is worse than none.
	Language string `json:"language"`

	// URL is relative and carries no ticket, exactly like stream_url and for the
	// same reason — a <track> is a same-origin subresource with a cookie.
	URL string `json:"url"`
}

// handleSubtitle serves one sidecar, converted if it has to be.
//
// **{name} is never joined onto a path.** It is compared against the names of
// the files this film's folder actually holds, so the only strings that can
// reach the filesystem are ones os.ReadDir produced. That is a stronger
// guarantee than sanitising the parameter would be, and it is why there is no
// sanitising here to review.
func (s *Server) handleSubtitle(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.libraryFolder(w, r)
	if !ok {
		return
	}

	tracks, err := subtitleTracks(folder)
	if err != nil {
		s.log.Warn("subtitles: could not read the film's folder", "library_path", folder, "err", err)
		s.fail(w, http.StatusNotFound, errors.New("this film has no subtitles"))
		return
	}

	want := r.PathValue("name")
	i := slices.IndexFunc(tracks, func(t library.Sidecar) bool { return t.Name == want })
	if i < 0 {
		// The ordinary miss: a page left open while the folder changed, or a
		// name nobody has. Not logged — it is the client's stale list, not the
		// server's problem, and this route is as reachable as the stream is.
		s.fail(w, http.StatusNotFound, errors.New("this film has no subtitle by that name"))
		return
	}
	track := tracks[i]

	body, modTime, err := s.readSubtitle(track.Path)
	if err != nil {
		s.fail(w, http.StatusNotFound, errors.New("this film has no subtitle by that name"))
		return
	}
	// The map's VALUE, where subtitleTracks above uses its membership: being in
	// the table is "a <track> can use this", and being true is "not until it has
	// been converted".
	if convert := trackFormats[strings.ToLower(filepath.Ext(track.Name))]; convert {
		body = subripToWebVTT(body)
	}

	// Explicit, and for the same reason the stream's is: measured,
	// mime.TypeByExtension(".vtt") is "" even on this Mac with Apache's table
	// loaded, and ".srt" is "application/x-subrip", which no browser renders as
	// a text track. The charset is not decoration either — WebVTT is defined as
	// UTF-8 and a browser will not guess.
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")

	// ServeContent over the converted bytes rather than a bare Write: it gets
	// Content-Length, HEAD and If-Modified-Since right, off the source file's
	// own ModTime, which is the honest validator for output derived from it.
	http.ServeContent(w, r, track.Name, modTime, bytes.NewReader(body))
}

// readSubtitle reads a sidecar whole, refusing one too large to be one.
func (s *Server) readSubtitle(path string) ([]byte, time.Time, error) {
	file, err := os.Open(path)
	if err != nil {
		// It was there for ReadDir and gone by the open: a race with a delete.
		s.log.Warn("subtitles: could not open a sidecar", "path", path, "err", err)
		return nil, time.Time{}, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		s.log.Warn("subtitles: could not stat a sidecar", "path", path, "err", err)
		return nil, time.Time{}, err
	}
	if info.Size() > maxSubtitleBytes {
		s.log.Warn("subtitles: refusing a sidecar far larger than any subtitle file",
			"path", path, "size", info.Size(), "limit", maxSubtitleBytes)
		return nil, time.Time{}, fmt.Errorf("subtitle %s is %d bytes", path, info.Size())
	}

	// Bounded by the limit and not by the size above it, because the two are read
	// at different moments and a file that grows in between must not decide how
	// much memory this takes.
	body, err := io.ReadAll(io.LimitReader(file, maxSubtitleBytes))
	if err != nil {
		s.log.Warn("subtitles: could not read a sidecar", "path", path, "err", err)
		return nil, time.Time{}, err
	}
	return body, info.ModTime(), nil
}

// subtitleTracks is the film's sidecars, narrowed to the ones a <track> can use.
//
// One function, used by both the playback list and the route that serves them,
// so the endpoint can never hand out a file the list did not offer — and so the
// `.ass` that is deliberately absent from the menu is absent from the URL space
// as well, rather than being reachable by anybody who guesses its name.
func subtitleTracks(folder string) ([]library.Sidecar, error) {
	all, err := library.ListSidecars(folder)
	if err != nil {
		return nil, err
	}
	out := make([]library.Sidecar, 0, len(all))
	for _, sidecar := range all {
		if _, usable := trackFormats[strings.ToLower(filepath.Ext(sidecar.Name))]; usable {
			out = append(out, sidecar)
		}
	}
	return out, nil
}

// flagLabels spell a sidecar's flags the way a person reads them. Two of the
// three are initialisms and look like a typo in lower case, which is what a
// track menu would otherwise show: "English (sdh)".
var flagLabels = map[string]string{
	"sdh":    "SDH",
	"cc":     "CC",
	"forced": "forced",
}

// subtitleLabel is what a player draws in its track menu.
func subtitleLabel(sidecar library.Sidecar) string {
	label := library.LanguageName(sidecar.Language)
	if label == "" {
		// A file whose name declared no language. "Subtitles" rather than the
		// file name, which after an import is the film's own title and would
		// read as a menu entry naming the film it is already inside.
		label = "Subtitles"
	}
	if len(sidecar.Flags) == 0 {
		return label
	}

	spelled := make([]string, 0, len(sidecar.Flags))
	for _, flag := range sidecar.Flags {
		if pretty, known := flagLabels[flag]; known {
			flag = pretty
		}
		spelled = append(spelled, flag)
	}
	return label + " (" + strings.Join(spelled, ", ") + ")"
}

// subripToWebVTT is the conversion, and it happens HERE rather than at import.
//
// The file on disk stays the `.srt` the release shipped, because that is what
// VLC and Jellyfin already want and it is the whole reason the importer bothers
// to name it `Title (Year).en.srt` (docs/tasks/T46-subtitles.md). Converting on
// the way in would produce a library that only curator can read.
//
// The conversion itself is genuinely small, which is why it is here and not
// behind ffmpeg: a WEBVTT header, `,` → `.` in the timestamps, and the cue
// numbers dropped. Cue text is passed through untouched — SubRip and WebVTT
// share <b>, <i> and <u>, and a tag WebVTT does not know it ignores.
//
// **What this does not do, said out loud: it does not transcode the text.** A
// SubRip written in Latin-1 or Windows-1252 — which plenty of older releases are
// — comes out as the same bytes with a UTF-8 label on them, and the accented
// characters are mojibake. Fixing it means guessing an encoding or taking a
// dependency on golang.org/x/text, and neither is what "serve the subtitle that
// is there" is worth today.
func subripToWebVTT(srt []byte) []byte {
	// The header curator writes, followed by the blank line the spec requires
	// before the first cue.
	out := bytes.NewBuffer(make([]byte, 0, len(srt)+16))
	out.WriteString("WEBVTT\n\n")

	lines := bytes.Split(bytes.TrimPrefix(srt, utf8BOM), []byte("\n"))
	// Split leaves an empty element after a trailing newline, and every line
	// below is written WITH one. Dropping it is the difference between copying
	// a file and copying a file plus a blank line each time it is served.
	if n := len(lines); n > 0 && len(lines[n-1]) == 0 {
		lines = lines[:n-1]
	}

	for i, line := range lines {
		line = bytes.TrimSuffix(line, []byte("\r"))

		switch {
		case bytes.Contains(line, srtArrow):
			out.Write(webVTTCue(line))
		case srtIndex.Match(line) && i+1 < len(lines) && bytes.Contains(lines[i+1], srtArrow):
			// A cue number: digits alone, immediately in front of a timing line.
			// The lookahead is what keeps a line of DIALOGUE that happens to be
			// a bare number — "1984" on its own — from being deleted as one.
			continue
		default:
			out.Write(line)
		}
		out.WriteByte('\n')
	}
	return out.Bytes()
}

// webVTTCue rewrites one timing line strictly.
func webVTTCue(line []byte) []byte {
	match := srtCue.FindSubmatch(line)
	if match == nil {
		// Something with an arrow in it that is not a timing line curator
		// recognises. A comma-to-dot substitution is the whole of what the
		// conversion would have done anyway, so it is done and the line is left
		// otherwise intact rather than dropped: a cue nobody can parse is
		// visible in the file, and a cue that is gone is not.
		return bytes.ReplaceAll(line, []byte(","), []byte("."))
	}

	// Padded, because WebVTT wants exactly three digits of milliseconds and at
	// least two of hours, and SubRip in the wild supplies neither reliably.
	cue := fmt.Sprintf("%s:%s:%s.%s --> %s:%s:%s.%s",
		pad(match[1], 2), match[2], match[3], pad(match[4], 3),
		pad(match[5], 2), match[6], match[7], pad(match[8], 3))
	if settings := match[9]; len(settings) > 0 {
		cue += " " + string(settings)
	}
	return []byte(cue)
}

// pad left-pads a digit field with zeros. A milliseconds field written with
// fewer than three digits means its LEADING zeros are missing, not its trailing
// ones — ",5" is five milliseconds, not five hundred.
func pad(digits []byte, width int) string {
	if len(digits) >= width {
		return string(digits)
	}
	return strings.Repeat("0", width-len(digits)) + string(digits)
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

	// Subtitles is one entry per sidecar in the film's folder that a <track>
	// element can actually use, and it is `[]` rather than absent when there are
	// none — an empty library is `[]` and never null everywhere else in this
	// package, and a UI that has to branch on undefined before it can map over a
	// list is a branch this could have spared it.
	//
	// It is on the playback response and not on the movie detail body because it
	// is a fact about the DISK, read with a ReadDir, and the detail body is
	// answered for every card on a browse screen.
	Subtitles []subtitleTrack `json:"subtitles"`

	// ExpiresAt is absent with authentication off, because nothing was minted.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// handlePlayback hands back the URLs this film can be played with, and mints a
// ticket only if there is a password for one to stand in for.
func (s *Server) handlePlayback(w http.ResponseWriter, r *http.Request) {
	movie, folder, ok := s.libraryFolder(w, r)
	if !ok {
		return
	}

	// The film has to actually be there. This used to resolve the folder only,
	// on the argument that a playback response should not touch the disk — and
	// the result was the thing the comment below it warned against: a 200
	// carrying a stream_url the stream would 404, with the failure arriving
	// inside <video>, where it is indistinguishable from a codec refusal.
	//
	// It costs one ReadDir of a directory this handler is about to read anyway
	// for the subtitles, and it is the same picker, so this cannot disagree with
	// the endpoint that will be asked next.
	if _, err := library.FindFeature(folder, library.FeatureOpts{}); err != nil {
		s.log.Warn("playback: no feature file", "movie_id", movie.ID, "library_path", folder, "err", err)
		s.fail(w, http.StatusNotFound, errors.New("this film has no playable file in the library"))
		return
	}

	path := streamPath(movie.ID)
	body := playbackBody{StreamURL: path, ExternalURL: s.absolute(r, path)}
	if s.remux != nil {
		body.RemuxURL = remuxPath(movie.ID)
	}

	// A folder whose SUBTITLES cannot be listed is still not a refusal to play
	// the film. The feature is the point and the subtitle is a courtesy, exactly
	// as it is at import: an empty list here and a Warn. The feature itself is a
	// different question and it was answered above.
	tracks, err := subtitleTracks(folder)
	if err != nil {
		s.log.Warn("playback: could not list this film's subtitles; offering none",
			"library_path", folder, "err", err)
	}
	body.Subtitles = make([]subtitleTrack, 0, len(tracks))
	for _, track := range tracks {
		body.Subtitles = append(body.Subtitles, subtitleTrack{
			Name:     track.Name,
			Label:    subtitleLabel(track),
			Language: track.Language,
			URL:      subtitlePath(movie.ID, track.Name),
		})
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
