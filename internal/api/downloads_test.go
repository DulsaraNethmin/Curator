package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

	// D19's delete path.
	deletion  download.Deletion
	deleteErr error
	deletedID int64
	deletes   int
}

func (f *fakeDispatcher) DeleteMovie(_ context.Context, id int64) (download.Deletion, error) {
	f.deletes++
	f.deletedID = id
	if f.deleteErr != nil {
		return download.Deletion{}, f.deleteErr
	}
	return f.deletion, nil
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
	return newDownloadServerWithStore(t, d, newFakeStore())
}

func newDownloadServerWithStore(t *testing.T, d Dispatcher, st Store) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	srv := New(st, ScannerFunc(nil), nil, fixtureRoot, quiet()).
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

// T55: GET /api/downloads carries the stall reason, and omits the key entirely
// when there is nothing to explain.
//
// Asserted against the raw JSON on purpose. A typed decode cannot tell an
// absent key from `"reason": ""`, and the difference is the whole contract the
// screen is written against: a key that is there is a sentence to render.
// Nothing was added to internal/api to carry it — the handler responds with
// store.Download, so the field is the store's and a copy here would be a second
// shape to keep in step.
func TestListDownloadsCarriesTheStallReason(t *testing.T) {
	const why = "no peers are connected — nobody appears to be seeding this release"
	stalled := savedDownload()
	stalled.State = store.DownloadStalled
	stalled.Reason = why

	fake := &fakeDispatcher{list: []store.Download{stalled, savedDownload()}}
	rec := do(t, newDownloadServer(t, fake), http.MethodGet, "/api/downloads")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw) != 2 {
		t.Fatalf("downloads = %d, want 2", len(raw))
	}

	var got string
	if err := json.Unmarshal(raw[0]["reason"], &got); err != nil {
		t.Fatalf("decode reason: %v", err)
	}
	if got != why {
		t.Errorf("reason = %q, want the backend's sentence verbatim — the UI renders it and translates nothing", got)
	}
	if _, present := raw[1]["reason"]; present {
		t.Errorf("reason = %s on a download with nothing wrong, want the key absent",
			raw[1]["reason"])
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

// --- refusing a film curator already has ------------------------------------

// dispatchBodyWithTMDB is the movie page's dispatch: it carries the TMDB id,
// which is what makes the check below possible at all.
const dispatchBodyWithTMDB = `{"release_id":"3f2a9c1b7d4e5a60","title":"Interstellar","year":2014,"tmdb_id":157336}`

func libraryStore(state store.LibraryState) *fakeStore {
	st := newFakeStore()
	st.library = map[int64]store.LibraryState{157336: state}
	return st
}

func TestDispatchRefusesAFilmAlreadyInTheLibrary(t *testing.T) {
	path := "/library/movies/Interstellar (2014)"
	fake := &fakeDispatcher{saved: savedDownload()}
	st := libraryStore(store.LibraryState{MovieID: 7, Status: store.StatusImported, LibraryPath: &path})

	rec := post(t, newDownloadServerWithStore(t, fake, st), "/api/downloads", dispatchBodyWithTMDB)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	assertErrorBody(t, rec)
	if !strings.Contains(rec.Body.String(), path) {
		t.Errorf("body = %s, want it to name the path", rec.Body.String())
	}
	// The refusal has to cost nothing: no release resolved, no torrent added.
	if fake.dispatches != 0 {
		t.Errorf("dispatched %d times, want 0 — a refusal must leave nothing behind", fake.dispatches)
	}
}

// The one an over-eager reading of "do not download what we already have" would
// break. A film mid-download is not "already downloaded", and when a torrent
// stalls, dispatching a different release is exactly what you want to do.
func TestDispatchAllowsAFilmThatIsStillDownloading(t *testing.T) {
	fake := &fakeDispatcher{saved: savedDownload()}
	st := libraryStore(store.LibraryState{MovieID: 7, Status: store.StatusWanted, Downloading: true})

	rec := post(t, newDownloadServerWithStore(t, fake, st), "/api/downloads", dispatchBodyWithTMDB)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 — a stalled torrent has to be replaceable: %s", rec.Code, rec.Body.String())
	}
	if fake.dispatches != 1 {
		t.Errorf("dispatched %d times, want 1", fake.dispatches)
	}
}

func TestDispatchAllowsAWantedFilm(t *testing.T) {
	fake := &fakeDispatcher{saved: savedDownload()}
	st := libraryStore(store.LibraryState{MovieID: 7, Status: store.StatusWanted})

	rec := post(t, newDownloadServerWithStore(t, fake, st), "/api/downloads", dispatchBodyWithTMDB)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
}

// Status and library_path agree in every row the importer writes. Requiring both
// means a half-written row refuses nothing rather than refusing wrongly.
func TestDispatchAllowsAnImportedRowWithNoLibraryPath(t *testing.T) {
	fake := &fakeDispatcher{saved: savedDownload()}
	st := libraryStore(store.LibraryState{MovieID: 7, Status: store.StatusImported}) // LibraryPath nil

	rec := post(t, newDownloadServerWithStore(t, fake, st), "/api/downloads", dispatchBodyWithTMDB)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
}

// The deliberate hole, named so it is found by reading rather than by surprise:
// /search's release-name mode dispatches with no tmdb_id, and there is no
// reliable identity for a film without one.
func TestDispatchWithNoTMDBIDIsNotRefused(t *testing.T) {
	path := "/library/movies/Interstellar (2014)"
	fake := &fakeDispatcher{saved: savedDownload()}
	st := libraryStore(store.LibraryState{MovieID: 7, Status: store.StatusImported, LibraryPath: &path})

	rec := post(t, newDownloadServerWithStore(t, fake, st), "/api/downloads", dispatchBody)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 — nothing can be concluded without a tmdb_id: %s", rec.Code, rec.Body.String())
	}
}

func TestDispatchLibraryLookupFailureIs500(t *testing.T) {
	fake := &fakeDispatcher{saved: savedDownload()}
	st := newFakeStore()
	st.libraryErr = errors.New("database is locked")

	rec := post(t, newDownloadServerWithStore(t, fake, st), "/api/downloads", dispatchBodyWithTMDB)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if fake.dispatches != 0 {
		t.Error("dispatched despite not knowing whether the film is already there")
	}
}

// The two dispatch failures that were not written: the 502 when the client is
// unreachable, and the 503 when the VPN guard refuses.
//
// The 503 is the interesting one. internal/vpn's sentences were always good —
// they name VPN_CONFIG, or VPN_REQUIRED=false, or the exit address that matched
// — and the only thing wrong was `dispatch <releaseID>: ` and a second sentinel
// in front of them. So this asserts the reason SURVIVES while the furniture goes.
func TestDispatchDependencyFailuresAreWrittenForAHuman(t *testing.T) {
	const guard = "the VPN tunnel has never completed a handshake with 203.0.113.9:51820"

	t.Run("the torrent client is unreachable", func(t *testing.T) {
		log, buffer := captured()
		chain := fmt.Errorf("dispatch yts-1234: %w: %w", download.ErrClient,
			fmt.Errorf("qbit torrents/add: calling qBittorrent at http://127.0.0.1:8080: connection refused"))
		mux := http.NewServeMux()
		srv := New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, log).
			WithSearch(&fakeSearcher{}).WithDownloads(&fakeDispatcher{err: chain})
		srv.Register(mux)
		srv.RegisterSearch(mux)
		srv.RegisterDownloads(mux)

		rec := post(t, mux, "/api/downloads", dispatchBody)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502 (%s)", rec.Code, rec.Body)
		}
		got := errorBody(t, rec)
		if !strings.Contains(got, "not started") {
			t.Errorf("body does not say nothing was started: %q", got)
		}
		assertNoLeak(t, "body", got, []string{
			"dispatch yts-1234", "yts-1234", "qbit", "engine:",
			"torrents/add", "calling qBittorrent at", "connection refused",
		})
		if logged := flattenLog(buffer); !strings.Contains(logged, "torrents/add") {
			t.Errorf("the log lost the endpoint that failed:\n%s", logged)
		}
	})

	t.Run("the VPN guard refuses", func(t *testing.T) {
		log, buffer := captured()
		chain := fmt.Errorf("dispatch yts-1234: %w",
			download.UnprotectedFor(errors.New(guard)))
		mux := http.NewServeMux()
		srv := New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, log).
			WithSearch(&fakeSearcher{}).WithDownloads(&fakeDispatcher{err: chain})
		srv.Register(mux)
		srv.RegisterSearch(mux)
		srv.RegisterDownloads(mux)

		rec := post(t, mux, "/api/downloads", dispatchBody)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (%s)", rec.Code, rec.Body)
		}
		got := errorBody(t, rec)
		// The actionable half is internal/vpn's, and it has to survive whole.
		if !strings.Contains(got, guard) {
			t.Errorf("body lost the guard's own sentence, which is the only actionable part: %q", got)
		}
		if !strings.Contains(got, "did not start") {
			t.Errorf("body does not say nothing was started: %q", got)
		}
		assertNoLeak(t, "body", got, []string{
			"dispatch yts-1234", "yts-1234", "refusing to dispatch",
		})
		if logged := flattenLog(buffer); !strings.Contains(logged, "dispatch yts-1234") {
			t.Errorf("the log lost the release id:\n%s", logged)
		}
	})

	// The fallback, which is the branch most of the suite exercises: a bare
	// sentinel with no Unprotected in it must still answer 503 with a true
	// sentence rather than an empty one.
	t.Run("a bare sentinel", func(t *testing.T) {
		rec := post(t, newDownloadServer(t, &fakeDispatcher{err: download.ErrUnprotected}), "/api/downloads", dispatchBody)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (%s)", rec.Code, rec.Body)
		}
		if got := errorBody(t, rec); !strings.Contains(got, "protected") {
			t.Errorf("fallback sentence is not the written one: %q", got)
		}
	})
}
