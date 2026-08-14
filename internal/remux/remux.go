// Package remux turns a container the browser refuses into one it does not, by
// running ffmpeg with the streams copied and nothing re-encoded.
//
// It is a package of its own — unlike the stream endpoint, which is a file open
// and http.ServeContent and lives in internal/api — because a subprocess with a
// lifetime is a thing with invariants. There are two, and both are here rather
// than in a handler so that no future caller can forget one: **the process dies
// with the request**, and **there is a cap on how many run at once**.
//
// It never transcodes (docs/decisions.md D24). `-c copy`, always. What a remux
// fixes is the container, which is most of what actually breaks: an H.264 + AAC
// stream inside an MKV is playable by every browser the moment it is inside a
// fragmented MP4 instead. What it cannot fix — HEVC a browser will not decode,
// DTS or TrueHD audio, VC-1 — stays unplayable, and the answer to that is the
// VLC link rather than a bigger ffmpeg.
package remux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DefaultConcurrent is how many remuxes may run at once.
//
// Three, because the household is three people and each one of these is a disk
// read and a pipe. The N+1th is refused rather than queued: queueing would turn
// "the film is slow to start" into "the film never starts", which is worse and
// much harder to explain to somebody holding a remote.
const DefaultConcurrent = 3

// stderrTailBytes bounds what is kept of ffmpeg's stderr.
//
// ffmpeg talks constantly — frame counters, bitrate, time, several lines a
// second — and the process log is a product surface that /api/logs serves
// (docs/decisions.md D18), so none of it is logged while the process is
// healthy. What is kept is the last few kilobytes, for the one moment anybody
// wants it, which is the exit that failed. Four kilobytes is comfortably more
// than the handful of lines an ffmpeg failure actually prints.
const stderrTailBytes = 4 << 10

// ErrBusy is every slot in use. The caller answers 503 and a Retry-After.
var ErrBusy = errors.New("every remux slot is in use")

// Remuxer runs ffmpeg, and holds the two invariants a subprocess brings.
type Remuxer struct {
	// ffmpeg is the resolved binary, as Find returned it. It is resolved once at
	// start-up rather than looked up per request: a PATH that changes under a
	// running process is not a case worth serving, and "which ffmpeg is this"
	// is a question the log should be able to answer.
	ffmpeg string

	// slots is the cap, as a buffered channel. A send that would block is the
	// refusal — there is no queue, on purpose.
	slots chan struct{}
}

// Find resolves the ffmpeg binary. configured is FFMPEG_PATH; empty means look
// on PATH.
//
// A miss is an ordinary state and not a start-up failure — absent ffmpeg means
// direct play only, the same posture an unset Jellyfin key already has
// (docs/decisions.md D15, D24). The error is returned rather than logged here
// so the one line about it is written where every other start-up decision is.
func Find(configured string) (string, error) {
	name := strings.TrimSpace(configured)
	if name == "" {
		name = "ffmpeg"
	}
	// LookPath answers both halves of this: a name with no separator in it is
	// searched for on PATH, and a path with one is checked directly for being
	// there and being executable. So a configured FFMPEG_PATH that is wrong is
	// reported as wrong rather than silently falling back to PATH — which would
	// be a setting that appears to work and is not the binary it names.
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("ffmpeg: %w", err)
	}
	return path, nil
}

// New builds a Remuxer around a resolved ffmpeg. concurrent below 1 means
// DefaultConcurrent.
func New(ffmpeg string, concurrent int) *Remuxer {
	if concurrent < 1 {
		concurrent = DefaultConcurrent
	}
	return &Remuxer{ffmpeg: ffmpeg, slots: make(chan struct{}, concurrent)}
}

// Path is the binary this will run. It is what start-up logs and what the
// settings screen reports, because "why is there no fallback" is otherwise a
// question with no evidence.
func (r *Remuxer) Path() string { return r.ffmpeg }

// Args is the whole of the invocation, and every flag in it is load-bearing.
//
//	ffmpeg -nostdin -loglevel warning [-ss <start>] -i <file> \
//	       -c copy -f mp4 -movflags frag_keyframe+empty_moov+default_base_moof pipe:1
//
// -nostdin, or ffmpeg reads the server's own stdin and a terminal that is not
// its own. -loglevel warning cuts the frame counters that would otherwise fill
// the log tail. -ss goes BEFORE -i, where it is an input option: that seeks by
// keyframe and is effectively instant, where the same flag after -i decodes and
// discards everything up to the offset. -f mp4 is explicit because pipe:1 has no
// extension for ffmpeg to guess a container from, and the failure when it
// guesses is confusing. frag_keyframe+empty_moov+default_base_moof is what makes
// the output playable BEFORE it is finished — an ordinary MP4 puts its moov atom
// at the end of the file, which a stream never reaches.
//
// And -c copy is the whole decision. No -c:v, no -crf, no -preset, no filter
// (docs/decisions.md D24). It is exported and asserted as a whole slice in the
// tests so that adding one cannot pass a build.
func Args(file string, start time.Duration) []string {
	args := make([]string, 0, 14)
	args = append(args, "-nostdin", "-loglevel", "warning")
	if start > 0 {
		// Formatted from the parsed duration and never from the query string it
		// came out of, which is what stops a request pasting a flag into an argv.
		args = append(args, "-ss", strconv.FormatFloat(start.Seconds(), 'f', -1, 64))
	}
	args = append(args,
		"-i", file,
		"-c", "copy",
		"-f", "mp4",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"pipe:1",
	)
	return args
}

// Stream runs one remux of file into w, seeking to start first, and returns how
// many bytes reached w.
//
// The byte count is not a statistic: it is what tells the caller whether the
// response has already begun, and therefore whether a failure can still become
// a status code or has to be left to the browser's error event.
//
// ErrBusy means the cap was hit and nothing was started. A cancelled ctx comes
// back as context.Canceled, which is the ordinary case — a closed tab — and is
// not a failure. Anything else carries ffmpeg's captured stderr in its message,
// once, for whoever logs it.
func (r *Remuxer) Stream(ctx context.Context, w io.Writer, file string, start time.Duration) (int64, error) {
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	default:
		// No queue. See DefaultConcurrent.
		return 0, ErrBusy
	}

	cmd := exec.CommandContext(ctx, r.ffmpeg, Args(file, start)...)

	// Its own process group, and the GROUP is what gets killed. ffmpeg spawns
	// nothing today, so the plain CommandContext kill would do — but a process
	// that outlives its reader is a file handle and a disk read on a 12 GB file
	// that nobody will ever collect, and a closed tab is the ordinary case here
	// rather than the exceptional one. Setpgid is darwin and linux, which are
	// the two platforms this ships on; the arm64 cross-compile is the gate that
	// says so.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// A negative pid is the group. Cancel only runs after Start, so
		// cmd.Process is set.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			// It exited on its own between the context being done and this
			// call. Saying so is what stops Wait reporting a kill that was not
			// needed as the reason the response ended.
			return os.ErrProcessDone
		}
		return err
	}

	// Read after Wait and never during, which is what makes the lack of a mutex
	// correct: os/exec copies stderr on its own goroutine and Wait waits for it,
	// so the write and the read are ordered by Wait rather than by luck. `go
	// test -race` is the thing that keeps that true.
	stderr := &tail{max: stderrTailBytes}
	cmd.Stderr = stderr

	out, err := cmd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("remux %s: %w", file, err)
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("remux %s: %w", file, err)
	}

	// Copy to completion before Wait, always: Wait closes the pipe when it sees
	// the process exit, so waiting first would truncate the film.
	written, copyErr := io.Copy(w, out)
	waitErr := cmd.Wait()

	switch {
	case ctx.Err() != nil:
		// A closed tab, which is not a failure and must not be logged as one.
		// It is checked first because a killed ffmpeg also produces a non-zero
		// wait and a broken pipe, and neither of those is the reason.
		return written, ctx.Err()
	case waitErr != nil:
		// The captured stderr travels in the error rather than being logged
		// here, so that it reaches the log exactly once and does so beside the
		// film it is about.
		return written, fmt.Errorf("ffmpeg %s: %w: %s", file, waitErr, stderr.String())
	case copyErr != nil:
		return written, fmt.Errorf("remux %s: %w", file, copyErr)
	}
	return written, nil
}

// tail keeps the last max bytes written to it and drops the rest.
//
// Bytes rather than lines, because it is a bound that holds whatever ffmpeg
// writes — a line-based one assumes there are newlines, and the case this
// exists for is the one where the process is misbehaving.
type tail struct {
	max int
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		// Copied down rather than resliced from the front, so the backing array
		// stays put instead of being reallocated on every write once the tail
		// is full.
		copy(t.buf, t.buf[len(t.buf)-t.max:])
		t.buf = t.buf[:t.max]
	}
	return len(p), nil
}

func (t *tail) String() string { return strings.TrimSpace(string(t.buf)) }
