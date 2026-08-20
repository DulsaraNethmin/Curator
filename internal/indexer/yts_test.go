package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fixtures are unedited responses from the live API, captured 2026-08-12 from
// https://movies-api.accel.li/api/v2/list_movies.json and only re-indented. The
// query that produced each one is in its name.
const (
	ytsFixtureInterstellar = "yts-interstellar.json"         // query_term=Interstellar
	ytsFixtureDunePart     = "yts-dune-part.json"            // query_term=Dune+Part
	ytsFixtureNoResults    = "yts-no-results.json"           // query_term=zzzznotamovieqqq
	ytsFixtureCountOnly    = "yts-count-without-movies.json" // query_term=Dune+Part+Two+1901
)

// ytsInterstellar1080pHash is the 1080p YIFY encode in yts-interstellar.json. It
// is the *same* torrent as x1337_test.go's realInfoHash, found through a
// completely different source — which is both a check that the two fixtures
// describe the same world and the reason T11 dedups on info hash at all.
const ytsInterstellar1080pHash = realInfoHash

func ytsFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return body
}

// ytsStub is an httptest.Server standing in for the YTS API. It serves one
// recorded fixture to every request and remembers what it was asked for, so a
// test can check the URL we build as well as what we do with the reply.
type ytsStub struct {
	indexer  *YTS
	requests []string
}

func newYTSStub(t *testing.T, fixture string) *ytsStub {
	t.Helper()
	return newYTSStubFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(ytsFixture(t, fixture))
	})
}

func newYTSStubFunc(t *testing.T, h http.HandlerFunc) *ytsStub {
	t.Helper()
	stub := &ytsStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.requests = append(stub.requests, r.URL.String())
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	// The base URL carries a path, exactly as the real one does, so a bug that
	// drops it would show as a request to "/list_movies.json".
	stub.indexer = NewYTS(srv.Client(), WithYTSBaseURL(srv.URL+"/api/v2"))
	return stub
}

// TestYTSSearch is the main parse: a recorded two-movie response becomes one
// Release per torrent, with quality, size, seeders and a magnet on every one.
func TestYTSSearch(t *testing.T) {
	stub := newYTSStub(t, ytsFixtureInterstellar)

	releases, err := stub.indexer.Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	// Two movies carrying two and three torrents. Five releases, not two: the
	// 720p, 1080p and 2160p cuts are separate downloads.
	if len(releases) != 5 {
		t.Fatalf("got %d releases, want 5 (2 movies, 2+3 torrents)", len(releases))
	}
	if want := "/api/v2/list_movies.json?query_term=Interstellar"; stub.requests[0] != want {
		t.Errorf("requested %q, want %q", stub.requests[0], want)
	}
	if len(stub.requests) != 1 {
		t.Errorf("made %d requests, want 1", len(stub.requests))
	}
	if stub.indexer.Name() != "yts" {
		t.Errorf("Name = %q, want yts", stub.indexer.Name())
	}

	want := []struct {
		title     string
		quality   string
		sizeBytes int64
		seeders   int
		hash      string
	}{
		{"The Science of Interstellar (2014) [720p] [BluRay] [x264]", "720p", 486340035, 0, "9B9021809A8F04946C0FF3D9FD056C4ED8D94E99"},
		{"The Science of Interstellar (2014) [1080p] [BluRay] [x264]", "1080p", 901764874, 0, "3E1011A393C92933980B96C5974A787C2FF5AAB0"},
		{"Interstellar (2014) [720p] [BluRay] [x264]", "720p", 1095216660, 0, "6E88B3F25BA49D483D740A652BF013C341BC5373"},
		// 1337x renders this one's size as the rounded "2.3 GB" (2469606195 bytes
		// after conversion); YTS states the byte count, which is why it is taken
		// from the field rather than re-derived from anything.
		{"Interstellar (2014) [1080p] [BluRay] [x264]", "1080p", 2426656522, 100, ytsInterstellar1080pHash},
		{"Interstellar (2014) [2160p] [BluRay] [x265]", "2160p", 8976481649, 100, "FA9773B96054338D1F1C10F2A4D2D9BD5A578B92"},
	}
	for i, w := range want {
		got := releases[i]
		if got.Title != w.title {
			t.Errorf("release %d title = %q, want %q", i, got.Title, w.title)
		}
		if got.Quality != w.quality {
			t.Errorf("release %d quality = %q, want %q", i, got.Quality, w.quality)
		}
		if got.SizeBytes != w.sizeBytes {
			t.Errorf("release %d size = %d, want %d", i, got.SizeBytes, w.sizeBytes)
		}
		if got.Seeders != w.seeders {
			t.Errorf("release %d seeders = %d, want %d", i, got.Seeders, w.seeders)
		}
		if InfoHash(got.Magnet) != w.hash {
			t.Errorf("release %d info hash = %q, want %q", i, InfoHash(got.Magnet), w.hash)
		}
		if got.Year != 2014 {
			t.Errorf("release %d year = %d, want 2014 from the movie's own field", i, got.Year)
		}
		if got.Indexer != "yts" {
			t.Errorf("release %d indexer = %q, want yts", i, got.Indexer)
		}
	}
}

// TestYTSMagnetsAreUsable proves the magnets are not decorative: each one parses
// back through the ported InfoHash to 40 characters, names itself, and announces
// somewhere. A magnet with only a btih would leave a cold torrent waiting on DHT.
func TestYTSMagnetsAreUsable(t *testing.T) {
	stub := newYTSStub(t, ytsFixtureDunePart)

	releases, err := stub.indexer.Search(context.Background(), Query{Title: "Dune Part"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(releases) == 0 {
		t.Fatal("no releases to check")
	}

	for _, r := range releases {
		if !strings.HasPrefix(r.Magnet, "magnet:?xt=urn:btih:") {
			t.Errorf("%s: magnet does not lead with xt: %q", r.Title, r.Magnet)
		}
		if hash := InfoHash(r.Magnet); len(hash) != 40 {
			t.Errorf("%s: InfoHash = %q (%d chars), want 40", r.Title, hash, len(hash))
		}
		q, err := url.ParseQuery(strings.TrimPrefix(r.Magnet, "magnet:?"))
		if err != nil {
			t.Fatalf("%s: magnet is not a parseable query: %v", r.Title, err)
		}
		if got := q.Get("dn"); got != r.Title {
			t.Errorf("%s: dn = %q, want the release title", r.Title, got)
		}
		if got := q["tr"]; len(got) != len(ytsTrackers) {
			t.Errorf("%s: %d trackers, want %d", r.Title, len(got), len(ytsTrackers))
		} else {
			for i, tr := range ytsTrackers {
				if got[i] != tr {
					t.Errorf("%s: tracker %d = %q, want %q", r.Title, i, got[i], tr)
				}
			}
		}
	}
}

// TestYTSSearchYearFilter: one response holds Part One (2021) and Part Two
// (2024), so the filter has to discriminate rather than just pass or drop the lot.
func TestYTSSearchYearFilter(t *testing.T) {
	for _, tt := range []struct {
		name      string
		year      int
		wantCount int
		wantTitle string // prefix every kept release must have
	}{
		{name: "no year keeps both films", year: 0, wantCount: 13, wantTitle: "Dune: Part "},
		{name: "2024 keeps Part Two", year: 2024, wantCount: 6, wantTitle: "Dune: Part Two (2024)"},
		{name: "2021 keeps Part One", year: 2021, wantCount: 7, wantTitle: "Dune: Part One (2021)"},
		{name: "a year neither film has drops everything", year: 1984, wantCount: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stub := newYTSStub(t, ytsFixtureDunePart)
			releases, err := stub.indexer.Search(context.Background(), Query{Title: "Dune Part", Year: tt.year})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(releases) != tt.wantCount {
				t.Fatalf("year %d kept %d releases, want %d", tt.year, len(releases), tt.wantCount)
			}
			for _, r := range releases {
				if !strings.HasPrefix(r.Title, tt.wantTitle) {
					t.Errorf("year %d kept %q", tt.year, r.Title)
				}
				if tt.year > 0 && r.Year != tt.year {
					t.Errorf("year %d kept a release from %d", tt.year, r.Year)
				}
			}
			// The year filter must not become a search parameter: a query_term
			// carrying a year YTS disagrees with comes back empty.
			if want := "/api/v2/list_movies.json?query_term=Dune+Part"; stub.requests[0] != want {
				t.Errorf("requested %q, want %q", stub.requests[0], want)
			}
		})
	}
}

// TestYTSQualityComesFromTheAPI guards rule 4 of the task: the quality is YTS's
// field, never parseQuality over the name we constructed. The tell is the 3D
// torrent in the Dune fixture — its title carries no resolution token at all, so
// parseQuality would flatten it to QualityUnknown.
func TestYTSQualityComesFromTheAPI(t *testing.T) {
	stub := newYTSStub(t, ytsFixtureDunePart)
	releases, err := stub.indexer.Search(context.Background(), Query{Title: "Dune Part", Year: 2021})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	var found bool
	for _, r := range releases {
		if !strings.Contains(r.Title, "[3D]") {
			continue
		}
		found = true
		if r.Quality != "3D" {
			t.Errorf("3D release quality = %q, want the API's own %q", r.Quality, "3D")
		}
		if parseQuality(r.Title) != QualityUnknown {
			t.Fatalf("fixture no longer proves anything: parseQuality(%q) = %q", r.Title, parseQuality(r.Title))
		}
	}
	if !found {
		t.Fatal("the Dune fixture no longer carries a 3D torrent")
	}
}

// TestYTSReleaseTitleDistinguishesTorrents: "Dune: Part Two" ships two 1080p WEB
// torrents differing only in codec. If the title dropped the codec they would
// render as the same line twice.
func TestYTSReleaseTitleDistinguishesTorrents(t *testing.T) {
	stub := newYTSStub(t, ytsFixtureDunePart)
	releases, err := stub.indexer.Search(context.Background(), Query{Title: "Dune Part"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	seen := make(map[string]int, len(releases))
	for _, r := range releases {
		seen[r.Title]++
	}
	for title, n := range seen {
		if n > 1 {
			t.Errorf("%d releases share the title %q", n, title)
		}
	}
	for _, want := range []string{
		"Dune: Part Two (2024) [1080p] [WEB] [x264]",
		"Dune: Part Two (2024) [1080p] [WEB] [x265]",
		"Dune: Part Two (2024) [1080p] [BluRay] [x264]",
	} {
		if seen[want] != 1 {
			t.Errorf("missing release %q", want)
		}
	}
}

// TestYTSSearchNoResults: "nothing found" is a normal outcome. Both real
// shapes of it are covered — including the one where YTS claims a movie_count of
// 1 and then sends no movies key at all.
func TestYTSSearchNoResults(t *testing.T) {
	for _, fixture := range []string{ytsFixtureNoResults, ytsFixtureCountOnly} {
		t.Run(fixture, func(t *testing.T) {
			stub := newYTSStub(t, fixture)
			releases, err := stub.indexer.Search(context.Background(), Query{Title: "zzzznotamovieqqq"})
			if err != nil {
				t.Fatalf("an empty result set must not be an error: %v", err)
			}
			if releases != nil {
				t.Errorf("got %v, want a nil slice", releases)
			}
		})
	}
}

// TestYTSSearchErrors: every failure has to name YTS, because a search runs
// three indexers concurrently and the per-indexer error block in the API response
// is the only place an operator finds out which one broke.
func TestYTSSearchErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "5xx",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			},
		},
		{
			name: "502 with an HTML error page",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte("<!DOCTYPE html>\n<html><head>\n<title>502 Bad Gateway</title>\n</head></html>"))
			},
		},
		{
			// A YTS host that is up but serving its site rather than its API.
			name: "200 that is not JSON",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte("<!DOCTYPE html><html><body>Just a moment...</body></html>"))
			},
		},
		{
			// What yts.rs and yts.hn actually answer: valid JSON from a different,
			// fake implementation. Decoding it silently would report "nothing
			// found" for every search.
			name: "clone site JSON",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"message":"Cannot read property 'moviesPerPage' of undefined"}`))
			},
		},
		{
			name: "api reports its own failure",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"error","status_message":"Query term is too short"}`))
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stub := newYTSStubFunc(t, tt.handler)
			releases, err := stub.indexer.Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
			if err == nil {
				t.Fatalf("got %d releases, want an error", len(releases))
			}
			if releases != nil {
				t.Errorf("an error must not come with partial results, got %v", releases)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "yts") {
				t.Errorf("error does not name yts: %v", err)
			}
			if !strings.Contains(err.Error(), `"Interstellar"`) {
				t.Errorf("error does not name the search: %v", err)
			}
		})
	}
}

func TestYTSSearchEmptyTitle(t *testing.T) {
	stub := newYTSStub(t, ytsFixtureInterstellar)
	if _, err := stub.indexer.Search(context.Background(), Query{Title: "   ", Year: 2014}); err == nil {
		t.Fatal("an empty title must be an error, not a search for everything")
	}
	if len(stub.requests) != 0 {
		t.Errorf("an empty title reached the network: %v", stub.requests)
	}
}

func TestYTSInfoHash(t *testing.T) {
	for _, tt := range []struct {
		label string
		in    string
		want  string
	}{
		{"as YTS sends it", ytsInterstellar1080pHash, ytsInterstellar1080pHash},
		{"lowercase is normalised", strings.ToLower(ytsInterstellar1080pHash), ytsInterstellar1080pHash},
		{"surrounding space", " " + ytsInterstellar1080pHash + " ", ytsInterstellar1080pHash},
		{"empty", "", ""},
		{"too short", "89599BF4DC369A3A8ECA26411C5CCF922D78B4", ""},
		{"not hex", strings.Repeat("z", 40), ""},
	} {
		t.Run(tt.label, func(t *testing.T) {
			if got := ytsInfoHash(tt.in); got != tt.want {
				t.Errorf("ytsInfoHash(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestYTSSkipsUnusableTorrents: YTS has no detail page, so a torrent whose hash
// cannot become a magnet is a release nobody could ever download. Dropping it is
// better than shipping a Release whose empty Magnet means "not resolved yet".
func TestYTSSkipsUnusableTorrents(t *testing.T) {
	got := ytsReleases([]ytsMovie{{
		TitleLong: "Made Up (2024)",
		Year:      2024,
		Torrents: []ytsTorrent{
			{Hash: "", Quality: "1080p", Type: "web", VideoCodec: "x264"},
			{Hash: "not-a-hash", Quality: "1080p", Type: "web", VideoCodec: "x264"},
			{Hash: ytsInterstellar1080pHash, Quality: "2160p", Type: "web", VideoCodec: "x265", Seeds: 7, SizeBytes: 42},
		},
	}}, 0)

	if len(got) != 1 {
		t.Fatalf("got %d releases, want only the one with a usable hash: %v", len(got), got)
	}
	if got[0].Title != "Made Up (2024) [2160p] [WEB] [x265]" {
		t.Errorf("title = %q", got[0].Title)
	}
	if got[0].Seeders != 7 || got[0].SizeBytes != 42 {
		t.Errorf("seeders = %d, size = %d", got[0].Seeders, got[0].SizeBytes)
	}
}

// TestYTSReleaseTitleFallbacks: a movie missing title_long falls back to title,
// and a torrent missing an attribute leaves out the bracket rather than printing
// an empty one.
func TestYTSReleaseTitleFallbacks(t *testing.T) {
	for _, tt := range []struct {
		name  string
		movie ytsMovie
		want  string
	}{
		{
			name: "title_long missing falls back to title",
			movie: ytsMovie{
				Title:    "No Long Title",
				Year:     1999,
				Torrents: []ytsTorrent{{Hash: ytsInterstellar1080pHash, Quality: "1080p"}},
			},
			want: "No Long Title [1080p]",
		},
		{
			name: "an unknown type is passed through, not guessed at",
			movie: ytsMovie{
				TitleLong: "Something (2030)",
				Year:      2030,
				Torrents:  []ytsTorrent{{Hash: ytsInterstellar1080pHash, Quality: "1080p", Type: "webrip", VideoCodec: "av1"}},
			},
			want: "Something (2030) [1080p] [webrip] [av1]",
		},
		{
			// No stray "[]" and no leading space when YTS leaves a field blank.
			name: "nothing but a name",
			movie: ytsMovie{
				TitleLong: "Bare (2030)",
				Year:      2030,
				Torrents:  []ytsTorrent{{Hash: ytsInterstellar1080pHash}},
			},
			want: "Bare (2030)",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := ytsReleases([]ytsMovie{tt.movie}, 0)
			if len(got) != 1 {
				t.Fatalf("got %d releases, want 1", len(got))
			}
			if got[0].Title != tt.want {
				t.Errorf("title = %q, want %q", got[0].Title, tt.want)
			}
		})
	}
}

// TestYTSLiveSearchInterstellar is the one test that talks to the real API. Every
// other test proves only that we read a recording correctly; this proves the
// request we build is one YTS still accepts — which matters more than usual here,
// because the base URL in the task file (yts.mx) has already gone NXDOMAIN once.
//
// Since T77 it also proves the base URL still RESOLVES, which is the thing that
// sentence has always implied and did not do: from T73 until T77 an NXDOMAIN
// host took the transport branch and skipped, so D12 recurring would have been
// green. The control name is apibay.org — see D42 and live_test.go.
//
//	go test -short ./internal/indexer   # never touches the network
func TestYTSLiveSearchInterstellar(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live YTS check in -short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The recorder carries the status of what YTS actually answered to the
	// classifier. This test used to read transport errors only, which is half
	// the rule: a 403 from movies-api.accel.li would have failed CI exactly as
	// apibay's did, with T73's fix sitting one file away and not applicable.
	client, rec := liveClient(30 * time.Second)

	dnsWorks := liveDNSWorks(ctx, ytsControlHost)

	releases, err := NewYTS(client).Search(ctx, Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		// Only a transport failure or a refused caller is "no network". A decode
		// failure or a status YTS should not be returning is a real regression
		// and must not be skipped past — that is how a dead base URL stays green
		// for a week. A base URL that does not resolve is that same regression,
		// and it is caught here only because apibay.org proves DNS works first.
		// classifyLiveFailure holds all three; see live_test.go.
		if verdict, why := classifyLiveFailure(err, rec.lastStatus(), dnsWorks); verdict == liveSkip {
			t.Skipf("skipping live YTS test: %s: %v", why, err)
		}
		t.Fatalf("live YTS search: %v", err)
	}
	if len(releases) == 0 {
		t.Fatalf("live search for Interstellar (2014) at %s found nothing", DefaultYTSBaseURL)
	}

	var has1080p bool
	for _, r := range releases {
		if r.Quality == "1080p" {
			has1080p = true
		}
		if r.Year != 2014 {
			t.Errorf("%q: year = %d, want 2014", r.Title, r.Year)
		}
		if r.Indexer != "yts" {
			t.Errorf("%q: indexer = %q", r.Title, r.Indexer)
		}
		if r.SizeBytes <= 0 {
			t.Errorf("%q: size = %d", r.Title, r.SizeBytes)
		}
		if len(InfoHash(r.Magnet)) != 40 {
			t.Errorf("%q: info hash = %q, want 40 characters", r.Title, InfoHash(r.Magnet))
		}
		t.Logf("live: %-6s %11d bytes %4d seeds  %s", r.Quality, r.SizeBytes, r.Seeders, r.Title)
	}
	if !has1080p {
		t.Error("live search returned no 1080p release")
	}
}

// Compile-time proof that YTS satisfies Indexer. It deliberately does not
// implement MagnetResolver: its search already carries every magnet, so there is
// nothing to resolve.
var _ Indexer = (*YTS)(nil)
