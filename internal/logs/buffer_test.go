package logs

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func newLogger(b *Buffer) *slog.Logger {
	return slog.New(b.Handler(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})))
}

// The test this package exists to survive.
//
// A log line used to go to stderr on a machine you had to SSH into. Behind
// GET /api/logs it goes to any browser on the LAN, with no authentication in
// front of it, so a secret reaching a log line is now a secret reaching the
// network.
func TestSecretsAreScrubbedFromMessagesAndAttributes(t *testing.T) {
	const (
		tmdbKey  = "TMDB-SECRET-3f1c9a"
		qbitPass = "QBIT-SECRET-77b210"
	)

	buf := NewBuffer(50, tmdbKey, qbitPass, "")
	log := newLogger(buf)

	// Every shape a secret could arrive in.
	log.Info("calling https://api.themoviedb.org/3/search/movie?api_key=" + tmdbKey)
	log.Warn("login failed", "url", "http://127.0.0.1:8080?password="+qbitPass)
	log.Error("boom", "err", "dial failed for "+tmdbKey+" and "+qbitPass)
	log.With("session", qbitPass).Info("with-attrs carries it too")

	entries, _, _ := buf.Since(0, 100)
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4", len(entries))
	}

	for _, entry := range entries {
		haystack := entry.Msg
		for key, value := range entry.Attrs {
			haystack += " " + key + "=" + value
		}
		for _, secret := range []string{tmdbKey, qbitPass, "3f1c9a", "77b210"} {
			if strings.Contains(haystack, secret) {
				t.Errorf("secret %q survived into %q", secret, haystack)
			}
		}
		if !strings.Contains(haystack, Redacted) {
			t.Errorf("nothing was redacted in %q", haystack)
		}
	}
}

// An unset QBIT_PASS is the empty string, and redacting "" would rewrite every
// line into nonsense. A two-character secret would mangle unrelated words while
// protecting nothing.
func TestShortAndEmptySecretsAreIgnored(t *testing.T) {
	buf := NewBuffer(10, "", "  ", "ab", "abcdef")
	newLogger(buf).Info("the quick brown fox jumps over ab and abcdef")

	entries, _, _ := buf.Since(0, 10)
	got := entries[0].Msg
	if !strings.Contains(got, "the quick brown fox") {
		t.Fatalf("the message was mangled: %q", got)
	}
	if strings.Contains(got, "abcdef") {
		t.Errorf("a six-character secret was not redacted: %q", got)
	}
	if !strings.Contains(got, " ab ") {
		t.Errorf("a two-character secret was redacted and mangled the line: %q", got)
	}
}

// stderr must keep getting exactly what it got before. The buffer is additive:
// delete this package and the logs are unchanged.
func TestTheWrappedHandlerStillReceivesEverything(t *testing.T) {
	var sink strings.Builder
	buf := NewBuffer(10)
	log := slog.New(buf.Handler(slog.NewTextHandler(&sink, nil)))

	log.Info("visible on stderr", "hash", "ABC123")

	if !strings.Contains(sink.String(), "visible on stderr") || !strings.Contains(sink.String(), "ABC123") {
		t.Errorf("the wrapped handler did not receive the record: %q", sink.String())
	}
	if buf.Len() != 1 {
		t.Errorf("buffer holds %d, want 1", buf.Len())
	}
}

func TestSinceIsACursor(t *testing.T) {
	buf := NewBuffer(100)
	log := newLogger(buf)

	for i := 0; i < 5; i++ {
		log.Info("line")
	}

	entries, cursor, missed := buf.Since(0, 100)
	if len(entries) != 5 || cursor != 5 || missed != 0 {
		t.Fatalf("first poll: %d entries, cursor %d, missed %d", len(entries), cursor, missed)
	}
	if entries[0].Seq != 1 || entries[4].Seq != 5 {
		t.Errorf("sequences are %d..%d, want 1..5", entries[0].Seq, entries[4].Seq)
	}

	// Nothing new: an idle poll costs an empty array, not the whole tail again.
	entries, cursor, _ = buf.Since(cursor, 100)
	if len(entries) != 0 || cursor != 5 {
		t.Errorf("idle poll returned %d entries, cursor %d", len(entries), cursor)
	}

	log.Info("one more")
	entries, cursor, _ = buf.Since(cursor, 100)
	if len(entries) != 1 || entries[0].Msg != "one more" || cursor != 6 {
		t.Errorf("incremental poll returned %+v, cursor %d", entries, cursor)
	}
}

// The ring drops the oldest, and says how many it dropped. A UI that showed a
// discontinuous log without saying so costs somebody an hour.
func TestOverflowReportsWhatWasMissed(t *testing.T) {
	buf := NewBuffer(10)
	log := newLogger(buf)

	for i := 0; i < 25; i++ {
		log.Info("line")
	}

	if buf.Len() != 10 {
		t.Errorf("Len = %d, want the ring size 10", buf.Len())
	}

	entries, cursor, missed := buf.Since(0, 100)
	if len(entries) != 10 {
		t.Fatalf("got %d entries, want 10", len(entries))
	}
	if cursor != 25 {
		t.Errorf("cursor = %d, want 25", cursor)
	}
	// Sequences 1..15 fell off the ring.
	if missed != 15 {
		t.Errorf("missed = %d, want 15", missed)
	}
	if entries[0].Seq != 16 || entries[9].Seq != 25 {
		t.Errorf("sequences are %d..%d, want 16..25", entries[0].Seq, entries[9].Seq)
	}

	// A client whose cursor fell off the ring is told, rather than silently
	// resynchronised.
	_, _, missed = buf.Since(3, 100)
	if missed != 12 {
		t.Errorf("stale cursor: missed = %d, want 12", missed)
	}
}

// A caller far behind gets the newest lines, not ancient history while the live
// tail runs away from it.
func TestLimitReturnsTheNewest(t *testing.T) {
	buf := NewBuffer(100)
	log := newLogger(buf)
	for i := 0; i < 50; i++ {
		log.Info("line")
	}

	entries, cursor, missed := buf.Since(0, 10)
	if len(entries) != 10 {
		t.Fatalf("got %d entries, want 10", len(entries))
	}
	if entries[9].Seq != 50 || entries[0].Seq != 41 {
		t.Errorf("sequences are %d..%d, want 41..50", entries[0].Seq, entries[9].Seq)
	}
	if cursor != 50 || missed != 40 {
		t.Errorf("cursor %d missed %d, want 50 and 40", cursor, missed)
	}
}

func TestEmptyBuffer(t *testing.T) {
	buf := NewBuffer(10)
	entries, cursor, missed := buf.Since(0, 100)
	if len(entries) != 0 || cursor != 0 || missed != 0 {
		t.Errorf("empty buffer returned %d entries, cursor %d, missed %d", len(entries), cursor, missed)
	}
}

// Attributes added with .With() must survive onto the buffered copy, or the
// stored line says less than the one on stderr.
func TestWithAttrsAndGroupsAreKept(t *testing.T) {
	buf := NewBuffer(10)
	log := newLogger(buf).With("component", "poller")

	log.Info("tick", "hash", "ABC")
	log.WithGroup("qbit").Info("call", "status", "200")

	entries, _, _ := buf.Since(0, 10)
	if got := entries[0].Attrs["component"]; got != "poller" {
		t.Errorf("component = %q, want the attr added with .With()", got)
	}
	if got := entries[0].Attrs["hash"]; got != "ABC" {
		t.Errorf("hash = %q", got)
	}
	if got := entries[1].Attrs["qbit.status"]; got != "200" {
		t.Errorf("grouped attr = %q, want the group prefix", got)
	}
}

func TestLevelsAreRecorded(t *testing.T) {
	buf := NewBuffer(10)
	log := newLogger(buf)

	log.Debug("d")
	log.Info("i")
	log.Warn("w")
	log.Error("e")

	entries, _, _ := buf.Since(0, 10)
	want := []string{"DEBUG", "INFO", "WARN", "ERROR"}
	if len(entries) != len(want) {
		t.Fatalf("got %d entries, want %d", len(entries), len(want))
	}
	for i, level := range want {
		if entries[i].Level != level {
			t.Errorf("entry %d level = %q, want %q", i, entries[i].Level, level)
		}
	}
}

// The poller, every handler and the importer log from different goroutines.
func TestConcurrentWritesAndReads(t *testing.T) {
	buf := NewBuffer(64)
	log := newLogger(buf)

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				log.Info("concurrent", "worker", w)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		var cursor uint64
		for i := 0; i < 200; i++ {
			_, cursor, _ = buf.Since(cursor, 50)
		}
	}()
	wg.Wait()

	if buf.Len() != 64 {
		t.Errorf("Len = %d, want 64", buf.Len())
	}
	if _, cursor, _ := buf.Since(0, 100); cursor != 800 {
		t.Errorf("cursor = %d, want 800 records", cursor)
	}
}

func TestEnabledDefersToTheWrappedHandler(t *testing.T) {
	buf := NewBuffer(10)
	handler := buf.Handler(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))

	if handler.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("Info is enabled though the wrapped handler is set to Warn")
	}
	if !handler.Enabled(context.Background(), slog.LevelError) {
		t.Error("Error is not enabled")
	}

	// And a disabled level is never recorded, so the buffer cannot fill with
	// lines stderr never saw.
	log := slog.New(handler)
	log.Info("suppressed")
	if buf.Len() != 0 {
		t.Errorf("buffer holds %d, want 0 — the level was disabled", buf.Len())
	}
}
