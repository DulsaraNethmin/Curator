package indexer

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Cache is an Indexer that remembers another Indexer's searches for a TTL.
//
// It exists for the one phase 2 requirement that is about cost rather than
// correctness: a repeat search must launch no browser. Only 1337x is worth
// wrapping — every one of its pages costs ~9 s through minter, while YTS and TPB
// answer in under a second and would only be made stale by caching. Which indexers
// get wrapped is the aggregator's decision; this type just makes wrapping possible.
//
// It is a decorator — it wraps an Indexer and is one — so composing it around 1337x
// changes nothing else's shape. It lives in package indexer, rather than somewhere
// tidier, because entries hold Release values complete with their unexported
// detailPath: that field is what lets a magnet be resolved later without searching
// again, and no cache outside this package could carry it across.
//
// In memory only, deliberately. A restart losing the cache is correct: entries hold
// detail paths for a pick that is seconds away, and serving a magnet resolved from a
// page fetched days ago is worse than fetching the page again.
type Cache struct {
	inner Indexer
	ttl   time.Duration

	// now is the clock. Injected so that expiry can be tested by moving time
	// rather than by sleeping for an hour, which is not a test. Defaults to
	// time.Now.
	now func() time.Time

	// mu guards entries. A plain Mutex, not an RWMutex: the read path writes too
	// (an expired entry is deleted where it is found), and a single-user service
	// runs a handful of searches an hour, so there is no read contention to
	// optimise away.
	mu      sync.Mutex
	entries map[cacheKey]cacheEntry
}

// cacheKey identifies one search. The wrapped indexer's name is part of it so two
// indexers' answers to the same question can never be taken for each other, even
// though one Cache holds exactly one indexer's entries today.
//
// The media type and the season are part of it for a sharper reason than
// tidiness, and 1337x is the indexer this Cache actually wraps. Without the media
// type, a film search for "Severance" would serve the FILM's releases to a
// television search under ok:true — and 1337x's documented failure mode is
// already ok:true, count:0 rather than an error, so nothing on the screen would
// look wrong. Without the season, "severance" and "severance s02" are one entry,
// and they are two different queries sent to 1337x.
type cacheKey struct {
	indexer string
	title   string
	year    int
	media   string
	season  int
}

// cacheEntry is one remembered search: the releases exactly as the wrapped indexer
// parsed them, and the instant they stop being usable.
type cacheEntry struct {
	releases []Release
	expires  time.Time
}

// NewCache wraps inner, remembering its searches for ttl.
//
// The TTL is an argument rather than a package constant because it is a deployment
// setting, not a property of caching. A ttl of zero or less disables caching
// entirely — every search calls through — so that turning the cache off is a
// configured value rather than a different wiring path.
func NewCache(inner Indexer, ttl time.Duration) *Cache {
	return &Cache{
		inner:   inner,
		ttl:     ttl,
		now:     time.Now,
		entries: make(map[cacheKey]cacheEntry),
	}
}

// Name implements Indexer by reporting the wrapped indexer's name.
//
// A cache is not a source. Releases must still be attributed to 1337x, and the
// per-indexer block of a search response would otherwise report an indexer called
// "cache" that nobody can search.
func (c *Cache) Name() string { return c.inner.Name() }

// Unwrap returns the wrapped indexer.
//
// Cache deliberately does not implement MagnetResolver. A Go type either has the
// method or it does not, so forwarding ResolveMagnet would make every Cache assert
// as a MagnetResolver — including one wrapping an indexer whose releases already
// carry magnets and which resolves nothing. A caller holding a Cache as an Indexer
// reaches the real resolver through here, which is how lazy 1337x magnet resolution
// survives the cache being composed around it.
func (c *Cache) Unwrap() Indexer { return c.inner }

// Search implements Indexer. A miss is indistinguishable from having no cache
// at all: call through, store, return.
func (c *Cache) Search(ctx context.Context, q Query) ([]Release, error) {
	key := cacheKeyFor(c.inner.Name(), q)
	if releases, ok := c.lookup(key); ok {
		return releases, nil
	}

	// The call through happens with the lock released. Holding it across a ~9 s
	// minter fetch would queue every other search behind this one, hits for
	// unrelated titles included. The price is that two identical searches racing on
	// a cold entry both call through: one duplicated fetch, weighed against a lock
	// held for seconds, in a service with one user.
	//
	// The caller's query goes through untouched — normalisation is for keying only,
	// and the indexer builds its query from what was actually asked for.
	releases, err := c.inner.Search(ctx, q)
	if err != nil {
		// A failure is never stored. minter being down for a minute must not become
		// an hour of "no results" for a title that has plenty.
		return nil, err
	}
	c.store(key, releases)
	return releases, nil
}

// lookup returns a live entry's releases, expiring a stale one where it finds it.
func (c *Cache) lookup(key cacheKey) ([]Release, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	// An entry is live while now is strictly before its expiry, so a search at
	// exactly TTL is a miss.
	if !c.now().Before(e.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return cacheCopyReleases(e.releases), true
}

// store records a successful search and prunes what has expired.
func (c *Cache) store(key cacheKey, releases []Release) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	c.pruneLocked(now)
	c.entries[key] = cacheEntry{
		releases: cacheCopyReleases(releases),
		expires:  now.Add(c.ttl),
	}
}

// pruneLocked drops every expired entry.
//
// It runs on write, and expiry also happens lazily on read, because the alternative
// is a janitor goroutine — and a goroutine that outlives the Cache that started it
// is a leak phase 6 would have to go looking for. A full sweep of a map holding a
// handful of searches an hour costs nothing worth optimising.
func (c *Cache) pruneLocked(now time.Time) {
	for k, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, k)
		}
	}
}

// cacheKeyFor builds the key for one search.
//
// The media type is taken through Query.mediaType, so a Query that leaves it
// blank and one that spells out MediaMovie are one entry rather than two names
// for one identical fetch.
func cacheKeyFor(indexerName string, q Query) cacheKey {
	return cacheKey{
		indexer: indexerName,
		title:   cacheNormaliseTitle(q.Title),
		year:    q.Year,
		media:   q.mediaType(),
		season:  q.Season,
	}
}

// cacheNormaliseTitle folds a title to its key form: trimmed and lowercased, and
// nothing else, so "Interstellar " and "interstellar" are one entry.
//
// Deliberately no harder than that. Collapsing punctuation would merge searches that
// are genuinely different — and given this library, it would fold "Spider-Man" into
// "Spider Man" and "Avengers: Infinity War" into "Avengers Infinity War", which are
// distinct keyword queries at every indexer we search.
func cacheNormaliseTitle(title string) string {
	return strings.ToLower(strings.TrimSpace(title))
}

// cacheCopyReleases copies a release slice so the cache and its callers never share
// backing storage.
//
// Release holds only value fields — strings and ints, detailPath included — so a
// shallow copy is a complete one. This is not paranoia: callers rank, dedup and fill
// in a resolved Magnet on what they get back, and sorting a returned slice in place
// alone would silently reorder a cached entry for everyone after. Copying on store
// and on hit costs one copy per call either way.
func cacheCopyReleases(in []Release) []Release {
	if in == nil {
		return nil
	}
	out := make([]Release, len(in))
	copy(out, in)
	return out
}

// Compile-time proof that a Cache is usable anywhere its wrapped indexer was.
var _ Indexer = (*Cache)(nil)
