package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DulsaraNethmin/curator/internal/indexer"
	"github.com/DulsaraNethmin/curator/internal/store"
)

// fakeSearcher stands in for the aggregator. It records what it was asked so a
// test can prove the handler passed the query through rather than inventing one.
type fakeSearcher struct {
	result indexer.SearchResult
	err    error

	magnets   map[string]string
	magnetErr error

	gotQuery indexer.Query
	gotTitle string
	gotYear  int
	gotID    string
}

func (f *fakeSearcher) Search(_ context.Context, q indexer.Query) (indexer.SearchResult, error) {
	f.gotQuery = q
	f.gotTitle, f.gotYear = q.Title, q.Year
	if f.err != nil {
		return indexer.SearchResult{}, f.err
	}
	return f.result, nil
}

func (f *fakeSearcher) ResolveMagnet(_ context.Context, id string) (string, error) {
	f.gotID = id
	if f.magnetErr != nil {
		return "", f.magnetErr
	}
	magnet, ok := f.magnets[id]
	if !ok {
		return "", fmt.Errorf("resolve %s: %w", id, indexer.ErrReleaseExpired)
	}
	return magnet, nil
}

func newSearchServer(t *testing.T, s Searcher) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	srv := New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, quiet()).WithSearch(s)
	srv.Register(mux)
	srv.RegisterSearch(mux)
	return mux
}

// searchFound builds a merged release the way the aggregator would hand one over.
// newTVSearchServer is newSearchServer with television switched on, which is
// what an install with LIBRARY_TV set has. Without it every media=tv request
// answers 503 naming the variable (D48), and a test asserting a 400 would pass
// or fail for the wrong reason.
func newTVSearchServer(t *testing.T, s Searcher) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	srv := New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, quiet()).
		WithSearch(s).
		WithTV(TV{Root: tvFixtureRoot, Scanner: tvFixtureScanner()})
	srv.Register(mux)
	srv.RegisterSearch(mux)
	return mux
}

func searchFound(id, title, quality string, seeders int, magnet string, indexers ...string) indexer.Found {
	return indexer.Found{
		Release: indexer.Release{
			Title: title, Year: 2014, Quality: quality,
			SizeBytes: 2469606195, Seeders: seeders, Magnet: magnet,
		},
		ID:       id,
		Indexers: indexers,
	}
}

func decodeSearch(t *testing.T, rec *httptest.ResponseRecorder) searchResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return out
}

// decodeError asserts the phase 1 error shape: {"error": "..."} with a real code.
func decodeError(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) string {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d: %s", rec.Code, wantStatus, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if body["error"] == "" {
		t.Errorf("body = %s, want an {\"error\": \"...\"} message", rec.Body.String())
	}
	return body["error"]
}

// internal/indexer spells the two media types out itself rather than importing
// internal/store, which would drag the SQLite driver into a package that
// searches torrent sites. This is the cheap half of that import: the two
// spellings have to agree, and nothing in either package would notice if they
// drifted. This is the one place that imports both.
func TestTheIndexersMediaTypesAreTheStoresMediaTypes(t *testing.T) {
	if indexer.MediaMovie != store.MediaTypeMovie {
		t.Errorf("indexer.MediaMovie = %q, store.MediaTypeMovie = %q", indexer.MediaMovie, store.MediaTypeMovie)
	}
	if indexer.MediaTV != store.MediaTypeTV {
		t.Errorf("indexer.MediaTV = %q, store.MediaTypeTV = %q", indexer.MediaTV, store.MediaTypeTV)
	}
}

func TestSearchReturnsReleasesAndIndexerOutcomes(t *testing.T) {
	fake := &fakeSearcher{result: indexer.SearchResult{
		Releases: []indexer.Found{
			searchFound("3f2a9c1b7d4e5a60", "Interstellar.2014.1080p.BluRay.x265", "1080p", 512,
				"magnet:?xt=urn:btih:"+"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0", "yts", "tpb"),
			searchFound("8b1d0e6f2c7a4593", "Interstellar.2014.PROPER.IMAX.1080p.UHD.BluRay", "1080p", 87,
				"", "1337x"),
		},
		Outcomes: []indexer.Outcome{
			{Name: "yts", OK: true, Count: 12},
			{Name: "tpb", OK: true, Count: 30},
			{Name: "1337x", OK: false, Error: "calling minter: connection refused"},
		},
	}}

	got := decodeSearch(t, do(t, newSearchServer(t, fake), http.MethodGet,
		"/api/search?title=Interstellar&year=2014"))

	if fake.gotTitle != "Interstellar" || fake.gotYear != 2014 {
		t.Errorf("searched for %q/%d, want Interstellar/2014", fake.gotTitle, fake.gotYear)
	}
	// This route is the film one. A television search reaches the indexers
	// through a query of its own, not by this handler learning a second media
	// type — and a blank media type here would be a film by default anyway,
	// which is exactly the kind of default worth spelling out.
	if fake.gotQuery.Media != indexer.MediaMovie {
		t.Errorf("searched media %q, want %q", fake.gotQuery.Media, indexer.MediaMovie)
	}
	if fake.gotQuery.Season != 0 {
		t.Errorf("searched season %d, want 0 — a film has none", fake.gotQuery.Season)
	}
	if got.Title != "Interstellar" || got.Year != 2014 {
		t.Errorf("echoed %q/%d", got.Title, got.Year)
	}
	if len(got.Releases) != 2 {
		t.Fatalf("releases = %d, want 2", len(got.Releases))
	}
	if got.Releases[0].Seeders != 512 || got.Releases[0].ID != "3f2a9c1b7d4e5a60" {
		t.Errorf("first release = %+v", got.Releases[0])
	}
	if len(got.Releases[0].Indexers) != 2 {
		t.Errorf("indexers = %v, want both recorded", got.Releases[0].Indexers)
	}

	// The per-indexer block is the point: a down indexer is reported, not hidden.
	if len(got.Indexers) != 3 {
		t.Fatalf("indexer outcomes = %d, want 3", len(got.Indexers))
	}
	var failures int
	for _, ix := range got.Indexers {
		if ix.OK {
			continue
		}
		failures++
		if ix.Name != "1337x" || ix.Error == "" {
			t.Errorf("failed outcome = %+v, want 1337x carrying its error", ix)
		}
	}
	if failures != 1 {
		t.Errorf("failures = %d, want 1", failures)
	}
}

// null means "not resolved yet"; an empty string would read as "there is no
// magnet", which for an unresolved 1337x release is simply false.
func TestSearchUnresolvedMagnetIsNullNotEmptyString(t *testing.T) {
	fake := &fakeSearcher{result: indexer.SearchResult{
		Releases: []indexer.Found{searchFound("8b1d0e6f2c7a4593", "unresolved", "1080p", 87, "", "1337x")},
		Outcomes: []indexer.Outcome{{Name: "1337x", OK: true, Count: 1}},
	}}

	rec := do(t, newSearchServer(t, fake), http.MethodGet, "/api/search?title=Interstellar")

	var raw struct {
		Releases []map[string]json.RawMessage `json:"releases"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := string(raw.Releases[0]["magnet"]); got != "null" {
		t.Errorf("magnet = %s, want null", got)
	}
}

func TestSearchRequiresATitle(t *testing.T) {
	h := newSearchServer(t, &fakeSearcher{})
	for _, target := range []string{"/api/search", "/api/search?title=", "/api/search?title=%20%20"} {
		t.Run(target, func(t *testing.T) {
			decodeError(t, do(t, h, http.MethodGet, target), http.StatusBadRequest)
		})
	}
}

func TestSearchRejectsANonNumericYear(t *testing.T) {
	h := newSearchServer(t, &fakeSearcher{})
	for _, target := range []string{
		"/api/search?title=Interstellar&year=two+thousand+fourteen",
		"/api/search?title=Interstellar&year=2014.5",
		"/api/search?title=Interstellar&year=-1",
	} {
		t.Run(target, func(t *testing.T) {
			decodeError(t, do(t, h, http.MethodGet, target), http.StatusBadRequest)
		})
	}
}

// A search with no year is normal, not an error.
func TestSearchWithoutAYearIsFine(t *testing.T) {
	fake := &fakeSearcher{result: indexer.SearchResult{Outcomes: []indexer.Outcome{{Name: "yts", OK: true}}}}
	got := decodeSearch(t, do(t, newSearchServer(t, fake), http.MethodGet, "/api/search?title=Dune"))
	if fake.gotYear != 0 {
		t.Errorf("year = %d, want 0 for an absent year", fake.gotYear)
	}
	if got.Releases == nil {
		t.Error("releases = null, want [] — an empty result is a list with nothing in it")
	}
}

// The request succeeded; the sources failed, and the response says which. A 500
// here would be a lie about whose fault it is.
func TestSearchEveryIndexerFailingIsStill200(t *testing.T) {
	fake := &fakeSearcher{result: indexer.SearchResult{
		Outcomes: []indexer.Outcome{
			{Name: "yts", OK: false, Error: "yts: 503"},
			{Name: "tpb", OK: false, Error: "tpb: dial tcp: i/o timeout"},
			{Name: "1337x", OK: false, Error: "calling minter: connection refused"},
		},
	}}

	rec := do(t, newSearchServer(t, fake), http.MethodGet, "/api/search?title=Interstellar&year=2014")
	got := decodeSearch(t, rec)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if len(got.Releases) != 0 {
		t.Errorf("releases = %d, want 0", len(got.Releases))
	}
	if len(got.Indexers) != 3 {
		t.Fatalf("outcomes = %d, want all 3 failures listed", len(got.Indexers))
	}
	for _, ix := range got.Indexers {
		if ix.OK || ix.Error == "" {
			t.Errorf("outcome %+v, want a failure carrying its cause", ix)
		}
	}
}

// An indexer whose companion service is not running is reported as
// unconfigured, and the flag reaches the client rather than being inferred from
// the error string (docs/tasks/T49-minter-on-demand.md). A screen matching on a
// message is a screen that stops working the first time the message improves.
func TestSearchReportsAnUnconfiguredIndexer(t *testing.T) {
	fake := &fakeSearcher{result: indexer.SearchResult{
		Releases: []indexer.Found{
			searchFound("a", "Dune", "1080p", 500, "magnet:?xt=urn:btih:aa", "yts"),
		},
		Outcomes: []indexer.Outcome{
			{Name: "yts", OK: true, Count: 1},
			{Name: "1337x", OK: false, Unconfigured: true,
				Error: "1337x search: calling minter at http://minter:8191 (is it running?): connection refused"},
		},
	}}

	rec := do(t, newSearchServer(t, fake), http.MethodGet, "/api/search?title=Dune")
	got := decodeSearch(t, rec)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	// The healthy indexer's release still comes back: an unconfigured source
	// does not empty a good search, it only stops looking like one.
	if len(got.Releases) != 1 {
		t.Fatalf("releases = %d, want the 1 from yts", len(got.Releases))
	}

	byName := map[string]indexerBody{}
	for _, ix := range got.Indexers {
		byName[ix.Name] = ix
	}
	if ix := byName["1337x"]; ix.OK || !ix.Unconfigured {
		t.Errorf("1337x outcome = %+v, want ok=false and unconfigured=true", ix)
	}
	if ix := byName["yts"]; !ix.OK || ix.Unconfigured {
		t.Errorf("yts outcome = %+v, want ok=true and unconfigured=false", ix)
	}

	// omitempty, so the key is absent on every ordinary outcome rather than
	// reading as a state each indexer has.
	if strings.Count(rec.Body.String(), `"unconfigured"`) != 1 {
		t.Errorf("body = %s, want exactly one unconfigured key", rec.Body.String())
	}
}

// The third state, and the one that would otherwise be invisible: a source the
// search never asked because the media type is not one it has. Reported rather
// than dropped, and reported as itself rather than as a failure or as a search
// that found nothing — ok:true, count:0 is the format that reads as "nobody
// uploaded it".
func TestSearchReportsASourceWithNothingToSearch(t *testing.T) {
	fake := &fakeSearcher{result: indexer.SearchResult{
		Releases: []indexer.Found{
			searchFound("a", "Severance S02E05", "1080p", 381, "magnet:?xt=urn:btih:aa", "tpb"),
		},
		Outcomes: []indexer.Outcome{
			{Name: "yts", NotApplicable: true},
			{Name: "tpb", OK: true, Count: 1},
		},
	}}

	rec := do(t, newSearchServer(t, fake), http.MethodGet, "/api/search?title=Severance")
	got := decodeSearch(t, rec)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	byName := map[string]indexerBody{}
	for _, ix := range got.Indexers {
		byName[ix.Name] = ix
	}
	if ix := byName["yts"]; !ix.NotApplicable || ix.OK || ix.Count != 0 || ix.Error != "" {
		t.Errorf("yts outcome = %+v, want not_applicable with no ok, no count and no error", ix)
	}
	if ix := byName["tpb"]; !ix.OK || ix.NotApplicable {
		t.Errorf("tpb outcome = %+v, want an ordinary success", ix)
	}
	if strings.Count(rec.Body.String(), `"not_applicable"`) != 1 {
		t.Errorf("body = %s, want exactly one not_applicable key", rec.Body.String())
	}
}

func TestSearchFiltersByQuality(t *testing.T) {
	fake := &fakeSearcher{result: indexer.SearchResult{
		Releases: []indexer.Found{
			searchFound("a", "four k", "2160p", 10, "magnet:?xt=urn:btih:aa", "yts"),
			searchFound("b", "ten eighty", "1080p", 500, "magnet:?xt=urn:btih:bb", "yts"),
			searchFound("c", "seven twenty", "720p", 300, "magnet:?xt=urn:btih:cc", "yts"),
		},
		Outcomes: []indexer.Outcome{{Name: "yts", OK: true, Count: 3}},
	}}
	h := newSearchServer(t, fake)

	got := decodeSearch(t, do(t, h, http.MethodGet, "/api/search?title=x&quality=1080p,2160p"))
	if len(got.Releases) != 2 {
		t.Fatalf("releases = %d, want the 2160p and 1080p kept", len(got.Releases))
	}
	for _, r := range got.Releases {
		if r.Quality == "720p" {
			t.Errorf("720p survived a 1080p,2160p filter")
		}
	}

	// FilterQuality accepts the spellings people actually type.
	if got := decodeSearch(t, do(t, h, http.MethodGet, "/api/search?title=x&quality=4k")); len(got.Releases) != 1 {
		t.Errorf("quality=4k kept %d, want 1", len(got.Releases))
	}
	if got := decodeSearch(t, do(t, h, http.MethodGet, "/api/search?title=x&quality=1080")); len(got.Releases) != 1 {
		t.Errorf("quality=1080 kept %d, want 1", len(got.Releases))
	}
	// A blank filter is no filter, not a filter matching nothing.
	if got := decodeSearch(t, do(t, h, http.MethodGet, "/api/search?title=x&quality=")); len(got.Releases) != 3 {
		t.Errorf("empty quality kept %d, want all 3", len(got.Releases))
	}
}

func TestResolveMagnetReturnsMagnetAndInfoHash(t *testing.T) {
	const hash = "89599BF4DC369A3A8ECA26411C5CCF922D78B486"
	fake := &fakeSearcher{magnets: map[string]string{
		"3f2a9c1b7d4e5a60": "magnet:?xt=urn:btih:" + hash + "&dn=Interstellar",
	}}

	rec := do(t, newSearchServer(t, fake), http.MethodGet, "/api/releases/3f2a9c1b7d4e5a60/magnet")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got magnetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if fake.gotID != "3f2a9c1b7d4e5a60" {
		t.Errorf("resolved id %q", fake.gotID)
	}
	if got.InfoHash != hash {
		t.Errorf("info_hash = %q, want %q", got.InfoHash, hash)
	}
	if got.Magnet == "" {
		t.Error("magnet is empty")
	}
}

// An id whose search has aged out is 410, not 404 and not 500: it was real, and
// searching again will produce a working one.
func TestResolveMagnetExpiredIs410(t *testing.T) {
	fake := &fakeSearcher{magnets: map[string]string{}}
	msg := decodeError(t, do(t, newSearchServer(t, fake), http.MethodGet,
		"/api/releases/deadbeefdeadbeef/magnet"), http.StatusGone)
	if msg == "" {
		t.Error("410 carried no message")
	}
}

func TestResolveMagnetOtherFailuresAre500(t *testing.T) {
	fake := &fakeSearcher{magnetErr: errors.New("1337x resolve magnet: minter returned 502")}
	decodeError(t, do(t, newSearchServer(t, fake), http.MethodGet,
		"/api/releases/3f2a9c1b7d4e5a60/magnet"), http.StatusInternalServerError)
}

// Phase 1's routes must still answer on a server that also serves search.
func TestPhase1RoutesSurviveSearchRegistration(t *testing.T) {
	h := newSearchServer(t, &fakeSearcher{})
	if rec := do(t, h, http.MethodGet, "/api/movies"); rec.Code != http.StatusOK {
		t.Errorf("/api/movies = %d, want 200", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/api/movies/999"); rec.Code != http.StatusNotFound {
		t.Errorf("/api/movies/999 = %d, want 404", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/api/movies/abc"); rec.Code != http.StatusBadRequest {
		t.Errorf("/api/movies/abc = %d, want 400", rec.Code)
	}
}

// The other half of D20, at the layer that decides the folder name.
//
// The aggregator strips the colon before asking an indexer, because a colon
// costs 1337x every result it would have returned. What must NOT change is what
// the API echoes: dispatch stores that title and library.DestFolder turns it
// into "Avengers - Endgame (2019)". If the normalisation ever leaked up to here,
// the library would fill with folders named after a search query.
func TestSearchEchoesTheCanonicalTitleColonIncluded(t *testing.T) {
	fake := &fakeSearcher{}
	rec := do(t, newSearchServer(t, fake), http.MethodGet, "/api/search?title=Avengers%3A+Endgame&year=2019")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body struct {
		Title string `json:"title"`
		Year  int    `json:"year"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Title != "Avengers: Endgame" {
		t.Errorf("echoed title = %q, want the canonical %q — this becomes the folder name",
			body.Title, "Avengers: Endgame")
	}
	// And the handler hands the searcher what it was given; normalising is the
	// aggregator's job, one layer down, where the cache key is decided.
	if fake.gotTitle != "Avengers: Endgame" {
		t.Errorf("the searcher was asked %q, want the title verbatim", fake.gotTitle)
	}
}

// TestSearchPassesTheEpisodeDown pins the happy path: the number reaches the
// indexers and comes back out in the echo, which is the only place the answer
// records which episode it is the answer to.
func TestSearchPassesTheEpisodeDown(t *testing.T) {
	fake := &fakeSearcher{}
	got := decodeSearch(t, do(t, newTVSearchServer(t, fake), http.MethodGet,
		"/api/search?title=Severance&media=tv&season=2&episode=5"))

	if fake.gotQuery.Season != 2 || fake.gotQuery.Episode != 5 {
		t.Errorf("searched season %d episode %d, want 2/5",
			fake.gotQuery.Season, fake.gotQuery.Episode)
	}
	if got.Season != 2 || got.Episode != 5 {
		t.Errorf("echoed season %d episode %d, want 2/5", got.Season, got.Episode)
	}
}

// TestSearchRefusesAnEpisodeItCannotHonour is the guard, and the middle case is
// the one worth having.
//
// An episode with no season is not a narrower search, it is a search that finds
// NOTHING while reporting ok:true, count:0 — no release names itself "E05", the
// convention is S02E05. That is the same silent-empty failure D20 and
// NormaliseQuery exist to prevent, and the only place a caller can still be told
// is here.
func TestSearchRefusesAnEpisodeItCannotHonour(t *testing.T) {
	for _, tt := range []struct {
		label string
		url   string
	}{
		{"an episode on a film", "/api/search?title=Interstellar&year=2014&episode=5"},
		{"an episode with no season", "/api/search?title=Severance&media=tv&episode=5"},
		{"an episode that is not a number", "/api/search?title=Severance&media=tv&season=2&episode=five"},
		{"a negative episode", "/api/search?title=Severance&media=tv&season=2&episode=-1"},
	} {
		t.Run(tt.label, func(t *testing.T) {
			fake := &fakeSearcher{}
			rec := do(t, newTVSearchServer(t, fake), http.MethodGet, tt.url)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			// And the search must not have been made at all. A 400 that still
			// launched a browser behind minter would cost thirteen seconds to
			// return an error it knew before it started.
			if fake.gotQuery.Title != "" {
				t.Errorf("the indexers were searched anyway, with %+v", fake.gotQuery)
			}
		})
	}
}

// The IMDb id reaches the indexers in TMDB's own spelling, prefix and all.
// EZTV is the only thing that reads it, and the strip to bare digits happens at
// that indexer's boundary rather than here.
func TestSearchPassesTheIMDbIDDown(t *testing.T) {
	fake := &fakeSearcher{}
	do(t, newTVSearchServer(t, fake), http.MethodGet,
		"/api/search?title=Silo&media=tv&season=3&episode=8&imdb_id=tt14688458")

	if fake.gotQuery.IMDBID != "tt14688458" {
		t.Errorf("searched with imdb_id %q, want tt14688458 verbatim", fake.gotQuery.IMDBID)
	}
}

// No id is the state every search made before this parameter existed, and the
// state every screen that has none is still in. It is not an error.
func TestSearchWithoutAnIMDbIDIsFine(t *testing.T) {
	fake := &fakeSearcher{}
	rec := do(t, newTVSearchServer(t, fake), http.MethodGet, "/api/search?title=Silo&media=tv")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if fake.gotQuery.IMDBID != "" {
		t.Errorf("IMDBID = %q, want empty", fake.gotQuery.IMDBID)
	}
}

// **A malformed id is a 400 rather than an ignored parameter**, and the reason
// is the same shape as the season's.
//
// The one indexer that reads it would simply DECLINE a value it cannot use,
// which on the screen is indistinguishable from that indexer being switched
// off. So a client sending a TMDB id here — the mistake that is actually
// available to make, since the show screen holds both numbers — would get a
// search quietly missing a source, with nothing anywhere saying so.
func TestSearchRefusesAnIDThatIsNotAnIMDbID(t *testing.T) {
	for _, tt := range []struct {
		label string
		url   string
	}{
		{"a tmdb id", "/api/search?title=Silo&media=tv&imdb_id=125988"},
		{"the digits with no prefix", "/api/search?title=Silo&media=tv&imdb_id=14688458"},
		{"a title", "/api/search?title=Silo&media=tv&imdb_id=Silo"},
		{"the prefix alone", "/api/search?title=Silo&media=tv&imdb_id=tt"},
	} {
		t.Run(tt.label, func(t *testing.T) {
			fake := &fakeSearcher{}
			rec := do(t, newTVSearchServer(t, fake), http.MethodGet, tt.url)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if fake.gotQuery.Title != "" {
				t.Errorf("the indexers were searched anyway, with %+v", fake.gotQuery)
			}
		})
	}
}

// TestSearchStampsTheMatchTier proves the screen is told which releases are the
// thing asked for and which merely contain it, rather than re-deriving it.
func TestSearchStampsTheMatchTier(t *testing.T) {
	pack := searchFound("aaaa000000000000", "Severance - Season 2 - Mp4 x264 AC3 1080p", "1080p", 727, "magnet:?xt=urn:btih:aa", "tpb")
	pack.Release.Season = 2
	episode := searchFound("bbbb000000000000", "Severance S02E05 1080p ATVP WEB-DL", "1080p", 381, "magnet:?xt=urn:btih:bb", "tpb")
	episode.Release.Season, episode.Release.Episode = 2, 5
	unstated := searchFound("cccc000000000000", "Severance COMPLETE 1080p WEB-DL", "1080p", 90, "magnet:?xt=urn:btih:cc", "tpb")

	fake := &fakeSearcher{result: indexer.SearchResult{
		Releases: []indexer.Found{episode, pack, unstated},
	}}
	got := decodeSearch(t, do(t, newTVSearchServer(t, fake), http.MethodGet,
		"/api/search?title=Severance&media=tv&season=2&episode=5"))

	want := []string{"exact", "pack", "unstated"}
	if len(got.Releases) != len(want) {
		t.Fatalf("releases = %d, want %d", len(got.Releases), len(want))
	}
	for i, w := range want {
		if got.Releases[i].Match != w {
			t.Errorf("release %d (%q) match = %q, want %q",
				i, got.Releases[i].Title, got.Releases[i].Match, w)
		}
	}
}

// TestSearchStampsNoTierWithoutASeason: without a season every release is exact
// by definition, and stamping "exact" on a film's releases would put a key on
// every search that has nothing to do with television.
func TestSearchStampsNoTierWithoutASeason(t *testing.T) {
	fake := &fakeSearcher{result: indexer.SearchResult{
		Releases: []indexer.Found{
			searchFound("3f2a9c1b7d4e5a60", "Interstellar.2014.1080p", "1080p", 512, "magnet:?xt=urn:btih:aa", "yts"),
		},
	}}
	got := decodeSearch(t, do(t, newSearchServer(t, fake), http.MethodGet,
		"/api/search?title=Interstellar&year=2014"))

	if len(got.Releases) != 1 {
		t.Fatalf("releases = %d, want 1", len(got.Releases))
	}
	if got.Releases[0].Match != "" {
		t.Errorf("match = %q on a film search, want it absent", got.Releases[0].Match)
	}
}
