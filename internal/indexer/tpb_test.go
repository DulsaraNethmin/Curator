package indexer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// The fixtures are verbatim apibay responses, captured 2026-08-12:
//
//	tpb-search-interstellar.json  q.php?q=Interstellar&cat=201,202,207,209,211
//	tpb-search-empty.json         q.php?q=zzqqxxnosuchmoviezz9999&cat=201,...,211
//
// tpb-search-malformed.json is the only hand-made one: real rows with their
// numeric strings and info hashes damaged, to pin what happens to a row we
// cannot fully read.
const (
	tpbFixtureSearch    = "testdata/tpb-search-interstellar.json"
	tpbFixtureEmpty     = "testdata/tpb-search-empty.json"
	tpbFixtureMalformed = "testdata/tpb-search-malformed.json"

	// tpbFixtureRows is how many rows apibay returned for that search; 100 is
	// its page cap, which is why the query is restricted to movie categories.
	tpbFixtureRows = 100
	// tpbFixtureRows2014 is how many survive the year filter — the other 12 are
	// "Interstellar Wars (2016)", "The Science Of Interstellar 2015" and friends.
	tpbFixtureRows2014 = 88
)

func tpbFixture(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return b
}

// newTPBTestServer stands in for apibay, serving one fixture and recording the
// requests it was asked for.
func newTPBTestServer(t *testing.T, body []byte, got *[]*url.URL) *TPB {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			*got = append(*got, r.URL)
		}
		w.Header().Set("content-type", "application/json; charset=UTF-8")
		if _, err := w.Write(body); err != nil {
			t.Errorf("write fixture: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	tpb := NewTPB(srv.Client())
	tpb.site = srv.URL
	return tpb
}

// tpbFind returns the release with the given title, or fails.
func tpbFind(t *testing.T, releases []Release, title string) Release {
	t.Helper()
	for _, r := range releases {
		if r.Title == title {
			return r
		}
	}
	t.Fatalf("no release titled %q among %d", title, len(releases))
	return Release{}
}

// TestTPBParseRows records the schema apibay actually returns, field by field.
func TestTPBParseRows(t *testing.T) {
	rows, err := tpbParseRows(tpbFixture(t, tpbFixtureSearch))
	if err != nil {
		t.Fatalf("tpbParseRows: %v", err)
	}
	if len(rows) != tpbFixtureRows {
		t.Fatalf("got %d rows, want %d (apibay caps a response at 100)", len(rows), tpbFixtureRows)
	}

	want := tpbRow{
		ID:       "11756968",
		Name:     "Interstellar (2014) (2014) 1080p BrRip x264 - YIFY",
		InfoHash: "89599BF4DC369A3A8ECA26411C5CCF922D78B486",
		Leechers: "320",
		Seeders:  "994",
		Size:     "2431905867",
		NumFiles: "2",
		Username: "YIFY",
		Added:    "1426426450",
		Status:   "vip",
		Category: "207",
		IMDB:     "tt4288316",
	}
	if rows[0] != want {
		t.Errorf("row 0 =\n%+v\nwant\n%+v", rows[0], want)
	}

	// Every row must carry the three things a Release is built out of.
	for i, r := range rows {
		if strings.TrimSpace(r.Name) == "" {
			t.Errorf("row %d has no name", i)
		}
		if !tpbUsableHash(r.InfoHash) {
			t.Errorf("row %d info hash %q is not 40 hex characters", i, r.InfoHash)
		}
		if _, err := tpbParseNumber(r.Size); err != nil {
			t.Errorf("row %d size %q: %v", i, r.Size, err)
		}
	}
}

// TestTPBNumbersArriveAsStrings is the trap this parser was written around: the
// numeric fields are JSON strings, so the obvious struct does not merely lose
// them, it fails to decode the response at all.
func TestTPBNumbersArriveAsStrings(t *testing.T) {
	type naive struct {
		Name    string `json:"name"`
		Seeders int64  `json:"seeders"`
		Size    int64  `json:"size"`
	}
	var rows []naive
	if err := json.Unmarshal(tpbFixture(t, tpbFixtureSearch), &rows); err == nil {
		t.Fatal("int64 fields decoded apibay's response — the fixture no longer pins the string-typed numbers this parser exists for")
	}

	// And the string form converts to the right value.
	got, err := tpbParseNumber("2431905867")
	if err != nil || got != 2431905867 {
		t.Errorf("tpbParseNumber(\"2431905867\") = %d, %v", got, err)
	}
}

// TestTPBSearchMovie drives the whole path against the recorded response.
func TestTPBSearchMovie(t *testing.T) {
	var asked []*url.URL
	tpb := newTPBTestServer(t, tpbFixture(t, tpbFixtureSearch), &asked)

	releases, err := tpb.SearchMovie(context.Background(), "Interstellar", 2014)
	if err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	if tpb.Name() != "tpb" {
		t.Errorf("Name = %q, want tpb", tpb.Name())
	}
	if len(asked) != 1 {
		t.Fatalf("made %d requests, want 1", len(asked))
	}
	if asked[0].Path != "/q.php" {
		t.Errorf("requested path %q, want /q.php", asked[0].Path)
	}
	if q := asked[0].Query().Get("q"); q != "Interstellar" {
		// The year stays out of the query: apibay matches on the release name,
		// so "Interstellar 2014" would hide every release that omits the year.
		t.Errorf("query q = %q, want the bare title", q)
	}
	if cat := asked[0].Query().Get("cat"); cat != tpbMovieCategories {
		t.Errorf("query cat = %q, want %q — categories must be restricted at the query", cat, tpbMovieCategories)
	}
	if len(releases) != tpbFixtureRows2014 {
		t.Fatalf("got %d releases, want %d", len(releases), tpbFixtureRows2014)
	}

	top := tpbFind(t, releases, "Interstellar (2014) (2014) 1080p BrRip x264 - YIFY")
	if top.Quality != "1080p" {
		t.Errorf("quality = %q, want 1080p", top.Quality)
	}
	if top.SizeBytes != 2431905867 {
		t.Errorf("size = %d, want 2431905867", top.SizeBytes)
	}
	if top.Seeders != 994 {
		t.Errorf("seeders = %d, want 994", top.Seeders)
	}
	if top.Year != 2014 {
		t.Errorf("year = %d, want the searched-for 2014", top.Year)
	}
	if top.Indexer != "tpb" {
		t.Errorf("indexer = %q, want tpb", top.Indexer)
	}
	if InfoHash(top.Magnet) != "89599BF4DC369A3A8ECA26411C5CCF922D78B486" {
		t.Errorf("magnet %q carries the wrong info hash", top.Magnet)
	}

	// A 2160p row, so the quality is not just "whatever the first row said".
	uhd := tpbFind(t, releases, "Interstellar.2014.2160p.UHD.BluRay.x265.10bit.HDR.DTS-HD.MA.5.1-")
	if uhd.Quality != "2160p" {
		t.Errorf("2160p release quality = %q", uhd.Quality)
	}
	if uhd.SizeBytes != 35951928861 {
		t.Errorf("2160p release size = %d, want 35951928861", uhd.SizeBytes)
	}

	for i, r := range releases {
		if r.Indexer != "tpb" {
			t.Fatalf("release %d indexer = %q, want tpb on every release", i, r.Indexer)
		}
		if r.Magnet == "" {
			t.Fatalf("release %d has no magnet — apibay gives the hash, nothing is lazy here", i)
		}
		if r.detailPath != "" {
			t.Fatalf("release %d carries a detail path; tpb has nothing to resolve later", i)
		}
	}
}

// TestTPBSearchMovieWithoutYear: no year means no year filter.
func TestTPBSearchMovieWithoutYear(t *testing.T) {
	tpb := newTPBTestServer(t, tpbFixture(t, tpbFixtureSearch), nil)
	releases, err := tpb.SearchMovie(context.Background(), "Interstellar", 0)
	if err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	if len(releases) != tpbFixtureRows {
		t.Fatalf("got %d releases, want all %d when no year is given", len(releases), tpbFixtureRows)
	}
	for _, r := range releases {
		if r.Year != 0 {
			t.Errorf("release %q got year %d; with nothing searched for there is nothing to claim", r.Title, r.Year)
			break
		}
	}
}

// TestTPBEmptySearchYieldsNoReleases is the point of this task. apibay answers a
// search with no matches with one sentinel row rather than an empty array, and
// that row must not reach a caller as a download candidate.
func TestTPBEmptySearchYieldsNoReleases(t *testing.T) {
	body := tpbFixture(t, tpbFixtureEmpty)

	// The fixture really is the trap, not an empty array — if apibay ever starts
	// returning `[]` this test would otherwise quietly stop proving anything.
	rows, err := tpbParseRows(body)
	if err != nil {
		t.Fatalf("tpbParseRows: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "No results returned" {
		t.Fatalf("empty-search fixture is %d rows (%+v); it was captured to pin apibay's sentinel row", len(rows), rows)
	}

	tpb := newTPBTestServer(t, body, nil)
	releases, err := tpb.SearchMovie(context.Background(), "zzqqxxnosuchmoviezz9999", 2014)
	if err != nil {
		t.Fatalf("SearchMovie: %v — nothing found is a normal outcome, not an error", err)
	}
	if len(releases) != 0 {
		t.Fatalf("got %d releases from a search with no matches: %+v", len(releases), releases)
	}

	// The same with no year, so the sentinel is not being removed by the year
	// filter happening to disagree with it.
	if releases, err = tpb.SearchMovie(context.Background(), "zzqqxxnosuchmoviezz9999", 0); err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	if len(releases) != 0 {
		t.Fatalf("got %d releases with no year filter: %+v", len(releases), releases)
	}
}

// TestTPBSentinelRejectedByHashNotName proves the rejection is structural. The
// same sentinel row wearing a plausible movie name is still not a release, and a
// row with a real hash and an odd name still is — so this survives apibay
// rewording, translating or dropping "No results returned".
func TestTPBSentinelRejectedByHashNotName(t *testing.T) {
	rows := []tpbRow{
		{Name: "Interstellar 2014 1080p BluRay x264", InfoHash: "0000000000000000000000000000000000000000", Seeders: "0", Size: "0"},
		{Name: "Interstellar 2014 1080p BluRay x264", InfoHash: "", Seeders: "9", Size: "1"},
		{Name: "No results returned", InfoHash: "89599BF4DC369A3A8ECA26411C5CCF922D78B486", Seeders: "5", Size: "1"},
	}
	got := tpbToReleases(rows, 2014)
	if len(got) != 1 {
		t.Fatalf("got %d releases, want 1 — only the row with a usable hash: %+v", len(got), got)
	}
	if got[0].Title != "No results returned" {
		t.Errorf("kept %q; the check is on the hash, not the name", got[0].Title)
	}

	for _, tt := range []struct {
		label string
		hash  string
		want  bool
	}{
		{"real hash", "89599BF4DC369A3A8ECA26411C5CCF922D78B486", true},
		{"lowercase hash", "32680d7846bec0c9c46db2d62816950548cba0c2", true},
		{"apibay sentinel", "0000000000000000000000000000000000000000", false},
		{"empty", "", false},
		{"truncated", "89599BF4DC369A3A", false},
		{"too long", "89599BF4DC369A3A8ECA26411C5CCF922D78B486AA", false},
		{"not hex", "ZZ599BF4DC369A3A8ECA26411C5CCF922D78B486", false},
		{"padded with spaces", "  89599BF4DC369A3A8ECA26411C5CCF922D78B486  ", true},
	} {
		t.Run(tt.label, func(t *testing.T) {
			if got := tpbUsableHash(tt.hash); got != tt.want {
				t.Errorf("tpbUsableHash(%q) = %v, want %v", tt.hash, got, tt.want)
			}
		})
	}
}

// TestTPBQualityComesFromPortedParser: real apibay names carrying "1080p.UHD"
// must stay 1080p. The fixture has five of them.
func TestTPBQualityComesFromPortedParser(t *testing.T) {
	tpb := newTPBTestServer(t, tpbFixture(t, tpbFixtureSearch), nil)
	releases, err := tpb.SearchMovie(context.Background(), "Interstellar", 2014)
	if err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}

	traps := 0
	for _, r := range releases {
		upper := strings.ToUpper(r.Title)
		if strings.Contains(upper, "1080P") && (strings.Contains(upper, "UHD") || strings.Contains(upper, "4K")) {
			traps++
			if r.Quality != "1080p" {
				t.Errorf("%q resolved to %q; a 1080p release advertising UHD is still 1080p", r.Title, r.Quality)
			}
		}
	}
	if traps < 5 {
		t.Errorf("found %d 1080p-plus-UHD names in the fixture, want the 5 that were captured — the trap is no longer being exercised", traps)
	}

	// And a name with no resolution token gets no invented one.
	if q := tpbFind(t, releases, "POtHS - End Times - 51 - The Interstellar Alien Savior Deception").Quality; q != QualityUnknown {
		t.Errorf("quality = %q, want %q for a name with no resolution", q, QualityUnknown)
	}
}

// TestTPBKeepsRowsWithUnconvertibleNumbers: a number we cannot read costs a
// zero, not the row. A hash we cannot use is different — there is no download
// behind it — and that row goes.
func TestTPBKeepsRowsWithUnconvertibleNumbers(t *testing.T) {
	tpb := newTPBTestServer(t, tpbFixture(t, tpbFixtureMalformed), nil)
	releases, err := tpb.SearchMovie(context.Background(), "Interstellar", 2014)
	if err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	if len(releases) != 3 {
		t.Fatalf("got %d releases, want 3 (two of the five fixture rows have unusable hashes): %+v", len(releases), releases)
	}

	broken := tpbFind(t, releases, "Interstellar.2014.2160p.UHD.BluRay.x265.10bit.HDR.DTS-HD.MA.5.1-")
	if broken.Seeders != 0 {
		t.Errorf("seeders = %d, want 0 for an empty seeder string", broken.Seeders)
	}
	if broken.SizeBytes != 0 {
		t.Errorf("size = %d, want 0 — apibay sizes are bytes, so \"35.9 GB\" is unreadable here", broken.SizeBytes)
	}
	if broken.Quality != "2160p" || broken.Magnet == "" {
		t.Errorf("the row lost more than its numbers: quality=%q magnet=%q", broken.Quality, broken.Magnet)
	}

	// A lowercase hash is a real hash; InfoHash normalises it on the way back.
	lower := tpbFind(t, releases, "Interstellar 2014 720p CAMRip x264 AAC")
	if got := InfoHash(lower.Magnet); got != "32680D7846BEC0C9C46DB2D62816950548CBA0C2" {
		t.Errorf("InfoHash = %q, want the uppercased hash", got)
	}
	for _, r := range releases {
		if strings.Contains(r.Title, "NOHASH") || strings.Contains(r.Title, "SHORTHASH") {
			t.Errorf("%q survived without a usable info hash; it has no magnet to offer", r.Title)
		}
	}
}

func TestTPBMagnet(t *testing.T) {
	const hash = "89599BF4DC369A3A8ECA26411C5CCF922D78B486"
	got := tpbMagnet(hash, "Interstellar (2014) 1080p BluRay")

	if !strings.HasPrefix(got, "magnet:?xt=urn:btih:"+hash) {
		t.Fatalf("magnet = %q, want it to open with an unescaped urn:btih:", got)
	}
	if !strings.Contains(got, "&dn=Interstellar+%282014%29+1080p+BluRay") {
		t.Errorf("magnet = %q, want an escaped display name", got)
	}
	if n := strings.Count(got, "&tr="); n != len(tpbTrackers) {
		t.Errorf("magnet carries %d trackers, want %d — a bare btih leans entirely on DHT", n, len(tpbTrackers))
	}
	for _, tr := range tpbTrackers {
		if !strings.Contains(got, "&tr="+url.QueryEscape(tr)) {
			t.Errorf("magnet is missing tracker %q", tr)
		}
	}
	if InfoHash(got) != hash {
		t.Errorf("InfoHash = %q, want %q", InfoHash(got), hash)
	}
}

// TestTPBMagnetsRoundTrip: every magnet from a real search must survive the
// ported InfoHash, since that is the handle qBittorrent and dedup both key on.
func TestTPBMagnetsRoundTrip(t *testing.T) {
	rows, err := tpbParseRows(tpbFixture(t, tpbFixtureSearch))
	if err != nil {
		t.Fatalf("tpbParseRows: %v", err)
	}
	releases := tpbToReleases(rows, 0)
	if len(releases) != len(rows) {
		t.Fatalf("got %d releases from %d rows", len(releases), len(rows))
	}
	for i, r := range releases {
		hash := InfoHash(r.Magnet)
		if len(hash) != 40 {
			t.Fatalf("release %d (%q): InfoHash = %q (%d chars), want 40", i, r.Title, hash, len(hash))
		}
		if hash != strings.ToUpper(rows[i].InfoHash) {
			t.Errorf("release %d: magnet hash %q, row hash %q", i, hash, rows[i].InfoHash)
		}
		if _, err := url.Parse(r.Magnet); err != nil {
			t.Errorf("release %d: magnet does not parse as a URI: %v", i, err)
		}
	}
}

func TestTPBNameAllowsYear(t *testing.T) {
	for _, tt := range []struct {
		name string
		year int
		want bool
	}{
		{"Interstellar.2014.1080p.BluRay.x265", 2014, true},
		{"Interstellar (2014) (2014) 1080p BrRip x264 - YIFY", 2014, true},
		{"Interstellar Wars (2016) 1080p BrRip x264 - VPPV", 2014, false},
		{"The Science Of Interstellar 2015 1080p BluRay x264-HANDJOB", 2014, false},
		// No year at all is ambiguous, and ambiguous is kept. This is the whole
		// reason the year is not put into the apibay query.
		{"Interstellar IMAX 1080p BluRay x265", 2014, true},
		{"POtHS - End Times - 51 - The Interstellar Alien Savior Deception", 2014, true},
		// Two years, one of them right: keep. Re-releases name both.
		{"Interstellar 2014 REMASTERED 2020 1080p BluRay", 2014, true},
		// Resolution and codec tokens must not read as years.
		{"Interstellar 2160p HDR x264 AC3", 2014, true},
		{"Interstellar 1920x1080 HEVC", 2014, true},
		{"Interstellar.2014.2160p.UHD.BluRay", 2160, false},
		// No year searched for: everything passes.
		{"Interstellar Wars (2016) 720p", 0, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tpbNameAllowsYear(tt.name, tt.year); got != tt.want {
				t.Errorf("tpbNameAllowsYear(%q, %d) = %v, want %v", tt.name, tt.year, got, tt.want)
			}
		})
	}
}

func TestTPBSearchErrors(t *testing.T) {
	for _, tt := range []struct {
		label  string
		status int
		body   string
	}{
		{"5xx", http.StatusBadGateway, "upstream is having a moment"},
		{"5xx with a JSON body", http.StatusInternalServerError, `[]`},
		{"not JSON at all", http.StatusOK, "<html><body>Just a moment...</body></html>"},
		{"JSON, but not an array of rows", http.StatusOK, `{"error":"nope"}`},
		{"truncated JSON", http.StatusOK, `[{"name":"Interstellar"`},
		{"empty body", http.StatusOK, ""},
	} {
		t.Run(tt.label, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				if _, err := w.Write([]byte(tt.body)); err != nil {
					t.Errorf("write: %v", err)
				}
			}))
			defer srv.Close()

			tpb := NewTPB(srv.Client())
			tpb.site = srv.URL
			releases, err := tpb.SearchMovie(context.Background(), "Interstellar", 2014)
			if err == nil {
				t.Fatalf("got %d releases and no error", len(releases))
			}
			if releases != nil {
				t.Errorf("got releases alongside an error: %+v", releases)
			}
			// A failing indexer is omitted rather than fatal, so the aggregator
			// reports this string. It has to say which indexer gave up.
			if !strings.Contains(err.Error(), "tpb") {
				t.Errorf("error %q does not name the indexer", err)
			}
		})
	}

	t.Run("unreachable host", func(t *testing.T) {
		tpb := NewTPB(&http.Client{Timeout: time.Second})
		tpb.site = "http://127.0.0.1:1"
		if _, err := tpb.SearchMovie(context.Background(), "Interstellar", 2014); err == nil {
			t.Fatal("want an error when apibay cannot be reached")
		} else if !strings.Contains(err.Error(), "tpb") {
			t.Errorf("error %q does not name the indexer", err)
		}
	})
}

func TestTPBNewTPBDefaultsClient(t *testing.T) {
	tpb := NewTPB(nil)
	if tpb.client == nil {
		t.Error("a nil client must fall back to http.DefaultClient, not panic on first use")
	}
	if tpb.site != tpbSite {
		t.Errorf("site = %q, want %q", tpb.site, tpbSite)
	}
}

// TestTPBLive is the one test that talks to apibay. It skips under -short and
// when the host is unreachable, so an offline machine does not fail the build —
// but once apibay answers at all, a wrong answer is a real failure.
func TestTPBLive(t *testing.T) {
	if testing.Short() {
		t.Skip("live apibay search skipped under -short")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest(http.MethodHead, tpbSite+"/q.php?q=probe&cat=201", nil)
	if err != nil {
		t.Fatalf("build probe: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Skipf("apibay unreachable, skipping live test: %v", err)
	}
	resp.Body.Close()

	// A 403 is a SUCCESSFUL http transaction, so the check above does not catch
	// it: err is nil and the test used to walk straight into a search that then
	// failed on the same 403. That is what turned every release red — apibay
	// answers 200 to this probe from a home connection and 403 from a GitHub
	// Actions runner (measured, 2026-08-18), so `make check` was green on a
	// laptop and could never pass in CI.
	if refusedTheNetwork(resp.StatusCode) {
		t.Skipf("apibay answered %s to a probe, so this network may not use it: skipping live test", resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apibay probe answered %s, which is neither a working apibay nor a refused network", resp.Status)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tpb := NewTPB(client)

	releases, err := tpb.SearchMovie(ctx, "Interstellar", 2014)
	if err != nil {
		t.Fatalf("live SearchMovie: %v", err)
	}
	if len(releases) == 0 {
		t.Fatal("live search for Interstellar (2014) returned nothing")
	}
	for i, r := range releases {
		if r.Title == "" || r.Indexer != "tpb" || len(InfoHash(r.Magnet)) != 40 {
			t.Fatalf("live release %d is malformed: %+v", i, r)
		}
	}

	// The trap, live: apibay answers this with its sentinel row, and none of it
	// may reach a caller.
	empty, err := tpb.SearchMovie(ctx, "zzqqxxnosuchmoviezz9999", 0)
	if err != nil {
		t.Fatalf("live SearchMovie (absurd query): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("absurd query returned %d releases: %+v", len(empty), empty)
	}
}

// Compile-time proof that TPB satisfies Indexer. It is deliberately not a
// MagnetResolver: its magnets are built at search time, so there is nothing to
// resolve.
var _ Indexer = (*TPB)(nil)

// refusedTheNetwork reports whether a status says THIS NETWORK may not use
// apibay, as opposed to apibay being broken.
//
// The distinction is the one yts_test.go argues for and it must not be lost
// here: "a status YTS should not be returning is a real regression and must not
// be skipped past — that is how a dead base URL stays green for a week." D12 is
// that lesson paid for once already, when yts.mx went NXDOMAIN.
//
// These three are not that. They are an access decision about the caller, and
// they say nothing about whether curator can parse what apibay returns. Every
// other non-200 still fails the test.
func refusedTheNetwork(status int) bool {
	switch status {
	case http.StatusForbidden, http.StatusUnauthorized, http.StatusTooManyRequests:
		return true
	}
	return false
}

// The classifier is the whole fix, so it is the thing with a test.
//
// A live test cannot assert its own skip — it takes whichever branch the network
// gives it — so what is pinned here is the decision it makes, both ways round.
func TestARefusedNetworkIsNotABrokenApibay(t *testing.T) {
	for _, status := range []int{
		http.StatusForbidden,       // what a GitHub Actions runner gets
		http.StatusUnauthorized,    // the same decision, differently worded
		http.StatusTooManyRequests, // a rate limit is about the caller too
	} {
		if !refusedTheNetwork(status) {
			t.Errorf("%d is apibay refusing the caller, and CI would fail on it forever", status)
		}
	}

	// The other half, and the one that keeps yts_test.go's argument true: a
	// broken apibay must NOT be skipped past, or a dead endpoint stays green.
	for _, status := range []int{
		http.StatusOK,
		http.StatusNotFound,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
	} {
		if refusedTheNetwork(status) {
			t.Errorf("%d would be skipped past, which is how a dead base URL stays green for a week", status)
		}
	}
}
