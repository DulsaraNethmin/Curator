package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
