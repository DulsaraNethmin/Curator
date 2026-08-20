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

// TestTPBSearch drives the whole path against the recorded response.
func TestTPBSearch(t *testing.T) {
	var asked []*url.URL
	tpb := newTPBTestServer(t, tpbFixture(t, tpbFixtureSearch), &asked)

	releases, err := tpb.Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		t.Fatalf("Search: %v", err)
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

// TestTPBSearchWithoutYear: no year means no year filter.
func TestTPBSearchWithoutYear(t *testing.T) {
	tpb := newTPBTestServer(t, tpbFixture(t, tpbFixtureSearch), nil)
	releases, err := tpb.Search(context.Background(), Query{Title: "Interstellar"})
	if err != nil {
		t.Fatalf("Search: %v", err)
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

// The television fixture is a verbatim apibay response captured 2026-08-20:
//
//	curl 'https://apibay.org/q.php?q=severance&cat=205,208'
//
// It is 100 rows — apibay's page cap, hit by one show — and it carries both
// shapes a television search has to survive at once: season packs and single
// episodes, with 98 rows in category 208 and 2 in 205.
const (
	tpbFixtureTVSearch = "testdata/tpb-search-severance-tv.json"
	tpbFixtureTVRows   = 100

	// The two rows whose NAME states a year: "Severance 2022 S02E02 MULTI 1080p
	// WEB H264-HiggsBoson" and "Severance 2022 Seasons 1 and 2 Complete 1080p
	// WEB x264 [i_c]". They are the only rows in the whole page that the year
	// filter can act on at all, which is the measurement Query.searchYear rests
	// on: 98 of 100 television releases say nothing about a year, and the two
	// that do state their own rather than the show's.
	tpbFixtureTVYearNamed = 2
)

// The media type is not expressible in a title, and this is where that stops
// being an argument and becomes a URL: cat= is the whole discriminator.
func TestTPBTelevisionAsksTheTelevisionCategories(t *testing.T) {
	var asked []*url.URL
	tpb := newTPBTestServer(t, tpbFixture(t, tpbFixtureTVSearch), &asked)

	releases, err := tpb.Search(context.Background(),
		Query{Title: "Severance", Year: 2022, Media: MediaTV, Season: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(asked) != 1 {
		t.Fatalf("made %d requests, want 1", len(asked))
	}
	if cat := asked[0].Query().Get("cat"); cat != tpbTVCategories {
		t.Errorf("query cat = %q, want %q — a television search in the movie categories finds films", cat, tpbTVCategories)
	}
	// The season stays out of the query string, and that is measured rather than
	// tidy: on 2026-08-20 q=severance answered 100 rows, q="severance s02"
	// answered 8, and the 727-seeder "Severance - Season 2 - Mp4 x264 AC3 1080p"
	// was in the first set and not the second.
	if q := asked[0].Query().Get("q"); q != "Severance" {
		t.Errorf("query q = %q, want the bare title: apibay matches the letters, and \"S02\" and \"Season 2\" are two spellings of one thing", q)
	}
	if len(releases) != tpbFixtureTVRows {
		t.Fatalf("got %d releases, want all %d rows of the page", len(releases), tpbFixtureTVRows)
	}

	// And a film search still asks for films, from the same code path.
	var askedFilm []*url.URL
	film := newTPBTestServer(t, tpbFixture(t, tpbFixtureSearch), &askedFilm)
	if _, err := film.Search(context.Background(), Query{Title: "Interstellar", Year: 2014}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if cat := askedFilm[0].Query().Get("cat"); cat != tpbMovieCategories {
		t.Errorf("film query cat = %q, want %q", cat, tpbMovieCategories)
	}
}

// The year trap, on the real page. A show's year is its first-air year and an
// episode's name carries its own, so running the film year filter over a
// television search drops rows for stating the truth — and the filter can never
// confirm one, because a show's first-air year is in essentially no release
// name. 98 of these 100 rows have no year at all.
func TestTPBTelevisionIgnoresTheYear(t *testing.T) {
	tpb := newTPBTestServer(t, tpbFixture(t, tpbFixtureTVSearch), nil)

	// A television query carrying a year the fixture disagrees with keeps every
	// row, because the year never reaches the filter.
	tv, err := tpb.Search(context.Background(), Query{Title: "Severance", Year: 1999, Media: MediaTV})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(tv) != tpbFixtureTVRows {
		t.Fatalf("television search returned %d releases, want all %d — the year filter ran", len(tv), tpbFixtureTVRows)
	}
	for _, r := range tv {
		if r.Year != 0 {
			t.Errorf("release %q got year %d; a television release is stamped with no year, because the show's is not the episode's", r.Title, r.Year)
			break
		}
	}

	// The same page asked for as a FILM with the same year: the two rows that
	// name 2022 are dropped. That difference is the whole reason the media type
	// has to reach this far down.
	film, err := tpb.Search(context.Background(), Query{Title: "Severance", Year: 1999})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if want := tpbFixtureTVRows - tpbFixtureTVYearNamed; len(film) != want {
		t.Errorf("film search returned %d releases, want %d — the year filter is what television must not run", len(film), want)
	}
}

// Season and episode are read back off the name, which is the only place either
// is stated: apibay's category says "television" and never which season.
func TestTPBParsesSeasonAndEpisodeFromTheName(t *testing.T) {
	tpb := newTPBTestServer(t, tpbFixture(t, tpbFixtureTVSearch), nil)
	releases, err := tpb.Search(context.Background(), Query{Title: "Severance", Media: MediaTV})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	pack := tpbFind(t, releases, "Severance - Season 1 - Mp4 x264 AC3 1080p")
	if pack.Season != 1 || pack.Episode != 0 {
		t.Errorf("season pack = season %d episode %d, want 1 and 0", pack.Season, pack.Episode)
	}
	if pack.Seeders != 844 {
		t.Errorf("season pack seeders = %d, want 844", pack.Seeders)
	}

	episode := tpbFind(t, releases, "Severance S02E05 Trojans Horse 1080p ATVP WEB-DL DDP5 1 H 264-NTb")
	if episode.Season != 2 || episode.Episode != 5 {
		t.Errorf("episode = season %d episode %d, want 2 and 5", episode.Season, episode.Episode)
	}
	if episode.Seeders != 381 {
		t.Errorf("episode seeders = %d, want 381", episode.Seeders)
	}

	// A film states neither, and the parse must not invent one for it.
	films := newTPBTestServer(t, tpbFixture(t, tpbFixtureSearch), nil)
	movies, err := films.Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, r := range movies {
		if r.Season != 0 || r.Episode != 0 {
			t.Errorf("film release %q parsed as season %d episode %d", r.Title, r.Season, r.Episode)
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
	releases, err := tpb.Search(context.Background(), Query{Title: "zzqqxxnosuchmoviezz9999", Year: 2014})
	if err != nil {
		t.Fatalf("Search: %v — nothing found is a normal outcome, not an error", err)
	}
	if len(releases) != 0 {
		t.Fatalf("got %d releases from a search with no matches: %+v", len(releases), releases)
	}

	// The same with no year, so the sentinel is not being removed by the year
	// filter happening to disagree with it.
	if releases, err = tpb.Search(context.Background(), Query{Title: "zzqqxxnosuchmoviezz9999"}); err != nil {
		t.Fatalf("Search: %v", err)
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
	releases, err := tpb.Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		t.Fatalf("Search: %v", err)
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
	releases, err := tpb.Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		t.Fatalf("Search: %v", err)
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
			releases, err := tpb.Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
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
		if _, err := tpb.Search(context.Background(), Query{Title: "Interstellar", Year: 2014}); err == nil {
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

// TestTPBLive is the one test that talks to apibay. It skips under -short, and
// it skips when this network cannot use apibay — but once apibay answers at all,
// a wrong answer is a real failure, and apibay's NAME going away is a real
// failure too.
//
// The skip/fail decision lives in live_test.go and is shared with YTS. It is
// applied to THE SEARCH rather than to a HEAD probe, which is the whole of T76:
// T73's probe read a status one call too early, so a probe could answer 200 and
// the search that followed could still time out — and this test then called
// t.Fatalf on a transport error that yts_test.go's own rule says is a skip.
// Classifying the call needs no probe, because the search is its own probe.
//
// The control name (T77, D42) is YTS's host, and it is what lets an unresolvable
// apibay.org fail loudly instead of skipping: if movies-api.accel.li resolves,
// DNS works here and apibay is genuinely gone. Until T77 that case skipped.
func TestTPBLive(t *testing.T) {
	if testing.Short() {
		t.Skip("live apibay search skipped under -short")
	}

	// The recorder is how a refused status reaches the classifier: Search
	// formats it into a plain error string, so it cannot be read with errors.As.
	client, rec := liveClient(20 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tpb := NewTPB(client)

	dnsWorks := liveDNSWorks(ctx, tpbControlHost)

	releases, err := tpb.Search(ctx, Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		if verdict, why := classifyLiveFailure(err, rec.lastStatus(), dnsWorks); verdict == liveSkip {
			t.Skipf("skipping live apibay test: %s: %v", why, err)
		}
		t.Fatalf("live Search: %v", err)
	}
	if len(releases) == 0 {
		t.Fatal("live search for Interstellar (2014) returned nothing")
	}
	for i, r := range releases {
		if r.Title == "" || r.Indexer != "tpb" || len(InfoHash(r.Magnet)) != 40 {
			t.Fatalf("live release %d is malformed: %+v", i, r)
		}
	}

	// Television, live, and in THIS test rather than a third one. The rule for a
	// failed live search lives in one place and both live tests ask it (T76); a
	// new test with a skip rule of its own is precisely the divergence that
	// existed to be removed. So this is another call under the same rule, the
	// same client and the same control name.
	//
	// What it proves is the one thing no fixture can: that cat=205,208 is still
	// what television is filed under at apibay. The fixture pins how the answer
	// parses, and it would go on passing forever after the categories moved.
	tv, err := tpb.Search(ctx, Query{Title: "Severance", Media: MediaTV, Season: 2})
	if err != nil {
		if verdict, why := classifyLiveFailure(err, rec.lastStatus(), dnsWorks); verdict == liveSkip {
			t.Skipf("skipping live apibay television check: %s: %v", why, err)
		}
		t.Fatalf("live Search (television): %v", err)
	}
	if len(tv) == 0 {
		t.Fatalf("live television search in categories %s returned nothing", tpbTVCategories)
	}
	// At least one row has to state a season, or these are not the television
	// categories any more — which is the failure this call exists to catch.
	var seasons int
	for _, r := range tv {
		if r.Season > 0 {
			seasons++
		}
		if r.Year != 0 {
			t.Errorf("live television release %q carries year %d, want none", r.Title, r.Year)
			break
		}
	}
	if seasons == 0 {
		t.Errorf("none of the %d live television releases names a season: %s", len(tv), tv[0].Title)
	}
	t.Logf("live: %d television releases, %d naming a season", len(tv), seasons)

	// The trap, live: apibay answers this with its sentinel row, and none of it
	// may reach a caller.
	empty, err := tpb.Search(ctx, Query{Title: "zzqqxxnosuchmoviezz9999"})
	if err != nil {
		// Same rule as the search above: apibay can refuse or time out between
		// the two calls, and neither is this assertion failing.
		if verdict, why := classifyLiveFailure(err, rec.lastStatus(), dnsWorks); verdict == liveSkip {
			t.Skipf("skipping live apibay sentinel check: %s: %v", why, err)
		}
		t.Fatalf("live Search (absurd query): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("absurd query returned %d releases: %+v", len(empty), empty)
	}
}

// Compile-time proof that TPB satisfies Indexer. It is deliberately not a
// MagnetResolver: its magnets are built at search time, so there is nothing to
// resolve.
var _ Indexer = (*TPB)(nil)
