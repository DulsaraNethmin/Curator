package api

import (
	"errors"
	"net/http"
	"testing"

	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/tmdb"
)

// serverWithBrowser is newTestServer plus the TMDB catalogue, which the artwork
// pass reads by id. Scanned against an EMPTY root, so nothing the fixture holds
// interferes with the rows a test seeds by hand.
func serverWithBrowser(t *testing.T, st Store, b Browser) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	New(st, fixtureScanner(), matchAll(), t.TempDir(), quiet()).WithBrowser(b).Register(mux)
	return mux
}

// seedDispatched is the row a Download creates: a TMDB id it was told, and no
// metadata at all. store.UpsertWanted writes exactly this much.
func (f *fakeStore) seedDispatched(mediaType, title string, year int, tmdbID int64) *store.Movie {
	f.nextID++
	id := tmdbID
	row := &store.Movie{
		ID:        f.nextID,
		Title:     title,
		Year:      year,
		MediaType: mediaType,
		Status:    store.StatusWanted,
	}
	if mediaType == store.MediaTypeTV {
		row.TMDBTVID = &id
	} else {
		row.TMDBID = &id
	}
	f.byID[row.ID] = row
	f.order = append(f.order, row)
	return row
}

// The bug, end to end. Measured on the Pi 2026-08-22: five of five rows carried
// a TMDB id and `poster_path: null`, and every one of them had been created by a
// dispatch. matchPass's work list is `<tmdbcol> IS NULL`, so no scan ever looked
// at them again and the library grid drew the noposter fallback for ever.
func TestAScanFillsTheArtworkOfARowItAlreadyMatched(t *testing.T) {
	st := newFakeStore()
	row := st.seedDispatched(store.MediaTypeMovie, "Deadpool", 2016, 293660)

	browser := &fakeBrowser{details: &tmdb.Details{
		Match: tmdb.Match{
			TMDBID:     293660,
			Title:      "Deadpool",
			Overview:   "A wisecracking mercenary.",
			PosterPath: "/3E53WEZJqP6aM84D8CckXx4pIHw.jpg",
		},
	}}
	h := serverWithBrowser(t, st, browser)

	got := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	if got.Artwork != 1 {
		t.Fatalf("artwork = %d, want 1: %+v", got.Artwork, got)
	}
	// Not counted as a match. The row was already matched; this pass fetched
	// what UpsertWanted had no way to know.
	if got.Matched != 0 || got.Unmatched != 0 {
		t.Errorf("matched/unmatched = %d/%d, want 0/0 — the row was matched already",
			got.Matched, got.Unmatched)
	}

	if browser.gotID != 293660 {
		t.Errorf("asked TMDB for id %d, want 293660 — the lookup is by id, never by title", browser.gotID)
	}
	if row.PosterPath == nil || *row.PosterPath != "/3E53WEZJqP6aM84D8CckXx4pIHw.jpg" {
		t.Errorf("poster_path = %v, want the one TMDB sent", row.PosterPath)
	}
	if row.Overview == nil || *row.Overview == "" {
		t.Errorf("overview = %v, want TMDB's", row.Overview)
	}
	// D9: the folder's title identifies the row, and TMDB's canonical spelling
	// would undo the " - " substitution that made it a legal filename.
	if row.Title != "Deadpool" {
		t.Errorf("title = %q, want the row's own — the artwork pass must not rename anything", row.Title)
	}

	// Idempotent: the row is off the list, so a second scan asks nothing.
	browser.gotID = 0
	second := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	if second.Artwork != 0 {
		t.Errorf("second artwork = %d, want 0 — the backfill must not re-ask every scan", second.Artwork)
	}
	if browser.gotID != 0 {
		t.Errorf("second scan asked TMDB for id %d, want nothing", browser.gotID)
	}
}

// A show is looked up against /tv/{id} and never /movie/{id}.
//
// Browser spans both id spaces — unlike Matcher and ShowMatcher, which are two
// interfaces precisely so the wrong one is a compile error — so this is the one
// place the split is made by hand and the only place a test can catch it. TMDB
// numbers films and shows independently: 95396 is Severance's tv id AND some
// film's movie id, so asking the wrong endpoint does not miss, it lands on an
// unrelated title and writes its poster onto the show (D48).
func TestAShowsArtworkComesFromTheTVEndpoint(t *testing.T) {
	st := newFakeStore()
	row := st.seedDispatched(store.MediaTypeTV, "Severance", 2022, 95396)

	browser := &fakeBrowser{
		// What /movie/95396 would answer. If the pass asks the wrong endpoint,
		// this is the poster that lands on the show.
		details: &tmdb.Details{Match: tmdb.Match{
			TMDBID: 95396, Title: "Some Unrelated Film", PosterPath: "/wrong.jpg",
		}},
		showDetails: &tmdb.Details{Match: tmdb.Match{
			TMDBID: 95396, Title: "Severance", Overview: "Work-life balance.", PosterPath: "/right.jpg",
		}},
	}
	h := serverWithBrowser(t, st, browser)

	got := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	if got.ShowsArtwork != 1 {
		t.Fatalf("shows_artwork = %d, want 1: %+v", got.ShowsArtwork, got)
	}
	if got.Artwork != 0 {
		t.Errorf("artwork = %d, want 0 — the show must not be on the film pass", got.Artwork)
	}
	if browser.gotID != 0 {
		t.Fatalf("the show was looked up as a FILM (/movie/%d) — it would take an unrelated film's poster",
			browser.gotID)
	}
	if browser.gotShowID != 95396 {
		t.Errorf("asked /tv/%d, want /tv/95396", browser.gotShowID)
	}
	if row.PosterPath == nil || *row.PosterPath != "/right.jpg" {
		t.Errorf("poster_path = %v, want the show's", row.PosterPath)
	}
}

// A TMDB failure is one row, not the scan. The pass is idempotent and runs again
// next time, so an outage costs a delay rather than a permanent hole — and a 500
// here would turn a working scan into a failed one over an enrichment.
func TestATMDBFailureLeavesTheRowAloneAndTheScanGreen(t *testing.T) {
	st := newFakeStore()
	row := st.seedDispatched(store.MediaTypeMovie, "Deadpool", 2016, 293660)

	browser := &fakeBrowser{detailsErr: errors.New("tmdb is down")}
	h := serverWithBrowser(t, st, browser)

	got := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	if got.Artwork != 0 {
		t.Errorf("artwork = %d, want 0", got.Artwork)
	}
	if row.PosterPath != nil {
		t.Errorf("poster_path = %v, want it left alone", *row.PosterPath)
	}
	if row.TMDBID == nil || *row.TMDBID != 293660 {
		t.Errorf("tmdb_id = %v, want 293660 untouched — a failed enrichment must not unmatch a row", row.TMDBID)
	}
}

// Without a TMDB key there is no catalogue, and the rows are left as they are
// rather than guessed at — the same posture matchPass takes.
func TestWithoutABrowserTheArtworkPassDoesNothing(t *testing.T) {
	st := newFakeStore()
	row := st.seedDispatched(store.MediaTypeMovie, "Deadpool", 2016, 293660)

	// newTestServer attaches no Browser.
	h := newTestServerAt(t, st, matchAll(), t.TempDir())

	got := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	if got.Artwork != 0 {
		t.Errorf("artwork = %d, want 0 with no TMDB key", got.Artwork)
	}
	if row.PosterPath != nil {
		t.Errorf("poster_path = %v, want none", *row.PosterPath)
	}
}
