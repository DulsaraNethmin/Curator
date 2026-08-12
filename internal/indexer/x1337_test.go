package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// A real magnet, taken from 1337x's detail page for the fixture's top result and
// captured through minter on the same run that produced the fixture. Its tracker
// list is trimmed to three for readability; the btih is untouched.
const (
	realMagnet = "magnet:?xt=urn:btih:89599BF4DC369A3A8ECA26411C5CCF922D78B486" +
		"&dn=Interstellar+%282014%29+1080p+BrRip+x264+-+YIFY" +
		"&tr=udp%3A%2F%2Ftracker.yify-torrents.com%2Fannounce" +
		"&tr=udp%3A%2F%2Fopen.demonii.com%3A1337" +
		"&tr=udp%3A%2F%2Fexodus.desync.com%3A6969"
	realInfoHash = "89599BF4DC369A3A8ECA26411C5CCF922D78B486"
)

func fixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/1337x-search-interstellar.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b)
}

// detailPage is a 1337x torrent page cut down to the part parseMagnet reads. The
// href is escaped the way HTML carries it, so this also checks that the &amp;
// entities come back decoded.
func detailPage() string {
	return `<html><body>
	  <div class="no-top-radius">
	    <ul class="download-links-dontblock button-inline">
	      <li><a class="l1" href="` + strings.ReplaceAll(realMagnet, "&", "&amp;") + `">Magnet Download</a></li>
	      <li><a class="l2" href="/torrent/1099151/">Report</a></li>
	    </ul>
	  </div>
	</body></html>`
}

func TestParseSearch(t *testing.T) {
	rows, err := parseSearch(fixture(t))
	if err != nil {
		t.Fatalf("parseSearch: %v", err)
	}
	if len(rows) != 20 {
		t.Fatalf("got %d rows, want 20 (1337x renders 20 results per page)", len(rows))
	}

	tests := []struct {
		index   int
		name    string
		quality string
		seeders string
		leeches string
		size    string
		href    string
	}{
		{
			index:   0,
			name:    "Interstellar (2014) 1080p BrRip x264 - YIFY",
			quality: "1080p",
			seeders: "1386",
			leeches: "119",
			size:    "2.3 GB",
			href:    "/torrent/1099151/Interstellar-2014-1080p-BrRip-x264-YIFY/",
		},
		{
			// The UHD-in-a-1080p-name case, straight off the real page.
			index:   4,
			name:    "Interstellar.2014.PROPER.IMAX.1080p.UHD.BluRay.x265.HDR.DV.DD+5.1.Dual.YG⭐",
			quality: "1080p",
			seeders: "135",
			leeches: "27",
			size:    "4.2 GB",
			href:    "/torrent/5776156/Interstellar-2014-PROPER-IMAX-1080p-UHD-BluRay-x265-HDR-DV-DD-5-1-Dual-YG/",
		},
		{
			// &amp; in the name must come back decoded, and a name with no
			// resolution token must not be given one.
			index:   7,
			name:    "VA-DJ Cube & DJ Kamelot - Interstellar Travel 10-2020 (MelissaPerry)",
			quality: QualityUnknown,
			seeders: "102",
			leeches: "0",
			size:    "131.3 MB",
			href:    "/torrent/4330492/VA-DJ-Cube-DJ-Kamelot-Interstellar-Travel-10-2020-MelissaPerry/",
		},
		{
			index:   13,
			name:    "Interstellar 2014 UHD BluRay 2160p DTS-HD MA 5 1 HEVC REMUX-FraMeSToR",
			quality: "2160p",
			seeders: "81",
			leeches: "11",
			size:    "65.7 GB",
			href:    "/torrent/6436239/Interstellar-2014-UHD-BluRay-2160p-DTS-HD-MA-5-1-HEVC-REMUX-FraMeSToR/",
		},
		{
			index:   19,
			name:    "Interstellar.2014.1080p.WEB-DL.AAC.x264-skyflickz",
			quality: "1080p",
			seeders: "62",
			leeches: "20",
			size:    "2.3 GB",
			href:    "/torrent/6496128/Interstellar-2014-1080p-WEB-DL-AAC-x264-skyflickz/",
		},
	}
	for _, tt := range tests {
		got := rows[tt.index]
		if got.Name != tt.name {
			t.Errorf("row %d name = %q, want %q", tt.index, got.Name, tt.name)
		}
		if got.Quality != tt.quality {
			t.Errorf("row %d quality = %q, want %q", tt.index, got.Quality, tt.quality)
		}
		if got.Seeders != tt.seeders {
			t.Errorf("row %d seeders = %q, want %q", tt.index, got.Seeders, tt.seeders)
		}
		if got.Leeches != tt.leeches {
			t.Errorf("row %d leeches = %q, want %q", tt.index, got.Leeches, tt.leeches)
		}
		if got.Size != tt.size {
			t.Errorf("row %d size = %q, want %q", tt.index, got.Size, tt.size)
		}
		if got.Href != tt.href {
			t.Errorf("row %d href = %q, want %q", tt.index, got.Href, tt.href)
		}
	}

	// The size cell nests a <span> holding the seed count for the mobile layout.
	// If Contents().First() ever stops excluding it, every size gains a suffix.
	for i, r := range rows {
		if r.Size == "" {
			t.Errorf("row %d has no size", i)
		}
		if r.Seeders != "" && strings.HasSuffix(r.Size, r.Seeders) {
			t.Errorf("row %d size %q swallowed the nested seeds span %q", i, r.Size, r.Seeders)
		}
		if r.Name == "" || r.Href == "" {
			t.Errorf("row %d incomplete: name=%q href=%q", i, r.Name, r.Href)
		}
		if !strings.HasPrefix(r.Href, "/torrent/") {
			t.Errorf("row %d href %q is not a detail-page path — the wrong anchor was taken", i, r.Href)
		}
	}
}

func TestParseSearchNoTable(t *testing.T) {
	// What a Cloudflare challenge page looks like to the parser: valid HTML, no
	// result table. It must not be an error, just nothing.
	rows, err := parseSearch(`<html><body><h1>Just a moment...</h1></body></html>`)
	if err != nil {
		t.Fatalf("parseSearch: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("got %d rows from a page with no table, want 0", len(rows))
	}
}

func TestToReleases(t *testing.T) {
	rows, err := parseSearch(fixture(t))
	if err != nil {
		t.Fatalf("parseSearch: %v", err)
	}
	releases := toReleases(rows, 2014)
	if len(releases) != len(rows) {
		t.Fatalf("got %d releases from %d rows — conversion dropped some", len(releases), len(rows))
	}

	top := releases[0]
	if top.Title != "Interstellar (2014) 1080p BrRip x264 - YIFY" {
		t.Errorf("title = %q", top.Title)
	}
	if top.Seeders != 1386 {
		t.Errorf("seeders = %d, want 1386", top.Seeders)
	}
	if top.SizeBytes != 2469606195 { // 2.3 GB
		t.Errorf("size = %d, want 2469606195", top.SizeBytes)
	}
	if top.Year != 2014 {
		t.Errorf("year = %d, want the searched-for 2014", top.Year)
	}
	if top.Indexer != "1337x" {
		t.Errorf("indexer = %q, want 1337x", top.Indexer)
	}
	if top.Magnet != "" {
		t.Errorf("search must not carry a magnet, got %q", top.Magnet)
	}
	if top.detailPath == "" {
		t.Error("detail path must survive conversion, or lazy resolution cannot work")
	}
}

func TestToReleasesKeepsUnconvertibleRows(t *testing.T) {
	// A size or seed count we cannot read is not a reason to hide a release.
	got := toReleases([]row{
		{Name: "Good", Quality: "1080p", Seeders: "12", Size: "1.0 GB", Href: "/torrent/1/"},
		{Name: "Bad numbers", Quality: "1080p", Seeders: "n/a", Size: "unknown", Href: "/torrent/2/"},
	}, 2020)

	if len(got) != 2 {
		t.Fatalf("got %d releases, want 2 — the unparseable row was dropped", len(got))
	}
	if got[0].Seeders != 12 || got[0].SizeBytes != 1073741824 {
		t.Errorf("good row = seeders %d size %d", got[0].Seeders, got[0].SizeBytes)
	}
	if got[1].Title != "Bad numbers" {
		t.Errorf("second release = %q", got[1].Title)
	}
	if got[1].Seeders != 0 || got[1].SizeBytes != 0 {
		t.Errorf("unreadable values must be zero, got seeders %d size %d", got[1].Seeders, got[1].SizeBytes)
	}
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{in: "2.3 GB", want: 2469606195},
		{in: "452.8 MB", want: 474795212},
		{in: "65.7 GB", want: 70544837836},
		{in: "1.0 GB", want: 1073741824},
		{in: "700 MB", want: 734003200},
		{in: "1.5 TB", want: 1649267441664},
		{in: "512 B", want: 512},
		{in: "  2.3 GB  ", want: 2469606195},
		{in: "2.3 gb", want: 2469606195},
		{in: "", wantErr: true},
		{in: "2.3", wantErr: true},
		{in: "2.3 PB", wantErr: true},
		{in: "big GB", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseSize(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseSize(%q) = %d, want an error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSize(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("parseSize(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseSeeders(t *testing.T) {
	for _, tt := range []struct {
		in      string
		want    int
		wantErr bool
	}{
		{in: "1386", want: 1386},
		{in: "0", want: 0},
		{in: " 42 ", want: 42},
		{in: "12,345", want: 12345},
		{in: "", wantErr: true},
		{in: "n/a", wantErr: true},
	} {
		got, err := parseSeeders(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseSeeders(%q) = %d, want an error", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSeeders(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseSeeders(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseMagnet(t *testing.T) {
	got := parseMagnet(detailPage())
	if got != realMagnet {
		t.Errorf("parseMagnet =\n%q\nwant\n%q", got, realMagnet)
	}
	if parseMagnet(`<html><body><a href="/torrent/1/">no magnet here</a></body></html>`) != "" {
		t.Error("a page with no magnet must yield an empty string")
	}
}

func TestInfoHash(t *testing.T) {
	for _, tt := range []struct {
		label  string
		magnet string
		want   string
	}{
		{"real magnet", realMagnet, realInfoHash},
		{"lowercase btih is normalised", strings.ToLower(realMagnet), realInfoHash},
		{"hash only", "magnet:?xt=urn:btih:" + realInfoHash, realInfoHash},
		{"not a magnet", "https://1337x.to/torrent/1099151/", ""},
		{"empty", "", ""},
	} {
		t.Run(tt.label, func(t *testing.T) {
			got := InfoHash(tt.magnet)
			if got != tt.want {
				t.Errorf("InfoHash = %q, want %q", got, tt.want)
			}
			if tt.want != "" && len(got) != 40 {
				t.Errorf("InfoHash = %q (%d chars), want 40", got, len(got))
			}
		})
	}
}

// TestSearchMovie drives the whole 1337x path against a fake minter serving the
// captured fixture. No network.
func TestSearchMovie(t *testing.T) {
	var fetched []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := decodeFetchTarget(t, r)
		fetched = append(fetched, target)
		w.Header().Set("content-type", "application/json")
		writeFetchResponse(t, w, fixture(t))
	}))
	defer srv.Close()

	x := NewX1337(NewMinter(srv.URL))
	releases, err := x.SearchMovie(context.Background(), "Interstellar", 2014)
	if err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	if len(releases) != 20 {
		t.Fatalf("got %d releases, want 20", len(releases))
	}
	if len(fetched) != 1 {
		t.Fatalf("made %d minter calls, want 1 — search must not resolve magnets eagerly", len(fetched))
	}
	if want := "https://1337x.to/search/Interstellar%202014/1/"; fetched[0] != want {
		t.Errorf("searched %q, want %q", fetched[0], want)
	}
	if x.Name() != "1337x" {
		t.Errorf("Name = %q", x.Name())
	}
	for i, r := range releases {
		if r.Magnet != "" {
			t.Fatalf("release %d came back with a magnet — resolution must stay lazy", i)
		}
	}
}

func TestSearchMovieWithoutYear(t *testing.T) {
	var fetched []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched = append(fetched, decodeFetchTarget(t, r))
		writeFetchResponse(t, w, fixture(t))
	}))
	defer srv.Close()

	if _, err := NewX1337(NewMinter(srv.URL)).SearchMovie(context.Background(), "Interstellar", 0); err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	if want := "https://1337x.to/search/Interstellar/1/"; fetched[0] != want {
		t.Errorf("searched %q, want %q", fetched[0], want)
	}
}

func TestResolveMagnet(t *testing.T) {
	var fetched []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := decodeFetchTarget(t, r)
		fetched = append(fetched, target)
		if strings.Contains(target, "/search/") {
			writeFetchResponse(t, w, fixture(t))
			return
		}
		writeFetchResponse(t, w, detailPage())
	}))
	defer srv.Close()

	x := NewX1337(NewMinter(srv.URL))
	releases, err := x.SearchMovie(context.Background(), "Interstellar", 2014)
	if err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}

	magnet, err := x.ResolveMagnet(context.Background(), releases[0])
	if err != nil {
		t.Fatalf("ResolveMagnet: %v", err)
	}
	if magnet != realMagnet {
		t.Errorf("magnet = %q", magnet)
	}
	if InfoHash(magnet) != realInfoHash {
		t.Errorf("infohash = %q, want %q", InfoHash(magnet), realInfoHash)
	}

	// Twenty results, two fetches. That is the whole point of deferring.
	if len(fetched) != 2 {
		t.Fatalf("made %d minter calls for a 20-result search plus one pick, want 2", len(fetched))
	}
	want := "https://1337x.to/torrent/1099151/Interstellar-2014-1080p-BrRip-x264-YIFY/"
	if fetched[1] != want {
		t.Errorf("resolved from %q, want %q", fetched[1], want)
	}
}

func TestResolveMagnetShortCircuits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("minter must not be called for a release that already has a magnet")
		writeFetchResponse(t, w, detailPage())
	}))
	defer srv.Close()

	x := NewX1337(NewMinter(srv.URL))
	got, err := x.ResolveMagnet(context.Background(), Release{Title: "already resolved", Magnet: realMagnet})
	if err != nil {
		t.Fatalf("ResolveMagnet: %v", err)
	}
	if got != realMagnet {
		t.Errorf("magnet = %q", got)
	}
}

func TestResolveMagnetErrors(t *testing.T) {
	t.Run("no detail page recorded", func(t *testing.T) {
		x := NewX1337(NewMinter("http://127.0.0.1:1"))
		if _, err := x.ResolveMagnet(context.Background(), Release{Title: "hand-made"}); err == nil {
			t.Fatal("want an error when there is no detail path to fetch")
		}
	})

	t.Run("detail page has no magnet", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			decodeFetchTarget(t, r)
			if strings.Contains(r.URL.Path, "fetch") {
				writeFetchResponse(t, w, `<html><body>gone</body></html>`)
			}
		}))
		defer srv.Close()

		x := NewX1337(NewMinter(srv.URL))
		r := toReleases([]row{{Name: "x", Href: "/torrent/1/"}}, 2014)[0]
		if _, err := x.ResolveMagnet(context.Background(), r); err == nil {
			t.Fatal("want an error when the detail page carries no magnet")
		}
	})
}

// Compile-time proof that X1337 satisfies both interfaces.
var (
	_ Indexer        = (*X1337)(nil)
	_ MagnetResolver = (*X1337)(nil)
)
