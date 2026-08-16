package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/download"
	"github.com/DulsaraNethmin/curator/internal/library"
	"github.com/DulsaraNethmin/curator/internal/store"
)

const importHash = "89599BF4DC369A3A8ECA26411C5CCF922D78B486"

func importedMovie() store.Movie {
	path := "/media/storage/media/movies/Interstellar (2014)"
	at := time.Date(2026, 8, 12, 19, 30, 0, 0, time.UTC)
	return store.Movie{
		ID: 7, Title: "Interstellar", Year: 2014, MediaType: store.MediaTypeMovie,
		Status: store.StatusImported, LibraryPath: &path, ImportedAt: &at,
	}
}

func TestImportReturnsTheMovieRow(t *testing.T) {
	fake := &fakeDispatcher{imported: importedMovie()}
	rec := post(t, newDownloadServer(t, fake), "/api/downloads/"+importHash+"/import", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if fake.imports != 1 {
		t.Errorf("the importer was called %d times, want 1", fake.imports)
	}
	// The hash reaches the service exactly as it was in the path; normalising is
	// the store's job and it does it on both sides.
	if fake.gotHash != importHash {
		t.Errorf("hash = %q, want %q", fake.gotHash, importHash)
	}

	var got store.Movie
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — body was %s", err, rec.Body)
	}
	if got.ID != 7 || got.Status != store.StatusImported {
		t.Errorf("movie = %+v, want id 7 and status imported", got)
	}
	if got.LibraryPath == nil || got.ImportedAt == nil {
		t.Error("the response carries no library_path or imported_at, which is what the caller asked for")
	}
}

// Each failure gets the status that says whose problem it is and whether trying
// again could help. Getting these wrong in either direction is the dishonesty
// this repo keeps legislating against.
func TestImportStatusCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{{
		name: "no download with that hash",
		err:  fmt.Errorf("import x: %w", store.ErrNotFound),
		want: http.StatusNotFound,
	}, {
		name: "downloads are unconfigured",
		err:  download.ErrUnconfigured,
		want: http.StatusServiceUnavailable,
	}, {
		name: "the torrent is still downloading",
		err:  fmt.Errorf("import x: %w", download.ErrNotCompleted),
		want: http.StatusConflict,
	}, {
		name: "the content path holds no film",
		err:  fmt.Errorf("import x: %w", library.ErrNoVideo),
		want: http.StatusUnprocessableEntity,
	}, {
		name: "the title cannot be a folder",
		err:  fmt.Errorf("import x: %w", library.ErrBadTitle),
		want: http.StatusUnprocessableEntity,
	}, {
		name: "qBittorrent is unreachable",
		err:  fmt.Errorf("import x: %w: dial tcp: connection refused", download.ErrClient),
		want: http.StatusBadGateway,
	}, {
		name: "the database is unwell",
		err:  errors.New("database is locked"),
		want: http.StatusInternalServerError,
	}}

	for _, c := range cases {
		fake := &fakeDispatcher{importErr: c.err}
		rec := post(t, newDownloadServer(t, fake), "/api/downloads/"+importHash+"/import", "")

		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body)
		}

		// Every failure carries phase 1's shape, so a client has one thing to
		// parse across the whole API.
		var body struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
			t.Errorf("%s: body = %s, want {\"error\": \"...\"}", c.name, rec.Body)
		}
	}
}

// An import error can carry more than one sentinel — ErrClient wrapping a
// deeper cause, say — so the ordering in failImport has to be tested rather
// than assumed.
func TestImportPrefersTheMoreSpecificSentinel(t *testing.T) {
	fake := &fakeDispatcher{
		importErr: fmt.Errorf("import x: %w: %w", download.ErrClient, download.ErrNotCompleted),
	}
	rec := post(t, newDownloadServer(t, fake), "/api/downloads/"+importHash+"/import", "")

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 — 'not finished' is more useful than 'bad gateway'", rec.Code)
	}
}

// Phase 3's routes must still answer, and the new one must not have shadowed
// them: "POST /api/downloads" and "POST /api/downloads/{hash}/import" are
// distinct patterns and Go 1.22 routing has to keep them apart.
func TestImportRouteDoesNotShadowDispatch(t *testing.T) {
	fake := &fakeDispatcher{saved: savedDownload(), imported: importedMovie()}
	h := newDownloadServer(t, fake)

	if rec := post(t, h, "/api/downloads", dispatchBody); rec.Code != http.StatusCreated {
		t.Errorf("dispatch status = %d, want 201: %s", rec.Code, rec.Body)
	}
	if fake.imports != 0 {
		t.Error("dispatching called the importer")
	}

	if rec := post(t, h, "/api/downloads/"+importHash+"/import", ""); rec.Code != http.StatusOK {
		t.Errorf("import status = %d, want 200", rec.Code)
	}
	if fake.dispatches != 1 {
		t.Errorf("importing dispatched %d times, want the one from above", fake.dispatches)
	}
}

// Every import refusal is a sentence somebody wrote, and the chain that
// produced it is not on the wire (T71, D40).
//
// The absences matter more than the presences. `import <hash>: ` came from
// internal/importer/importer.go:95 and duplicates a "hash" attribute the log
// line already carries; `find feature in <path>` and `destination folder for`
// name a path on curator's own download disk that no reader can act on.
func TestImportRefusalsAreWrittenForAHuman(t *testing.T) {
	leaks := []string{
		"import " + importHash, "find feature in", "destination folder",
		"destination file", importHash, "cannot be a folder name: ",
	}

	cases := []struct {
		name   string
		err    error
		want   int
		expect []string
	}{{
		name: "still downloading, with the state",
		err: fmt.Errorf("import %s: %w", importHash,
			download.NotCompleted{State: "stalled"}),
		want:   http.StatusConflict,
		expect: []string{`"stalled"`, "has not finished"},
	}, {
		// The state is what a bare sentinel cannot carry, so the sentence drops
		// the clause rather than the truth.
		name:   "still downloading, sentinel only",
		err:    fmt.Errorf("import %s: %w", importHash, download.ErrNotCompleted),
		want:   http.StatusConflict,
		expect: []string{"has not finished"},
	}, {
		name: "the title cannot be a folder, with the reason",
		err: fmt.Errorf("import %s: %w", importHash,
			library.BadTitle{Title: "Dune", Reason: "it is empty"}),
		want:   http.StatusUnprocessableEntity,
		expect: []string{`"Dune"`, "it is empty", "nowhere to put"},
	}, {
		name:   "the title cannot be a folder, sentinel only",
		err:    fmt.Errorf("import %s: %w", importHash, library.ErrBadTitle),
		want:   http.StatusUnprocessableEntity,
		expect: []string{"nowhere to put"},
	}, {
		name:   "nothing in the download is a film",
		err:    fmt.Errorf("import %s: find feature in /downloads/x: %w", importHash, library.ErrNoVideo),
		want:   http.StatusUnprocessableEntity,
		expect: []string{"no video file", "nothing to import"},
	}}

	for _, c := range cases {
		fake := &fakeDispatcher{importErr: c.err}
		rec := post(t, newDownloadServer(t, fake), "/api/downloads/"+importHash+"/import", "")

		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body)
			continue
		}
		got := errorBody(t, rec)
		for _, want := range c.expect {
			if !strings.Contains(got, want) {
				t.Errorf("%s: body does not contain %q: %q", c.name, want, got)
			}
		}
		for _, leak := range leaks {
			if strings.Contains(got, leak) {
				t.Errorf("%s: body leaks %q from the error chain: %q", c.name, leak, got)
			}
		}
	}
}

// ErrNoVideo and ErrBadTitle share a status and used to share a sentence. That
// is the 422 half of D39's finding — "Nothing to import" was false for a film
// that has something to import and a title that cannot be a folder name — and
// since the status cannot separate them, the two sentences must.
func TestTheTwo422sDoNotSayTheSameThing(t *testing.T) {
	sentence := func(err error) string {
		fake := &fakeDispatcher{importErr: fmt.Errorf("import %s: %w", importHash, err)}
		rec := post(t, newDownloadServer(t, fake), "/api/downloads/"+importHash+"/import", "")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422 (%s)", rec.Code, rec.Body)
		}
		return errorBody(t, rec)
	}

	noVideo := sentence(library.ErrNoVideo)
	badTitle := sentence(library.BadTitle{Title: "Dune", Reason: "it is empty"})
	if noVideo == badTitle {
		t.Errorf("both 422s say %q — one of them is false", noVideo)
	}
}

// The 502 arm of failImport, which T71 left alone one line under the 422 it
// rewrote.
func TestImportDependencyFailureIsWrittenForAHuman(t *testing.T) {
	log, buffer := captured()
	mux := http.NewServeMux()
	chain := fmt.Errorf("import %s: %w: %w", importHash, download.ErrClient,
		fmt.Errorf("qbit torrents/info: calling qBittorrent at http://127.0.0.1:8080: connection refused"))
	srv := New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, log).
		WithDownloads(&fakeDispatcher{importErr: chain})
	srv.Register(mux)
	srv.RegisterDownloads(mux)

	rec := post(t, mux, "/api/downloads/"+importHash+"/import", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (%s)", rec.Code, rec.Body)
	}

	got := errorBody(t, rec)
	// Retrying is the whole remedy and the sentence has to say so, because a
	// 502 with no advice reads as "this download is broken".
	if !strings.Contains(got, "again") {
		t.Errorf("body does not say trying again will work: %q", got)
	}
	assertNoLeak(t, "body", got, []string{
		"import " + importHash, importHash, "qbit", "engine:",
		"torrents/info", "calling qBittorrent at", "connection refused",
	})

	if logged := flattenLog(buffer); !strings.Contains(logged, "torrents/info") {
		t.Errorf("the log lost the endpoint that failed:\n%s", logged)
	}
}
