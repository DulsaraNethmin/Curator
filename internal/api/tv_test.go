package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DulsaraNethmin/curator/internal/library"
	"github.com/DulsaraNethmin/curator/internal/store"
)

// Television, at the HTTP boundary.
//
// The first three tests in this file are the ones that lose data, and they are
// first on purpose. Everything else here is a wrong answer; those three are a
// deleted library.

const tvFixtureRoot = "../../testdata/library/tv"

// tvFixtureScanner walks the television fixture at a floor it can clear. The
// fixture files are kilobytes, because a 50 MiB fixture would be a 400 MB
// repository — the same reason fixtureScanner drops the floor for films.
func tvFixtureScanner() ShowScannerFunc {
	return func(root string) ([]library.Show, []library.Skipped, error) {
		return library.ScanShows(root, library.FeatureOpts{MinBytes: 1024})
	}
}

// televisionServer is a server with both roots configured, which is the shape
// an install with LIBRARY_TV set has.
func televisionServer(t *testing.T, st Store) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	srv := New(st, fixtureScanner(), nil, fixtureRoot, quiet()).
		WithTV(TV{Root: tvFixtureRoot, Scanner: tvFixtureScanner()})
	srv.Register(mux)
	srv.RegisterStream(mux)
	return mux
}

// filmsOnlyServer is the same install with LIBRARY_TV unset — television off,
// which is the default and the state every existing install upgrades into.
func filmsOnlyServer(t *testing.T, st Store) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	srv := New(st, fixtureScanner(), nil, fixtureRoot, quiet())
	srv.Register(mux)
	srv.RegisterStream(mux)
	return mux
}

// **The trap that decided the whole design.**
//
// prune computes `outside` as AssertInside(root, row.LibraryPath) != nil, and
// `case outside` sits BEFORE `case recorded`. With one root, a show row was not
// merely unfound by a scan — it was affirmatively DELETED, with a log line
// reading "outside LIBRARY_MOVIES, so it can never be served", and
// store.DeleteMovie took its downloads with it through the foreign key. The
// first movie scan after the first television import emptied the TV library.
func TestAScanDoesNotPruneTheOtherMediaTypesRows(t *testing.T) {
	st := newFakeStore()
	film := st.seedOnDisk(fixtureRoot+"/Interstellar (2014)", "Interstellar", 2014)
	show := st.seedShowOnDisk(tvFixtureRoot+"/Severance (2022)", "Severance", 2022)

	rec := do(t, televisionServer(t, st), http.MethodPost, "/api/scan")
	out := decodeScan(t, rec)

	if _, ok := st.byID[show.ID]; !ok {
		t.Fatalf("the movie half of the scan deleted the show row — removed=%d", out.Removed)
	}
	if _, ok := st.byID[film.ID]; !ok {
		t.Fatalf("the television half of the scan deleted the film row — removed=%d", out.Removed)
	}
	if out.Removed != 0 {
		t.Errorf("removed = %d, want 0: both rows describe a folder that is right there", out.Removed)
	}
}

// The same trap from the direction every existing install will actually meet
// it: television was configured, shows were imported, and then LIBRARY_TV was
// unset — or the image was upgraded and never set it.
//
// A root nobody walked produces no finding about anything under it. That is
// D33's asymmetry, and it is why the `!walked` arm has to sit BEFORE the
// containment check: AssertInside("", path) resolves the empty root to the
// working directory, so asking it would find every show "outside" and delete
// the lot.
func TestWithNoTelevisionRootShowRowsAreKeptRatherThanDeleted(t *testing.T) {
	st := newFakeStore()
	show := st.seedShowOnDisk("/media/tv/Severance (2022)", "Severance", 2022)
	orphan := st.seedShowOnDisk("/media/tv/Nothing Here (2019)", "Nothing Here", 2019)

	rec := do(t, filmsOnlyServer(t, st), http.MethodPost, "/api/scan")
	out := decodeScan(t, rec)

	for _, row := range []*store.Movie{show, orphan} {
		if _, ok := st.byID[row.ID]; !ok {
			t.Fatalf("%q was deleted by a scan that never walked its root", row.Title)
		}
	}
	if out.Removed != 0 {
		t.Errorf("removed = %d, want 0", out.Removed)
	}
	// Kept, and SAID to be kept. A row nothing accounted for is reported, not
	// silently dropped from the counts.
	if out.Missing != 2 {
		t.Errorf("missing = %d, want 2 — both shows are unaccounted for, not invisible", out.Missing)
	}
	// And the television counters are all zero, which is the honest report of a
	// root that was never walked rather than of an empty one.
	if out.Shows != 0 || out.ShowsEmpty != 0 {
		t.Errorf("shows=%d shows_empty=%d, want 0 and 0 with LIBRARY_TV unset", out.Shows, out.ShowsEmpty)
	}
}

// A show folder that genuinely holds no episode still loses its row, because
// that is a positive finding — the folder was read and there is nothing in it.
// D33 unchanged, applied to the second root.
func TestAShowFolderWithNoEpisodeInItLosesItsRow(t *testing.T) {
	st := newFakeStore()
	// The fixture's own empty show: read fine, nothing named like an episode.
	gone := st.seedShowOnDisk(tvFixtureRoot+"/The Leftovers (2014)", "The Leftovers", 2014)
	kept := st.seedShowOnDisk(tvFixtureRoot+"/Severance (2022)", "Severance", 2022)

	rec := do(t, televisionServer(t, st), http.MethodPost, "/api/scan")
	out := decodeScan(t, rec)

	if _, ok := st.byID[gone.ID]; ok {
		t.Error("a show folder with no episode in it kept its row")
	}
	if _, ok := st.byID[kept.ID]; !ok {
		t.Fatal("the show that does hold episodes lost its row")
	}
	if out.Removed != 1 {
		t.Errorf("removed = %d, want 1", out.Removed)
	}
	if out.ShowsEmpty < 1 {
		t.Errorf("shows_empty = %d, want at least 1 — an empty folder is reported, not just acted on", out.ShowsEmpty)
	}
}

// The scan finds the fixture's shows, and finds them again unchanged. A rescan
// that added rows would mean the identity key was not the folder.
func TestScanningTelevisionTwiceIsIdempotent(t *testing.T) {
	st := newFakeStore()
	h := televisionServer(t, st)

	first := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	if first.Shows == 0 {
		t.Fatal("the television fixture produced no shows")
	}
	if first.ShowsAdded != first.Shows {
		t.Errorf("shows_added = %d, shows = %d — the first scan should add all of them", first.ShowsAdded, first.Shows)
	}

	second := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	if second.Shows != first.Shows {
		t.Errorf("second scan found %d shows, first found %d", second.Shows, first.Shows)
	}
	if second.ShowsAdded != 0 {
		t.Errorf("shows_added = %d on a rescan, want 0", second.ShowsAdded)
	}
	if second.Removed != 0 {
		t.Errorf("a rescan removed %d rows", second.Removed)
	}
}

// The film keys keep exactly the meaning they had before phase 11, so a screen
// reading `scanned` still gets films.
func TestTheFilmCountersAreUnchangedByTelevision(t *testing.T) {
	st := newFakeStore()
	withTV := decodeScan(t, do(t, televisionServer(t, st), http.MethodPost, "/api/scan"))

	st2 := newFakeStore()
	withoutTV := decodeScan(t, do(t, filmsOnlyServer(t, st2), http.MethodPost, "/api/scan"))

	if withTV.Scanned != withoutTV.Scanned || withTV.Empty != withoutTV.Empty {
		t.Errorf("television changed the film counters: scanned %d vs %d, empty %d vs %d",
			withTV.Scanned, withoutTV.Scanned, withTV.Empty, withoutTV.Empty)
	}
}

// One list per media type. Before T88 these were one query, so a show would
// have rendered as a film on the library grid.
func TestFilmsAndShowsDoNotAppearInEachOthersLists(t *testing.T) {
	st := newFakeStore()
	st.seedOnDisk("/media/movies/Interstellar (2014)", "Interstellar", 2014)
	st.seedShowOnDisk("/media/tv/Severance (2022)", "Severance", 2022)
	h := televisionServer(t, st)

	for _, tc := range []struct{ path, want, notWant string }{
		{"/api/movies", "Interstellar", "Severance"},
		{"/api/shows", "Severance", "Interstellar"},
	} {
		rec := do(t, h, http.MethodGet, tc.path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d: %s", tc.path, rec.Code, rec.Body.String())
		}
		var rows []store.Movie
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatalf("%s: decode: %v", tc.path, err)
		}
		if len(rows) != 1 {
			t.Fatalf("%s returned %d rows, want 1", tc.path, len(rows))
		}
		if rows[0].Title != tc.want {
			t.Errorf("%s returned %q, want %q", tc.path, rows[0].Title, tc.want)
		}
		if strings.Contains(rec.Body.String(), tc.notWant) {
			t.Errorf("%s leaked %q from the other media type", tc.path, tc.notWant)
		}
	}
}

// D40: the refusal's sentence is written at the boundary that answers it, and
// it names the variable somebody has to set.
func TestTelevisionRoutesRefuseByNamingLibraryTV(t *testing.T) {
	st := newFakeStore()
	h := filmsOnlyServer(t, st)

	for _, path := range []string{"/api/shows", "/api/shows/1"} {
		rec := do(t, h, http.MethodGet, path)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503: %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "LIBRARY_TV") {
			t.Errorf("%s: %q does not name LIBRARY_TV", path, rec.Body.String())
		}
	}
}

// Playback is deliberately films-only in phase 11: a show is not one file, and
// episodes play in Jellyfin. This needs no new guard — stream.go's
// AssertInside(s.libraryRoot, …) already refuses a row under the TV root — and
// this test exists so that nobody "fixes" that refusal by widening the check.
func TestStreamingAShowIsStillRefused(t *testing.T) {
	st := newFakeStore()
	show := st.seedShowOnDisk(tvFixtureRoot+"/Severance (2022)", "Severance", 2022)
	h := televisionServer(t, st)

	for _, path := range []string{
		"/api/movies/1/stream",
		"/api/movies/1/remux",
	} {
		rec := do(t, h, http.MethodGet, path)
		if rec.Code == http.StatusOK {
			t.Errorf("%s served a show (row %d)", path, show.ID)
		}
	}
}

// The collision that made tmdb_tv_id necessary, at the dispatch boundary.
//
// alreadyHave consults LibraryByTMDBID, and unscoped it would find the FILM
// holding movie id 95396 and refuse a grab for Severance — tv id 95396 — with
// "curator already has this film, at /media/movies/…". A sentence the user
// cannot act on, about a film they never asked about.
func TestATelevisionGrabIsNotRefusedByAFilmHoldingTheSameNumber(t *testing.T) {
	st := newFakeStore()
	film := st.seedOnDisk("/media/movies/Coincidence (2011)", "Coincidence", 2011)
	id := int64(95396)
	film.TMDBID = &id
	path := *film.LibraryPath
	st.library = map[int64]store.LibraryState{
		95396: {MovieID: film.ID, Status: store.StatusImported, LibraryPath: &path},
	}

	mux := http.NewServeMux()
	srv := New(st, fixtureScanner(), nil, fixtureRoot, quiet()).
		WithTV(TV{Root: tvFixtureRoot, Scanner: tvFixtureScanner()}).
		WithDownloads(&fakeDispatcher{})
	srv.Register(mux)
	srv.RegisterDownloads(mux)

	body := `{"release_id":"abc123","media_type":"tv","title":"Severance","year":2022,"tmdb_id":95396}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/downloads", strings.NewReader(body))
	mux.ServeHTTP(rec, req)

	if rec.Code == http.StatusConflict {
		t.Fatalf("a television grab was refused because a FILM holds the same TMDB number: %s", rec.Body.String())
	}
	// And the film's own id space still refuses, so the guard was scoped rather
	// than removed.
	film2 := `{"release_id":"abc123","title":"Coincidence","year":2011,"tmdb_id":95396}`
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/api/downloads", strings.NewReader(film2)))
	if rec2.Code != http.StatusConflict {
		t.Errorf("a film curator already has was dispatched anyway: status = %d", rec2.Code)
	}
}
