package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/logs"
	"github.com/DulsaraNethmin/curator/internal/remux"
	"github.com/DulsaraNethmin/curator/internal/store"
)

// featureSize clears library.DefaultMinFeatureBytes. The files are sparse, so
// this costs no disk and no time, and the real 50 MiB floor is exercised rather
// than a lowered one — which is the point, because the sample-file case below
// is the one both the floor and the skip list exist for.
const featureSize = 60 << 20

// sampleSize is what a release group's sample.mkv actually weighs. Under the
// floor, deliberately.
const sampleSize = 6 << 20

// headBytes is how much of a fixture file holds real, predictable content: byte
// n is n%256, so a range request's body can be checked against what was asked
// for rather than only its length.
const headBytes = 1024

// ebmlMagic opens every Matroska file, and every WebM one — which is the whole
// problem. It is written at the head of the fixtures so that a fixture sniffed
// instead of looked up is mislabelled exactly the way a real film would be.
var ebmlMagic = []byte{0x1A, 0x45, 0xDF, 0xA3}

// streamFixture is a library on disk, a store that points at it, and the mux
// both with and without the password in front.
type streamFixture struct {
	t    *testing.T
	root string
	mux  *http.ServeMux

	// bare is the mux; guarded is the mux behind Auth.Middleware, which is what
	// cmd/curator serves. Every non-auth test uses bare, so a failure there is
	// never an authentication failure in disguise.
	bare    http.Handler
	guarded http.Handler

	srv   *Server
	auth  *Auth
	store *fakeStore
	logs  *logs.Buffer
}

func newStreamFixture(t *testing.T, credential Credential) *streamFixture {
	t.Helper()

	buffer := logs.NewBuffer(200)
	log := slog.New(buffer.Handler(slog.NewTextHandler(io.Discard, nil)))

	f := &streamFixture{t: t, root: t.TempDir(), store: newFakeStore(), logs: buffer}

	f.auth = NewAuth(authKey, func(context.Context) (Credential, error) { return credential, nil }, log)
	f.auth.failDelay = time.Millisecond
	if err := f.auth.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	f.mux = http.NewServeMux()
	f.srv = New(f.store, ScannerFunc(nil), nil, f.root, log).WithTickets(f.auth)
	f.srv.Register(f.mux)
	f.srv.RegisterStream(f.mux)
	f.auth.Register(f.mux)

	f.bare, f.guarded = f.mux, f.auth.Middleware(f.mux)
	return f
}

// film creates a library folder holding one feature and returns the movie id
// the API will be asked for. That is curator's movies.id and never TMDB's
// (docs/decisions.md D21).
func (f *streamFixture) film(folder, file string) int64 {
	f.t.Helper()
	dir := filepath.Join(f.root, folder)
	f.feature(filepath.Join(dir, file), featureSize)
	return f.row(folder, dir)
}

// row records a movie whose library_path is dir. It is the FOLDER, exactly as
// the importer writes it — the scanner's identity key.
func (f *streamFixture) row(title, dir string) int64 {
	f.t.Helper()
	saved, _, err := f.store.UpsertMovieByPath(context.Background(), store.ScannedMovie{
		LibraryPath: dir, Title: title, Year: 2014, Status: "imported",
	})
	if err != nil {
		f.t.Fatalf("upsert %s: %v", dir, err)
	}
	return saved.ID
}

// feature writes a sparse file of size bytes whose first headBytes are
// predictable. Truncate does not allocate blocks, so a 60 MiB fixture costs
// nothing.
func (f *streamFixture) feature(path string, size int64) string {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatalf("mkdir: %v", err)
	}
	file, err := os.Create(path)
	if err != nil {
		f.t.Fatalf("create %s: %v", path, err)
	}
	defer file.Close()

	head := make([]byte, headBytes)
	for i := range head {
		head[i] = byte(i)
	}
	// The first four bytes only, so bytes 100-199 stay predictable for the
	// range assertions.
	copy(head, ebmlMagic)
	if _, err := file.Write(head); err != nil {
		f.t.Fatalf("write %s: %v", path, err)
	}
	if err := file.Truncate(size); err != nil {
		f.t.Fatalf("truncate %s: %v", path, err)
	}
	return path
}

// entries is the log as records, for the assertions that are about a LEVEL
// rather than about a string.
func (f *streamFixture) entries() []logs.Entry {
	out, _, _ := f.logs.Since(0, 200)
	return out
}

// logged flattens the buffer, message and attributes, which is where a path
// would actually end up.
func (f *streamFixture) logged() string {
	entries, _, _ := f.logs.Since(0, 200)
	var out strings.Builder
	for _, entry := range entries {
		out.WriteString(entry.Msg)
		for key, value := range entry.Attrs {
			out.WriteString(" " + key + "=" + value)
		}
		out.WriteString("\n")
	}
	return out.String()
}

// ffmpeg writes an executable script standing in for the real one and attaches
// a remuxer around it. body is shell; the tests that need to observe what it
// was given interpolate a path into it, because the child inherits the test
// process's environment and there is nothing to arrange there.
//
// A fake and not the real binary: what this file owns is the endpoint — its
// framing, its refusals and its credential — and none of that is a question
// about what ffmpeg does with a film. internal/remux owns the subprocess.
func (f *streamFixture) ffmpeg(concurrent int, body string) {
	f.t.Helper()
	path := filepath.Join(f.t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		f.t.Fatalf("write the fake ffmpeg: %v", err)
	}
	f.srv.WithRemux(remux.New(path, concurrent))
}

// live puts the mux on a real listener.
//
// The remux tests need one and the rest do not. Its whole contract is the
// framing — a 200 with NO Content-Length, chunked, flushed as it is produced —
// and an httptest.ResponseRecorder has no framing at all, so the assertion that
// matters most here could not fail against one.
func (f *streamFixture) live(h http.Handler) *httptest.Server {
	f.t.Helper()
	srv := httptest.NewServer(h)
	f.t.Cleanup(srv.Close)
	return srv
}

// waitFor polls until cond, or fails. Everything it waits for is another
// process starting or a slot coming back, so there is nothing to synchronise on.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func streamOf(id int64) string { return streamPath(id) }
func remuxOf(id int64) string  { return remuxPath(id) }

func rangeRequest(target, spec string) *http.Request {
	req := get(target)
	req.Header.Set("Range", spec)
	return req
}

// --- ranges, which are ServeContent's and are asserted because they are the
// difference between streaming and downloading ---------------------------

func TestStreamServesRangesAndTheWholeFile(t *testing.T) {
	f := newStreamFixture(t, Credential{})
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")

	whole := serve(f.bare, get(streamOf(id)))
	if whole.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", whole.Code, whole.Body)
	}
	if got := whole.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes — without it a player downloads instead of seeking", got)
	}
	if got := whole.Body.Len(); int64(got) != int64(featureSize) {
		t.Errorf("body = %d bytes, want %d", got, featureSize)
	}

	part := serve(f.bare, rangeRequest(streamOf(id), "bytes=100-199"))
	if part.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206: %s", part.Code, part.Body)
	}
	if part.Body.Len() != 100 {
		t.Errorf("body = %d bytes, want exactly 100", part.Body.Len())
	}
	want := make([]byte, 100)
	for i := range want {
		want[i] = byte(100 + i)
	}
	if !bytes.Equal(part.Body.Bytes(), want) {
		t.Errorf("body is not bytes 100-199 of the file")
	}
	if got := part.Header().Get("Content-Range"); got != "bytes 100-199/"+strconv.Itoa(featureSize) {
		t.Errorf("Content-Range = %q", got)
	}
}

func TestStreamRefusesAnUnsatisfiableRange(t *testing.T) {
	f := newStreamFixture(t, Credential{})
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")

	rec := serve(f.bare, rangeRequest(streamOf(id), "bytes=999999999999-"))
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want 416", rec.Code)
	}
	// The part a hand-rolled implementation gets wrong.
	if got := rec.Header().Get("Content-Range"); got != "bytes */"+strconv.Itoa(featureSize) {
		t.Errorf("Content-Range = %q, want bytes */%d", got, featureSize)
	}
}

// Players probe with HEAD before they ask for a byte, and Go 1.22 routing
// matches HEAD on a GET pattern — which is what makes this work at all.
func TestStreamAnswersHEADWithTheLengthAndNoBody(t *testing.T) {
	f := newStreamFixture(t, Credential{})
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")

	rec := serve(f.bare, httptest.NewRequest(http.MethodHead, streamOf(id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(featureSize) {
		t.Errorf("Content-Length = %q, want %d", got, featureSize)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD returned %d bytes of body", rec.Body.Len())
	}
}

// --- Content-Type, the trap this endpoint exists to avoid -------------------

// The whole reason curator carries its own four-entry table. mime.TypeByExtension
// answers "video/x-matroska" on this Mac only because Go read
// /etc/apache2/mime.types; in phase 9's FROM scratch image it answers "" and
// ServeContent sniffs, and sniffing an MKV gives video/webm because Matroska
// and WebM share the EBML magic. Both measured.
func TestStreamContentTypeComesFromCuratorsTable(t *testing.T) {
	cases := []struct {
		file string
		want string
	}{
		{"Interstellar (2014).mkv", "video/x-matroska"},
		{"Interstellar (2014).mp4", "video/mp4"},
		{"Interstellar (2014).m4v", "video/x-m4v"},
		{"Interstellar (2014).avi", "video/x-msvideo"},
		// Case folding, because release groups disagree and library's own
		// extension check folds too.
		{"Interstellar (2014).MKV", "video/x-matroska"},
	}

	for _, c := range cases {
		f := newStreamFixture(t, Credential{})
		id := f.film("Interstellar (2014)", c.file)
		rec := serve(f.bare, get(streamOf(id)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d: %s", c.file, rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Content-Type"); got != c.want {
			t.Errorf("%s: Content-Type = %q, want %q", c.file, got, c.want)
		}
	}
}

// Named on its own, because it is the specific mislabel this table exists to
// prevent.
//
// **Be honest about what this can prove on the machine it runs on.** Measured
// here: mime.TypeByExtension answers video/x-matroska, video/mp4, video/x-m4v
// and video/x-msvideo — curator's four exactly — because Go read
// /etc/apache2/mime.types. So on THIS Mac the assertion below passes with the
// table and passes without it, and it cannot fail no matter what is deleted.
//
// What makes it a real test is where it is not run yet. In phase 9's FROM
// scratch image none of the four files mime/type_unix.go looks in exists, every
// TypeByExtension answer becomes "", ServeContent sniffs, and the fixture's
// EBML head — asserted below to be exactly what a real .mkv starts with —
// becomes video/webm. That is the failure this catches the moment the suite
// runs in the image, and the reason the header is set unconditionally rather
// than only when the lookup comes back empty.
func TestStreamNeverCallsAnMKVWebM(t *testing.T) {
	f := newStreamFixture(t, Credential{})
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")

	rec := serve(f.bare, get(streamOf(id)))
	if got := rec.Header().Get("Content-Type"); got == "video/webm" {
		t.Fatal("an .mkv was labelled video/webm: sniffing decided the type")
	}

	// The measured fact the line above defends against, pinned so that it is
	// this test that fails if Go's sniffer ever changes its mind, rather than a
	// browser in six months.
	if got := http.DetectContentType(rec.Body.Bytes()); got != "video/webm" {
		t.Errorf("sniffing the fixture gives %q, want video/webm — the fixture no longer "+
			"reproduces the mislabel this test is about", got)
	}
}

// The one test here that DOES fail on this Mac if the header line goes.
//
// The image's failure is that mime.TypeByExtension answers "" and ServeContent
// sniffs. That state cannot be reached from a test — nothing removes an entry
// from mime's table — but the invariant underneath it can: curator's table
// wins over whatever the standard library thinks, whether the standard library
// is empty or merely wrong. So the standard library is made wrong, on purpose,
// and the answer has to be curator's anyway.
func TestStreamContentTypeBeatsTheStandardLibrarys(t *testing.T) {
	const poison = "application/x-mime-types-said-so"

	// mime's table is process-global, so it is put back afterwards. On this Mac
	// `before` is video/x-matroska, read out of /etc/apache2/mime.types.
	before := mime.TypeByExtension(".mkv")
	if err := mime.AddExtensionType(".mkv", poison); err != nil {
		t.Fatalf("AddExtensionType: %v", err)
	}
	t.Cleanup(func() {
		if before != "" {
			_ = mime.AddExtensionType(".mkv", before)
		}
	})

	f := newStreamFixture(t, Credential{})
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")

	rec := serve(f.bare, get(streamOf(id)))
	if got := rec.Header().Get("Content-Type"); got != "video/x-matroska" {
		t.Fatalf("Content-Type = %q, want video/x-matroska — the standard library's "+
			"table decided the type instead of curator's", got)
	}
}

// The table itself, away from the wire, because on this Mac the response-level
// assertion above cannot tell curator's answer from the operating system's.
// This one can: it is curator's map or nothing.
func TestContentTypeIsCuratorsTable(t *testing.T) {
	for ext, want := range map[string]string{
		".mkv": "video/x-matroska",
		".mp4": "video/mp4",
		".m4v": "video/x-m4v",
		".avi": "video/x-msvideo",
		".MKV": "video/x-matroska",
	} {
		if got := contentType("Interstellar (2014)" + ext); got != want {
			t.Errorf("%s = %q, want %q", ext, got, want)
		}
	}

	// Unreachable while FindFeature returns only the four above. An honest
	// "some bytes" beats a confident wrong label: the browser refuses it and
	// fires the error event, which is the codec authority this phase defers to.
	if got := contentType("Interstellar (2014).ogv"); got != "application/octet-stream" {
		t.Errorf("an extension not in the table = %q, want application/octet-stream", got)
	}
}

// --- which file in the folder is the film -----------------------------------

// The floor and the skip list, both inherited from the importer's picker. A
// folder whose only video is a 6 MB sample has nothing playable in it, and
// streaming thirty seconds of trailer would be worse than saying so.
func TestStreamRefusesAFolderWhoseOnlyVideoIsASample(t *testing.T) {
	f := newStreamFixture(t, Credential{})
	dir := filepath.Join(f.root, "Interstellar (2014)")
	f.feature(filepath.Join(dir, "sample.mkv"), sampleSize)
	id := f.row("Interstellar (2014)", dir)

	rec := serve(f.bare, get(streamOf(id)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — a 6 MB sample was served as the film", rec.Code)
	}
}

func TestStreamServesTheLargerOfTwoFilmsAndSaysSo(t *testing.T) {
	f := newStreamFixture(t, Credential{})
	dir := filepath.Join(f.root, "Interstellar (2014)")
	f.feature(filepath.Join(dir, "part1.mkv"), featureSize)
	f.feature(filepath.Join(dir, "part2.mkv"), featureSize*2)
	id := f.row("Interstellar (2014)", dir)

	rec := serve(f.bare, get(streamOf(id)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(featureSize*2) {
		t.Errorf("Content-Length = %q, want the larger file's %d", got, featureSize*2)
	}
	if !strings.Contains(f.logged(), "more than one film in this folder") {
		t.Errorf("passing one over was not logged:\n%s", f.logged())
	}
}

// A browser seeking issues a range request every time, and this line at one per
// request would push every real entry out of the tail /api/logs serves (D18).
func TestStreamSaysItPassedOneOverOnlyOnce(t *testing.T) {
	f := newStreamFixture(t, Credential{})
	dir := filepath.Join(f.root, "Interstellar (2014)")
	f.feature(filepath.Join(dir, "part1.mkv"), featureSize)
	f.feature(filepath.Join(dir, "part2.mkv"), featureSize*2)
	id := f.row("Interstellar (2014)", dir)

	for range 5 {
		serve(f.bare, rangeRequest(streamOf(id), "bytes=0-99"))
	}
	if got := strings.Count(f.logged(), "more than one film in this folder"); got != 1 {
		t.Errorf("logged %d times, want 1 — five range requests is one ordinary seek", got)
	}
}

// --- the misses: four distinct answers, none of them a 500 ------------------

func TestStreamMissesAreNeverA500(t *testing.T) {
	f := newStreamFixture(t, Credential{})

	noPath := f.film("No File (2014)", "No File (2014).mkv")
	// A wanted film that was never imported: only imported rows have a
	// library_path, which is also why this endpoint can never read a partial
	// download.
	f.store.byID[noPath].LibraryPath = nil

	gone := f.row("Gone (2014)", filepath.Join(f.root, "Gone (2014)"))

	emptyDir := filepath.Join(f.root, "Empty (2014)")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	empty := f.row("Empty (2014)", emptyDir)

	cases := []struct {
		name   string
		target string
		want   int
	}{
		{"library_path is NULL", streamOf(noPath), http.StatusNotFound},
		{"the folder has gone missing", streamOf(gone), http.StatusNotFound},
		{"the folder holds no video", streamOf(empty), http.StatusNotFound},
		{"no such movie", streamOf(9999), http.StatusNotFound},
		{"the id is not a number", "/api/movies/seven/stream", http.StatusBadRequest},
	}
	for _, c := range cases {
		rec := serve(f.bare, get(c.target))
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body)
		}
		var body struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
			t.Errorf("%s: no {\"error\"} in %s", c.name, rec.Body)
		}
	}
}

// The containment check defends a ROW, not a URL: the id is a ParseInt, so
// nothing traverses in from the request. What this catches is a library_path
// written when LIBRARY_MOVIES pointed elsewhere, or a database restored beside
// a different library — the operator's problem, which is why the assertion is
// against the log and not only the status.
func TestStreamRefusesARowPointingOutsideTheLibrary(t *testing.T) {
	f := newStreamFixture(t, Credential{})

	elsewhere := filepath.Join(t.TempDir(), "Interstellar (2014)")
	f.feature(filepath.Join(elsewhere, "Interstellar (2014).mkv"), featureSize)
	id := f.row("Interstellar (2014)", elsewhere)

	rec := serve(f.bare, get(streamOf(id)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — a row outside the library was served", rec.Code)
	}

	logged := f.logged()
	if !strings.Contains(logged, "outside the library root") {
		t.Errorf("the refusal is not in the log, which is where it gets diagnosed:\n%s", logged)
	}
	// Both paths, because the operator has to see which one is wrong.
	if !strings.Contains(logged, elsewhere) || !strings.Contains(logged, f.root) {
		t.Errorf("the log line names neither the row's path nor the root:\n%s", logged)
	}
}

// --- POST .../playback ------------------------------------------------------

func decodePlayback(t *testing.T, rec *httptest.ResponseRecorder) playbackBody {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var body playbackBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v — body was %s", err, rec.Body)
	}
	return body
}

func playback(h http.Handler, id int64) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/movies/"+strconv.FormatInt(id, 10)+"/playback", nil)
	return serve(h, req)
}

// The default, and the state every install that exists is in.
func TestPlaybackWithAuthenticationOffMintsNothing(t *testing.T) {
	f := newStreamFixture(t, Credential{})
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")

	body := decodePlayback(t, playback(f.bare, id))
	if body.StreamURL != streamOf(id) {
		t.Errorf("stream_url = %q, want %q", body.StreamURL, streamOf(id))
	}
	if body.ExpiresAt != nil {
		t.Errorf("expires_at = %v, want absent: nothing was minted", body.ExpiresAt)
	}
	if strings.Contains(body.ExternalURL, "ticket") {
		t.Errorf("external_url carries a ticket with no password to stand in for: %q", body.ExternalURL)
	}
	// Absolute, because VLC needs a host, and built from the request's own Host.
	if !strings.HasPrefix(body.ExternalURL, "http://example.com"+streamOf(id)) {
		t.Errorf("external_url = %q, want an absolute URL on the request's host", body.ExternalURL)
	}

	// And the URL it returns actually works.
	if rec := serve(f.guarded, get(body.StreamURL)); rec.Code != http.StatusOK {
		t.Errorf("the returned stream_url answered %d", rec.Code)
	}
}

func TestPlaybackWithAuthenticationOnMintsATicket(t *testing.T) {
	f := newStreamFixture(t, Credential{Enabled: true, Hash: hashed(t, authPassword)})
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")

	body := decodePlayback(t, playback(f.bare, id))

	// stream_url NEVER carries the ticket. The page's own <video> is a
	// same-origin subresource and sends the cookie by itself.
	if strings.Contains(body.StreamURL, "ticket") {
		t.Errorf("stream_url carries a ticket: %q", body.StreamURL)
	}
	if body.ExpiresAt == nil {
		t.Fatal("expires_at is absent with a password set")
	}
	// Twelve hours: longer than a film, far shorter than a cookie's thirty days.
	if until := time.Until(*body.ExpiresAt); until < 11*time.Hour || until > ticketLifetime {
		t.Errorf("expires in %v, want about %v", until, ticketLifetime)
	}

	parsed, err := url.Parse(body.ExternalURL)
	if err != nil {
		t.Fatalf("external_url is not a URL: %v", err)
	}
	if parsed.Query().Get(ticketParam) == "" {
		t.Fatalf("external_url carries no ticket: %q", body.ExternalURL)
	}
	// The whole point: it plays without a cookie and without a password.
	if rec := serve(f.guarded, get(parsed.RequestURI())); rec.Code != http.StatusOK {
		t.Errorf("the minted URL answered %d, want 200: %s", rec.Code, rec.Body)
	}
}

func TestPlaybackRefusesAFilmThatIsNotInTheLibrary(t *testing.T) {
	f := newStreamFixture(t, Credential{})
	id := f.film("Wanted (2014)", "Wanted (2014).mkv")
	f.store.byID[id].LibraryPath = nil

	// A playback response handing out a URL the stream would 404 on is worse
	// than a 404 here.
	if rec := playback(f.bare, id); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if rec := playback(f.bare, 9999); rec.Code != http.StatusNotFound {
		t.Errorf("no such movie: status = %d, want 404", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/movies/seven/playback", nil)
	if rec := serve(f.bare, req); rec.Code != http.StatusBadRequest {
		t.Errorf("id not a number: status = %d, want 400", rec.Code)
	}
}

// --- the stream is behind the same password as everything else (D31) --------

func TestStreamTakesTheThreeCredentialsAndRefusesNone(t *testing.T) {
	f := newStreamFixture(t, Credential{Enabled: true, Hash: hashed(t, authPassword)})
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")
	target := streamOf(id)

	// No credential. An install that protects the list of titles and serves the
	// films to anyone on the network is not a posture, it is an oversight.
	if rec := serve(f.guarded, get(target)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no credential: status = %d, want 401", rec.Code)
	}

	// The browser's, which a <video src> sends without being asked.
	cookie := session(t, serve(f.guarded, postLogin(authPassword)))
	if rec := serve(f.guarded, withCookie(target, cookie)); rec.Code != http.StatusOK {
		t.Errorf("cookie: status = %d, want 200: %s", rec.Code, rec.Body)
	}

	// curl's.
	if rec := serve(f.guarded, basic(target, authPassword)); rec.Code != http.StatusOK {
		t.Errorf("basic: status = %d, want 200: %s", rec.Code, rec.Body)
	}

	// VLC's.
	if rec := serve(f.guarded, get(ticketed(t, f, target))); rec.Code != http.StatusOK {
		t.Errorf("ticket: status = %d, want 200: %s", rec.Code, rec.Body)
	}
}

// ticketed mints a ticket for target and returns the URL carrying it.
func ticketed(t *testing.T, f *streamFixture, target string) string {
	t.Helper()
	value, _, ok := f.auth.Ticket(target, ticketLifetime)
	if !ok {
		t.Fatal("no ticket was minted")
	}
	return target + "?" + url.Values{ticketParam: {value}}.Encode()
}

// A ticket is for ONE path, because the path is in the signed message. A ticket
// for one film is not a ticket for the library.
func TestATicketForOneFilmIsNotATicketForAnother(t *testing.T) {
	f := newStreamFixture(t, Credential{Enabled: true, Hash: hashed(t, authPassword)})
	mine := f.film("Interstellar (2014)", "Interstellar (2014).mkv")
	theirs := f.film("Arrival (2016)", "Arrival (2016).mkv")

	value, _, ok := f.auth.Ticket(streamOf(mine), ticketLifetime)
	if !ok {
		t.Fatal("no ticket was minted")
	}
	replayed := streamOf(theirs) + "?" + url.Values{ticketParam: {value}}.Encode()

	if rec := serve(f.guarded, get(replayed)); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 — one film's ticket opened another", rec.Code)
	}
	// And it is not a ticket for the list of titles either.
	listed := "/api/movies?" + url.Values{ticketParam: {value}}.Encode()
	if rec := serve(f.guarded, get(listed)); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/movies = %d, want 401 — a film's ticket read the library", rec.Code)
	}
}

func TestAnExpiredTicketIsRefused(t *testing.T) {
	f := newStreamFixture(t, Credential{Enabled: true, Hash: hashed(t, authPassword)})
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")

	value, _, ok := f.auth.Ticket(streamOf(id), -time.Minute)
	if !ok {
		t.Fatal("no ticket was minted")
	}
	target := streamOf(id) + "?" + url.Values{ticketParam: {value}}.Encode()
	if rec := serve(f.guarded, get(target)); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// The property that distinguishes a ticket from a password in a URL: changing
// the password kills every outstanding one, with nothing to evict.
func TestChangingThePasswordKillsEveryOutstandingTicket(t *testing.T) {
	credential := Credential{Enabled: true, Hash: hashed(t, authPassword)}
	f := newStreamFixture(t, credential)
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")
	target := ticketed(t, f, streamOf(id))

	if rec := serve(f.guarded, get(target)); rec.Code != http.StatusOK {
		t.Fatalf("the ticket did not work before the change: %d", rec.Code)
	}

	next := Credential{Enabled: true, Hash: hashed(t, "something else entirely")}
	f.auth.source = func(context.Context) (Credential, error) { return next, nil }
	if err := f.auth.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if rec := serve(f.guarded, get(target)); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 — the ticket outlived the password", rec.Code)
	}
}

// The domain separation, asserted rather than argued. A session signs bare
// digits; a ticket signs "ticket\n" + path + "\n" + expiry under the same key,
// so neither message can ever be the other.
func TestASessionIsNotATicketAndATicketIsNotASession(t *testing.T) {
	f := newStreamFixture(t, Credential{Enabled: true, Hash: hashed(t, authPassword)})
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")
	target := streamOf(id)

	cookie := session(t, serve(f.guarded, postLogin(authPassword)))
	asTicket := target + "?" + url.Values{ticketParam: {cookie.Value}}.Encode()
	if rec := serve(f.guarded, get(asTicket)); rec.Code != http.StatusUnauthorized {
		t.Errorf("a cookie value worked as a ticket: %d", rec.Code)
	}

	value, _, ok := f.auth.Ticket(target, ticketLifetime)
	if !ok {
		t.Fatal("no ticket was minted")
	}
	asSession := withCookie(target, &http.Cookie{Name: cookieName, Value: value})
	if rec := serve(f.guarded, asSession); rec.Code != http.StatusUnauthorized {
		t.Errorf("a ticket worked as a cookie: %d", rec.Code)
	}
}

// It is a credential, so it does not go in the log. The path does.
func TestARefusedTicketIsNotWrittenDown(t *testing.T) {
	f := newStreamFixture(t, Credential{Enabled: true, Hash: hashed(t, authPassword)})
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")

	const forged = "9999999999.NOT-A-REAL-SIGNATURE-3f81b2"
	target := streamOf(id) + "?" + url.Values{ticketParam: {forged}}.Encode()
	if rec := serve(f.guarded, get(target)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}

	logged := f.logged()
	if strings.Contains(logged, forged) {
		t.Errorf("the presented ticket reached the log:\n%s", logged)
	}
	// It IS logged as a failure, unlike a request with no credential at all: an
	// expired VLC URL is worth being able to see.
	if !strings.Contains(logged, "auth failed") || !strings.Contains(logged, streamOf(id)) {
		t.Errorf("the refusal is not in the log:\n%s", logged)
	}
}

// --- GET .../remux, which is not the stream endpoint ------------------------

// The framing, which is the whole difference between the two endpoints. A pipe
// has no length and no byte-range semantics, so: 200 and never 206, no
// Content-Length, and Accept-Ranges: none said explicitly so a player stops
// asking for something nothing here can answer.
func TestRemuxIsAStreamOfUnknownLengthAndSaysSo(t *testing.T) {
	f := newStreamFixture(t, Credential{})
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")
	f.ffmpeg(1, `printf 'FRAGMENTEDMP4'`)

	resp, err := http.Get(f.live(f.bare).URL + remuxOf(id))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "video/mp4" {
		t.Errorf("Content-Type = %q, want video/mp4 — whatever went in, -f mp4 came out", got)
	}
	if got := resp.Header.Get("Accept-Ranges"); got != "none" {
		t.Errorf("Accept-Ranges = %q, want none", got)
	}
	// -1 is "no Content-Length, and the body ends when the connection says so".
	// The flush on every write is what guarantees it: without one, net/http
	// buffers a short response, sees the handler return, and helpfully adds the
	// length of a stream that does not have one.
	if resp.ContentLength != -1 {
		t.Errorf("Content-Length = %d, want none: a pipe does not know how long it is", resp.ContentLength)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != "FRAGMENTEDMP4" {
		t.Errorf("body = %q, want what ffmpeg wrote", body)
	}
}

// Seeking is a parameter and not a header, and `start` rather than `t` because
// `ticket` already owns that letter (docs/phase-8.md, the traps).
func TestRemuxSeeksWithStartAndRefusesRubbishRatherThanPastingIt(t *testing.T) {
	f := newStreamFixture(t, Credential{})
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")
	argv := filepath.Join(t.TempDir(), "argv")
	f.ffmpeg(1, `printf '%s\n' "$@" > `+strconv.Quote(argv)+`
printf 'FRAGMENTEDMP4'`)

	if rec := serve(f.bare, get(remuxOf(id)+"?start=90")); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	raw, err := os.ReadFile(argv)
	if err != nil {
		t.Fatalf("the fake recorded no argv: %v", err)
	}
	if !strings.Contains(string(raw), "-ss\n90\n") {
		t.Errorf("-ss 90 did not reach ffmpeg:\n%s", raw)
	}

	// Never pasted into an argv. The handler parses a number and internal/remux
	// formats that number back out, so the string below has no path into the
	// command line even if this assertion were deleted — this is the second
	// guard, and the one that answers with a status code.
	for _, rubbish := range []string{"abc", "-5", "90 -c:v libx264", "NaN", "1e999", "; rm -rf /"} {
		rec := serve(f.bare, get(remuxOf(id)+"?"+url.Values{startParam: {rubbish}}.Encode()))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("start=%q: status = %d, want 400", rubbish, rec.Code)
		}
	}

	// An empty one is an absent one, which is what a player sends when it has
	// not seeked yet.
	if rec := serve(f.bare, get(remuxOf(id)+"?start=")); rec.Code != http.StatusOK {
		t.Errorf("start= : status = %d, want 200", rec.Code)
	}
}

// Refused, never queued: queueing would turn "the film is slow to start" into
// "the film never starts", which is worse and much harder to explain.
func TestTheNthRemuxIsRefusedWithARetryAfterAndTheSlotComesBack(t *testing.T) {
	f := newStreamFixture(t, Credential{})
	long := f.film("Long (2014)", "Long (2014).mkv")
	short := f.film("Short (2014)", "Short (2014).mkv")

	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	// One fake, two behaviours, chosen by the film it is asked for: the long one
	// holds its slot until it is killed and the short one exits by itself.
	f.ffmpeg(1, `echo started >> `+strconv.Quote(started)+`
case "$*" in
  *Short*) printf 'OK'; exit 0;;
esac
sleep 60`)

	// The request's own context rather than a client that hangs up, because what
	// frees the slot is the handler returning and that is what this asserts. A
	// cancelled client would go through the server's disconnect detection as
	// well, which is net/http's invariant and not curator's.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	held := make(chan struct{})
	go func() {
		defer close(held)
		serve(f.bare, get(remuxOf(long)).WithContext(ctx))
	}()

	waitFor(t, "the only slot to be occupied", func() bool { return exists(started) })

	// The one over the cap.
	rec := serve(f.bare, get(remuxOf(short)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 — the cap queued instead of refusing", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("a 503 with no Retry-After tells a player nothing about when to come back")
	}
	// A designed refusal is not a server fault. It was an ERROR line first —
	// s.fail logs anything past 500 that way — and a red line in the tail
	// /api/logs serves (docs/decisions.md D18) reads as a bug rather than as a
	// cap doing exactly what it was built to do.
	for _, entry := range f.entries() {
		if entry.Level == "ERROR" {
			t.Errorf("the cap logged at ERROR: %s %v", entry.Msg, entry.Attrs)
		}
	}
	if !strings.Contains(f.logged(), "every slot is in use") {
		t.Errorf("the refusal is not in the log at all, so \"it told me to try again\" is undiagnosable:\n%s", f.logged())
	}

	// A slot freeing lets the next one through, which is the half that makes
	// this a cap rather than a ceiling nothing comes back down from. There is
	// nothing to poll for: the slot is released as the handler returns.
	cancel()
	<-held
	if rec := serve(f.bare, get(remuxOf(short))); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — the freed slot was never given back: %s", rec.Code, rec.Body)
	}
}

// The headers went out with the first byte, so a failure after that cannot
// become an error page. It ends the response and the browser's error event does
// the rest — which is exactly the fallback chain's next step.
func TestAFailureMidStreamEndsTheResponseAndIsLoggedOnce(t *testing.T) {
	const complaint = "Invalid data found when processing input"
	f := newStreamFixture(t, Credential{})
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")
	f.ffmpeg(1, `printf 'PARTIAL'
echo `+strconv.Quote(complaint)+` >&2
exit 1`)

	rec := serve(f.bare, get(remuxOf(id)))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — the headers went out with the first byte", rec.Code)
	}
	if rec.Body.String() != "PARTIAL" {
		t.Errorf("body = %q, want the bytes that did come out", rec.Body)
	}
	// Once. ffmpeg writes several lines a second at its default verbosity, and
	// the log tail is a product surface (docs/decisions.md D18).
	if got := strings.Count(f.logged(), complaint); got != 1 {
		t.Errorf("ffmpeg's stderr reached the log %d times, want exactly 1:\n%s", got, f.logged())
	}
}

// Before a byte comes out there is still a status code to be had, and it is
// worth having: this is a film ffmpeg would not open at all.
func TestAFailureBeforeTheFirstByteIsStillAStatusCode(t *testing.T) {
	const complaint = "Invalid data found when processing input"
	f := newStreamFixture(t, Credential{})
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")
	f.ffmpeg(1, `echo `+strconv.Quote(complaint)+` >&2
exit 1`)

	rec := serve(f.bare, get(remuxOf(id)))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	// The caller is told it failed and NOT what ffmpeg said: the captured stderr
	// names a path on the server's own disk.
	if strings.Contains(rec.Body.String(), complaint) {
		t.Errorf("ffmpeg's stderr was sent to the caller: %s", rec.Body)
	}
	if got := strings.Count(f.logged(), complaint); got != 1 {
		t.Errorf("ffmpeg's stderr reached the log %d times, want exactly 1:\n%s", got, f.logged())
	}
}

// A player probes with HEAD before it asks for a byte, and T45's player probes
// specifically to tell a 401 from a codec failure. Spawning an ffmpeg to answer
// a question about headers would burn one of three slots on a probe.
func TestAHEADOnTheRemuxStartsNothing(t *testing.T) {
	f := newStreamFixture(t, Credential{})
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")
	started := filepath.Join(t.TempDir(), "started")
	f.ffmpeg(1, `echo started > `+strconv.Quote(started)+`
printf 'FRAGMENTEDMP4'`)

	rec := serve(f.bare, httptest.NewRequest(http.MethodHead, remuxOf(id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "none" {
		t.Errorf("Accept-Ranges = %q, want none — a HEAD is where a player learns that", got)
	}
	if exists(started) {
		t.Error("a HEAD started an ffmpeg")
	}
}

// --- no ffmpeg is an ordinary state, not a degraded one ---------------------

// A URL that exists and never works is worse than one that does not exist. So
// the field is omitted entirely and the route says what it is rather than
// answering 503 for ever.
func TestWithNoFFmpegThereIsNoRemuxURLAndTheRouteSaysSo(t *testing.T) {
	f := newStreamFixture(t, Credential{})
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")

	body := decodePlayback(t, playback(f.bare, id))
	if body.RemuxURL != "" {
		t.Errorf("remux_url = %q, want absent with no ffmpeg configured", body.RemuxURL)
	}
	// Absent from the JSON and not merely empty: the UI's question is whether
	// the fallback exists at all.
	if strings.Contains(playback(f.bare, id).Body.String(), "remux_url") {
		t.Errorf("remux_url is in the body: %s", playback(f.bare, id).Body)
	}
	// Direct play is untouched by any of this.
	if body.StreamURL != streamOf(id) {
		t.Errorf("stream_url = %q, want %q", body.StreamURL, streamOf(id))
	}

	if rec := serve(f.bare, get(remuxOf(id))); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 — there is nothing here to come back for", rec.Code)
	}
}

func TestWithAnFFmpegPlaybackOffersTheRemux(t *testing.T) {
	f := newStreamFixture(t, Credential{})
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")
	f.ffmpeg(1, `printf 'FRAGMENTEDMP4'`)

	body := decodePlayback(t, playback(f.bare, id))
	if body.RemuxURL != remuxOf(id) {
		t.Fatalf("remux_url = %q, want %q", body.RemuxURL, remuxOf(id))
	}
	// Relative and with no ticket on it, exactly like stream_url: this is what
	// the page's own <video> re-points at, and that carries a cookie.
	if strings.Contains(body.RemuxURL, "ticket") {
		t.Errorf("remux_url carries a ticket: %q", body.RemuxURL)
	}
	// And it works.
	if rec := serve(f.guarded, get(body.RemuxURL)); rec.Code != http.StatusOK {
		t.Errorf("the returned remux_url answered %d", rec.Code)
	}
}

// The remux is behind the same password as everything else under /api/, with no
// exemption (docs/decisions.md D31). Serving a film through ffmpeg to anybody on
// the network would be the same oversight as serving the file.
func TestTheRemuxIsBehindTheSamePasswordAsTheStream(t *testing.T) {
	f := newStreamFixture(t, Credential{Enabled: true, Hash: hashed(t, authPassword)})
	id := f.film("Interstellar (2014)", "Interstellar (2014).mkv")
	f.ffmpeg(1, `printf 'FRAGMENTEDMP4'`)
	target := remuxOf(id)

	if rec := serve(f.guarded, get(target)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no credential: status = %d, want 401", rec.Code)
	}
	cookie := session(t, serve(f.guarded, postLogin(authPassword)))
	if rec := serve(f.guarded, withCookie(target, cookie)); rec.Code != http.StatusOK {
		t.Errorf("cookie: status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := serve(f.guarded, basic(target, authPassword)); rec.Code != http.StatusOK {
		t.Errorf("basic: status = %d, want 200: %s", rec.Code, rec.Body)
	}

	// A STREAM ticket does not open the remux, and that falls out of the path
	// being in the signed message rather than being a rule anybody wrote. It is
	// also why nothing mints one: the ticket exists for VLC, and VLC has never
	// needed an MKV rewritten. The middleware is uniform all the same — a ticket
	// minted for this path does work — so the route is not an exemption in
	// either direction.
	value, _, ok := f.auth.Ticket(streamOf(id), ticketLifetime)
	if !ok {
		t.Fatal("no ticket was minted")
	}
	replayed := target + "?" + url.Values{ticketParam: {value}}.Encode()
	if rec := serve(f.guarded, get(replayed)); rec.Code != http.StatusUnauthorized {
		t.Errorf("a stream ticket opened the remux: %d", rec.Code)
	}
	if rec := serve(f.guarded, get(ticketed(t, f, target))); rec.Code != http.StatusOK {
		t.Errorf("a ticket for this very path answered %d, want 200: %s", rec.Code, rec.Body)
	}
}

// The same four answers the stream gives, because it is the same lookup and the
// same picker — a folder whose only video is a 6 MB sample has nothing to remux
// either.
func TestRemuxMissesMatchTheStreams(t *testing.T) {
	f := newStreamFixture(t, Credential{})
	f.ffmpeg(1, `printf 'FRAGMENTEDMP4'`)

	sampleOnly := filepath.Join(f.root, "Sample (2014)")
	f.feature(filepath.Join(sampleOnly, "sample.mkv"), sampleSize)

	cases := []struct {
		name   string
		target string
		want   int
	}{
		{"only a sample in the folder", remuxOf(f.row("Sample (2014)", sampleOnly)), http.StatusNotFound},
		{"the folder has gone missing", remuxOf(f.row("Gone (2014)", filepath.Join(f.root, "Gone (2014)"))), http.StatusNotFound},
		{"no such movie", remuxOf(9999), http.StatusNotFound},
		{"the id is not a number", "/api/movies/seven/remux", http.StatusBadRequest},
	}
	for _, c := range cases {
		if rec := serve(f.bare, get(c.target)); rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body)
		}
	}
}

// With no password there is nothing for a ticket to stand in for, so nothing is
// minted — which is what makes the response above have one shape.
func TestNoTicketIsMintedWithoutAPassword(t *testing.T) {
	f := newStreamFixture(t, Credential{})
	if _, _, ok := f.auth.Ticket("/api/movies/7/stream", ticketLifetime); ok {
		t.Error("a ticket was minted with authentication off")
	}
}

// auth_enabled true with no password is the state install refuses to enforce.
// A ticket minted there would be a token that kept working after somebody
// actually set one.
func TestNoTicketIsMintedForASwitchWithNothingBehindIt(t *testing.T) {
	f := newStreamFixture(t, Credential{Enabled: true})
	if _, _, ok := f.auth.Ticket("/api/movies/7/stream", ticketLifetime); ok {
		t.Error("a ticket was minted for a password that does not exist")
	}
}
