package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/library"
	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/tmdb"
)

// fixtureRoot is the 29-folder library fixture, mirroring the real one on the Pi.
const fixtureRoot = "../../testdata/library/movies"

// fakeStore is an in-memory stand-in for internal/store, keyed on library_path
// exactly as the real one is.
type fakeStore struct {
	byPath map[string]*store.Movie
	byID   map[int64]*store.Movie
	order  []*store.Movie // insertion order; ListMovies reverses it
	nextID int64

	upsertErr error
	listErr   error
	missErr   error
	setErr    error // e.g. the UNIQUE violation two folders matching one TMDB id causes
}

func newFakeStore() *fakeStore {
	return &fakeStore{byPath: map[string]*store.Movie{}, byID: map[int64]*store.Movie{}}
}

func (f *fakeStore) UpsertMovieByPath(_ context.Context, m store.ScannedMovie) (store.Movie, bool, error) {
	if f.upsertErr != nil {
		return store.Movie{}, false, f.upsertErr
	}
	if existing, ok := f.byPath[m.LibraryPath]; ok {
		// The scan owns these columns; tmdb_id, overview and poster_path are not
		// touched, matching the real store's division of authority.
		existing.Title = m.Title
		existing.Year = m.Year
		existing.SizeBytes = m.SizeBytes
		existing.Status = m.Status
		return *existing, false, nil
	}
	f.nextID++
	path := m.LibraryPath
	status := m.Status
	if status == "" {
		status = store.StatusImported
	}
	row := &store.Movie{
		ID:          f.nextID,
		Title:       m.Title,
		Year:        m.Year,
		MediaType:   store.MediaTypeMovie,
		Status:      status,
		LibraryPath: &path,
		SizeBytes:   m.SizeBytes,
		AddedAt:     time.Now().UTC(),
	}
	f.byPath[path] = row
	f.byID[row.ID] = row
	f.order = append(f.order, row)
	return *row, true, nil
}

func (f *fakeStore) SetTMDBMetadata(_ context.Context, id int64, match store.TMDBMatch) error {
	if f.setErr != nil {
		return f.setErr
	}
	row, ok := f.byID[id]
	if !ok {
		return store.ErrNotFound
	}
	tmdbID := match.TMDBID
	row.TMDBID = &tmdbID
	row.Overview = match.Overview
	row.PosterPath = match.PosterPath
	if match.Title != nil {
		row.Title = *match.Title
	}
	return nil
}

func (f *fakeStore) ListMovies(context.Context) ([]store.Movie, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]store.Movie, 0, len(f.order))
	for i := len(f.order) - 1; i >= 0; i-- { // newest first
		out = append(out, *f.order[i])
	}
	return out, nil
}

func (f *fakeStore) GetMovie(_ context.Context, id int64) (store.Movie, error) {
	row, ok := f.byID[id]
	if !ok {
		return store.Movie{}, fmt.Errorf("get movie %d: %w", id, store.ErrNotFound)
	}
	return *row, nil
}

func (f *fakeStore) MoviesMissingMetadata(context.Context) ([]store.Movie, error) {
	if f.missErr != nil {
		return nil, f.missErr
	}
	var out []store.Movie
	for _, row := range f.order {
		if row.TMDBID == nil {
			out = append(out, *row)
		}
	}
	return out, nil
}

// matcherFunc adapts a function to Matcher.
type matcherFunc func(ctx context.Context, title string, year int) (*tmdb.Match, error)

func (f matcherFunc) SearchMovie(ctx context.Context, title string, year int) (*tmdb.Match, error) {
	return f(ctx, title, year)
}

// matchAll resolves every title to a distinct, plausible TMDB id.
func matchAll() matcherFunc {
	var n int
	return func(_ context.Context, title string, year int) (*tmdb.Match, error) {
		n++
		return &tmdb.Match{TMDBID: 100000 + n, Title: title, Year: year, Overview: "o", PosterPath: "/p.jpg"}, nil
	}
}

// quiet keeps expected warnings out of the test output.
func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestServer(t *testing.T, st Store, matcher Matcher) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	New(st, ScannerFunc(library.Scan), matcher, fixtureRoot, quiet()).Register(mux)
	return mux
}

func do(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func decodeScan(t *testing.T, rec *httptest.ResponseRecorder) scanResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out scanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return out
}

func TestScanFixtureReports29(t *testing.T) {
	st := newFakeStore()
	h := newTestServer(t, st, matchAll())

	got := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	want := scanResponse{Scanned: 29, Added: 29, Matched: 29, Unmatched: 0}
	if got != want {
		t.Errorf("scan = %+v, want %+v", got, want)
	}
}

func TestSecondScanAddsNothing(t *testing.T) {
	st := newFakeStore()
	h := newTestServer(t, st, matchAll())

	decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	second := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))

	if second.Scanned != 29 {
		t.Errorf("second scanned = %d, want 29", second.Scanned)
	}
	if second.Added != 0 {
		t.Errorf("second added = %d, want 0 — rescans must be idempotent", second.Added)
	}
	// Everything matched on the first pass, so there is nothing left to look up.
	if second.Matched != 0 || second.Unmatched != 0 {
		t.Errorf("second matched/unmatched = %d/%d, want 0/0", second.Matched, second.Unmatched)
	}
	if len(st.order) != 29 {
		t.Errorf("stored rows = %d, want 29 — a rescan duplicated rows", len(st.order))
	}
}

func TestTMDBErrorLeavesRowPresentAndUnmatched(t *testing.T) {
	st := newFakeStore()
	failing := "Interstellar"
	matcher := matcherFunc(func(_ context.Context, title string, year int) (*tmdb.Match, error) {
		if title == failing {
			return nil, errors.New("tmdb: connection reset")
		}
		return &tmdb.Match{TMDBID: 1000 + year, Title: title, Year: year}, nil
	})
	h := newTestServer(t, st, matcher)

	got := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	if got.Scanned != 29 {
		t.Errorf("scanned = %d, want 29 — one TMDB failure must not abort the scan", got.Scanned)
	}
	if got.Unmatched != 1 {
		t.Errorf("unmatched = %d, want 1", got.Unmatched)
	}
	if got.Matched != 28 {
		t.Errorf("matched = %d, want 28", got.Matched)
	}

	var found bool
	for _, row := range st.order {
		if row.Title == failing {
			found = true
			if row.TMDBID != nil {
				t.Errorf("%s: tmdb_id = %d, want NULL", failing, *row.TMDBID)
			}
		}
	}
	if !found {
		t.Errorf("%s is absent from the store — the row must survive a failed lookup", failing)
	}
}

// A TMDB id collision (two folders resolving to one entry) surfaces as a write
// error. The row stays unmatched and the scan carries on.
func TestMetadataWriteErrorLeavesRowUnmatched(t *testing.T) {
	st := newFakeStore()
	st.setErr = errors.New("UNIQUE constraint failed: movies.tmdb_id")
	h := newTestServer(t, st, matchAll())

	got := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	if got.Matched != 0 {
		t.Errorf("matched = %d, want 0", got.Matched)
	}
	if got.Unmatched != 29 {
		t.Errorf("unmatched = %d, want 29", got.Unmatched)
	}
}

func TestScanWithoutAPIKeyStillScans(t *testing.T) {
	st := newFakeStore()
	h := newTestServer(t, st, nil) // no TMDB key configured

	got := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	want := scanResponse{Scanned: 29, Added: 29, Matched: 0, Unmatched: 29}
	if got != want {
		t.Errorf("scan = %+v, want %+v — the disk is the source of truth", got, want)
	}
	if len(st.order) != 29 {
		t.Errorf("stored rows = %d, want 29", len(st.order))
	}
}

func TestScannerErrorIsAnError(t *testing.T) {
	mux := http.NewServeMux()
	New(newFakeStore(), ScannerFunc(library.Scan), matchAll(), "./no/such/library", quiet()).Register(mux)

	rec := do(t, mux, http.MethodPost, "/api/scan")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — never a 200 carrying a failure", rec.Code)
	}
	assertErrorBody(t, rec)
}

func TestListMovies(t *testing.T) {
	st := newFakeStore()
	h := newTestServer(t, st, matchAll())
	do(t, h, http.MethodPost, "/api/scan")

	rec := do(t, h, http.MethodGet, "/api/movies")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var movies []store.Movie
	if err := json.Unmarshal(rec.Body.Bytes(), &movies); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(movies) != 29 {
		t.Fatalf("len = %d, want 29", len(movies))
	}
}

func TestListMoviesEmptyIsArrayNotNull(t *testing.T) {
	h := newTestServer(t, newFakeStore(), nil)

	rec := do(t, h, http.MethodGet, "/api/movies")
	if got := rec.Body.String(); got != "[]\n" {
		t.Errorf("body = %q, want %q", got, "[]\n")
	}
}

// tmdb_id must reach the client as JSON null, not 0 — phase 1's verification
// greps for exactly that, and "unmatched" and "matched to id 0" must stay apart.
func TestUnmatchedMovieSerialisesTMDBIDAsNull(t *testing.T) {
	st := newFakeStore()
	h := newTestServer(t, st, nil)
	do(t, h, http.MethodPost, "/api/scan")

	rec := do(t, h, http.MethodGet, "/api/movies")
	var raw []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, m := range raw {
		v, present := m["tmdb_id"]
		if !present {
			t.Fatalf("tmdb_id key missing from %v", m)
		}
		if v != nil {
			t.Fatalf("tmdb_id = %v, want null", v)
		}
	}
}

func TestGetMovie(t *testing.T) {
	st := newFakeStore()
	h := newTestServer(t, st, matchAll())
	do(t, h, http.MethodPost, "/api/scan")

	rec := do(t, h, http.MethodGet, "/api/movies/1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var movie store.Movie
	if err := json.Unmarshal(rec.Body.Bytes(), &movie); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if movie.ID != 1 {
		t.Errorf("id = %d, want 1", movie.ID)
	}
}

func TestGetMovieNotFound(t *testing.T) {
	h := newTestServer(t, newFakeStore(), nil)

	rec := do(t, h, http.MethodGet, "/api/movies/404")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	assertErrorBody(t, rec)
}

func TestGetMovieBadID(t *testing.T) {
	h := newTestServer(t, newFakeStore(), nil)

	rec := do(t, h, http.MethodGet, "/api/movies/not-a-number")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	assertErrorBody(t, rec)
}

func TestGetMovieStoreErrorIs500(t *testing.T) {
	st := newFakeStore()
	st.listErr = errors.New("database is locked")
	h := newTestServer(t, st, nil)

	rec := do(t, h, http.MethodGet, "/api/movies")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	assertErrorBody(t, rec)
}

// assertErrorBody checks the consistent {"error": "..."} shape and the JSON
// content type.
func assertErrorBody(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if ct := rec.Header().Get("content-type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if body["error"] == "" {
		t.Errorf("body = %v, want a non-empty error field", body)
	}
}
