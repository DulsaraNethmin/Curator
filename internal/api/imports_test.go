package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
