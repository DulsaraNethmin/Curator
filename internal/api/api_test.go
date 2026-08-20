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
	"strings"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/library"
	"github.com/DulsaraNethmin/curator/internal/logs"
	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/tmdb"
)

// fixtureRoot is the 29-folder library fixture, mirroring the real one on the Pi.
const fixtureRoot = "../../testdata/library/movies"

// deleteHash is an info hash in the store's upper-case form. It exists so the
// T71 tests can assert it is ABSENT from a refusal: the delete chain used to
// render it twice, once upper-case from the row and once lower-case from
// qBittorrent's wire form, reading as two different torrents.
const deleteHash = "2B6A8D9F3C1E4B7A0D5F8E2C9A4B6D1E3F7C0A85"

// errorBody pulls the one prose field every failure answers with. A body that
// is not that shape is a failure of its own — phase 1 established
// {"error": "..."} and web/lib/api.ts parses nothing else.
func errorBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body = %s, want {\"error\": \"...\"}: %v", rec.Body, err)
	}
	if body.Error == "" {
		t.Fatalf("body carries no error sentence: %s", rec.Body)
	}
	return body.Error
}

func ptrInt(n int) *int { return &n }

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
	matchErr  error // forces MatchMovie's failure path without staging a row for it
	// Separate from matchErr so a test cannot force one method's failure and
	// accidentally assert it against the other — the two answer different 409s.
	correctErr error

	// The library index behind a TMDB card's "already in your library" badge.
	library    map[int64]store.LibraryState
	libraryErr error

	// downloading marks a movie id as having a torrent still running for it, so
	// the scan's prune can be tested against the one row it must never remove.
	downloading map[int64]bool
	onDiskErr   error
	deleteErr   error
	deleted     []int64 // ids passed to DeleteMovie, in order
}

// MoviesOnDisk mirrors the real query's two exclusions rather than returning
// everything: a row with no library_path is not a prune candidate, and neither is
// one with a download in flight. A fake that skipped them would let a broken
// pruner pass.
func (f *fakeStore) MoviesOnDisk(context.Context) ([]store.OnDisk, error) {
	if f.onDiskErr != nil {
		return nil, f.onDiskErr
	}
	out := []store.OnDisk{}
	for _, row := range f.order {
		if row.LibraryPath == nil || strings.TrimSpace(*row.LibraryPath) == "" {
			continue
		}
		out = append(out, store.OnDisk{
			ID:    row.ID,
			Title: row.Title,
			Year:  row.Year,
			// On the row and never a filter, exactly as the real query has it
			// since T88: this list is what a prune may CONSIDER, so a row the
			// caller cannot see is a row it cannot keep either. A fake that
			// dropped the media type would let a prune that deletes every show
			// pass every test here.
			MediaType:   row.MediaType,
			LibraryPath: *row.LibraryPath,
			Downloading: f.downloading[row.ID],
		})
	}
	return out, nil
}

func (f *fakeStore) DeleteMovie(_ context.Context, id int64) (store.Deleted, error) {
	if f.deleteErr != nil {
		return store.Deleted{}, f.deleteErr
	}
	row, ok := f.byID[id]
	if !ok {
		return store.Deleted{}, store.ErrNotFound
	}
	f.deleted = append(f.deleted, id)
	delete(f.byID, id)
	if row.LibraryPath != nil {
		delete(f.byPath, *row.LibraryPath)
	}
	for i, r := range f.order {
		if r.ID == id {
			f.order = append(f.order[:i], f.order[i+1:]...)
			break
		}
	}
	return store.Deleted{Movie: *row}, nil
}

// seedOnDisk records a film row the way an earlier scan would have, so a test
// can put a stale row in front of the pruner.
func (f *fakeStore) seedOnDisk(path, title string, year int) *store.Movie {
	return f.seed(store.MediaTypeMovie, path, title, year)
}

// seedShowOnDisk is seedOnDisk for the other media type — a row under
// LIBRARY_TV, which is the row a movie scan used to delete.
func (f *fakeStore) seedShowOnDisk(path, title string, year int) *store.Movie {
	return f.seed(store.MediaTypeTV, path, title, year)
}

func (f *fakeStore) seed(mediaType, path, title string, year int) *store.Movie {
	f.nextID++
	p := path
	row := &store.Movie{
		ID:          f.nextID,
		Title:       title,
		Year:        year,
		MediaType:   mediaType,
		Status:      store.StatusImported,
		LibraryPath: &p,
		AddedAt:     time.Now().UTC(),
	}
	f.byPath[p] = row
	f.byID[row.ID] = row
	f.order = append(f.order, row)
	return row
}

// seedWanted records a row that was never on disk: library_path NULL, which is
// what a film asked for but not yet fetched looks like.
func (f *fakeStore) seedWanted(title string, year int) *store.Movie {
	f.nextID++
	row := &store.Movie{
		ID:        f.nextID,
		Title:     title,
		Year:      year,
		MediaType: store.MediaTypeMovie,
		Status:    store.StatusWanted,
		AddedAt:   time.Now().UTC(),
	}
	f.byID[row.ID] = row
	f.order = append(f.order, row)
	return row
}

// The three media-scoped reads filter for real rather than ignoring the
// argument. A fake that accepted the parameter and answered the same thing
// regardless would let every test pass while the store mixed films and shows,
// which is the exact bug the parameter exists to prevent.
func (f *fakeStore) LibraryByTMDBID(_ context.Context, mediaType string) (map[int64]store.LibraryState, error) {
	if f.libraryErr != nil {
		return nil, f.libraryErr
	}
	if f.library == nil {
		return map[int64]store.LibraryState{}, nil
	}
	// f.library is keyed by TMDB id and holds no media type of its own, so it is
	// scoped through the rows it points at.
	out := map[int64]store.LibraryState{}
	for tmdbID, state := range f.library {
		if row, ok := f.byID[state.MovieID]; ok && row.MediaType != mediaType {
			continue
		}
		out[tmdbID] = state
	}
	return out, nil
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		byPath:      map[string]*store.Movie{},
		byID:        map[int64]*store.Movie{},
		downloading: map[int64]bool{},
	}
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
		// REWRITTEN on every pass, exactly as the real UpsertMovieByPath does
		// it. That rewrite is why ScannedMovie.MediaType is required, and a
		// fake that ignored the field would hide a scan that relabels a show
		// as a film — the failure T88 called a loaded gun.
		existing.MediaType = m.MediaType
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
		MediaType:   m.MediaType,
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
	// **The ROW decides the column**, not the caller — the real store writes
	// through a CASE over media_type so there is no argument by which a tv id
	// could land in tmdb_id (D48). A fake that always wrote tmdb_id would make
	// a scan that matched a show as a film look like a pass.
	if row.MediaType == store.MediaTypeTV {
		row.TMDBTVID = &tmdbID
	} else {
		row.TMDBID = &tmdbID
	}
	row.Overview = match.Overview
	row.PosterPath = match.PosterPath
	if match.Title != nil {
		row.Title = *match.Title
	}
	return nil
}

// MatchMovie mirrors the real store's refusals rather than just writing, because
// the handler's job is almost entirely deciding which of them applies: a fake that
// always succeeded would let every 409 test pass against a handler that had lost
// the check.
func (f *fakeStore) MatchMovie(_ context.Context, id int64, match store.TMDBMatch) (store.Movie, error) {
	if f.matchErr != nil {
		return store.Movie{}, f.matchErr
	}
	row, ok := f.byID[id]
	if !ok {
		return store.Movie{}, fmt.Errorf("match movie %d: %w", id, store.ErrNotFound)
	}
	if row.TMDBID != nil {
		return store.Movie{}, fmt.Errorf("match movie %d: %w", id, store.ErrAlreadyMatched)
	}
	for otherID, other := range f.byID {
		if otherID != id && other.TMDBID != nil && *other.TMDBID == match.TMDBID {
			return store.Movie{}, fmt.Errorf("match movie %d: %w", id, store.ErrTMDBIDTaken)
		}
	}
	tmdbID := match.TMDBID
	row.TMDBID = &tmdbID
	row.Overview = match.Overview
	row.PosterPath = match.PosterPath
	// tmdb_year, and deliberately not year — the fake mirrors the real store's
	// division or the year tests prove nothing (D37).
	row.TMDBYear = match.Year
	if match.Title != nil {
		row.Title = *match.Title
	}
	return *row, nil
}

// CorrectMatch mirrors the real store's refusals for the same reason MatchMovie
// does — including the one that is deliberately NOT the inverse. ErrAlreadyMatched
// becomes ErrNotMatched, but the taken probe still ignores this row, so re-picking
// the film already stored is a refresh rather than a collision with itself.
func (f *fakeStore) CorrectMatch(_ context.Context, id int64, match store.TMDBMatch) (store.Movie, error) {
	if f.correctErr != nil {
		return store.Movie{}, f.correctErr
	}
	row, ok := f.byID[id]
	if !ok {
		return store.Movie{}, fmt.Errorf("correct movie %d: %w", id, store.ErrNotFound)
	}
	if row.TMDBID == nil {
		return store.Movie{}, fmt.Errorf("correct movie %d: %w", id, store.ErrNotMatched)
	}
	for otherID, other := range f.byID {
		if otherID != id && other.TMDBID != nil && *other.TMDBID == match.TMDBID {
			return store.Movie{}, fmt.Errorf("correct movie %d: %w", id, store.ErrTMDBIDTaken)
		}
	}
	tmdbID := match.TMDBID
	row.TMDBID = &tmdbID
	row.Overview = match.Overview
	row.PosterPath = match.PosterPath
	row.TMDBYear = match.Year
	if match.Title != nil {
		row.Title = *match.Title
	}
	return *row, nil
}

func (f *fakeStore) ListMovies(_ context.Context, mediaType string) ([]store.Movie, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]store.Movie, 0, len(f.order))
	for i := len(f.order) - 1; i >= 0; i-- { // newest first
		if f.order[i].MediaType != mediaType {
			continue
		}
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

func (f *fakeStore) MoviesMissingMetadata(_ context.Context, mediaType string) ([]store.Movie, error) {
	if f.missErr != nil {
		return nil, f.missErr
	}
	var out []store.Movie
	for _, row := range f.order {
		if row.MediaType != mediaType {
			continue
		}
		// The id column is the one this media type owns, exactly as the store
		// picks it — a show's NULL tmdb_id is not a missing match.
		unmatched := row.TMDBID == nil
		if mediaType == store.MediaTypeTV {
			unmatched = row.TMDBTVID == nil
		}
		if unmatched {
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

// captured is quiet() with the ring buffer /api/logs serves attached.
//
// T71's tests only had to prove a chain was ABSENT from a body. T72's have to
// prove it is absent from the body AND present in the log, because at 502 and
// 503 — unlike at 409 and 422 — there is a second reader, and a sentence that
// replaced the chain in both places would have destroyed it (docs/decisions.md
// D41).
func captured() (*slog.Logger, *logs.Buffer) {
	buffer := logs.NewBuffer(200)
	return slog.New(buffer.Handler(slog.NewTextHandler(io.Discard, nil))), buffer
}

// flattenLog renders message and attributes together, which is where a wrapped
// chain actually ends up — `fail` and `failCause` log it as an `err` attribute,
// not in the message.
func flattenLog(buffer *logs.Buffer) string {
	entries, _, _ := buffer.Since(0, 200)
	var out strings.Builder
	for _, entry := range entries {
		out.WriteString(entry.Msg)
		for key, value := range entry.Attrs {
			out.WriteString(" " + key + "=" + value)
		}
		out.WriteString("\n")
	}
	return out.String()
}

// assertNoLeak fails for every substring of the error chain that reached the
// reader. It takes the whole list rather than one at a time so a failure names
// every leak at once, which is what makes a rewrite one iteration instead of six.
func assertNoLeak(t *testing.T, what, got string, leaks []string) {
	t.Helper()
	for _, leak := range leaks {
		if strings.Contains(got, leak) {
			t.Errorf("%s leaks %q from the error chain: %q", what, leak, got)
		}
	}
}

// fixtureScanner is library.Scan with the feature picker's floor lowered.
//
// The fixture's two videos are 4096 and 2048 bytes — committed files, and a 50 MiB
// blob does not belong in git (docs/phase-4.md). At the production floor the whole
// fixture holds no film, so these tests would meet an empty library instead of the
// 29 real folder names they exist to exercise. The floor itself is proven in
// internal/library, against sparse files in t.TempDir().
func fixtureScanner() ScannerFunc {
	return func(root string) ([]library.Movie, []library.Skipped, error) {
		return library.ScanWith(root, library.FeatureOpts{MinBytes: 1})
	}
}

func newTestServer(t *testing.T, st Store, matcher Matcher) http.Handler {
	t.Helper()
	return newTestServerAt(t, st, matcher, fixtureRoot)
}

func newTestServerAt(t *testing.T, st Store, matcher Matcher, root string) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	New(st, fixtureScanner(), matcher, root, quiet()).Register(mux)
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

// The fixture is 29 folders and 2 films. `scanned` counts the films, `empty`
// counts the folders with nothing playable in them, and the two together are what
// stop 2 looking like a scanner that broke (docs/decisions.md D33).
func TestScanFixtureReportsTwoFilmsIn29Folders(t *testing.T) {
	st := newFakeStore()
	h := newTestServer(t, st, matchAll())

	got := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	want := scanResponse{Scanned: 2, Added: 2, Matched: 2, Unmatched: 0, Empty: 27}
	if got != want {
		t.Errorf("scan = %+v, want %+v", got, want)
	}
	if len(st.deleted) != 0 {
		t.Errorf("deleted %v, want none — there were no rows to prune", st.deleted)
	}
}

func TestSecondScanAddsNothing(t *testing.T) {
	st := newFakeStore()
	h := newTestServer(t, st, matchAll())

	decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	second := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))

	if second.Scanned != 2 {
		t.Errorf("second scanned = %d, want 2", second.Scanned)
	}
	if second.Added != 0 {
		t.Errorf("second added = %d, want 0 — rescans must be idempotent", second.Added)
	}
	// Everything matched on the first pass, so there is nothing left to look up.
	if second.Matched != 0 || second.Unmatched != 0 {
		t.Errorf("second matched/unmatched = %d/%d, want 0/0", second.Matched, second.Unmatched)
	}
	// The prune is idempotent too, and this is the assertion that catches a
	// pruner that removes the rows it has just written.
	if second.Removed != 0 {
		t.Errorf("second removed = %d, want 0 — a rescan of an unchanged library removes nothing", second.Removed)
	}
	if len(st.order) != 2 {
		t.Errorf("stored rows = %d, want 2 — a rescan duplicated rows", len(st.order))
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
	if got.Scanned != 2 {
		t.Errorf("scanned = %d, want 2 — one TMDB failure must not abort the scan", got.Scanned)
	}
	if got.Unmatched != 1 {
		t.Errorf("unmatched = %d, want 1", got.Unmatched)
	}
	if got.Matched != 1 {
		t.Errorf("matched = %d, want 1", got.Matched)
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
	if got.Unmatched != 2 {
		t.Errorf("unmatched = %d, want 2", got.Unmatched)
	}
}

func TestScanWithoutAPIKeyStillScans(t *testing.T) {
	st := newFakeStore()
	h := newTestServer(t, st, nil) // no TMDB key configured

	got := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	want := scanResponse{Scanned: 2, Added: 2, Matched: 0, Unmatched: 2, Empty: 27}
	if got != want {
		t.Errorf("scan = %+v, want %+v — the disk is the source of truth", got, want)
	}
	if len(st.order) != 2 {
		t.Errorf("stored rows = %d, want 2", len(st.order))
	}
}

func TestScannerErrorIsAnError(t *testing.T) {
	st := newFakeStore()
	st.seedOnDisk("/somewhere/Gladiator (2000)", "Gladiator", 2000)
	mux := http.NewServeMux()
	New(st, fixtureScanner(), matchAll(), "./no/such/library", quiet()).Register(mux)

	rec := do(t, mux, http.MethodPost, "/api/scan")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — never a 200 carrying a failure", rec.Code)
	}
	assertErrorBody(t, rec)

	// The whole protection behind decision 5 of T57: a root that cannot be read
	// must prune NOTHING. An unmounted library disk is exactly this request.
	if len(st.deleted) != 0 {
		t.Errorf("deleted %v after a failed scan — a root that cannot be read must remove no rows", st.deleted)
	}
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
	if len(movies) != 2 {
		t.Fatalf("len = %d, want 2", len(movies))
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
