package indexer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// cacheStubIndexer stands in for a real indexer so a test can count call-throughs.
// That count is the entire point of the task: a repeat search inside the TTL must
// add nothing to it.
type cacheStubIndexer struct {
	name     string
	releases []Release

	mu       sync.Mutex
	err      error
	calls    int
	searched []cacheSearchArgs
}

// cacheSearchArgs is what the wrapped indexer was actually asked, recorded so a test
// can prove the caller's title reaches it unnormalised.
type cacheSearchArgs struct {
	title string
	year  int
}

func (s *cacheStubIndexer) Name() string { return s.name }

func (s *cacheStubIndexer) SearchMovie(_ context.Context, title string, year int) ([]Release, error) {
	s.mu.Lock()
	s.calls++
	s.searched = append(s.searched, cacheSearchArgs{title: title, year: year})
	err := s.err
	s.mu.Unlock()

	if err != nil {
		return nil, err
	}
	// A fresh slice per call, the way a real indexer parsing a page returns one.
	return cacheCopyReleases(s.releases), nil
}

func (s *cacheStubIndexer) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *cacheStubIndexer) searches() []cacheSearchArgs {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]cacheSearchArgs(nil), s.searched...)
}

func (s *cacheStubIndexer) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// cacheTestClock is the injected clock. Expiry is proved by moving it; a test that
// sleeps an hour is not a test. Guarded because the concurrency test reads it from
// several goroutines at once.
type cacheTestClock struct {
	mu sync.Mutex
	t  time.Time
}

func newCacheTestClock() *cacheTestClock {
	// A fixed instant, so no test depends on when it ran.
	return &cacheTestClock{t: time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)}
}

func (c *cacheTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *cacheTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// cacheTestReleases is what a stub 1337x search returns: no magnets, detail paths
// present, exactly as the real one produces.
func cacheTestReleases() []Release {
	return []Release{
		{
			Title:      "Interstellar (2014) 1080p BrRip x264 - YIFY",
			Year:       2014,
			Quality:    "1080p",
			SizeBytes:  2469606195,
			Seeders:    1386,
			Indexer:    "1337x",
			detailPath: "/torrent/1099151/Interstellar-2014-1080p-BrRip-x264-YIFY/",
		},
		{
			Title:      "Interstellar 2014 UHD BluRay 2160p DTS-HD MA 5 1 HEVC REMUX-FraMeSToR",
			Year:       2014,
			Quality:    "2160p",
			SizeBytes:  70544837836,
			Seeders:    81,
			Indexer:    "1337x",
			detailPath: "/torrent/6436239/Interstellar-2014-UHD-BluRay-2160p-DTS-HD-MA-5-1-HEVC-REMUX-FraMeSToR/",
		},
	}
}

// newCacheForTest wires a Cache around a stub with a clock a test can move.
func newCacheForTest(ttl time.Duration) (*Cache, *cacheStubIndexer, *cacheTestClock) {
	stub := &cacheStubIndexer{name: "1337x", releases: cacheTestReleases()}
	clock := newCacheTestClock()
	c := NewCache(stub, ttl)
	c.now = clock.Now
	return c, stub, clock
}

// cacheEntryCount reports how many entries are held, for the pruning test.
func cacheEntryCount(c *Cache) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func TestCacheMissCallsThroughOnce(t *testing.T) {
	c, stub, _ := newCacheForTest(time.Hour)

	got, err := c.SearchMovie(context.Background(), "Interstellar", 2014)
	if err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	if stub.callCount() != 1 {
		t.Errorf("wrapped indexer called %d times on a miss, want 1", stub.callCount())
	}
	want := cacheTestReleases()
	if len(got) != len(want) {
		t.Fatalf("got %d releases, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("release %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestCacheHitInsideTTLCallsThroughZeroMoreTimes is the requirement: a second search
// inside the hour launches no browser.
func TestCacheHitInsideTTLCallsThroughZeroMoreTimes(t *testing.T) {
	c, stub, clock := newCacheForTest(time.Hour)

	first, err := c.SearchMovie(context.Background(), "Interstellar", 2014)
	if err != nil {
		t.Fatalf("first SearchMovie: %v", err)
	}
	clock.Advance(59 * time.Minute)

	second, err := c.SearchMovie(context.Background(), "Interstellar", 2014)
	if err != nil {
		t.Fatalf("second SearchMovie: %v", err)
	}
	if stub.callCount() != 1 {
		t.Fatalf("wrapped indexer called %d times for two identical searches, want 1 — the second launched a browser", stub.callCount())
	}
	if len(second) != len(first) {
		t.Fatalf("hit returned %d releases, miss returned %d", len(second), len(first))
	}
	for i := range first {
		if second[i] != first[i] {
			t.Errorf("release %d differs between miss and hit:\n hit  %+v\n miss %+v", i, second[i], first[i])
		}
	}
}

// TestCacheHitPreservesDetailPath covers the reason this cache lives in package
// indexer at all: without the unexported detail path, a cached search cannot resolve
// a magnet and the hit would be worse than useless.
func TestCacheHitPreservesDetailPath(t *testing.T) {
	c, _, _ := newCacheForTest(time.Hour)

	if _, err := c.SearchMovie(context.Background(), "Interstellar", 2014); err != nil {
		t.Fatalf("first SearchMovie: %v", err)
	}
	got, err := c.SearchMovie(context.Background(), "Interstellar", 2014)
	if err != nil {
		t.Fatalf("second SearchMovie: %v", err)
	}
	for i, r := range got {
		want := cacheTestReleases()[i].detailPath
		if r.detailPath != want {
			t.Errorf("release %d detail path = %q, want %q — lazy resolution cannot work from this entry", i, r.detailPath, want)
		}
		if r.Magnet != "" {
			t.Errorf("release %d came back with a magnet %q; the cache must not invent one", i, r.Magnet)
		}
	}
}

func TestCacheExpiresPastTTL(t *testing.T) {
	c, stub, clock := newCacheForTest(time.Hour)

	if _, err := c.SearchMovie(context.Background(), "Interstellar", 2014); err != nil {
		t.Fatalf("first SearchMovie: %v", err)
	}
	clock.Advance(time.Hour + time.Second)

	if _, err := c.SearchMovie(context.Background(), "Interstellar", 2014); err != nil {
		t.Fatalf("second SearchMovie: %v", err)
	}
	if stub.callCount() != 2 {
		t.Errorf("wrapped indexer called %d times across an expiry, want 2", stub.callCount())
	}
	// Expiring on read must also drop the dead entry rather than leave it behind.
	if n := cacheEntryCount(c); n != 1 {
		t.Errorf("holding %d entries after one expiry and one re-search, want 1", n)
	}
}

func TestCacheKeying(t *testing.T) {
	for _, tt := range []struct {
		label     string
		title     string
		year      int
		wantCalls int // total call-throughs after the second search
	}{
		{label: "identical", title: "Interstellar", year: 2014, wantCalls: 1},
		{label: "trailing whitespace is the same entry", title: "Interstellar ", year: 2014, wantCalls: 1},
		{label: "differing case is the same entry", title: "iNTERSTELLAR", year: 2014, wantCalls: 1},
		{label: "case and whitespace together", title: "  interstellar  ", year: 2014, wantCalls: 1},
		{label: "differing title is a different entry", title: "Inception", year: 2014, wantCalls: 2},
		{label: "differing year is a different entry", title: "Interstellar", year: 2015, wantCalls: 2},
		{label: "no year is a different entry", title: "Interstellar", year: 0, wantCalls: 2},
		// Punctuation is not collapsed: these are genuinely different keyword
		// searches, and this library is full of titles where it matters.
		{label: "punctuation is not collapsed", title: "Spider Man", year: 2014, wantCalls: 2},
	} {
		t.Run(tt.label, func(t *testing.T) {
			c, stub, _ := newCacheForTest(time.Hour)
			if _, err := c.SearchMovie(context.Background(), "Interstellar", 2014); err != nil {
				t.Fatalf("first SearchMovie: %v", err)
			}
			if _, err := c.SearchMovie(context.Background(), tt.title, tt.year); err != nil {
				t.Fatalf("second SearchMovie: %v", err)
			}
			if got := stub.callCount(); got != tt.wantCalls {
				t.Errorf("wrapped indexer called %d times, want %d", got, tt.wantCalls)
			}
		})
	}
}

// TestCacheKeyIncludesIndexerName pins the third component of the key. Two Caches
// never share a map today, so nothing else can observe it.
func TestCacheKeyIncludesIndexerName(t *testing.T) {
	a := cacheKeyFor("1337x", "Interstellar", 2014)
	b := cacheKeyFor("yts", "Interstellar", 2014)
	if a == b {
		t.Error("two indexers' answers to the same question share a key")
	}
	if a != cacheKeyFor("1337x", "  INTERSTELLAR ", 2014) {
		t.Error("normalisation is not applied consistently to the key")
	}
}

// TestCacheCallsThroughWithTheCallersTitle: normalisation is for keying only. The
// indexer builds its query from the title the caller asked for, so lowercasing it on
// the way through would change what is searched for.
func TestCacheCallsThroughWithTheCallersTitle(t *testing.T) {
	c, stub, _ := newCacheForTest(time.Hour)

	if _, err := c.SearchMovie(context.Background(), "  Avengers - Infinity War  ", 2018); err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	got := stub.searches()
	if len(got) != 1 {
		t.Fatalf("wrapped indexer saw %d searches, want 1", len(got))
	}
	if got[0].title != "  Avengers - Infinity War  " || got[0].year != 2018 {
		t.Errorf("wrapped indexer searched %q/%d, want the caller's own %q/%d",
			got[0].title, got[0].year, "  Avengers - Infinity War  ", 2018)
	}
}

func TestCacheDoesNotCacheErrors(t *testing.T) {
	c, stub, _ := newCacheForTest(time.Hour)
	down := errors.New("calling minter: connection refused")
	stub.setErr(down)

	if _, err := c.SearchMovie(context.Background(), "Interstellar", 2014); !errors.Is(err, down) {
		t.Fatalf("error = %v, want it returned unchanged", err)
	}
	if n := cacheEntryCount(c); n != 0 {
		t.Errorf("holding %d entries after a failure, want 0", n)
	}

	// The next call must try again rather than replay the failure for an hour.
	if _, err := c.SearchMovie(context.Background(), "Interstellar", 2014); !errors.Is(err, down) {
		t.Fatalf("second error = %v, want the same failure from a fresh call", err)
	}
	if stub.callCount() != 2 {
		t.Fatalf("wrapped indexer called %d times, want 2 — a failure was cached", stub.callCount())
	}

	// And once minter is back, the recovery is what gets cached.
	stub.setErr(nil)
	got, err := c.SearchMovie(context.Background(), "Interstellar", 2014)
	if err != nil {
		t.Fatalf("SearchMovie after recovery: %v", err)
	}
	if len(got) != len(cacheTestReleases()) {
		t.Fatalf("got %d releases after recovery, want %d", len(got), len(cacheTestReleases()))
	}
	if _, err := c.SearchMovie(context.Background(), "Interstellar", 2014); err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	if stub.callCount() != 3 {
		t.Errorf("wrapped indexer called %d times, want 3 — the recovered result was not cached", stub.callCount())
	}
}

// TestCacheReturnedSliceCannotCorruptEntry covers the aliasing decision: callers get
// a copy, in both directions. The aggregator ranks and dedups what it is handed and
// fills in a magnet once one is resolved; sharing backing storage would let one
// caller rewrite every later hit.
func TestCacheReturnedSliceCannotCorruptEntry(t *testing.T) {
	c, _, _ := newCacheForTest(time.Hour)

	miss, err := c.SearchMovie(context.Background(), "Interstellar", 2014)
	if err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	// Mutate what the miss returned — the slice the wrapped indexer produced.
	miss[0].Title = "corrupted by the caller"
	miss[0].Magnet = "magnet:?xt=urn:btih:0000000000000000000000000000000000000000"
	miss[0].detailPath = "/torrent/0/wrong/"
	miss[0], miss[1] = miss[1], miss[0] // an in-place sort would do exactly this

	hit, err := c.SearchMovie(context.Background(), "Interstellar", 2014)
	if err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	want := cacheTestReleases()
	for i := range want {
		if hit[i] != want[i] {
			t.Fatalf("entry corrupted by a caller mutating the miss result: release %d = %+v, want %+v", i, hit[i], want[i])
		}
	}

	// Same again from a hit's slice.
	hit[0].Title = "corrupted by the second caller"
	second, err := c.SearchMovie(context.Background(), "Interstellar", 2014)
	if err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	if second[0] != want[0] {
		t.Errorf("entry corrupted by a caller mutating a hit result: release 0 = %+v, want %+v", second[0], want[0])
	}
}

// TestCachePrunesExpiredEntriesOnWrite: a long-lived process must not accumulate
// every search ever made. Pruning happens on write, with no janitor goroutine.
func TestCachePrunesExpiredEntriesOnWrite(t *testing.T) {
	c, _, clock := newCacheForTest(time.Hour)

	for _, title := range []string{"Interstellar", "Inception", "Dune"} {
		if _, err := c.SearchMovie(context.Background(), title, 2014); err != nil {
			t.Fatalf("SearchMovie(%q): %v", title, err)
		}
	}
	if n := cacheEntryCount(c); n != 3 {
		t.Fatalf("holding %d entries after three searches, want 3", n)
	}

	clock.Advance(2 * time.Hour)
	if n := cacheEntryCount(c); n != 3 {
		t.Fatalf("holding %d entries before any write, want 3 — expiry is lazy, not a timer", n)
	}

	// One unrelated search, and every dead entry goes with it.
	if _, err := c.SearchMovie(context.Background(), "Arrival", 2016); err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	if n := cacheEntryCount(c); n != 1 {
		t.Errorf("holding %d entries after a write past the TTL, want 1 — the map grows without bound", n)
	}
	if _, ok := c.entries[cacheKeyFor("1337x", "Arrival", 2016)]; !ok {
		t.Error("pruning removed the entry that was just written")
	}
}

func TestCacheZeroTTLDisablesCaching(t *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Hour} {
		c, stub, _ := newCacheForTest(ttl)
		for i := 0; i < 3; i++ {
			if _, err := c.SearchMovie(context.Background(), "Interstellar", 2014); err != nil {
				t.Fatalf("ttl %v: SearchMovie: %v", ttl, err)
			}
		}
		if stub.callCount() != 3 {
			t.Errorf("ttl %v: wrapped indexer called %d times, want 3 — caching is meant to be off", ttl, stub.callCount())
		}
		if n := cacheEntryCount(c); n != 0 {
			t.Errorf("ttl %v: holding %d entries, want 0", ttl, n)
		}
	}
}

// TestCacheConcurrentSearchesAreSafe fans out the way the aggregator does, over a mix
// of hits, misses and an expiry running underneath. It exists for -race.
func TestCacheConcurrentSearchesAreSafe(t *testing.T) {
	c, stub, clock := newCacheForTest(time.Hour)
	titles := []string{"Interstellar", "interstellar ", "Inception", "Dune", "Arrival"}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				title := titles[(i+j)%len(titles)]
				got, err := c.SearchMovie(context.Background(), title, 2014)
				if err != nil {
					t.Errorf("SearchMovie(%q): %v", title, err)
					return
				}
				if len(got) != len(cacheTestReleases()) {
					t.Errorf("SearchMovie(%q) returned %d releases, want %d", title, len(got), len(cacheTestReleases()))
					return
				}
				// Writing into a result must stay a caller's own business even when
				// several callers hold results at once.
				got[0].Title = "mutated by a caller"
			}
		}(i)
	}
	// Time moving under the fan-out exercises lazy expiry and pruning in the race
	// detector too.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			clock.Advance(20 * time.Minute)
		}
	}()
	wg.Wait()

	if stub.callCount() < 4 {
		t.Errorf("wrapped indexer called %d times for four distinct titles, want at least 4", stub.callCount())
	}
}

func TestCacheNameIsTheWrappedIndexers(t *testing.T) {
	c, stub, _ := newCacheForTest(time.Hour)
	if c.Name() != "1337x" {
		t.Errorf("Name = %q, want the wrapped %q — a cache is not a source", c.Name(), "1337x")
	}
	if c.Unwrap() != Indexer(stub) {
		t.Error("Unwrap must return the wrapped indexer, or lazy magnet resolution cannot reach it")
	}
}

func TestCacheDefaultsToWallClock(t *testing.T) {
	c := NewCache(&cacheStubIndexer{name: "1337x"}, time.Hour)
	if c.now == nil {
		t.Fatal("clock is nil: NewCache must default it to time.Now")
	}
	if d := time.Since(c.now()); d < 0 || d > time.Minute {
		t.Errorf("default clock is %v away from now, want time.Now", d)
	}
}
