package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DulsaraNethmin/curator/internal/download"
	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/torrent"
)

func deleteServer(t *testing.T, d Dispatcher) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	srv := New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, quiet()).WithDownloads(d)
	srv.Register(mux)
	srv.RegisterMovieDelete(mux)
	return mux
}

func deleteMovie(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, target, nil))
	return rec
}

func TestDeleteMovieReportsWhatWentAndHowMuchWasFreed(t *testing.T) {
	fake := &fakeDispatcher{deletion: download.Deletion{
		Title: "Interstellar", Year: 2014,
		LibraryPath:     "/library/movies/Interstellar (2014)",
		TorrentsRemoved: 1, BytesFreed: 3_219_186_473,
	}}
	rec := deleteMovie(t, deleteServer(t, fake), "/api/movies/7")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if fake.deletedID != 7 {
		t.Errorf("deleted id = %d, want 7", fake.deletedID)
	}

	var got download.Deletion
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — body was %s", err, rec.Body)
	}
	// The caller is told exactly what was destroyed. This is the only request in
	// curator that destroys anything.
	if got.BytesFreed != 3_219_186_473 || got.TorrentsRemoved != 1 {
		t.Errorf("report = %+v", got)
	}
}

func TestDeleteMovieStatusCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"no such movie", fmt.Errorf("delete movie 7: %w", store.ErrNotFound), http.StatusNotFound},
		// The guard fired: the request was well-formed and the refusal is
		// deliberate, because the *arr stack shares that qBittorrent.
		{"the torrent belongs to radarr", fmt.Errorf("delete: %w", torrent.ErrWrongCategory), http.StatusConflict},
		{"downloads unconfigured", download.ErrUnconfigured, http.StatusServiceUnavailable},
		{"qBittorrent unreachable", fmt.Errorf("delete: %w: refused", download.ErrClient), http.StatusBadGateway},
		{"database is unwell", errors.New("database is locked"), http.StatusInternalServerError},
	}

	for _, c := range cases {
		fake := &fakeDispatcher{deleteErr: c.err}
		rec := deleteMovie(t, deleteServer(t, fake), "/api/movies/7")
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body)
		}
		var body struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
			t.Errorf("%s: body = %s, want the {\"error\": \"...\"} shape", c.name, rec.Body)
		}
	}
}

func TestDeleteMovieRejectsANonNumericID(t *testing.T) {
	fake := &fakeDispatcher{}
	rec := deleteMovie(t, deleteServer(t, fake), "/api/movies/not-a-number")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if fake.deletes != 0 {
		t.Error("a malformed id still reached the dispatcher")
	}
}

// GET must keep working on the same pattern: DELETE /api/movies/{id} and
// GET /api/movies/{id} are different routes and Go 1.22 has to keep them apart.
func TestDeleteRouteDoesNotShadowGet(t *testing.T) {
	fake := &fakeDispatcher{}
	h := deleteServer(t, fake)

	rec := do(t, h, http.MethodGet, "/api/movies/1")
	if rec.Code == http.StatusMethodNotAllowed {
		t.Fatal("GET /api/movies/{id} stopped working when DELETE was added")
	}
	if fake.deletes != 0 {
		t.Error("a GET reached the delete handler")
	}
}

// The refusal is a sentence somebody wrote, and the chain that produced it is
// not on the wire (T71, D40).
//
// The string this replaced was three packages of prefixes ending in the info
// hash twice in two different cases — "delete movie 7: removing torrent
// 2B6A…: the torrent client: qbit torrents/delete: 2b6a… is in category
// "radarr", not "curator": the torrent is not in the required category" — so
// what is asserted is mostly an ABSENCE. Every one of these substrings was in
// the body before and none of them is a thing a reader can act on.
func TestDeleteRefusalIsWrittenForAHuman(t *testing.T) {
	leaks := []string{
		"delete movie", "removing torrent", "the torrent client",
		"qbit", "engine:", "torrents/delete", deleteHash, "required category",
	}

	// With the category: the one word a reader can act on survives.
	fake := &fakeDispatcher{deleteErr: fmt.Errorf("delete movie 7: removing torrent %s: %w: %w",
		deleteHash, download.ErrClient, fmt.Errorf("qbit torrents/delete: %w", torrent.WrongCategory{
			Hash: deleteHash, Actual: "radarr", Required: "curator",
		}))}
	rec := deleteMovie(t, deleteServer(t, fake), "/api/movies/7")

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body)
	}
	got := errorBody(t, rec)
	if !strings.Contains(got, `"radarr"`) {
		t.Errorf("body does not name the owning category, which is the only actionable word in it: %q", got)
	}
	// Nothing was deleted, and the sentence has to say so — the sentinel alone
	// reads like a partial delete.
	if !strings.Contains(got, "still in your library") {
		t.Errorf("body does not say the film survived: %q", got)
	}
	for _, leak := range leaks {
		if strings.Contains(got, leak) {
			t.Errorf("body leaks %q from the error chain: %q", leak, got)
		}
	}

	// Without it: a bare wrapped sentinel is what six other callers produce and
	// what every pre-T71 test constructs, and the sentence must still be true.
	fake = &fakeDispatcher{deleteErr: fmt.Errorf("delete: %w", torrent.ErrWrongCategory)}
	rec = deleteMovie(t, deleteServer(t, fake), "/api/movies/7")

	if rec.Code != http.StatusConflict {
		t.Fatalf("fallback: status = %d, want 409 (%s)", rec.Code, rec.Body)
	}
	got = errorBody(t, rec)
	if !strings.Contains(got, "another application") || !strings.Contains(got, "still in your library") {
		t.Errorf("fallback sentence is not the written one: %q", got)
	}
	for _, leak := range leaks {
		if strings.Contains(got, leak) {
			t.Errorf("fallback body leaks %q: %q", leak, got)
		}
	}
}

// Both backends must produce the same sentence. Before T71 they wrapped the
// sentinel with their own prefixes — "qbit torrents/delete: " and "engine: " —
// so which words a user was shown depended on TORRENT_BACKEND.
func TestDeleteRefusalReadsTheSameOnBothBackends(t *testing.T) {
	sentence := func(prefix string) string {
		fake := &fakeDispatcher{deleteErr: fmt.Errorf("%s: %w", prefix, torrent.WrongCategory{
			Hash: deleteHash, Actual: "radarr", Required: "curator",
		})}
		rec := deleteMovie(t, deleteServer(t, fake), "/api/movies/7")
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s: status = %d, want 409", prefix, rec.Code)
		}
		return errorBody(t, rec)
	}

	if qbit, engine := sentence("qbit torrents/delete"), sentence("engine"); qbit != engine {
		t.Errorf("the backend changes what the user reads:\n qbit:   %q\n engine: %q", qbit, engine)
	}
}
