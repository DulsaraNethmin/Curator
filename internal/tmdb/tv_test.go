package tmdb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

// The two browse lists, and the paths are half the point: TMDB puts the media
// type in a different position for each — /trending/tv/week but /tv/popular —
// so a plausible-looking /trending/movie/week → /trending/tv/week edit paired
// with /movie/popular → /movie/tv is exactly the mistake to guard. pathServer
// fails any path the map does not name.
//
// The recorded pages are trimmed to three shows each. What is under test is the
// name/first_air_date mapping and the path, neither of which gets any truer at
// twenty.
// tv_top_rated.json and tv_on_the_air.json are the two pages here that were
// WRITTEN rather than captured — three shows each, in the recorded shape, with
// obviously synthetic poster paths so nobody mistakes them for a real response.
// That is enough for what this test is: the path asked for, and the
// name/first_air_date mapping. Neither gets truer from a captured page, and
// on_the_air in particular could not be captured usefully — it is a rolling
// seven-day window, so a real recording is stale the week after it is taken.
func TestTrendingShowsAndPopularShows(t *testing.T) {
	client := browseClient(t, map[string]string{
		"/trending/tv/week": "trending_tv_week.json",
		"/tv/popular":       "tv_popular.json",
		"/tv/top_rated":     "tv_top_rated.json",
		"/tv/on_the_air":    "tv_on_the_air.json",
	})
	ctx := context.Background()

	for name, tc := range map[string]struct {
		call      func() ([]Match, error)
		wantFirst string
		wantYear  int
	}{
		"TrendingShows": {func() ([]Match, error) { return client.TrendingShows(ctx) }, "The Last of Us", 2023},
		"PopularShows":  {func() ([]Match, error) { return client.PopularShows(ctx) }, "Grey's Anatomy", 2005},
		"TopRatedShows": {func() ([]Match, error) { return client.TopRatedShows(ctx) }, "Breaking Bad", 2008},
		"OnTheAir":      {func() ([]Match, error) { return client.OnTheAir(ctx) }, "Silo", 2023},
	} {
		got, err := tc.call()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(got) != 3 {
			t.Errorf("%s: got %d, want the recorded page of 3", name, len(got))
			continue
		}
		// Each list must come back from ITS OWN path — a swapped pair would
		// still be three well-formed cards.
		if got[0].Title != tc.wantFirst || got[0].Year != tc.wantYear {
			t.Errorf("%s: first card = %q (%d), want %q (%d)", name, got[0].Title, got[0].Year, tc.wantFirst, tc.wantYear)
		}
		for i, m := range got {
			if m.TMDBID == 0 || m.Title == "" || m.Year == 0 {
				t.Errorf("%s: card %d is unmapped: %+v", name, i, m)
			}
		}
	}
}

// A show with no backdrop is a card like any other — Family Guy's is null in the
// recorded page, and dropping it to make a grid look tidy is not this project's
// habit.
func TestPopularShowsKeepsACardWithNoBackdrop(t *testing.T) {
	client := browseClient(t, map[string]string{"/tv/popular": "tv_popular.json"})

	got, err := client.PopularShows(context.Background())
	if err != nil {
		t.Fatalf("PopularShows: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d cards, want 3 — the one with a null backdrop was dropped", len(got))
	}
	if got[1].Title != "Family Guy" || got[1].BackdropPath != "" {
		t.Errorf("card 1 = %+v, want Family Guy with an empty BackdropPath", got[1])
	}
}

func TestTVBrowseEmptyPageIsNotNil(t *testing.T) {
	client := browseClient(t, map[string]string{"/tv/popular": "empty.json"})

	got, err := client.PopularShows(context.Background())
	if err != nil {
		t.Fatalf("PopularShows: %v", err)
	}
	if got == nil {
		t.Error("an empty page came back nil")
	}
	if len(got) != 0 {
		t.Errorf("got %d results, want 0", len(got))
	}
}

func TestShowDecodesEverythingTheScreenShows(t *testing.T) {
	client := browseClient(t, map[string]string{"/tv/1396": "tv_1396.json"})

	got, err := client.Show(context.Background(), 1396)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}

	if got.TMDBID != 1396 || got.Title != "Breaking Bad" || got.Year != 2008 {
		t.Errorf("identity = %d %q %d", got.TMDBID, got.Title, got.Year)
	}
	if got.NumberOfSeasons != 5 {
		t.Errorf("NumberOfSeasons = %d, want 5", got.NumberOfSeasons)
	}
	if got.NumberOfEpisodes != 62 {
		t.Errorf("NumberOfEpisodes = %d, want 62", got.NumberOfEpisodes)
	}
	if got.FirstAirDate != "2008-01-20" {
		t.Errorf("FirstAirDate = %q — the full date, not just the year", got.FirstAirDate)
	}
	if got.LastAirDate != "2013-09-29" {
		t.Errorf("LastAirDate = %q", got.LastAirDate)
	}
	// episode_run_time is [45, 47]; the first entry is what TMDB's own pages
	// show, and averaging would invent a number TMDB never states.
	if got.EpisodeRuntime != 45 {
		t.Errorf("EpisodeRuntime = %d, want 45 — the first entry of episode_run_time", got.EpisodeRuntime)
	}
	if got.Status != "Ended" {
		t.Errorf("Status = %q, want television's own vocabulary", got.Status)
	}
	if got.Tagline != "Change the equation." {
		t.Errorf("Tagline = %q", got.Tagline)
	}
	if got.Homepage == "" {
		t.Error("Homepage is empty")
	}
	if got.OriginalLanguage != "en" {
		t.Errorf("OriginalLanguage = %q", got.OriginalLanguage)
	}
	if len(got.Genres) != 2 || got.Genres[0] != "Drama" {
		t.Errorf("Genres = %v, want them in TMDB's own order", got.Genres)
	}
	if len(got.Studios) == 0 || got.Studios[0] != "Sony Pictures Television Studios" {
		t.Errorf("Studios = %v", got.Studios)
	}
	if len(got.SpokenLanguages) != 2 || got.SpokenLanguages[0] != "English" {
		t.Errorf("SpokenLanguages = %v", got.SpokenLanguages)
	}
	if got.BackdropPath == "" || got.PosterPath == "" {
		t.Error("the screen has no images")
	}
}

// The film-only fields stay zero rather than being filled with something close.
// A show with ReleaseDate set would read as "released 2008-01-20" about
// something that ran until 2013, and a Runtime of 45 would compare an episode
// to a whole picture.
func TestShowLeavesTheFilmOnlyFieldsZero(t *testing.T) {
	client := browseClient(t, map[string]string{"/tv/1396": "tv_1396.json"})

	got, err := client.Show(context.Background(), 1396)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.ReleaseDate != "" {
		t.Errorf("ReleaseDate = %q, want empty — a show has FirstAirDate", got.ReleaseDate)
	}
	if got.Runtime != 0 {
		t.Errorf("Runtime = %d, want 0 — a show has EpisodeRuntime", got.Runtime)
	}
}

// The IMDb id, which /tv/{id} does not carry on its own — append_to_response
// nests /tv/{id}/external_ids into the same payload, and this pins that it is
// actually read out of there rather than left at the zero value.
//
// Verbatim, `tt` and all. Stripping the prefix is one indexer's business and is
// done at that indexer's boundary; a package that reports what TMDB says must
// not start reformatting for a caller.
func TestShowCarriesTheIMDbIDFromTheAppendedExternalIDs(t *testing.T) {
	client := browseClient(t, map[string]string{"/tv/1396": "tv_1396.json"})

	got, err := client.Show(context.Background(), 1396)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.IMDBID != "tt0903747" {
		t.Errorf("IMDBID = %q, want tt0903747 with the prefix TMDB spells", got.IMDBID)
	}
}

// The request itself, because the append is invisible in the decoded result
// when the fixture happens to carry external_ids anyway. A Show that stopped
// asking for them would keep passing the test above forever against this
// fixture and return an empty id against the live API.
func TestShowAsksForExternalIDsInOneRequest(t *testing.T) {
	var paths, appends []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		appends = append(appends, r.URL.Query().Get("append_to_response"))
		w.Header().Set("Content-Type", "application/json")
		w.Write(readFixture(t, "tv_1396.json"))
	}))
	defer srv.Close()

	if _, err := New("k", nil, WithBaseURL(srv.URL)).Show(context.Background(), 1396); err != nil {
		t.Fatalf("Show: %v", err)
	}
	// One request. The whole argument for append_to_response over a second GET
	// is that there is no second GET.
	if len(paths) != 1 || paths[0] != "/tv/1396" {
		t.Fatalf("requested %v, want exactly one /tv/1396", paths)
	}
	if appends[0] != "external_ids" {
		t.Errorf("append_to_response = %q, want external_ids", appends[0])
	}
}

// seasons[] is decoded whole and unfiltered, and the fixture is chosen for the
// two rows that are not ordinary: Specials at season_number 0, and — on Silo,
// which is where this was measured — a season with episode_count 0.
//
// Reported rather than filtered here on purpose. number_of_seasons cannot say
// how many episodes a season has, which is the entire reason this field exists;
// deciding which of them a person may click is the screen's job.
func TestShowDecodesEverySeasonTMDBLists(t *testing.T) {
	client := browseClient(t, map[string]string{"/tv/1396": "tv_1396.json"})

	got, err := client.Show(context.Background(), 1396)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if len(got.Seasons) != 3 {
		t.Fatalf("got %d seasons, want 3 — in TMDB's own order", len(got.Seasons))
	}
	// Specials first, exactly as TMDB orders them. This is the row a picker
	// must not turn into a button, because season=0 already means "no season
	// constraint" at the search API.
	if got.Seasons[0].Number != 0 || got.Seasons[0].Name != "Specials" || got.Seasons[0].EpisodeCount != 11 {
		t.Errorf("season 0 = %+v, want Specials with 11 episodes", got.Seasons[0])
	}
	if got.Seasons[1].Number != 1 || got.Seasons[1].EpisodeCount != 7 {
		t.Errorf("season 1 = %+v, want 7 episodes", got.Seasons[1])
	}
	if got.Seasons[1].AirDate != "2008-01-20" {
		t.Errorf("season 1 AirDate = %q", got.Seasons[1].AirDate)
	}
	// The count and the list are two different facts and both are kept: 5
	// against three listed here, because this fixture is trimmed. On the live
	// API they disagree for a better reason — Silo reports 4 and lists a fourth
	// with no episodes in it.
	if got.NumberOfSeasons != 5 {
		t.Errorf("NumberOfSeasons = %d, want 5 — the count is not the list", got.NumberOfSeasons)
	}
}

// A season TMDB announced but has not aired, which is the case the count cannot
// express and the one that put an empty Season 4 on Silo's screen.
func TestSeasonWithNoEpisodesIsReportedRatherThanDropped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":125988,"name":"Silo","first_air_date":"2023-05-04",
			"number_of_seasons":4,
			"seasons":[
			  {"season_number":1,"name":"Season 1","episode_count":10,"air_date":"2023-05-04"},
			  {"season_number":4,"name":"Season 4","episode_count":0,"air_date":null}
			]}`))
	}))
	defer srv.Close()

	got, err := New("k", nil, WithBaseURL(srv.URL)).Show(context.Background(), 125988)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if len(got.Seasons) != 2 {
		t.Fatalf("got %d seasons, want both — the empty one is a fact, not a row to hide here", len(got.Seasons))
	}
	if got.Seasons[1].Number != 4 || got.Seasons[1].EpisodeCount != 0 {
		t.Errorf("season 4 = %+v, want episode_count 0 preserved", got.Seasons[1])
	}
	// A null air_date decodes to empty rather than failing the whole show.
	if got.Seasons[1].AirDate != "" {
		t.Errorf("AirDate = %q, want empty for a season with no date", got.Seasons[1].AirDate)
	}
}

// An id comes from a URL a human can type, so "no such show" must be tellable
// from "TMDB is having a bad day". The fixture is the movie one because TMDB's
// error envelope is byte-identical for both — it names no media type.
func TestShowNotFound(t *testing.T) {
	client := browseClient(t, map[string]string{"/tv/999999999": "movie_notfound.json"})

	_, err := client.Show(context.Background(), 999999999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound — the same sentinel Movie returns", err)
	}
	if !strings.Contains(err.Error(), "could not be found") {
		t.Errorf("err = %q, want TMDB's message", err)
	}
	// The error names what was asked for, and never the URL, which carries the key.
	if !strings.Contains(err.Error(), "show 999999999") {
		t.Errorf("err = %q, want it to say which show", err)
	}
}

func TestShowRejectsANonID(t *testing.T) {
	for _, id := range []int{0, -1} {
		if _, err := New("k", nil).Show(context.Background(), id); err == nil {
			t.Errorf("Show(%d) was accepted", id)
		}
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
