package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// eztvFixtureSilo is a recorded get-torrents response, captured 2026-08-21 from
// https://eztvx.to/api/get-torrents?imdb_id=14688458 and trimmed to eight rows.
//
// The eight are chosen rather than taken off the top, because each one is a
// shape the parser has to survive: four exact S03E08s, another episode of the
// same season, TWO SEASON PACKS (episode "0" beside a stated season, which is
// how EZTV spells one), and a row from a different season entirely. The sizes
// are untouched and one of them — 1521403286 — is past what a 32-bit int holds.
const eztvFixtureSilo = "eztv-silo.json"

// eztvSiloIMDbID is Silo's id in both spellings the boundary has to accept.
const (
	eztvSiloIMDbID     = "tt14688458"
	eztvSiloIMDbDigits = "14688458"
)

// eztvStub is an httptest.Server standing in for the EZTV API. It records every
// URL it was asked for, which is how the pagination tests read what happened.
type eztvStub struct {
	indexer  *EZTV
	requests []string
}

func newEZTVStubFunc(t *testing.T, h http.HandlerFunc) *eztvStub {
	t.Helper()
	stub := &eztvStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.requests = append(stub.requests, r.URL.String())
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	// The base URL carries a path, exactly as the real one does, so a bug that
	// drops it would show as a request to "/get-torrents".
	stub.indexer = NewEZTV(srv.Client(), WithEZTVBaseURL(srv.URL+"/api"))
	return stub
}

func newEZTVStub(t *testing.T, fixture string) *eztvStub {
	t.Helper()
	return newEZTVStubFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(ytsFixture(t, fixture))
	})
}

// eztvPage builds a synthetic page of n rows, for the pagination tests where
// what matters is the COUNT of rows rather than what is in them.
func eztvPage(t *testing.T, count, total int) []byte {
	t.Helper()
	type row struct {
		Title     string `json:"title"`
		MagnetURL string `json:"magnet_url"`
		Seeds     int    `json:"seeds"`
		SizeBytes string `json:"size_bytes"`
		Season    string `json:"season"`
		Episode   string `json:"episode"`
	}
	page := struct {
		TorrentsCount int   `json:"torrents_count"`
		Torrents      []row `json:"torrents"`
	}{TorrentsCount: total}
	for i := 0; i < count; i++ {
		page.Torrents = append(page.Torrents, row{
			Title: fmt.Sprintf("Show S01E%02d 1080p", i+1),
			// A distinct, valid 40-hex info hash per row, so dedupe does not
			// collapse the page into one release and hide a counting bug.
			MagnetURL: fmt.Sprintf("magnet:?xt=urn:btih:%040x", i+1),
			Seeds:     i,
			SizeBytes: "1000",
			Season:    "1",
			Episode:   fmt.Sprintf("%d", i+1),
		})
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("build page: %v", err)
	}
	return raw
}

// The main parse: a recorded response becomes one Release per row, carrying the
// season and episode EZTV states rather than a reading of the name.
func TestEZTVSearch(t *testing.T) {
	stub := newEZTVStub(t, eztvFixtureSilo)

	releases, err := stub.indexer.Search(context.Background(),
		Query{Title: "Silo", Media: MediaTV, IMDBID: eztvSiloIMDbID})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(releases) != 8 {
		t.Fatalf("got %d releases, want 8", len(releases))
	}

	// One page, because the fixture is short — the early stop, not the cap.
	if len(stub.requests) != 1 {
		t.Fatalf("made %d requests, want 1 for a short page: %v", len(stub.requests), stub.requests)
	}
	got, err := url.Parse(stub.requests[0])
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if got.Path != "/api/get-torrents" {
		t.Errorf("path = %q, want the base URL's own path kept", got.Path)
	}
	// The tt is stripped HERE and nowhere earlier: TMDB reports it, EZTV
	// refuses it.
	if q := got.Query(); q.Get("imdb_id") != eztvSiloIMDbDigits {
		t.Errorf("imdb_id = %q, want the bare digits %q", q.Get("imdb_id"), eztvSiloIMDbDigits)
	}
	if q := got.Query(); q.Get("limit") != "100" {
		t.Errorf("limit = %q, want 100 — above the cap the API silently gives 30", q.Get("limit"))
	}

	first := releases[0]
	if first.Indexer != eztvIndexerName {
		t.Errorf("Indexer = %q", first.Indexer)
	}
	if first.Title != "Silo S03E08 XviD-AFG EZTV" {
		t.Errorf("Title = %q", first.Title)
	}
	// size_bytes arrives as the STRING "288220838". A decode that assumed a
	// number would have failed the whole page; one that shrugged would zero
	// every size on the screen.
	if first.SizeBytes != 288220838 {
		t.Errorf("SizeBytes = %d, want 288220838 read out of a JSON string", first.SizeBytes)
	}
	if first.Seeders != 254 {
		t.Errorf("Seeders = %d, want 254 — `seeds` is a number in the same object", first.Seeders)
	}
	if first.Season != 3 || first.Episode != 8 {
		t.Errorf("season/episode = %d/%d, want 3/8 from the stated fields", first.Season, first.Episode)
	}
	if InfoHash(first.Magnet) == "" {
		t.Errorf("Magnet = %q, want a usable one straight from magnet_url", first.Magnet)
	}
	// The year is 0 on every television release — a show's year is its first
	// air year and an episode's name carries its own. See Query.searchYear.
	for i, r := range releases {
		if r.Year != 0 {
			t.Errorf("release %d has Year %d, want 0 for television", i, r.Year)
		}
	}
}

// A size past 2^31, which the fixture carries on purpose: 1521403286 is fine in
// an int64 and wrong in anything narrower, and it is a real row.
func TestEZTVReadsASizePastTwoGigabytes(t *testing.T) {
	stub := newEZTVStub(t, eztvFixtureSilo)

	releases, err := stub.indexer.Search(context.Background(),
		Query{Title: "Silo", Media: MediaTV, IMDBID: eztvSiloIMDbID})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	var found bool
	for _, r := range releases {
		if r.SizeBytes == 1521403286 {
			found = true
		}
	}
	if !found {
		t.Error("no release carries the fixture's 1521403286-byte row")
	}
}

// EZTV's own fields beat parseSeasonEpisode, and the case that proves it is one
// the parser would get WRONG rather than merely differently.
func TestEZTVPrefersTheStatedSeasonOverTheName(t *testing.T) {
	// A name that says nothing usable at all beside fields that say everything.
	// parseSeasonEpisode reads names and never guesses, so it would return 0/0
	// here and the row would sort as TierUnstated — kept, but last, under
	// releases that answer the question less well.
	body := `{"torrents_count":1,"torrents":[{
		"title":"Silo Gray Goo 1080p WEB-DL","magnet_url":"magnet:?xt=urn:btih:` +
		strings.Repeat("a", 40) + `","seeds":10,"size_bytes":"100","season":"3","episode":"8"}]}`

	stub := newEZTVStubFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})

	releases, err := stub.indexer.Search(context.Background(),
		Query{Title: "Silo", Media: MediaTV, IMDBID: eztvSiloIMDbDigits})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("got %d releases, want 1", len(releases))
	}
	if season, episode := parseSeasonEpisode(releases[0].Title); season != 0 || episode != 0 {
		t.Fatalf("the name now parses to %d/%d — pick one that does not, or this test proves nothing", season, episode)
	}
	if releases[0].Season != 3 || releases[0].Episode != 8 {
		t.Errorf("season/episode = %d/%d, want 3/8 — the stated fields, not the name",
			releases[0].Season, releases[0].Episode)
	}
}

// A season pack is episode "0" beside a stated season, and it must NOT trigger
// the name fallback: re-reading "Silo S02 1080p x265-ELiTE EZTV" would at best
// agree and at worst lose the season. It is D49's TierPack and the reason a
// single-episode search still finds a pack that contains it.
func TestEZTVKeepsASeasonPackAsAPack(t *testing.T) {
	stub := newEZTVStub(t, eztvFixtureSilo)

	releases, err := stub.indexer.Search(context.Background(),
		Query{Title: "Silo", Media: MediaTV, IMDBID: eztvSiloIMDbID})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	var packs int
	for _, r := range releases {
		if r.Episode != 0 {
			continue
		}
		packs++
		if r.Season == 0 {
			t.Errorf("pack %q lost its season", r.Title)
		}
		// And it answers a single-episode query for that season as a pack.
		q := Query{Media: MediaTV, Season: r.Season, Episode: 5}
		if tier := SeasonTier(r, q); tier != TierPack {
			t.Errorf("%q is tier %d against season %d episode 5, want TierPack", r.Title, tier, r.Season)
		}
	}
	if packs != 2 {
		t.Fatalf("found %d packs in the fixture, want 2", packs)
	}
}

// Nothing stated at all falls back to the name, which is what every other
// indexer does for every row.
func TestEZTVFallsBackToTheNameWhenNothingIsStated(t *testing.T) {
	body := `{"torrents_count":1,"torrents":[{
		"title":"Silo S03E08 1080p WEB-DL","magnet_url":"magnet:?xt=urn:btih:` +
		strings.Repeat("b", 40) + `","seeds":10,"size_bytes":"100","season":"0","episode":"0"}]}`

	stub := newEZTVStubFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})

	releases, err := stub.indexer.Search(context.Background(),
		Query{Title: "Silo", Media: MediaTV, IMDBID: eztvSiloIMDbDigits})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("got %d releases, want 1", len(releases))
	}
	if releases[0].Season != 3 || releases[0].Episode != 8 {
		t.Errorf("season/episode = %d/%d, want 3/8 read off the name",
			releases[0].Season, releases[0].Episode)
	}
}

// The number type accepts a JSON number as well as a JSON string, so EZTV
// changing its mind about the encoding costs one field rather than every
// television search.
func TestEZTVReadsANumberAsWellAsAString(t *testing.T) {
	body := `{"torrents_count":1,"torrents":[{
		"title":"Silo S03E08 1080p","magnet_url":"magnet:?xt=urn:btih:` +
		strings.Repeat("c", 40) + `","seeds":10,"size_bytes":288220838,"season":3,"episode":8}]}`

	stub := newEZTVStubFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})

	releases, err := stub.indexer.Search(context.Background(),
		Query{Title: "Silo", Media: MediaTV, IMDBID: eztvSiloIMDbDigits})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("got %d releases, want 1", len(releases))
	}
	if releases[0].SizeBytes != 288220838 || releases[0].Season != 3 || releases[0].Episode != 8 {
		t.Errorf("release = %+v, want the same values a string encoding gives", releases[0])
	}
}

// An id EZTV does not know answers 200 with a count and NO `torrents` key at
// all — measured, imdb_id=0000000. That decodes cleanly into nothing, and
// nothing is the right answer. It must not be an error and must not panic.
func TestEZTVCountWithoutTorrentsIsNoResults(t *testing.T) {
	stub := newEZTVStubFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"imdb_id":"0000000","torrents_count":0,"limit":100,"page":1}`))
	})

	releases, err := stub.indexer.Search(context.Background(),
		Query{Title: "Nothing", Media: MediaTV, IMDBID: "tt0000000"})
	if err != nil {
		t.Fatalf("Search: %v, want no error — an unknown show is not a failure", err)
	}
	if len(releases) != 0 {
		t.Errorf("got %d releases, want 0", len(releases))
	}
	if len(stub.requests) != 1 {
		t.Errorf("made %d requests, want 1 — an empty page ends the paging", len(stub.requests))
	}
}

// THE pagination test: three pages and not a fourth, because the cap is a
// deliberate bound on the shared search budget rather than an accident.
func TestEZTVStopsAtTheThirdPage(t *testing.T) {
	stub := newEZTVStubFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		// Always a full page, and a total far beyond what three pages can
		// reach — The Simpsons' 2,425 rows are this case.
		_, _ = w.Write(eztvPage(t, eztvPageLimit, 2425))
	})

	releases, err := stub.indexer.Search(context.Background(),
		Query{Title: "The Simpsons", Media: MediaTV, IMDBID: "tt0096697"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(stub.requests) != eztvMaxPages {
		t.Fatalf("made %d requests, want %d — the cap is the point", len(stub.requests), eztvMaxPages)
	}
	if len(releases) != eztvMaxPages*eztvPageLimit {
		t.Errorf("got %d releases, want %d", len(releases), eztvMaxPages*eztvPageLimit)
	}
	// And each page was asked for by number, in order. A paging bug that asked
	// for page 1 three times would otherwise pass everything above.
	for i, raw := range stub.requests {
		got, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse request: %v", err)
		}
		if want := fmt.Sprintf("%d", i+1); got.Query().Get("page") != want {
			t.Errorf("request %d asked for page %q, want %q", i, got.Query().Get("page"), want)
		}
	}
}

// A short page is the last page, so a show that fits in two costs two requests
// rather than the cap.
func TestEZTVStopsOnAShortPage(t *testing.T) {
	stub := newEZTVStubFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write(eztvPage(t, eztvPageLimit, 146))
			return
		}
		_, _ = w.Write(eztvPage(t, 46, 146))
	})

	releases, err := stub.indexer.Search(context.Background(),
		Query{Title: "Game of Thrones", Media: MediaTV, IMDBID: "tt0944947"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(stub.requests) != 2 {
		t.Fatalf("made %d requests, want 2 — a short page ends it", len(stub.requests))
	}
	if len(releases) != 146 {
		t.Errorf("got %d releases, want 146", len(releases))
	}
}

// torrents_count is a stop condition, for the show whose catalogue divides
// exactly into full pages and would otherwise cost a wasted third request.
func TestEZTVStopsWhenTheCountIsCovered(t *testing.T) {
	stub := newEZTVStubFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(eztvPage(t, eztvPageLimit, 2*eztvPageLimit))
	})

	if _, err := stub.indexer.Search(context.Background(),
		Query{Title: "Show", Media: MediaTV, IMDBID: "tt1"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(stub.requests) != 2 {
		t.Errorf("made %d requests, want 2 — the count was covered", len(stub.requests))
	}
}

// Page one failing is a failed search: nothing was fetched, so there is nothing
// to salvage and the aggregator must be told.
func TestEZTVFailsWhenTheFirstPageFails(t *testing.T) {
	stub := newEZTVStubFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("<html>nginx</html>"))
	})

	_, err := stub.indexer.Search(context.Background(),
		Query{Title: "Silo", Media: MediaTV, IMDBID: eztvSiloIMDbID})
	if err == nil {
		t.Fatal("Search succeeded against a 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v, want the status in it", err)
	}
}

// A LATER page failing keeps what the earlier ones returned. That is the same
// trade the three-page cap already makes — the deep tail is not guaranteed —
// and discarding a hundred good rows over one hiccup is the worse answer.
func TestEZTVKeepsWhatItHasWhenALaterPageFails(t *testing.T) {
	stub := newEZTVStubFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write(eztvPage(t, eztvPageLimit, 2425))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	})

	releases, err := stub.indexer.Search(context.Background(),
		Query{Title: "The Simpsons", Media: MediaTV, IMDBID: "tt0096697"})
	if err != nil {
		t.Fatalf("Search: %v, want page 1's rows rather than a failure", err)
	}
	if len(releases) != eztvPageLimit {
		t.Errorf("got %d releases, want page 1's %d", len(releases), eztvPageLimit)
	}
}

// A row with no usable magnet is dropped rather than handed out with an empty
// one: an empty Magnet means "not resolved yet" everywhere else in this
// package, and EZTV has no detail page to resolve one from.
func TestEZTVDropsARowWithNoMagnet(t *testing.T) {
	body := `{"torrents_count":2,"torrents":[
		{"title":"Silo S03E08","magnet_url":"","seeds":1,"size_bytes":"1","season":"3","episode":"8"},
		{"title":"Silo S03E07","magnet_url":"magnet:?xt=urn:btih:` + strings.Repeat("d", 40) +
		`","seeds":1,"size_bytes":"1","season":"3","episode":"7"}]}`

	stub := newEZTVStubFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})

	releases, err := stub.indexer.Search(context.Background(),
		Query{Title: "Silo", Media: MediaTV, IMDBID: eztvSiloIMDbDigits})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(releases) != 1 || releases[0].Episode != 7 {
		t.Fatalf("got %+v, want only the row with a magnet", releases)
	}
}

func TestEZTVHandlesTelevisionAndNothingElse(t *testing.T) {
	e := NewEZTV(nil)
	if e.Handles(MediaMovie) {
		t.Error("EZTV claims films")
	}
	if !e.Handles(MediaTV) {
		t.Error("EZTV does not claim television")
	}
}

// The declaration that keeps EZTV from being asked a question whose answer
// would be a page of the whole site.
func TestEZTVAnswersOnlyAQueryCarryingAnIMDbID(t *testing.T) {
	e := NewEZTV(nil)
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"tt14688458", true},
		{"14688458", true},   // already stripped
		{"TT14688458", true}, // the prefix is matched case-insensitively
		{" tt14688458 ", true},
		{"", false},
		{"tt", false},
		{"Silo", false},
		{"125988x", false},
	} {
		if got := e.Answers(Query{Media: MediaTV, IMDBID: tc.id}); got != tc.want {
			t.Errorf("Answers(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// A direct caller that skips Answers gets an error rather than an empty slice,
// because an empty slice would mean "EZTV does not have this show" and that is
// not what happened.
func TestEZTVSearchWithoutAnIDIsAnError(t *testing.T) {
	stub := newEZTVStubFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("EZTV was fetched without an imdb_id")
		_, _ = w.Write([]byte(`{"torrents_count":1075625,"torrents":[]}`))
	})

	if _, err := stub.indexer.Search(context.Background(),
		Query{Title: "Silo", Media: MediaTV}); err == nil {
		t.Fatal("Search with no imdb_id succeeded")
	}
}

// The whole paged search shares one deadline, so a slow EZTV cannot spend a
// budget that belongs to four sources.
func TestEZTVBoundsTheWholePagedSearch(t *testing.T) {
	if eztvRequestTimeout >= 30*time.Second {
		t.Errorf("eztvRequestTimeout is %s, which is not inside the 30s whole-search deadline", eztvRequestTimeout)
	}
	// Three pages at the measured ~2.5s is ~7.5s, so the bound must leave room
	// for the cap it exists to survive.
	if eztvRequestTimeout <= eztvMaxPages*3*time.Second {
		t.Errorf("eztvRequestTimeout is %s, too tight for %d pages at the measured ~2.5s each",
			eztvRequestTimeout, eztvMaxPages)
	}
}

// The aggregator's side of the declaration: EZTV is skipped and reported
// not_applicable, never failed and never asked, for a television query with no
// id. This is the whole reason QueryCapable exists rather than an error.
func TestAggregatorSkipsAnIndexerThatCannotAnswerTheQuery(t *testing.T) {
	stub := newEZTVStubFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("EZTV was searched for a query it cannot answer")
		_, _ = w.Write([]byte(`{"torrents_count":0}`))
	})

	agg := NewAggregator([]Indexer{stub.indexer}, 5*time.Second, time.Minute)
	result, err := agg.Search(context.Background(), Query{Title: "Silo", Media: MediaTV})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Outcomes) != 1 {
		t.Fatalf("got %d outcomes, want one per configured indexer", len(result.Outcomes))
	}
	got := result.Outcomes[0]
	if got.Name != eztvIndexerName {
		t.Errorf("outcome names %q", got.Name)
	}
	if !got.NotApplicable {
		t.Errorf("outcome = %+v, want NotApplicable — it was never asked", got)
	}
	if got.OK || got.Error != "" {
		t.Errorf("outcome = %+v, want no failure: nothing went wrong", got)
	}
}

// And with an id it is asked normally, so the skip is about the query rather
// than about EZTV.
func TestAggregatorAsksTheIndexerOnceTheQueryCarriesAnID(t *testing.T) {
	stub := newEZTVStub(t, eztvFixtureSilo)

	agg := NewAggregator([]Indexer{stub.indexer}, 5*time.Second, time.Minute)
	result, err := agg.Search(context.Background(),
		Query{Title: "Silo", Media: MediaTV, IMDBID: eztvSiloIMDbID})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !result.Outcomes[0].OK || result.Outcomes[0].Count == 0 {
		t.Fatalf("outcome = %+v, want a real search", result.Outcomes[0])
	}
	if len(result.Releases) == 0 {
		t.Error("no releases came back")
	}
}

// An indexer that declares nothing answers everything, which is what keeps the
// other three from needing a declaration they do not have.
func TestAnswersQueryDefaultsToYes(t *testing.T) {
	if !answersQuery(NewTPB(nil), Query{Title: "Silo", Media: MediaTV}) {
		t.Error("TPB, which declares nothing, was treated as unable to answer")
	}
	// And a Cache forwards what it wraps, so composing one cannot silently lose
	// a declaration.
	cached := NewCache(NewEZTV(nil), time.Minute)
	if answersQuery(cached, Query{Media: MediaTV}) {
		t.Error("a cached EZTV claimed it could answer a query with no id")
	}
	if !answersQuery(cached, Query{Media: MediaTV, IMDBID: eztvSiloIMDbID}) {
		t.Error("a cached EZTV refused a query it can answer")
	}
}

// TestEZTVLive is the third test that talks to a real indexer, and it asks the
// SAME skip/fail rule the other two do (classifyLiveFailure, live_test.go).
//
// A private rule here is exactly the divergence T76 existed to remove, and
// CLAUDE.md forbids a third — so this is another caller of the one rule rather
// than a new one. The control name (T77, D42) is TPB's host, which is what lets
// an unresolvable eztvx.to fail loudly instead of skipping: if apibay.org
// resolves, DNS works on this machine and EZTV is genuinely gone.
//
// What it proves that no fixture can: that eztvx.to is still the front door
// (architecture.md named eztv.re from phase 2 until T97, and eztv.re answers a
// 301), and that season and episode are still STATED — the one property this
// indexer was added for.
func TestEZTVLive(t *testing.T) {
	if testing.Short() {
		t.Skip("live eztv search skipped under -short")
	}

	client, rec := liveClient(20 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	dnsWorks := liveDNSWorks(ctx, eztvControlHost)

	// Silo, the show every measurement in T97 was taken against.
	releases, err := NewEZTV(client).Search(ctx,
		Query{Title: "Silo", Media: MediaTV, IMDBID: eztvSiloIMDbID})
	if err != nil {
		if verdict, why := classifyLiveFailure(err, rec.lastStatus(), dnsWorks); verdict == liveSkip {
			t.Skipf("skipping live eztv test: %s: %v", why, err)
		}
		t.Fatalf("live Search: %v", err)
	}
	if len(releases) == 0 {
		t.Fatal("live search for Silo returned nothing")
	}

	var stated int
	for i, r := range releases {
		if r.Title == "" || r.Indexer != eztvIndexerName || len(InfoHash(r.Magnet)) != 40 {
			t.Fatalf("live release %d is malformed: %+v", i, r)
		}
		if r.SizeBytes <= 0 {
			t.Fatalf("live release %d has size %d — size_bytes is a JSON string and this is what a bad decode looks like", i, r.SizeBytes)
		}
		if r.Season > 0 {
			stated++
		}
	}
	// The whole point of this indexer. If EZTV stopped returning the fields,
	// every row would fall back to parseSeasonEpisode and this would drop —
	// which is a real regression wearing the appearance of a working search.
	if stated < len(releases)/2 {
		t.Errorf("only %d of %d live rows state a season", stated, len(releases))
	}
	t.Logf("live eztv: %d releases, %d stating a season", len(releases), stated)
}
