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
