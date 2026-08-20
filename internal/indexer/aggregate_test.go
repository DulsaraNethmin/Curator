package indexer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// aggStub is a stand-in indexer. It counts searches so a test can assert how many
// requests a search really made, which is the only way to prove laziness.
type aggStub struct {
	name     string
	releases []Release
	err      error
	delay    time.Duration

	searches atomic.Int32
	resolves atomic.Int32
	magnet   string

	mu  sync.Mutex
	saw []Query
}

func (s *aggStub) Name() string { return s.name }

func (s *aggStub) Search(ctx context.Context, q Query) ([]Release, error) {
	s.searches.Add(1)
	s.mu.Lock()
	s.saw = append(s.saw, q)
	s.mu.Unlock()
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.releases, nil
}

// queries returns every Query this stub was handed, so a test can prove what
// reached the indexer rather than what the aggregator was asked for.
func (s *aggStub) queries() []Query {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Query(nil), s.saw...)
}

// aggResolvingStub also implements MagnetResolver, standing in for 1337x — the
// indexer whose releases arrive without magnets.
type aggResolvingStub struct{ aggStub }

func (s *aggResolvingStub) ResolveMagnet(ctx context.Context, r Release) (string, error) {
	s.resolves.Add(1)
	if s.magnet == "" {
		return "", errors.New("no magnet")
	}
	return s.magnet, nil
}

// aggMagnet builds a magnet with a distinct info hash per seed character.
func aggMagnet(hexChar string) string {
	return "magnet:?xt=urn:btih:" + strings.Repeat(hexChar, 40) + "&dn=test"
}

func aggRelease(indexer, title string, seeders int, magnet string) Release {
	return Release{
		Title: title, Year: 2014, Quality: "1080p",
		SizeBytes: 1 << 30, Seeders: seeders, Magnet: magnet, Indexer: indexer,
	}
}

func newAggTest(t *testing.T, timeout time.Duration, indexers ...Indexer) *Aggregator {
	t.Helper()
	return NewAggregator(indexers, timeout, time.Hour)
}

func TestAggregatorMergesEveryIndexer(t *testing.T) {
	a := newAggTest(t, 2*time.Second,
		&aggStub{name: "yts", releases: []Release{aggRelease("yts", "A", 10, aggMagnet("a"))}},
		&aggStub{name: "tpb", releases: []Release{aggRelease("tpb", "B", 20, aggMagnet("b"))}},
		&aggStub{name: "1337x", releases: []Release{aggRelease("1337x", "C", 30, aggMagnet("c"))}},
	)

	got, err := a.Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got.Releases) != 3 {
		t.Fatalf("releases = %d, want 3", len(got.Releases))
	}
	if len(got.Outcomes) != 3 {
		t.Fatalf("outcomes = %d, want 3", len(got.Outcomes))
	}
	for _, o := range got.Outcomes {
		if !o.OK || o.Count != 1 {
			t.Errorf("outcome %s = %+v, want ok with count 1", o.Name, o)
		}
	}
}

// The whole Query reaches the indexers, not just the title. The media type and
// the season are what a source discriminates on — TPB's cat= parameter, 1337x's
// keyword string — and neither can be recovered from a title however it is
// spelled, which is why the interface takes a Query at all.
func TestAggregatorPassesTheWholeQueryDown(t *testing.T) {
	ix := &aggStub{name: "tpb"}
	a := newAggTest(t, 2*time.Second, ix)

	if _, err := a.Search(context.Background(),
		Query{Title: "Severance: The Show", Year: 2022, Media: MediaTV, Season: 2}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	got := ix.queries()
	if len(got) != 1 {
		t.Fatalf("the indexer was called %d times, want 1", len(got))
	}
	// The title is normalised on the way down (D20) and everything else is
	// handed over as it was asked for.
	want := Query{Title: "Severance The Show", Year: 2022, Media: MediaTV, Season: 2}
	if got[0] != want {
		t.Errorf("the indexer was asked %+v, want %+v", got[0], want)
	}
}

// The blank media type is resolved once, at the top, so the capability check,
// the cache key and the indexer all read the same word for the default.
func TestAggregatorResolvesTheDefaultMediaTypeOnce(t *testing.T) {
	ix := &aggStub{name: "tpb"}
	if _, err := newAggTest(t, 2*time.Second, ix).
		Search(context.Background(), Query{Title: "Interstellar", Year: 2014}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := ix.queries()[0].Media; got != MediaMovie {
		t.Errorf("the indexer was asked for media %q, want %q", got, MediaMovie)
	}
}

// D10: an id is a property of the release, not of the search that found it. The
// same magnet found by a film search and by a television search is one release
// and must stamp one id — which is why the media type is deliberately NOT part
// of releaseID, however inviting that looks once Query has a Media field.
func TestReleaseIDIsTheSameWhicheverMediaFoundIt(t *testing.T) {
	release := aggRelease("tpb", "Severance - Season 1 - Mp4 x264 AC3 1080p", 844, aggMagnet("a"))

	film, err := newAggTest(t, 2*time.Second, &aggStub{name: "tpb", releases: []Release{release}}).
		Search(context.Background(), Query{Title: "Severance", Year: 2022})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	tv, err := newAggTest(t, 2*time.Second, &aggStub{name: "tpb", releases: []Release{release}}).
		Search(context.Background(), Query{Title: "Severance", Year: 2022, Media: MediaTV, Season: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if film.Releases[0].ID != tv.Releases[0].ID {
		t.Errorf("one release stamped two ids, %q as a film and %q as television — a magnet is a magnet",
			film.Releases[0].ID, tv.Releases[0].ID)
	}
}

// The whole point of not using errgroup.WithContext: one indexer failing must not
// cancel its siblings, or a downed minter would silently empty a good search.
func TestAggregatorOneFailureDoesNotCancelTheOthers(t *testing.T) {
	yts := &aggStub{name: "yts", releases: []Release{aggRelease("yts", "A", 10, aggMagnet("a"))}}
	tpb := &aggStub{name: "tpb", releases: []Release{aggRelease("tpb", "B", 20, aggMagnet("b"))}}
	x := &aggStub{name: "1337x", err: errors.New("calling minter: connection refused")}

	got, err := newAggTest(t, 2*time.Second, yts, tpb, x).Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		t.Fatalf("Search returned an error for one failing indexer: %v", err)
	}
	if len(got.Releases) != 2 {
		t.Fatalf("releases = %d, want the 2 from the healthy indexers", len(got.Releases))
	}
	if yts.searches.Load() != 1 || tpb.searches.Load() != 1 {
		t.Errorf("healthy indexers searched %d and %d times, want 1 each — a sibling's error cancelled them",
			yts.searches.Load(), tpb.searches.Load())
	}

	var failed int
	for _, o := range got.Outcomes {
		if o.OK {
			continue
		}
		failed++
		if o.Name != "1337x" {
			t.Errorf("failed outcome is %q, want 1337x", o.Name)
		}
		if !strings.Contains(o.Error, "connection refused") {
			t.Errorf("outcome error = %q, want it to carry the cause", o.Error)
		}
	}
	if failed != 1 {
		t.Errorf("failed outcomes = %d, want 1", failed)
	}
}

func TestAggregatorAllFailingIsStillASuccess(t *testing.T) {
	boom := errors.New("boom")
	got, err := newAggTest(t, 2*time.Second,
		&aggStub{name: "yts", err: boom},
		&aggStub{name: "tpb", err: boom},
		&aggStub{name: "1337x", err: boom},
	).Search(context.Background(), Query{Title: "Interstellar", Year: 2014})

	// The request succeeded; the sources failed. A fatal error here would be a lie
	// about whose fault it is.
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got.Releases) != 0 {
		t.Errorf("releases = %d, want 0", len(got.Releases))
	}
	if len(got.Outcomes) != 3 {
		t.Fatalf("outcomes = %d, want 3", len(got.Outcomes))
	}
	for _, o := range got.Outcomes {
		if o.OK {
			t.Errorf("outcome %s reports ok, want failed", o.Name)
		}
	}
}

// The task this whole distinction exists for: a 1337x search with no minter has
// to be reported as unconfigured, not as a search that found nothing
// (docs/tasks/T49-minter-on-demand.md). The two look identical to a user
// otherwise, and one of them is fixed by a pasted line.
func TestAggregatorUnreachableCompanionReportsUnconfigured(t *testing.T) {
	yts := &aggStub{name: "yts", releases: []Release{aggRelease("yts", "A", 10, aggMagnet("a"))}}
	// Wrapped the way X1337.Search wraps it, so this test fails if that
	// wrapping ever stops carrying the sentinel through.
	x := &aggStub{name: "1337x", err: fmt.Errorf("1337x search %q: %w", "interstellar 2014",
		unreachable{errors.New("calling minter at http://minter:8191 (is it running?): connection refused")})}

	got, err := newAggTest(t, 2*time.Second, yts, x).Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	// The healthy indexer's releases still come back. An unconfigured sibling
	// must not empty a good search — that is the failure this reporting exists
	// to make visible, not one to introduce.
	if len(got.Releases) != 1 {
		t.Fatalf("releases = %d, want the 1 from the healthy indexer", len(got.Releases))
	}

	byName := map[string]Outcome{}
	for _, o := range got.Outcomes {
		byName[o.Name] = o
	}
	if o := byName["1337x"]; o.OK || !o.Unconfigured {
		t.Errorf("1337x outcome = %+v, want ok=false and unconfigured=true", o)
	}
	// And the reason survives: the screen shows a command, but the log and the
	// banner still say what actually happened.
	if !strings.Contains(byName["1337x"].Error, "connection refused") {
		t.Errorf("outcome error = %q, want it to carry the cause", byName["1337x"].Error)
	}
	if o := byName["yts"]; !o.OK || o.Unconfigured {
		t.Errorf("yts outcome = %+v, want ok=true and unconfigured=false", o)
	}
}

// The other direction, and the one that would make the flag useless: a minter
// that IS running and simply failed must not be reported as unconfigured, or the
// screen offers a compose command for a container that is already up.
func TestAggregatorOrdinaryFailureIsNotUnconfigured(t *testing.T) {
	x := &aggStub{name: "1337x", err: errors.New("1337x search: minter returned 500: browser did not start")}

	got, err := newAggTest(t, 2*time.Second, x).Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if o := got.Outcomes[0]; o.OK || o.Unconfigured {
		t.Errorf("outcome = %+v, want ok=false and unconfigured=false", o)
	}
}

// An indexer that found nothing is a SUCCESS with a count of zero, and it must
// never be confused with either of the two above. This is the third leg of the
// same distinction and the one that is easiest to lose in a refactor.
func TestAggregatorNoResultsIsNotAFailure(t *testing.T) {
	got, err := newAggTest(t, 2*time.Second,
		&aggStub{name: "1337x", releases: nil},
	).Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if o := got.Outcomes[0]; !o.OK || o.Count != 0 || o.Unconfigured || o.Error != "" {
		t.Errorf("outcome = %+v, want ok=true, count=0, unconfigured=false and no error", o)
	}
}

func TestAggregatorStragglerIsOmittedNotWaitedFor(t *testing.T) {
	const timeout = 100 * time.Millisecond
	slow := &aggStub{name: "1337x", delay: 5 * time.Second,
		releases: []Release{aggRelease("1337x", "slow", 99, aggMagnet("f"))}}
	fast := &aggStub{name: "yts", releases: []Release{aggRelease("yts", "fast", 10, aggMagnet("a"))}}

	start := time.Now()
	got, err := newAggTest(t, timeout, fast, slow).Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if elapsed > time.Second {
		t.Errorf("search took %v, want it to return near the %v deadline rather than waiting out the straggler",
			elapsed, timeout)
	}
	if len(got.Releases) != 1 || got.Releases[0].Title != "fast" {
		t.Errorf("releases = %+v, want only the fast indexer's", got.Releases)
	}
	for _, o := range got.Outcomes {
		if o.Name == "1337x" && o.OK {
			t.Error("straggler reported ok, want it reported as failed")
		}
	}
}

// aggDeafStub ignores its context entirely, which is what a badly-behaved client
// library actually does. The aggregator must return at its deadline anyway rather
// than depending on the indexer to notice.
type aggDeafStub struct {
	name  string
	delay time.Duration
}

func (s *aggDeafStub) Name() string { return s.name }

func (s *aggDeafStub) Search(context.Context, Query) ([]Release, error) {
	time.Sleep(s.delay)
	return nil, nil
}

func TestAggregatorDoesNotWaitForAnIndexerThatIgnoresCancellation(t *testing.T) {
	const timeout = 100 * time.Millisecond
	fast := &aggStub{name: "yts", releases: []Release{aggRelease("yts", "fast", 10, aggMagnet("a"))}}

	start := time.Now()
	got, err := newAggTest(t, timeout, fast, &aggDeafStub{name: "1337x", delay: 3 * time.Second}).
		Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("search took %v, want it to return near the %v deadline", elapsed, timeout)
	}
	if len(got.Releases) != 1 {
		t.Errorf("releases = %d, want the fast indexer's 1", len(got.Releases))
	}
	for _, o := range got.Outcomes {
		if o.Name != "1337x" {
			continue
		}
		if o.OK {
			t.Error("unreported indexer says ok, want it reported as failed")
		}
		if !strings.Contains(o.Error, "timed out") {
			t.Errorf("outcome error = %q, want it to say it timed out", o.Error)
		}
	}
}

func TestAggregatorIDsAreStableAndDistinct(t *testing.T) {
	mk := func() *Aggregator {
		return newAggTest(t, 2*time.Second, &aggStub{name: "yts", releases: []Release{
			aggRelease("yts", "A", 10, aggMagnet("a")),
			aggRelease("yts", "B", 20, aggMagnet("b")),
		}})
	}
	first, _ := mk().Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	second, _ := mk().Search(context.Background(), Query{Title: "Interstellar", Year: 2014})

	if len(first.Releases) != 2 {
		t.Fatalf("releases = %d, want 2", len(first.Releases))
	}
	for i := range first.Releases {
		if first.Releases[i].ID != second.Releases[i].ID {
			t.Errorf("id for %q changed between identical searches: %q then %q",
				first.Releases[i].Title, first.Releases[i].ID, second.Releases[i].ID)
		}
		if first.Releases[i].ID == "" {
			t.Errorf("release %q has no id", first.Releases[i].Title)
		}
	}
	if first.Releases[0].ID == first.Releases[1].ID {
		t.Error("two different releases share an id")
	}
}

// An unresolved 1337x release has no magnet, so its id has to come from the detail
// path instead — otherwise every hashless release from one indexer would collide.
func TestAggregatorIDUsesDetailPathWhenThereIsNoMagnet(t *testing.T) {
	a := Release{Title: "same", Indexer: "1337x", detailPath: "/torrent/1/a/"}
	b := Release{Title: "same", Indexer: "1337x", detailPath: "/torrent/2/b/"}
	if releaseID(a) == releaseID(b) {
		t.Error("releases with different detail paths share an id")
	}
	if releaseID(a) != releaseID(a) {
		t.Error("releaseID is not deterministic")
	}
	if got := len(releaseID(a)); got != 16 {
		t.Errorf("id length = %d, want 16 hex chars for 8 bytes", got)
	}
}

func TestAggregatorDedupesOnInfoHash(t *testing.T) {
	shared := aggMagnet("d")
	got, err := newAggTest(t, 2*time.Second,
		&aggStub{name: "yts", releases: []Release{aggRelease("yts", "Interstellar.1080p", 100, shared)}},
		&aggStub{name: "tpb", releases: []Release{aggRelease("tpb", "Interstellar.1080p", 500, shared)}},
	).Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(got.Releases) != 1 {
		t.Fatalf("releases = %d, want the two copies collapsed to 1", len(got.Releases))
	}
	r := got.Releases[0]
	if r.Seeders != 500 {
		t.Errorf("seeders = %d, want the higher count 500 kept", r.Seeders)
	}
	if len(r.Indexers) != 2 {
		t.Errorf("indexers = %v, want both recorded", r.Indexers)
	}
}

// Dedup keeps the copy with the most seeders while merging the indexer names, so
// the surviving release need not have come from the first name in the list. It has
// to be resolved through the indexer it actually came from, not through whichever
// one happened to be seen first.
func TestAggregatorDedupKeepsTheWinnersOwner(t *testing.T) {
	shared := aggMagnet("d")
	// yts is configured first and is seen first, but tpb's copy has more seeders
	// and is the one kept.
	yts := &aggStub{name: "yts", releases: []Release{aggRelease("yts", "dupe", 100, shared)}}
	tpb := &aggStub{name: "tpb", releases: []Release{aggRelease("tpb", "dupe", 500, shared)}}
	a := newAggTest(t, 2*time.Second, yts, tpb)

	got, err := a.Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got.Releases) != 1 {
		t.Fatalf("releases = %d, want 1", len(got.Releases))
	}
	kept := got.Releases[0]
	if kept.Seeders != 500 {
		t.Fatalf("kept the wrong copy: seeders = %d", kept.Seeders)
	}
	if kept.owner != "tpb" {
		t.Errorf("owner = %q, want tpb — the indexer the surviving copy came from", kept.owner)
	}

	a.mu.Lock()
	e := a.byID[kept.ID]
	a.mu.Unlock()
	if e.indexer == nil || e.indexer.Name() != "tpb" {
		t.Errorf("id registered against the wrong indexer; resolution would ask the wrong source")
	}
}

// 1337x releases have no hash until they are resolved, so they cannot be
// deduplicated against. A near-duplicate row is the honest outcome.
func TestAggregatorHashlessReleasesDoNotCollapse(t *testing.T) {
	got, err := newAggTest(t, 2*time.Second, &aggStub{name: "1337x", releases: []Release{
		{Title: "Interstellar.1080p.A", Indexer: "1337x", detailPath: "/torrent/1/a/", Seeders: 5},
		{Title: "Interstellar.1080p.B", Indexer: "1337x", detailPath: "/torrent/2/b/", Seeders: 4},
	}}).Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got.Releases) != 2 {
		t.Fatalf("releases = %d, want 2 — hashless releases must pass through untouched", len(got.Releases))
	}
}

// D11: seeders first. A 1-seeder 2160p below a 500-seeder 1080p is the point.
func TestAggregatorRanksBySeedersThenQualityThenName(t *testing.T) {
	rel := func(title, quality string, seeders int, hash string) Release {
		r := aggRelease("yts", title, seeders, aggMagnet(hash))
		r.Quality = quality
		return r
	}
	got, err := newAggTest(t, 2*time.Second, &aggStub{name: "yts", releases: []Release{
		rel("lonely 4k", "2160p", 1, "1"),
		rel("popular 1080p", "1080p", 500, "2"),
		rel("tie b", "1080p", 50, "3"),
		rel("tie a", "1080p", 50, "4"),
		rel("tie better quality", "2160p", 50, "5"),
	}}).Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	want := []string{"popular 1080p", "tie better quality", "tie a", "tie b", "lonely 4k"}
	for i, w := range want {
		if got.Releases[i].Title != w {
			t.Errorf("rank %d = %q, want %q (full order: %v)", i, got.Releases[i].Title, w, aggTitles(got.Releases))
		}
	}
}

func aggTitles(in []Found) []string {
	out := make([]string, len(in))
	for i, f := range in {
		out[i] = fmt.Sprintf("%s(%d/%s)", f.Title, f.Seeders, f.Quality)
	}
	return out
}

// Lazy resolution is the reason 1337x is affordable: a 20-result search must stay
// one protected request through minter, not 21.
func TestAggregatorResolveMagnetIsLazy(t *testing.T) {
	yts := &aggStub{name: "yts", releases: []Release{aggRelease("yts", "carries its own", 10, aggMagnet("a"))}}
	x := &aggResolvingStub{aggStub: aggStub{
		name:     "1337x",
		releases: []Release{{Title: "needs a fetch", Indexer: "1337x", detailPath: "/torrent/1/a/", Seeders: 5}},
		magnet:   aggMagnet("e"),
	}}
	a := newAggTest(t, 2*time.Second, yts, x)

	got, err := a.Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if x.resolves.Load() != 0 {
		t.Fatalf("search resolved %d magnets, want 0 — resolution must wait for a pick", x.resolves.Load())
	}

	var ytsID, xID string
	for _, r := range got.Releases {
		switch r.Indexers[0] {
		case "yts":
			ytsID = r.ID
		case "1337x":
			xID = r.ID
		}
	}

	magnet, err := a.ResolveMagnet(context.Background(), ytsID)
	if err != nil {
		t.Fatalf("ResolveMagnet(yts): %v", err)
	}
	if magnet != aggMagnet("a") {
		t.Errorf("yts magnet = %q, want the one the search already carried", magnet)
	}
	if yts.searches.Load() != 1 {
		t.Errorf("resolving a yts id made another request (searches = %d)", yts.searches.Load())
	}

	magnet, err = a.ResolveMagnet(context.Background(), xID)
	if err != nil {
		t.Fatalf("ResolveMagnet(1337x): %v", err)
	}
	if magnet != aggMagnet("e") {
		t.Errorf("1337x magnet = %q, want the resolved one", magnet)
	}
	if x.resolves.Load() != 1 {
		t.Errorf("1337x resolves = %d, want exactly 1", x.resolves.Load())
	}
}

// The composition phase 2 actually ships: 1337x wrapped in a Cache. A Cache is not
// a MagnetResolver, so an aggregator asserting on the outer value would report
// "cannot resolve" for every 1337x pick — a break that only appears once the two
// tasks are wired together, which is exactly why it is tested here.
func TestAggregatorResolvesThroughTheCache(t *testing.T) {
	x := &aggResolvingStub{aggStub: aggStub{
		name:     "1337x",
		releases: []Release{{Title: "needs a fetch", Indexer: "1337x", detailPath: "/torrent/1/a/", Seeders: 5}},
		magnet:   aggMagnet("e"),
	}}
	a := newAggTest(t, 2*time.Second, NewCache(x, time.Hour))

	got, err := a.Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got.Releases) != 1 {
		t.Fatalf("releases = %d, want 1", len(got.Releases))
	}
	if got.Outcomes[0].Name != "1337x" {
		t.Errorf("outcome name = %q, want the wrapped indexer's name, not the cache's",
			got.Outcomes[0].Name)
	}

	magnet, err := a.ResolveMagnet(context.Background(), got.Releases[0].ID)
	if err != nil {
		t.Fatalf("ResolveMagnet through a cache: %v", err)
	}
	if magnet != aggMagnet("e") {
		t.Errorf("magnet = %q, want the resolved one", magnet)
	}
	if x.resolves.Load() != 1 {
		t.Errorf("resolves = %d, want exactly 1", x.resolves.Load())
	}

	// And the cache is doing its job underneath: the second search launches nothing.
	if _, err := a.Search(context.Background(), Query{Title: "Interstellar", Year: 2014}); err != nil {
		t.Fatalf("second Search: %v", err)
	}
	if x.searches.Load() != 1 {
		t.Errorf("searches = %d, want 1 — the repeat search should have been a cache hit", x.searches.Load())
	}
}

// Release lets a caller recording a download read the indexer and release name
// off the server's own copy instead of accepting them from the client.
func TestAggregatorReleaseByID(t *testing.T) {
	a := NewAggregator([]Indexer{
		&aggStub{name: "yts", releases: []Release{aggRelease("yts", "Interstellar.1080p", 10, aggMagnet("a"))}},
	}, 2*time.Second, time.Hour)
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	got, err := a.Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	id := got.Releases[0].ID

	found, ok := a.Release(id)
	if !ok {
		t.Fatal("Release did not find an id the search had just issued")
	}
	if found.Title != "Interstellar.1080p" || found.Indexers[0] != "yts" {
		t.Errorf("release = %+v, want the searched one with its indexer", found)
	}

	if _, ok := a.Release("deadbeefdeadbeef"); ok {
		t.Error("Release found an id that was never issued")
	}

	// It expires with the search that issued it, so a stale id cannot be recorded
	// as a fresh download.
	now = now.Add(time.Hour + time.Minute)
	if _, ok := a.Release(id); ok {
		t.Error("Release returned an id whose search has expired")
	}
}

func TestAggregatorUnknownIDIsTheSentinel(t *testing.T) {
	a := newAggTest(t, 2*time.Second, &aggStub{name: "yts"})
	if _, err := a.ResolveMagnet(context.Background(), "deadbeefdeadbeef"); !errors.Is(err, ErrReleaseExpired) {
		t.Errorf("err = %v, want ErrReleaseExpired so the API can answer 410", err)
	}
}

// Expiry is proved by moving a clock. A test that sleeps an hour is not a test.
func TestAggregatorExpiredIDIsTheSentinel(t *testing.T) {
	a := NewAggregator([]Indexer{
		&aggStub{name: "yts", releases: []Release{aggRelease("yts", "A", 10, aggMagnet("a"))}},
	}, 2*time.Second, time.Hour)

	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	got, err := a.Search(context.Background(), Query{Title: "Interstellar", Year: 2014})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	id := got.Releases[0].ID

	if _, err := a.ResolveMagnet(context.Background(), id); err != nil {
		t.Fatalf("inside the TTL: %v", err)
	}

	now = now.Add(time.Hour + time.Minute)
	if _, err := a.ResolveMagnet(context.Background(), id); !errors.Is(err, ErrReleaseExpired) {
		t.Errorf("past the TTL: err = %v, want ErrReleaseExpired", err)
	}
}

// A long-lived process must not accumulate every id it has ever issued.
func TestAggregatorPrunesExpiredIDsOnSearch(t *testing.T) {
	stub := &aggStub{name: "yts", releases: []Release{aggRelease("yts", "A", 10, aggMagnet("a"))}}
	a := NewAggregator([]Indexer{stub}, 2*time.Second, time.Hour)
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	if _, err := a.Search(context.Background(), Query{Title: "Interstellar", Year: 2014}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	now = now.Add(2 * time.Hour)

	stub.releases = []Release{aggRelease("yts", "B", 10, aggMagnet("b"))}
	if _, err := a.Search(context.Background(), Query{Title: "Dune", Year: 2021}); err != nil {
		t.Fatalf("Search: %v", err)
	}

	a.mu.Lock()
	n := len(a.byID)
	a.mu.Unlock()
	if n != 1 {
		t.Errorf("ids held = %d, want 1 — the expired entry should have been pruned", n)
	}
}

func TestAggregatorConcurrentSearchesAreSafe(t *testing.T) {
	a := newAggTest(t, 2*time.Second,
		&aggStub{name: "yts", releases: []Release{aggRelease("yts", "A", 10, aggMagnet("a"))}},
		&aggStub{name: "tpb", releases: []Release{aggRelease("tpb", "B", 20, aggMagnet("b"))}},
	)
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			if _, err := a.Search(context.Background(), Query{Title: "Interstellar", Year: 2014}); err != nil {
				t.Errorf("Search: %v", err)
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
