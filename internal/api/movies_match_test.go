package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/tmdb"
)

func matchServer(t *testing.T, b Browser, st *fakeStore) http.Handler {
	t.Helper()
	if st == nil {
		st = newFakeStore()
	}
	mux := http.NewServeMux()
	srv := New(st, ScannerFunc(nil), nil, fixtureRoot, quiet())
	if b != nil {
		srv = srv.WithBrowser(b)
	}
	srv.Register(mux)
	srv.RegisterMovieMatch(mux)
	return mux
}

func postMatch(t *testing.T, h http.Handler, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, target, strings.NewReader(body)))
	return rec
}

// The happy path, and the two things about it that are not obvious: the row keeps
// the title parsed off its folder, and the response is the same body GET
// /api/movies/{id} answers so the page can swap the row without a reload.
func TestMatchMovieWritesTheMetadataAndKeepsTheFolderTitle(t *testing.T) {
	st := newFakeStore()
	row := st.seedOnDisk("/media/movies/Avengers - Endgame (2019)", "Avengers - Endgame", 2019)
	browser := &fakeBrowser{details: &tmdb.Details{Match: endgame()}}

	rec := postMatch(t, matchServer(t, browser, st),
		fmt.Sprintf("/api/movies/%d/match", row.ID), `{"tmdb_id":299534}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if browser.gotID != 299534 {
		t.Errorf("asked TMDB for id %d, want 299534 — the body must not be trusted", browser.gotID)
	}

	var got movieBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — body was %s", err, rec.Body)
	}
	if got.TMDBID == nil || *got.TMDBID != 299534 {
		t.Errorf("tmdb_id = %v, want 299534", got.TMDBID)
	}
	if got.Overview == nil || *got.Overview != endgame().Overview {
		t.Errorf("overview = %v, want TMDB's", got.Overview)
	}
	if got.PosterPath == nil || *got.PosterPath != endgame().PosterPath {
		t.Errorf("poster_path = %v, want TMDB's", got.PosterPath)
	}
	// D9: TMDB's canonical "Avengers: Endgame" would undo the ' - ' substitution
	// that identifies the folder, so the row keeps its own title.
	if got.Title != "Avengers - Endgame" {
		t.Errorf("title = %q, want the folder's %q", got.Title, "Avengers - Endgame")
	}
	// Neither title nor year moves. TestMatchMovieKeepsTheFoldersYear says why the
	// year is the interesting half of that.
	if got.Year != 2019 {
		t.Errorf("year = %d, want the folder's 2019", got.Year)
	}
	// The embedded store.Movie means the shape is the GET's, not a new one.
	if got.ID != row.ID {
		t.Errorf("id = %d, want %d", got.ID, row.ID)
	}
}

// **The row keeps the folder's year, and this test is here so the next person to
// "fix" that finds out why before shipping it.**
//
// Writing TMDB's year onto `year` was built in T67, run against a real library,
// and reverted: UpsertMovieByPath's SET list includes `year`, so the very next
// scan rewrites it from the folder name. The row answered 2008 and a deep link,
// then 2019 and a search one scan later.
//
// T68 did make it stick, and deliberately not by freeing this column. `year` is
// half the directory name — importer.go builds the destination folder out of the
// row's title and year — so a matched row whose `year` moved to TMDB's would
// import its next release into a second folder beside the first. TMDB's year
// lives in `tmdb_year` instead ([D37]), which is what
// TestMatchMovieWritesTMDBsYearIntoItsOwnColumn asserts. This test still holds,
// unchanged, and that is the point: both years are now true at once.
//
// [D37]: ../../docs/decisions.md
func TestMatchMovieKeepsTheFoldersYear(t *testing.T) {
	st := newFakeStore()
	// A folder whose year disagrees with TMDB's, which is the only case where
	// this is observable at all.
	row := st.seedOnDisk("/media/movies/Endgame (2018)", "Endgame", 2018)
	browser := &fakeBrowser{details: &tmdb.Details{Match: endgame()}} // TMDB says 2019

	rec := postMatch(t, matchServer(t, browser, st),
		fmt.Sprintf("/api/movies/%d/match", row.ID), `{"tmdb_id":299534}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var got movieBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Year != 2018 {
		t.Errorf("year = %d, want the folder's 2018 — see this test's comment before changing it", got.Year)
	}
}

// The other half of the same match: TMDB's year is recorded, in the column the
// scan does not own, and the response carries it.
func TestMatchMovieWritesTMDBsYearIntoItsOwnColumn(t *testing.T) {
	st := newFakeStore()
	row := st.seedOnDisk("/media/movies/Endgame (2018)", "Endgame", 2018)
	browser := &fakeBrowser{details: &tmdb.Details{Match: endgame()}} // TMDB says 2019

	rec := postMatch(t, matchServer(t, browser, st),
		fmt.Sprintf("/api/movies/%d/match", row.ID), `{"tmdb_id":299534}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var got movieBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.TMDBYear == nil || *got.TMDBYear != 2019 {
		t.Fatalf("tmdb_year = %v, want TMDB's 2019", got.TMDBYear)
	}
	if got.MatchYear() != 2019 {
		t.Errorf("MatchYear() = %d, want 2019 — this is what Jellyfin is asked with", got.MatchYear())
	}
	// Both years survive the round trip through JSON, because the UI reads the
	// row back out of this response rather than reloading it.
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if raw["year"] != float64(2018) || raw["tmdb_year"] != float64(2019) {
		t.Errorf("year/tmdb_year = %v/%v, want 2018/2019", raw["year"], raw["tmdb_year"])
	}
}

// The keyless install is the population that cannot use this at all, and it is
// told which variable to set rather than shown a search that answers nothing.
func TestMatchMovieWithoutABrowserIs503(t *testing.T) {
	st := newFakeStore()
	row := st.seedOnDisk("/media/movies/Backrooms (2026)", "Backrooms", 2026)

	rec := postMatch(t, matchServer(t, nil, st),
		fmt.Sprintf("/api/movies/%d/match", row.ID), `{"tmdb_id":1083381}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "TMDB_API_KEY") {
		t.Errorf("body %s does not name the variable to set", rec.Body)
	}
}

// A row that already names a film is not the row to correct. imports.go decided
// this for the same column and the handler must not have quietly widened it.
func TestMatchMovieOnAnAlreadyMatchedRowIs409(t *testing.T) {
	st := newFakeStore()
	row := st.seedOnDisk("/media/movies/Avengers - Endgame (2019)", "Avengers - Endgame", 2019)
	existing := int64(299534)
	row.TMDBID = &existing
	browser := &fakeBrowser{details: &tmdb.Details{Match: endgame()}}

	rec := postMatch(t, matchServer(t, browser, st),
		fmt.Sprintf("/api/movies/%d/match", row.ID), `{"tmdb_id":24428}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body)
	}
	if row.TMDBID == nil || *row.TMDBID != 299534 {
		t.Errorf("tmdb_id = %v, want the original 299534 untouched", row.TMDBID)
	}
	// Refused before TMDB was asked: the row is read first precisely so a request
	// that cannot succeed does not spend a request finding that out.
	if browser.gotID != 0 {
		t.Errorf("asked TMDB for id %d, want no lookup at all", browser.gotID)
	}
}

// tmdb_id is UNIQUE, and the collision must arrive as a 409 with a sentence
// rather than a 500 carrying a SQLite constraint message.
func TestMatchMovieToAnIDAnotherRowHoldsIs409(t *testing.T) {
	st := newFakeStore()
	taken := st.seedOnDisk("/media/movies/Avengers - Endgame (2019)", "Avengers - Endgame", 2019)
	held := int64(299534)
	taken.TMDBID = &held
	row := st.seedOnDisk("/media/movies/Endgame (2019)", "Endgame", 2019)
	browser := &fakeBrowser{details: &tmdb.Details{Match: endgame()}}

	rec := postMatch(t, matchServer(t, browser, st),
		fmt.Sprintf("/api/movies/%d/match", row.ID), `{"tmdb_id":299534}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "UNIQUE") || strings.Contains(rec.Body.String(), "constraint") {
		t.Errorf("body leaks the driver's constraint message: %s", rec.Body)
	}
	if row.TMDBID != nil {
		t.Errorf("tmdb_id = %v, want it still NULL", *row.TMDBID)
	}
}

// An id TMDB does not know writes nothing. Without the lookup a typo would record
// a row pointing at a film that does not exist.
func TestMatchMovieToAnUnknownTMDBIDIs404AndWritesNothing(t *testing.T) {
	st := newFakeStore()
	row := st.seedOnDisk("/media/movies/Backrooms (2026)", "Backrooms", 2026)
	browser := &fakeBrowser{detailsErr: fmt.Errorf("tmdb movie 999999999: %w", tmdb.ErrNotFound)}

	rec := postMatch(t, matchServer(t, browser, st),
		fmt.Sprintf("/api/movies/%d/match", row.ID), `{"tmdb_id":999999999}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
	if row.TMDBID != nil {
		t.Errorf("tmdb_id = %v, want nothing written", *row.TMDBID)
	}
}

// 502 and not 503: in this codebase 503 means "you did not set the variable",
// which would be a lie when it is set and simply wrong (browse.go).
func TestMatchMovieWithARejectedKeyIs502(t *testing.T) {
	st := newFakeStore()
	row := st.seedOnDisk("/media/movies/Backrooms (2026)", "Backrooms", 2026)
	browser := &fakeBrowser{detailsErr: fmt.Errorf("tmdb movie 1: %w: Invalid API key", tmdb.ErrUnauthorized)}

	rec := postMatch(t, matchServer(t, browser, st),
		fmt.Sprintf("/api/movies/%d/match", row.ID), `{"tmdb_id":1083381}`)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body)
	}
}

func TestMatchMovieRejectsABadRequest(t *testing.T) {
	st := newFakeStore()
	row := st.seedOnDisk("/media/movies/Backrooms (2026)", "Backrooms", 2026)
	good := fmt.Sprintf("/api/movies/%d/match", row.ID)

	for _, tc := range []struct {
		name, target, body string
		want               int
	}{
		{"no tmdb_id", good, `{}`, http.StatusBadRequest},
		{"zero tmdb_id", good, `{"tmdb_id":0}`, http.StatusBadRequest},
		{"negative tmdb_id", good, `{"tmdb_id":-3}`, http.StatusBadRequest},
		{"not json", good, `nonsense`, http.StatusBadRequest},
		{"id not a number", "/api/movies/not-a-number/match", `{"tmdb_id":1}`, http.StatusBadRequest},
		{"no such movie", "/api/movies/4242/match", `{"tmdb_id":1}`, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			browser := &fakeBrowser{details: &tmdb.Details{Match: endgame()}}
			rec := postMatch(t, matchServer(t, browser, st), tc.target, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
			if row.TMDBID != nil {
				t.Errorf("tmdb_id = %v, want nothing written", *row.TMDBID)
			}
		})
	}
}

// The store's refusals must reach the client as themselves. A handler that
// swallowed them into 500 would still pass every test above that stages the
// conflict in the fake's own data, so this forces the error directly.
func TestMatchMoviePassesTheStoresRefusalsThrough(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"already matched", fmt.Errorf("x: %w", store.ErrAlreadyMatched), http.StatusConflict},
		{"id taken", fmt.Errorf("x: %w", store.ErrTMDBIDTaken), http.StatusConflict},
		{"row vanished", fmt.Errorf("x: %w", store.ErrNotFound), http.StatusNotFound},
		{"anything else", errors.New("disk on fire"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeStore()
			row := st.seedOnDisk("/media/movies/Backrooms (2026)", "Backrooms", 2026)
			st.matchErr = tc.err
			browser := &fakeBrowser{details: &tmdb.Details{Match: endgame()}}

			rec := postMatch(t, matchServer(t, browser, st),
				fmt.Sprintf("/api/movies/%d/match", row.ID), `{"tmdb_id":299534}`)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}
