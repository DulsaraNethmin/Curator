package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DulsaraNethmin/curator/internal/logs"
)

func logServer(t *testing.T, tail LogTail) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	srv := New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, quiet())
	if tail != nil {
		srv = srv.WithLogs(tail)
	}
	srv.RegisterLogs(mux)
	return mux
}

// filled returns a buffer holding n entries written through a real slog
// handler, so the test exercises the same path production does.
func filled(t *testing.T, size, n int) *logs.Buffer {
	t.Helper()
	buf := logs.NewBuffer(size)
	log := slog.New(buf.Handler(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})))
	for i := 0; i < n; i++ {
		switch i % 4 {
		case 0:
			log.Debug("debug line", "i", i)
		case 1:
			log.Info("info line", "i", i)
		case 2:
			log.Warn("warn line", "i", i)
		default:
			log.Error("error line", "i", i)
		}
	}
	return buf
}

func getLogs(t *testing.T, h http.Handler, target string) (logsBody, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))

	var body logsBody
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s: %v — body was %s", target, err, rec.Body)
		}
	}
	return body, rec
}

func TestLogsReturnsTheTail(t *testing.T) {
	body, rec := getLogs(t, logServer(t, filled(t, 100, 12)), "/api/logs")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(body.Entries) != 12 {
		t.Fatalf("got %d entries, want 12", len(body.Entries))
	}
	if body.Cursor != 12 || body.Buffered != 12 || body.Missed != 0 {
		t.Errorf("cursor %d buffered %d missed %d", body.Cursor, body.Buffered, body.Missed)
	}
	// Newest last: a log reads top to bottom.
	if body.Entries[0].Seq != 1 || body.Entries[11].Seq != 12 {
		t.Errorf("sequences %d..%d, want 1..12", body.Entries[0].Seq, body.Entries[11].Seq)
	}
	if body.Entries[0].Attrs["i"] != "0" {
		t.Errorf("attributes were dropped: %+v", body.Entries[0].Attrs)
	}
}

// The cursor is what makes a two-second poll cheap: an idle tick transfers an
// empty array rather than the whole tail again.
func TestLogsCursorMakesAnIdlePollEmpty(t *testing.T) {
	h := logServer(t, filled(t, 100, 5))

	first, _ := getLogs(t, h, "/api/logs")
	if first.Cursor != 5 {
		t.Fatalf("cursor = %d, want 5", first.Cursor)
	}

	second, _ := getLogs(t, h, "/api/logs?since=5")
	if len(second.Entries) != 0 {
		t.Errorf("idle poll returned %d entries, want 0", len(second.Entries))
	}
	if second.Cursor != 5 {
		t.Errorf("idle poll moved the cursor to %d", second.Cursor)
	}
}

// A gap has to be reported. A log that silently skips lines is worse than one
// that admits it skipped them.
func TestLogsReportWhatFellOffTheRing(t *testing.T) {
	body, _ := getLogs(t, logServer(t, filled(t, 10, 30)), "/api/logs")

	if len(body.Entries) != 10 {
		t.Fatalf("got %d entries, want the ring size 10", len(body.Entries))
	}
	if body.Missed != 20 {
		t.Errorf("missed = %d, want 20", body.Missed)
	}
	if body.Buffered != 10 {
		t.Errorf("buffered = %d, want 10", body.Buffered)
	}
}

func TestLogsLevelFilter(t *testing.T) {
	h := logServer(t, filled(t, 100, 40)) // 10 of each level

	body, rec := getLogs(t, h, "/api/logs?level=warn")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, entry := range body.Entries {
		if entry.Level == "DEBUG" || entry.Level == "INFO" {
			t.Errorf("level=warn returned a %s entry", entry.Level)
		}
	}
	if len(body.Entries) != 20 {
		t.Errorf("got %d entries, want 20 (warn + error)", len(body.Entries))
	}

	// The cursor must still advance past the filtered-out entries, or a quiet
	// spell of DEBUG lines would make every poll re-deliver the same tail.
	if body.Cursor != 40 {
		t.Errorf("cursor = %d, want 40 — it tracks the buffer, not the filter", body.Cursor)
	}
}

// A typo must not look like a working filter.
func TestLogsRejectsBadParameters(t *testing.T) {
	h := logServer(t, filled(t, 100, 5))

	for _, target := range []string{
		"/api/logs?level=verbose",
		"/api/logs?level=trace",
		"/api/logs?since=abc",
		"/api/logs?since=-1",
		"/api/logs?limit=0",
		"/api/logs?limit=-5",
		"/api/logs?limit=lots",
	} {
		_, rec := getLogs(t, h, target)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", target, rec.Code)
		}
		var body struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
			t.Errorf("%s body = %s, want the {\"error\": \"...\"} shape", target, rec.Body)
		}
	}
}

func TestLogsLimit(t *testing.T) {
	body, _ := getLogs(t, logServer(t, filled(t, 100, 50)), "/api/logs?limit=5")

	if len(body.Entries) != 5 {
		t.Fatalf("got %d entries, want 5", len(body.Entries))
	}
	// The newest five, not the oldest: a caller behind the tail wants what just
	// happened, not ancient history.
	if body.Entries[4].Seq != 50 {
		t.Errorf("last seq = %d, want 50", body.Entries[4].Seq)
	}
	if body.Missed != 45 {
		t.Errorf("missed = %d, want 45", body.Missed)
	}
}

func TestLogsEmptyBufferIsAnEmptyArray(t *testing.T) {
	body, rec := getLogs(t, logServer(t, logs.NewBuffer(10)), "/api/logs")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body.Entries == nil {
		t.Error("entries is null; the UI iterates it")
	}
	if len(body.Entries) != 0 || body.Cursor != 0 {
		t.Errorf("got %d entries, cursor %d", len(body.Entries), body.Cursor)
	}
}

// The endpoint is mounted whether or not a buffer was attached, so an
// unconfigured one has to say so rather than panic.
func TestLogsWithoutABufferIs503(t *testing.T) {
	_, rec := getLogs(t, logServer(t, nil), "/api/logs")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}
