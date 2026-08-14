package remux

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Everything here runs against a FAKE ffmpeg — a shell script this file writes
// — because what this package owns is not what ffmpeg does with a film. It is
// the subprocess: the argv it is given, that it dies with the request, that
// there is a cap on how many of it run, and that its stderr is kept for the one
// exit where anybody wants it. A real ffmpeg would make all four slower to
// assert and none of them clearer.

// fake writes an executable script that stands in for ffmpeg and returns its
// path. body is shell, and the caller interpolates whatever paths it needs to
// observe — the child inherits the test process's environment, so there is
// nothing to arrange there.
func fake(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write the fake ffmpeg: %v", err)
	}
	return path
}

// waitFor polls until cond, or fails. Every wait in this file is for something
// another process does, so there is nothing to synchronise on but the disk.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// alive reports whether a pid is still there. Signal 0 checks for the process
// without sending anything, which is the invariant the kill test is about —
// not a log line saying a kill was attempted.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func readPID(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("%s holds %q, not a pid", path, raw)
	}
	return pid
}

// --- the argv, which is D24 in code rather than in prose ---------------------

// The whole slice, not a substring of it. A -c:v libx264 added anywhere in here
// is a transcode, and D24 says there is never one — so the assertion is
// equality, and anybody adding a flag has to come and say so here.
func TestTheArgvIsCopyAndNothingElse(t *testing.T) {
	want := []string{
		"-nostdin", "-loglevel", "warning",
		"-i", "/library/Interstellar (2014)/Interstellar (2014).mkv",
		"-c", "copy",
		"-f", "mp4",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"pipe:1",
	}
	got := Args("/library/Interstellar (2014)/Interstellar (2014).mkv", 0)
	if !slices.Equal(got, want) {
		t.Errorf("argv =\n  %q\nwant\n  %q", got, want)
	}

	// -ss goes BEFORE -i, where it is an input option and seeks by keyframe. The
	// same flag after -i decodes and discards everything up to the offset, which
	// on a two-hour film is the difference between instant and minutes.
	seeking := Args("/library/f.mkv", 90*time.Second)
	ss := slices.Index(seeking, "-ss")
	if ss < 0 {
		t.Fatalf("no -ss in %q", seeking)
	}
	if seeking[ss+1] != "90" {
		t.Errorf("-ss %q, want 90", seeking[ss+1])
	}
	if input := slices.Index(seeking, "-i"); ss > input {
		t.Errorf("-ss is at %d and -i at %d: an input option was passed as an output one", ss, input)
	}

	// A fractional offset, because <video>.currentTime is one.
	if got := Args("/library/f.mkv", 1500*time.Millisecond); got[slices.Index(got, "-ss")+1] != "1.5" {
		t.Errorf("-ss %q, want 1.5", got[slices.Index(got, "-ss")+1])
	}
}

// And the argv as the process actually receives it, because Args being right is
// only half of it being passed correctly.
func TestFFmpegIsRunWithExactlyThatArgv(t *testing.T) {
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	ffmpeg := fake(t, `printf '%s\n' "$@" > `+strconv.Quote(argv)+`
printf 'FRAGMENTEDMP4'`)

	var out bytes.Buffer
	written, err := New(ffmpeg, 1).Stream(context.Background(), &out, "/library/f.mkv", 0)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got := out.String(); got != "FRAGMENTEDMP4" {
		t.Errorf("body = %q, want what the fake wrote", got)
	}
	if written != int64(len("FRAGMENTEDMP4")) {
		t.Errorf("written = %d, want %d", written, len("FRAGMENTEDMP4"))
	}

	raw, err := os.ReadFile(argv)
	if err != nil {
		t.Fatalf("the fake recorded no argv: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if want := Args("/library/f.mkv", 0); !slices.Equal(got, want) {
		t.Errorf("ffmpeg received\n  %q\nand Args says\n  %q", got, want)
	}
}

// --- the process dies with the request --------------------------------------

// The invariant, and the reason the kill is aimed at the process GROUP.
//
// The fake leaves a child holding the output pipe open, which is exactly the
// shape ffmpeg would have if it ever spawned anything: killing only the process
// this package started would leave the pipe open, io.Copy would never return,
// and a closed tab would leak a reader of a 12 GB file for as long as curator
// runs. So the test asserts both are gone, and it bounds its own wait rather
// than hanging for the package timeout when they are not.
func TestCancellingTheRequestKillsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	parent, child := filepath.Join(dir, "parent.pid"), filepath.Join(dir, "child.pid")
	ffmpeg := fake(t, `echo $$ > `+strconv.Quote(parent)+`
sleep 60 &
echo $! > `+strconv.Quote(child)+`
wait`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	var out bytes.Buffer
	go func() {
		_, err := New(ffmpeg, 1).Stream(ctx, &out, "/library/f.mkv", 0)
		done <- err
	}()

	waitFor(t, "the fake ffmpeg and its child to start", func() bool {
		a, errA := os.ReadFile(parent)
		b, errB := os.ReadFile(child)
		return errA == nil && errB == nil && len(bytes.TrimSpace(a)) > 0 && len(bytes.TrimSpace(b)) > 0
	})
	parentPID, childPID := readPID(t, parent), readPID(t, child)

	cancel()
	select {
	case err := <-done:
		// A cancelled request is the ordinary case — a closed tab — and comes
		// back as the context's own error so that a caller can stay quiet about
		// it rather than logging a failure every time somebody stops watching.
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Stream returned %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stream did not return after the request was cancelled: something is still " +
			"holding the output pipe open, which is the process group this test is about")
	}

	// The one this package started is reaped by Wait, so it is gone the moment
	// Stream returns and there is nothing to poll for.
	if alive(parentPID) {
		t.Errorf("ffmpeg (pid %d) outlived the request", parentPID)
	}
	// The child is reparented on the way out and reaped by init rather than by
	// us, so this one is a poll. Without the group kill it would sit there for
	// the full sixty seconds.
	waitFor(t, "the child ffmpeg left behind to be gone", func() bool { return !alive(childPID) })
}

// --- a non-zero exit ---------------------------------------------------------

// ffmpeg writes several lines a second at its default verbosity and none of it
// is logged while it is healthy; the tail exists for this exit and no other.
func TestANonZeroExitCarriesTheCapturedStderr(t *testing.T) {
	const complaint = "Invalid data found when processing input"
	ffmpeg := fake(t, `printf 'PARTIAL'
echo `+strconv.Quote(complaint)+` >&2
exit 1`)

	var out bytes.Buffer
	written, err := New(ffmpeg, 1).Stream(context.Background(), &out, "/library/f.mkv", 0)
	if err == nil {
		t.Fatal("a non-zero exit was reported as success")
	}
	if !strings.Contains(err.Error(), complaint) {
		t.Errorf("the error does not carry what ffmpeg said: %v", err)
	}
	// What it managed to write still reached the caller, and the byte count is
	// what tells a handler the response has already begun.
	if written != int64(len("PARTIAL")) || out.String() != "PARTIAL" {
		t.Errorf("written = %d, body = %q; want the bytes that did come out", written, out.String())
	}
}

// The bound, which is what keeps a misbehaving process out of the log tail that
// /api/logs serves (docs/decisions.md D18).
func TestTheStderrTailKeepsTheEndAndDropsTheRest(t *testing.T) {
	tl := &tail{max: 8}
	for _, chunk := range []string{"abcd", "efgh", "ijkl"} {
		if _, err := tl.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if got := tl.String(); got != "efghijkl" {
		t.Errorf("tail = %q, want the last 8 bytes", got)
	}

	// One write larger than the whole bound.
	tl = &tail{max: 8}
	if _, err := tl.Write(bytes.Repeat([]byte("x"), 100)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := tl.String(); len(got) != 8 {
		t.Errorf("tail = %d bytes, want 8", len(got))
	}
}

// --- the cap refuses rather than queues --------------------------------------

// Queueing would turn "the film is slow to start" into "the film never starts",
// which is worse and much harder to explain. So the N+1th is refused, at once,
// and a slot freeing lets the next one through.
func TestTheCapRefusesTheNextOneAndFreesItsSlot(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")

	// One fake with two behaviours, chosen by the file it is asked for: a long
	// film holds its slot until it is killed, and a short one exits by itself.
	// Two fakes would need two Remuxers, and the point of the last assertion is
	// that it is the SAME one whose slot came back.
	ffmpeg := fake(t, `echo started >> `+strconv.Quote(started)+`
case "$*" in
  *short.mkv*) printf 'OK'; exit 0;;
esac
sleep 60`)

	const limit = 2
	r := New(ffmpeg, limit)

	running := make([]context.CancelFunc, 0, limit)
	done := make(chan error, limit)
	for range limit {
		ctx, cancel := context.WithCancel(context.Background())
		running = append(running, cancel)
		go func() {
			_, err := r.Stream(ctx, discard{}, "/library/long.mkv", 0)
			done <- err
		}()
	}
	defer func() {
		for _, cancel := range running {
			cancel()
		}
	}()

	waitFor(t, "both slots to be occupied", func() bool {
		raw, err := os.ReadFile(started)
		return err == nil && bytes.Count(raw, []byte("\n")) == limit
	})

	// The one over the cap. Refused, and nothing was started for it.
	if _, err := r.Stream(context.Background(), discard{}, "/library/long.mkv", 0); !errors.Is(err, ErrBusy) {
		t.Fatalf("the %dth concurrent remux returned %v, want ErrBusy", limit+1, err)
	}
	raw, _ := os.ReadFile(started)
	if got := bytes.Count(raw, []byte("\n")); got != limit {
		t.Errorf("%d ffmpegs were started, want %d — the refused one ran anyway", got, limit)
	}

	// A slot freeing lets the next through, which is the half that makes this a
	// cap rather than a ceiling nothing ever comes back down from.
	running[0]()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a cancelled remux did not return, so its slot never came back")
	}

	var out bytes.Buffer
	if _, err := r.Stream(context.Background(), &out, "/library/short.mkv", 0); err != nil {
		t.Fatalf("the freed slot was not given back: %v", err)
	}
	if out.String() != "OK" {
		t.Errorf("body = %q, want the short film's OK", out.String())
	}
}

// discard is an io.Writer that keeps nothing. The concurrency tests care about
// how many processes are running, never about what they wrote.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// --- finding the binary ------------------------------------------------------

func TestFindTakesTheConfiguredPathAndThenThePATH(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	// An empty PATH, so the only ffmpeg findable is one this test put there.
	t.Setenv("PATH", "")

	if got, err := Find(binary); err != nil || got != binary {
		t.Errorf("Find(%q) = %q, %v; want the configured binary", binary, got, err)
	}

	// A configured path that is wrong is an error and NOT a quiet fall back to
	// PATH: a setting that names a binary and runs a different one is worse
	// than one that says it is broken.
	if _, err := Find(filepath.Join(dir, "not-here")); err == nil {
		t.Error("a configured path that does not exist was accepted")
	}

	// Empty means look on PATH.
	if _, err := Find(""); err == nil {
		t.Error("ffmpeg was found with an empty PATH")
	}
	t.Setenv("PATH", dir)
	if got, err := Find(""); err != nil || got != binary {
		t.Errorf("Find(\"\") = %q, %v; want the one on PATH", got, err)
	}
	// Whitespace is not a configuration.
	if got, err := Find("   "); err != nil || got != binary {
		t.Errorf("Find(\"   \") = %q, %v; want the one on PATH", got, err)
	}
}
