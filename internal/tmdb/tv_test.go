package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// The fixtures in testdata/search_tv_*.json carry TMDB's real television shape:
// `name` and `first_air_date` where a film has `title` and `release_date`. That
// difference is what these tests exist to pin — decode the movie struct against
// a TV payload and every card comes back with an empty title and year 0, which
// is a bug no compiler catches.

// newTVFixtureServer is newFixtureServer against /search/tv.
func newTVFixtureServer(t *testing.T, byQuery map[string]string) *fixtureServer {
	t.Helper()
	return newSearchServer(t, "/search/tv", byQuery)
}

func TestSearchShowMapsNameAndFirstAirDate(t *testing.T) {
	fs := newTVFixtureServer(t, map[string]string{
		"Breaking Bad": "search_tv_breaking_bad.json",
	})

	got, err := newTestClient(t, fs).SearchShow(context.Background(), "Breaking Bad", 2008)
	if err != nil {
		t.Fatalf("SearchShow: %v", err)
	}
	if got == nil {
		t.Fatal("SearchShow returned no match, want Breaking Bad")
	}
	if got.TMDBID != 1396 {
		t.Errorf("TMDBID = %d, want 1396", got.TMDBID)
	}
	// The whole point: Title comes from `name`, which the movie struct does not
	// read, and Year from `first_air_date`, which it does not read either.
	if got.Title != "Breaking Bad" {
		t.Errorf("Title = %q, want it mapped from `name`", got.Title)
	}
	if got.Year != 2008 {
		t.Errorf("Year = %d, want 2008 from `first_air_date`", got.Year)
	}
	if got.Overview == "" {
		t.Error("Overview is empty")
	}
	if got.PosterPath != "/ggFHVNu6YYI5L9pCfOacjizRGt.jpg" {
		t.Errorf("PosterPath = %q", got.PosterPath)
	}
	if got.BackdropPath == "" {
		t.Error("BackdropPath is empty; the show screen has no banner")
	}
	if got.VoteAverage == 0 {
		t.Error("VoteAverage is zero")
	}
	if n := len(fs.seen()); n != 1 {
		t.Errorf("made %d requests, want 1", n)
	}
}

// The year parameter for television is first_air_date_year. TMDB ignores
// primary_release_year on /search/tv rather than rejecting it, so a copy-paste
// from the movie path would silently stop narrowing anything and nothing but
// this test would say so.
func TestSearchShowSendsQueryParams(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/tv" {
			t.Errorf("path = %q, want /search/tv", r.URL.Path)
		}
		got = r.URL.Query()
		w.Write(readFixture(t, "empty.json"))
	}))
	defer srv.Close()

	c := New("secret-key", srv.Client(), WithBaseURL(srv.URL))
	if _, err := c.SearchShow(context.Background(), "Fringe", 2008); err != nil {
		t.Fatalf("SearchShow: %v", err)
	}
	for key, want := range map[string]string{
		"api_key":             "secret-key",
		"query":               "Fringe",
		"first_air_date_year": "2008",
	} {
		if v := got.Get(key); v != want {
			t.Errorf("%s = %q, want %q", key, v, want)
		}
	}
	if v := got.Get("primary_release_year"); v != "" {
		t.Errorf("primary_release_year = %q, want it absent — TMDB ignores it here", v)
	}
}

// Television is where a wrong year does the most damage: TMDB lists four
// distinct shows called "The Office" and popularity puts the 2005 one first, so
// asking for 2001 must return the 2001 one rather than the leading result.
func TestSearchShowPicksYearNotFirstResult(t *testing.T) {
	fs := newTVFixtureServer(t, map[string]string{
		"The Office": "search_tv_the_office.json",
	})

	got, err := newTestClient(t, fs).SearchShow(context.Background(), "The Office", 2001)
	if err != nil {
		t.Fatalf("SearchShow: %v", err)
	}
	if got == nil || got.TMDBID != 2426 {
		t.Fatalf("match = %+v, want the 2001 Office (2426), not the leading 2005 entry", got)
	}
	if got.Year != 2001 {
		t.Errorf("Year = %d, want 2001", got.Year)
	}
}

// A year that agrees with nothing in the result set is "not found", never the
// closest thing TMDB happened to return.
func TestSearchShowWrongYearRejected(t *testing.T) {
	fs := newTVFixtureServer(t, map[string]string{
		"Breaking Bad": "search_tv_breaking_bad.json",
	})

	got, err := newTestClient(t, fs).SearchShow(context.Background(), "Breaking Bad", 1999)
	if err != nil {
		t.Fatalf("SearchShow: %v, want nil", err)
	}
	if got != nil {
		t.Fatalf("match = %+v, want nil for a year that disagrees", got)
	}
	if n := len(fs.seen()); n != 1 {
		t.Errorf("made %d requests, want 1 — rejected results are not zero results", n)
	}
}

// The title-parsing trap, on the television side. A folder named
// "Star Wars - The Clone Wars" is a colon substitution; the fallback is tried
// only because the raw title found nothing.
func TestSearchShowFallbackAfterCollapse(t *testing.T) {
	fs := newTVFixtureServer(t, map[string]string{
		"Star Wars - The Clone Wars": "empty.json",
		"Star Wars The Clone Wars":   "search_tv_clone_wars.json",
	})

	got, err := newTestClient(t, fs).SearchShow(context.Background(), "Star Wars - The Clone Wars", 2008)
	if err != nil {
		t.Fatalf("SearchShow: %v", err)
	}
	if got == nil || got.TMDBID != 4194 {
		t.Fatalf("match = %+v, want TMDBID 4194", got)
	}
	if got.Title != "Star Wars: The Clone Wars" {
		t.Errorf("Title = %q, want the colon restored by TMDB itself", got.Title)
	}
	want := []string{"Star Wars - The Clone Wars", "Star Wars The Clone Wars"}
	if queries := fs.seen(); !equal(queries, want) {
		t.Errorf("queries = %q, want %q", queries, want)
	}
}

// The other half of the same rule: results that exist but disagree on the year
// are not zero results, so they must not trigger the fallback. Verified here by
// omitting the collapsed query from the map — a second request fails the test.
func TestSearchShowDoesNotFallBackOnARejectedYear(t *testing.T) {
	fs := newTVFixtureServer(t, map[string]string{
		"Star Wars - The Clone Wars": "search_tv_clone_wars.json",
	})

	got, err := newTestClient(t, fs).SearchShow(context.Background(), "Star Wars - The Clone Wars", 1977)
	if err != nil {
		t.Fatalf("SearchShow: %v", err)
	}
	if got != nil {
		t.Fatalf("match = %+v, want nil", got)
	}
	if n := len(fs.seen()); n != 1 {
		t.Fatalf("made %d requests, want exactly 1 — a rejected year is not a fallback", n)
	}
}

func TestSearchShowZeroResultsGivesUpAfterTheFallback(t *testing.T) {
	fs := newTVFixtureServer(t, map[string]string{
		"Nothing At All - Really": "empty.json",
		"Nothing At All Really":   "empty.json",
	})

	got, err := newTestClient(t, fs).SearchShow(context.Background(), "Nothing At All - Really", 2026)
	if err != nil {
		t.Fatalf("SearchShow: %v, want nil — not found is not a failure", err)
	}
	if got != nil {
		t.Fatalf("match = %+v, want nil", got)
	}
	if n := len(fs.seen()); n != 2 {
		t.Errorf("made %d requests, want 2 — one attempt plus one fallback", n)
	}
}

func TestSearchShowEmptyTitle(t *testing.T) {
	c := New("test-key", http.DefaultClient, WithBaseURL("http://127.0.0.1:0"))
	if _, err := c.SearchShow(context.Background(), "  ", 2008); err == nil {
		t.Fatal("err = nil, want an error rather than a pointless request")
	}
}

// SearchShows is not SearchShow: it returns the whole page, in TMDB's order,
// and never rejects a year — the four Offices are the choice, not a problem to
// solve on the human's behalf.
func TestSearchShowsReturnsEveryResult(t *testing.T) {
	fs := newTVFixtureServer(t, map[string]string{
		"The Office": "search_tv_the_office.json",
	})

	got, err := newTestClient(t, fs).SearchShows(context.Background(), "The Office", 0)
	if err != nil {
		t.Fatalf("SearchShows: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d results, want the whole page of 4", len(got))
	}
	if got[0].TMDBID != 2316 || got[0].Year != 2005 {
		t.Errorf("first result = %+v, want the 2005 Office first, in TMDB's own order", got[0])
	}
	// Every card is mapped, not just the one the year test looks at.
	for i, m := range got {
		if m.Title != "The Office" {
			t.Errorf("result %d: Title = %q, want it mapped from `name`", i, m.Title)
		}
		if m.Year == 0 {
			t.Errorf("result %d: Year = 0, want it mapped from `first_air_date`", i)
		}
	}
	// A show with no poster comes back like any other; the UI has a fallback and
	// dropping data to make a grid look tidy is not this project's habit.
	if got[2].PosterPath == "" {
		t.Error("the Indian adaptation lost its poster")
	}
}

func TestSearchShowsFallsBackOnceToo(t *testing.T) {
	fs := newTVFixtureServer(t, map[string]string{
		"Star Wars - The Clone Wars": "empty.json",
		"Star Wars The Clone Wars":   "search_tv_clone_wars.json",
	})

	got, err := newTestClient(t, fs).SearchShows(context.Background(), "Star Wars - The Clone Wars", 0)
	if err != nil {
		t.Fatalf("SearchShows: %v", err)
	}
	if len(got) != 1 || got[0].TMDBID != 4194 {
		t.Fatalf("results = %+v, want the collapsed query's single hit", got)
	}
	if n := len(fs.seen()); n != 2 {
		t.Errorf("made %d requests, want 2", n)
	}
}

func TestSearchShowsRejectsAnEmptyQuery(t *testing.T) {
	// No server: it must fail before making a request.
	if _, err := New("k", nil).SearchShows(context.Background(), "   ", 0); err == nil {
		t.Error("an empty query was accepted")
	}
}

// An empty page is [] and never nil, because the UI iterates it.
func TestSearchShowsEmptyPageIsNotNil(t *testing.T) {
	fs := newTVFixtureServer(t, map[string]string{"Backrooms": "empty.json"})

	got, err := newTestClient(t, fs).SearchShows(context.Background(), "Backrooms", 0)
	if err != nil {
		t.Fatalf("SearchShows: %v", err)
	}
	if got == nil {
		t.Error("an empty page came back nil")
	}
	if len(got) != 0 {
		t.Errorf("got %d results, want 0", len(got))
	}
}
