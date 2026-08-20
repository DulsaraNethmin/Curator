package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	tpbIndexerName = "tpb"
	tpbSite        = "https://apibay.org"

	// tpbMovieCategories are The Pirate Bay's movie categories: 201 Movies,
	// 202 Movies DVDR, 207 HD Movies, 209 3D, 211 UHD/4K Movies. TV, music and
	// games live under other numbers, so restricting the query is what keeps a
	// search for "Dune" from returning the game and the audiobook.
	//
	// apibay caps a response at 100 rows, so this is not merely a tidier result
	// set: every row spent on a TV episode is a movie release that never arrives.
	tpbMovieCategories = "201,202,207,209,211"

	// tpbTVCategories are The Pirate Bay's television categories: 205 TV shows,
	// 208 HD TV shows. This is the whole of the media discriminator at TPB —
	// there is nothing in a title that can stand in for it, which is why the
	// interface takes a Query.
	//
	// Measured 2026-08-20: q=severance&cat=205,208 answers 100 rows, and both
	// shapes a television search must cover are in them — the season pack
	// "Severance - Season 1 - Mp4 x264 AC3 1080p" at 844 seeders alongside the
	// single episode "Severance S02E05 Trojans Horse 1080p ATVP WEB-DL DDP5 1 H
	// 264-NTb" at 381.
	//
	// The 100-row cap inverts here and gets sharper. For a film the competition
	// is other cuts of the same film; for a show a season pack competes with
	// every single episode of every season, and a long-running series can spend
	// the whole page on episodes.
	tpbTVCategories = "205,208"

	// tpbMaxResponse bounds the body we will read. A real search is around 30 KB
	// (measured: 29 KB for 100 rows), so this is three orders of magnitude of
	// headroom and exists only so a hung or hostile endpoint cannot make the
	// search allocate without limit.
	tpbMaxResponse = 8 << 20
)

// tpbTrackers are appended to every magnet this indexer builds.
//
// A bare `magnet:?xt=urn:btih:...` has no way to find peers except DHT, which
// makes "does the magnet work" depend on the client's bootstrap luck. This list
// is copied from print_trackers() in The Pirate Bay's own main.js (fetched
// 2026-08-12) — the same trackers the site attaches when you click "Get This
// Torrent" — so a magnet we hand out behaves like one copied from the site.
//
// One entry from that list is omitted: udp://torrent.gresille.org:80/announce no
// longer resolves in DNS (checked the same day). The rest are kept verbatim
// rather than curated, because tracker liveness is not something this repo can
// keep measuring and an unreachable tracker costs one dropped UDP packet.
var tpbTrackers = []string{
	"udp://tracker.opentrackr.org:1337",
	"udp://open.stealth.si:80/announce",
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://tracker.bittor.pw:1337/announce",
	"udp://public.popcorn-tracker.org:6969/announce",
	"udp://tracker.dler.org:6969/announce",
	"udp://exodus.desync.com:6969",
	"udp://open.demonii.com:1337/announce",
	"udp://glotorrents.pw:6969/announce",
	"udp://tracker.coppersurfer.tk:6969",
	"udp://p4p.arenabg.com:1337",
	"udp://tracker.internetwarriors.net:1337",
}

// TPB searches The Pirate Bay through apibay, its JSON API. No browser and no
// Cloudflare: one HTTP round trip under a second, which is why it is not cached
// the way 1337x is.
type TPB struct {
	client *http.Client
	site   string
}

// NewTPB returns a Pirate Bay indexer fetching with c. A nil client uses
// http.DefaultClient.
func NewTPB(c *http.Client) *TPB {
	if c == nil {
		c = http.DefaultClient
	}
	return &TPB{client: c, site: tpbSite}
}

// Name implements Indexer.
func (t *TPB) Name() string { return tpbIndexerName }

// Search implements Indexer. Releases come back with their magnets already
// built — apibay hands over the info hash, so there is nothing to resolve later.
func (t *TPB) Search(ctx context.Context, q Query) ([]Release, error) {
	rows, err := t.search(ctx, q)
	if err != nil {
		return nil, err
	}
	return tpbToReleases(rows, q.searchYear()), nil
}

// tpbCategories restricts a search to the categories of one media type.
func tpbCategories(q Query) string {
	if q.IsTV() {
		return tpbTVCategories
	}
	return tpbMovieCategories
}

// search runs one q.php query and returns its rows.
//
// Neither the year nor the season is part of the query string, and both are the
// same measurement twice: apibay matches keywords against the release name, so
// anything added to the query silently excludes every release whose name spells
// it differently. Measured 2026-08-20 against the live API:
//
//	q=severance                 100 rows, packs and episodes, cat 205,208
//	q=severance s02               8 rows, all "Severance.S02.*" — and the
//	                              727-seeder "Severance - Season 2 - Mp4 x264
//	                              AC3 1080p" is not among them
//	q=severance season 2          4 rows, a different four
//
// A season in the query costs the best season pack this show has, because "S02"
// and "Season 2" are two spellings of one thing and apibay matches the letters.
// So the query stays the bare title and the season is read back off each name
// instead (parseSeasonEpisode), which is the same posture the year already had:
// narrowing happens where "ambiguous" can mean "keep".
func (t *TPB) search(ctx context.Context, query Query) ([]tpbRow, error) {
	title := query.Title
	q := url.Values{}
	q.Set("q", title)
	q.Set("cat", tpbCategories(query))
	target := t.site + "/q.php?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("tpb search %q: %w", title, err)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tpb search %q: %w", title, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tpb search %q: apibay returned %s", title, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, tpbMaxResponse))
	if err != nil {
		return nil, fmt.Errorf("tpb search %q: read apibay response: %w", title, err)
	}
	rows, err := tpbParseRows(body)
	if err != nil {
		return nil, fmt.Errorf("tpb search %q: %w", title, err)
	}
	return rows, nil
}

// tpbRow is one row of an apibay q.php response, written out in full as the
// recorded schema.
//
// Every field is a string, including the ones holding numbers: the wire format
// is `"seeders":"994"` and `"size":"2431905867"`, never `994`. A struct with
// int64 fields does not merely lose them, it fails to unmarshal the whole
// response — so the conversion is done in tpbToReleases, one field at a time,
// where a value we cannot read costs a zero rather than the row.
type tpbRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	InfoHash string `json:"info_hash"`
	Leechers string `json:"leechers"`
	Seeders  string `json:"seeders"`
	Size     string `json:"size"` // bytes, not a human "2.3 GB"
	NumFiles string `json:"num_files"`
	Username string `json:"username"`
	Added    string `json:"added"` // unix seconds
	Status   string `json:"status"`
	Category string `json:"category"`
	IMDB     string `json:"imdb"`
}

func tpbParseRows(body []byte) ([]tpbRow, error) {
	var rows []tpbRow
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode apibay response: %w", err)
	}
	return rows, nil
}

// tpbToReleases converts apibay rows into the shared Release shape, keeping only
// rows that could be downloaded and that the year does not rule out.
func tpbToReleases(rows []tpbRow, year int) []Release {
	out := make([]Release, 0, len(rows))
	for _, r := range rows {
		name := strings.TrimSpace(r.Name)
		if !tpbUsableHash(r.InfoHash) {
			continue
		}
		if !tpbNameAllowsYear(name, year) {
			continue
		}
		rel := Release{
			Title: name,
			// The year that was searched for, not one parsed out of the name.
			// apibay has no year field and x1337.go made the same call for the
			// same reason: the searched year is a fact, a scraped one is a guess.
			Year: year,
			// The ported parser, which orders resolution tokens before marketing
			// ones so that a real apibay name like
			// "Interstellar.2014.PROPER.IMAX.1080p.UHD.BluRay..." stays 1080p.
			Quality: parseQuality(name),
			Magnet:  tpbMagnet(r.InfoHash, name),
			Indexer: tpbIndexerName,
		}
		// Read off the name, unconditionally: apibay's category says a row is
		// television but never which season, and a film's name simply states
		// neither. See parseSeasonEpisode.
		rel.Season, rel.Episode = parseSeasonEpisode(name)
		// A field that will not convert yields a zero and the row survives. Zero
		// seeders sorts last, which is the honest answer; a vanished release is
		// not information at all.
		if n, err := tpbParseNumber(r.Seeders); err == nil {
			rel.Seeders = int(n)
		}
		if n, err := tpbParseNumber(r.Size); err == nil {
			rel.SizeBytes = n
		}
		out = append(out, rel)
	}
	return out
}

// tpbHashPattern matches a BitTorrent v1 info hash as apibay renders it: 40 hex
// characters.
var tpbHashPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// tpbUsableHash reports whether a row's info hash can become a magnet.
//
// This is the guard against apibay's "no results" answer, which is not an empty
// array but a single sentinel row:
//
//	[{"id":"0","name":"No results returned",
//	  "info_hash":"0000000000000000000000000000000000000000","seeders":"0",...}]
//
// Shipping that as a download candidate is the failure this indexer exists to
// avoid. The test is structural on purpose — a row with no usable info hash
// cannot be downloaded whatever it is called, so this also rejects a truncated
// or missing hash. Matching the string "No results returned" would instead be a
// test of apibay's current English wording, and would pass right up until the
// day that wording changed.
//
// Dropping such a row is not the same as dropping a row whose seed count failed
// to convert: there is no release here to keep.
func tpbUsableHash(hash string) bool {
	hash = strings.TrimSpace(hash)
	if !tpbHashPattern.MatchString(hash) {
		return false
	}
	// All zeros is a placeholder, not a torrent.
	return strings.Trim(hash, "0") != ""
}

// tpbYearPattern finds four-digit years in a release name.
//
// The word boundaries are what keep resolution and codec tokens out: "2160p"
// and "1920x1080" have a word character on the far side of the digits, so
// neither is read as a year.
var tpbYearPattern = regexp.MustCompile(`\b(?:19|20)\d{2}\b`)

// tpbNameAllowsYear decides whether a release name is compatible with the year
// being searched for.
//
// apibay has no year field, so this is all there is to go on — which makes the
// year a hint rather than a key. A name carrying no year at all is kept: it is
// ambiguous, and dropping "Interstellar IMAX 1080p BluRay" to enforce a year the
// uploader never wrote is worse than showing it. Only a name that positively
// states a different year is dropped, which on a real "Interstellar" search is
// what removes "Interstellar Wars (2016)" and "The Science Of Interstellar 2015".
func tpbNameAllowsYear(name string, year int) bool {
	if year <= 0 {
		return true
	}
	found := tpbYearPattern.FindAllString(name, -1)
	if len(found) == 0 {
		return true
	}
	want := strconv.Itoa(year)
	for _, y := range found {
		if y == want {
			return true
		}
	}
	return false
}

// tpbParseNumber reads one of apibay's string-wrapped integers.
func tpbParseNumber(s string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("number %q: %w", s, err)
	}
	return n, nil
}

// tpbMagnet builds a magnet from an info hash.
//
// The xt parameter is written raw rather than through url.Values.Encode, which
// would percent-escape the colons in "urn:btih:". Escaped colons are legal but
// unconventional, and this way the link is byte-identical in shape to the ones
// the site itself produces. dn and tr are escaped, since a release name can
// contain anything.
func tpbMagnet(infoHash, name string) string {
	var b strings.Builder
	b.WriteString("magnet:?xt=urn:btih:")
	b.WriteString(strings.TrimSpace(infoHash))
	if name != "" {
		b.WriteString("&dn=")
		b.WriteString(url.QueryEscape(name))
	}
	for _, tr := range tpbTrackers {
		b.WriteString("&tr=")
		b.WriteString(url.QueryEscape(tr))
	}
	return b.String()
}
