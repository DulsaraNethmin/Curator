# T10 — Search cache

**Owns:** `internal/indexer/cache.go`, `cache_test.go`
**Depends on:** nothing — it wraps the `Indexer` interface that already exists

## Goal

Make a repeat search free. Specifically: **a second search inside the hour must launch no browser**,
which is the one phase 2 requirement that is about cost rather than correctness.

## Do

1. A `Cache` that **wraps an `Indexer` and is itself an `Indexer`** — a decorator, so the aggregator
   composes it around 1337x and nothing else changes shape.
2. Key on `(wrapped indexer's Name(), normalised title, year)`. Normalise by trimming and folding
   case, so `interstellar` and `Interstellar ` are one entry. Do not normalise harder than that —
   collapsing punctuation would merge searches that are genuinely different.
3. Cache the parsed `[]Release` **including their unexported detail paths**, which is why this lives
   in `package indexer` and not somewhere tidier. That slice is what makes a later magnet resolution
   possible without a second search.
4. Store entries with an expiry and take the **TTL as a constructor argument**, not a package
   constant. `internal/config` is T12's to touch.
5. **Inject the clock.** A `func() time.Time` field, defaulting to `time.Now`. Tests must prove
   expiry by moving a fake clock, never by sleeping — a test that sleeps an hour is not a test.
6. Safe under concurrent use: several searches can be in flight at once, and the aggregator fans out
   by design. Guard it, and run the tests with `-race`.
7. Expire lazily on read, and prune on write so a long-lived process does not accumulate every search
   ever made. No background goroutine and no janitor ticker — this is a single-user service with a
   handful of searches an hour, and a goroutine that outlives its owner is a leak waiting to be
   found in phase 6.
8. A miss must be indistinguishable from no cache at all: call through, store, return. An error from
   the wrapped indexer is **not** cached — a minter that was briefly down must not poison the entry
   for an hour.

## Do not

- Persist to SQLite or the `settings` table. In memory only. A restart losing the cache is correct:
  the entries hold detail paths for a pick that is seconds away, and serving a magnet resolved from a
  page fetched days ago is worse than fetching again.
- Wrap YTS or TPB. They cost under a second; caching them buys nothing and makes results stale for an
  hour. The aggregator decides what to wrap — this task just makes wrapping possible.
- Edit `indexer.go`, `x1337.go`, `minter.go` or `quality.go`.
- Add a caching library.

## Verify

`go test -race ./internal/indexer`, all with a fake clock:

- a miss calls through exactly once and returns the wrapped result
- a second identical search inside the TTL calls through **zero** more times — assert the call count
  on a stub indexer, since this is the whole point of the task
- a search past the TTL calls through again
- differing title, differing year and differing case-or-whitespace behave as: different, different,
  and the same entry
- an error from the wrapped indexer is returned and **not** cached — the next call tries again
- concurrent identical searches are safe under `-race`
- pruning removes expired entries rather than growing without bound
