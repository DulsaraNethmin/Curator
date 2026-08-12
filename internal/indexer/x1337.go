package indexer

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

const (
	indexerName = "1337x"
	x1337Site   = "https://1337x.to"
)

// X1337 searches 1337x. It is the broadest public catalogue we have and the only
// one behind Cloudflare, so every request goes through minter and costs ~9 s.
// That price is what makes lazy magnet resolution worth the extra method.
type X1337 struct {
	minter *Minter
	site   string
}

// NewX1337 returns a 1337x indexer fetching through m.
func NewX1337(m *Minter) *X1337 {
	return &X1337{minter: m, site: x1337Site}
}

// Name implements Indexer.
func (x *X1337) Name() string { return indexerName }

// SearchMovie implements Indexer. The returned releases have no Magnet — call
// ResolveMagnet for the one that gets picked.
func (x *X1337) SearchMovie(ctx context.Context, title string, year int) ([]Release, error) {
	q := searchQuery(title, year)
	page, err := x.minter.Fetch(ctx, fmt.Sprintf("%s/search/%s/1/", x.site, url.PathEscape(q)))
	if err != nil {
		return nil, fmt.Errorf("1337x search %q: %w", q, err)
	}
	rows, err := parseSearch(page.HTML)
	if err != nil {
		return nil, fmt.Errorf("1337x search %q: parse results: %w", q, err)
	}
	// Zero rows is a legitimate "no hits", but it is also what a challenge page or
	// a markup change looks like. A caller that cares can check page.Solved.
	return toReleases(rows, year), nil
}

// searchQuery builds the keyword string 1337x is searched with.
//
// cfprobe passed the bare title because its CLI had no year to pass. The Indexer
// interface supplies one, and 1337x's search is a plain keyword match over the
// release name, so including the year is what keeps "Interstellar" from also
// returning a 2020 DJ mix and a FitGirl game repack. It is kept in its own
// function so that if searches start coming back empty, this is the one line to
// suspect.
func searchQuery(title string, year int) string {
	if year > 0 {
		return fmt.Sprintf("%s %d", title, year)
	}
	return title
}

// ResolveMagnet implements MagnetResolver by fetching the release's detail page.
func (x *X1337) ResolveMagnet(ctx context.Context, r Release) (string, error) {
	if r.Magnet != "" {
		return r.Magnet, nil
	}
	if r.detailPath == "" {
		return "", fmt.Errorf("1337x resolve magnet for %q: no detail page recorded", r.Title)
	}
	page, err := x.minter.Fetch(ctx, x.site+r.detailPath)
	if err != nil {
		return "", fmt.Errorf("1337x resolve magnet for %q: %w", r.Title, err)
	}
	magnet := parseMagnet(page.HTML)
	if magnet == "" {
		return "", fmt.Errorf("1337x resolve magnet for %q: no magnet link on %s", r.Title, r.detailPath)
	}
	return magnet, nil
}

// row is one line of 1337x's result table, exactly as the HTML gives it — seeders
// and size as the strings the page renders. Conversion to Release's int64 and int
// happens in toReleases, so that the parsing above it stays a faithful read of
// the markup and a value we cannot convert is not a parse failure.
type row struct {
	Name    string
	Quality string
	Seeders string
	Leeches string
	Size    string
	Href    string
}

// parseSearch reads 1337x's result table. Magnets are not here — they live on each
// torrent's detail page, which is why resolution is deferred until a user picks one.
func parseSearch(html string) ([]row, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, err
	}
	var out []row
	doc.Find("table.table-list tbody tr").Each(func(_ int, tr *goquery.Selection) {
		links := tr.Find("td.coll-1.name a")
		// The name cell holds two anchors: a category icon first, the release name
		// last. The last one is also the one carrying the detail-page href.
		name := strings.TrimSpace(links.Last().Text())
		if name == "" {
			return
		}
		href, _ := links.Last().Attr("href")
		out = append(out, row{
			Name:    name,
			Quality: parseQuality(name),
			Seeders: strings.TrimSpace(tr.Find("td.coll-2").Text()),
			Leeches: strings.TrimSpace(tr.Find("td.coll-3").Text()),
			// The size cell has a <span> with the seed count nested inside it for
			// the mobile layout. Contents().First() takes only the leading text
			// node, so "2.3 GB" does not come back as "2.3 GB1386".
			Size: strings.TrimSpace(tr.Find("td.coll-4").Contents().First().Text()),
			Href: href,
		})
	})
	return out, nil
}

// toReleases converts parsed rows into the shared Release shape.
//
// A row whose seeders or size will not parse is kept, not dropped: it is still a
// real release, and hiding it entirely is worse than showing it with a zero that
// simply sorts last.
func toReleases(rows []row, year int) []Release {
	out := make([]Release, 0, len(rows))
	for _, r := range rows {
		rel := Release{
			Title: r.Name,
			// Not parsed from the release name — this is the year that was searched
			// for. cfprobe never extracted a year, and inventing that parse here
			// would be a change smuggled in with a move.
			Year:       year,
			Quality:    r.Quality,
			Indexer:    indexerName,
			detailPath: r.Href,
		}
		if n, err := parseSeeders(r.Seeders); err == nil {
			rel.Seeders = n
		}
		if n, err := parseSize(r.Size); err == nil {
			rel.SizeBytes = n
		}
		out = append(out, rel)
	}
	return out
}

func parseSeeders(s string) (int, error) {
	// Strip thousands separators; the listing renders them once a torrent is
	// popular enough.
	n, err := strconv.Atoi(strings.ReplaceAll(strings.TrimSpace(s), ",", ""))
	if err != nil {
		return 0, fmt.Errorf("seeders %q: %w", s, err)
	}
	return n, nil
}

// sizeUnits are 1024-based, which is what torrent sites mean by "GB".
var sizeUnits = map[string]int64{
	"B": 1, "KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40,
}

// parseSize turns the human size 1337x renders ("2.3 GB", "452.8 MB") into bytes.
// cfprobe kept these as strings because it only printed them; Release stores bytes.
func parseSize(s string) (int64, error) {
	f := strings.Fields(strings.TrimSpace(s))
	if len(f) != 2 {
		return 0, fmt.Errorf("size %q: want \"<number> <unit>\"", s)
	}
	n, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, fmt.Errorf("size %q: %w", s, err)
	}
	unit, ok := sizeUnits[strings.ToUpper(f[1])]
	if !ok {
		return 0, fmt.Errorf("size %q: unknown unit %q", s, f[1])
	}
	return int64(n * float64(unit)), nil
}

// parseMagnet pulls the first magnet link off a 1337x detail page.
func parseMagnet(html string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return ""
	}
	magnet := ""
	doc.Find("a[href^='magnet:']").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		magnet, _ = s.Attr("href")
		return false
	})
	return magnet
}

var btih = regexp.MustCompile(`(?i)urn:btih:([a-z0-9]+)`)

// InfoHash pulls the btih out of a magnet. It is the only part that matters —
// everything after it (dn, trackers) is optional metadata a client can do without,
// and it is the handle qBittorrent and the downloads table both key on.
func InfoHash(magnet string) string {
	m := btih.FindStringSubmatch(magnet)
	if len(m) < 2 {
		return ""
	}
	return strings.ToUpper(m[1])
}
