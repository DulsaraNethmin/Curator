package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/download"
	"github.com/DulsaraNethmin/curator/internal/indexer"
	"github.com/DulsaraNethmin/curator/internal/store"
)

// fakeDispatcher stands in for phase 3's download service.
type fakeDispatcher struct {
	saved      store.Download
	err        error
	list       []store.Download
	listErr    error
	gotReq     download.Request
	dispatches int

	// Phase 4's manual import. The Dispatcher interface grew a method, so this
	// fake grew one too; the import handler's own tests are in imports_test.go.
	imported  store.Movie
	importErr error
	gotHash   string
	imports   int
}

func (f *fakeDispatcher) Dispatch(_ context.Context, req download.Request) (store.Download, error) {
	f.dispatches++
	f.gotReq = req
	if f.err != nil {
		return store.Download{}, f.err
	}
	return f.saved, nil
}

func (f *fakeDispatcher) Downloads(context.Context) ([]store.Download, error) {
	return f.list, f.listErr
}

func (f *fakeDispatcher) Import(_ context.Context, hash string) (store.Movie, error) {
	f.imports++
	f.gotHash = hash
	if f.importErr != nil {
		return store.Movie{}, f.importErr
	}
	return f.imported, nil
}

func newDownloadServer(t *testing.T, d Dispatcher) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	srv := New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, quiet()).
		WithSearch(&fakeSearcher{}).
		WithDownloads(d)
	srv.Register(mux)
	srv.RegisterSearch(mux)
	srv.RegisterDownloads(mux)
	return mux
}

func post(t *testing.T, h http.Handler, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewBufferString(body))
	req.Header.Set("content-type", "application/json")
	h.ServeHTTP(rec, req)
	return rec
}

func savedDownload() store.Download {
	return store.Download{
		ID: 1, MovieID: 7, TorrentHash: "89599BF4DC369A3A8ECA26411C5CCF922D78B486",
		Indexer: "yts,tpb", ReleaseName: "Interstellar.2014.1080p.BluRay.x265",
		Magnet: "magnet:?xt=urn:btih:89599BF4DC369A3A8ECA26411C5CCF922D78B486",
		State:  store.DownloadQueued, AddedAt: time.Now().UTC(),
	}
}

const dispatchBody = `{"release_id":"3f2a9c1b7d4e5a60","title":"Interstellar","year":2014}`

func TestDispatchReturns201AndTheRow(t *testing.T) {
	fake := &fakeDispatcher{saved: savedDownload()}
	rec := post(t, newDownloadServer(t, fake), "/api/downloads", dispatchBody)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var got store.Download
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.TorrentHash != savedDownload().TorrentHash {
		t.Errorf("hash = %q", got.TorrentHash)
	}
	if fake.gotReq.ReleaseID != "3f2a9c1b7d4e5a60" || fake.gotReq.Title != "Interstellar" || fake.gotReq.Year != 2014 {
		t.Errorf("dispatched %+v", fake.gotReq)
	}
}

// An expired search is 410, not 404 and not 500: the id was real, and searching
// again produces a working one.
func TestDispatchExpiredReleaseIs410(t *testing.T) {
	fake := &fakeDispatcher{err: fmt.Errorf("dispatch: %w", indexer.ErrReleaseExpired)}
	rec := post(t, newDownloadServer(t, fake), "/api/downloads", dispatchBody)
	msg := decodeError(t, rec, http.StatusGone)
	if msg == "" {
		t.Error("410 carried no message")
	}
}

// The request was fine and a dependency was not, so it is 502 — a 500 would blame
// curator for qBittorrent's bad day.
func TestDispatchUnreachableQBittorrentIs502(t *testing.T) {
	fake := &fakeDispatcher{err: fmt.Errorf("dispatch: %w: connection refused", download.ErrClient)}
	rec := post(t, newDownloadServer(t, fake), "/api/downloads", dispatchBody)
	decodeError(t, rec, http.StatusBadGateway)
}

// Unconfigured is a deployment state, not a failure of this request.
func TestDispatchUnconfiguredIs503(t *testing.T) {
	fake := &fakeDispatcher{err: download.ErrUnconfigured}
	rec := post(t, newDownloadServer(t, fake), "/api/downloads", dispatchBody)
	msg := decodeError(t, rec, http.StatusServiceUnavailable)
	if !bytes.Contains([]byte(msg), []byte("QBIT_USER")) {
		t.Errorf("error = %q, want it to name the missing variable", msg)
	}
}

// Anything else really is ours.
func TestDispatchDatabaseFailureIs500(t *testing.T) {
	fake := &fakeDispatcher{err: errors.New("database is locked")}
	rec := post(t, newDownloadServer(t, fake), "/api/downloads", dispatchBody)
	decodeError(t, rec, http.StatusInternalServerError)
}

func TestDispatchValidation(t *testing.T) {
	tests := []struct {
		name, body string
	}{
		{"missing release_id", `{"title":"Interstellar","year":2014}`},
		{"blank release_id", `{"release_id":"  ","title":"Interstellar","year":2014}`},
		{"missing title", `{"release_id":"abc","year":2014}`},
		{"blank title", `{"release_id":"abc","title":"   ","year":2014}`},
		{"non-numeric year", `{"release_id":"abc","title":"Interstellar","year":"twenty fourteen"}`},
		{"negative year", `{"release_id":"abc","title":"Interstellar","year":-5}`},
		{"not json", `not json at all`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeDispatcher{saved: savedDownload()}
			rec := post(t, newDownloadServer(t, fake), "/api/downloads", tt.body)
			decodeError(t, rec, http.StatusBadRequest)
			if fake.dispatches != 0 {
				t.Errorf("a rejected request still reached the dispatcher")
			}
		})
	}
}

func TestListDownloads(t *testing.T) {
	fake := &fakeDispatcher{list: []store.Download{savedDownload()}}
	rec := do(t, newDownloadServer(t, fake), http.MethodGet, "/api/downloads")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []store.Download
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].ReleaseName != savedDownload().ReleaseName {
		t.Errorf("downloads = %+v", got)
	}
}

func TestListDownloadsEmptyIsNotNull(t *testing.T) {
	rec := do(t, newDownloadServer(t, &fakeDispatcher{}), http.MethodGet, "/api/downloads")
	if body := rec.Body.String(); !bytes.Contains([]byte(body), []byte("[]")) {
		t.Errorf("body = %s, want [] — nothing downloading is a list with nothing in it", body)
	}
}

// A download that has not finished has no completion time, and a zero timestamp
// would be a lie the UI would render.
func TestUnfinishedDownloadHasNullCompletedAt(t *testing.T) {
	fake := &fakeDispatcher{list: []store.Download{savedDownload()}}
	rec := do(t, newDownloadServer(t, fake), http.MethodGet, "/api/downloads")

	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := string(raw[0]["completed_at"]); got != "null" {
		t.Errorf("completed_at = %s, want null", got)
	}
}

// Phases 1 and 2 must keep answering on a server that also serves downloads.
func TestEarlierPhasesSurviveDownloadRegistration(t *testing.T) {
	h := newDownloadServer(t, &fakeDispatcher{})
	for _, target := range []string{"/api/movies", "/api/search?title=Interstellar"} {
		if rec := do(t, h, http.MethodGet, target); rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", target, rec.Code)
		}
	}
	if rec := do(t, h, http.MethodGet, "/api/movies/999"); rec.Code != http.StatusNotFound {
		t.Errorf("/api/movies/999 = %d, want 404", rec.Code)
	}
}
