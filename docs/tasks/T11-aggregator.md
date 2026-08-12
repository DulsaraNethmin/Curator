# T11 — Aggregator and ranking

**Owns:** `internal/indexer/aggregate.go`, `aggregate_test.go`
**Depends on:** T8 (YTS), T9 (TPB), T10 (cache) — and the 1337x client already in the package

## Goal

Search every indexer at once, merge what comes back, rank it, and hand out stable ids so a magnet can
be resolved later. This is the task that turns three indexers into one search.

## Do

1. An `Aggregator` holding a list of `Indexer`s and a whole-search deadline, both given to the
   constructor. It does no HTTP itself.
2. **Fan out concurrently with `errgroup` — and never let one goroutine's error cancel the others.**
   `errgroup.WithContext` cancels every sibling the moment one returns non-nil, which is precisely
   backwards here: a failing indexer must be omitted, never fatal. Each goroutine records its own
   result-or-error and returns `nil`. Getting this wrong means a downed minter silently empties an
   otherwise good search, so make it a test, not a comment.
3. Report **per-indexer outcomes** alongside the merged releases: name, ok, count, and the error text
   when it failed. A search where 1337x is down is still a success and still says so. "Omitted, never
   fatal" must not decay into "invisible" — that is minter's 200-carrying-a-failure bug in another
   costume.
4. Honour the deadline. A straggler past it is omitted and reported as failed, not waited for. The
   whole search must not outlive the deadline by more than it takes to collect.
5. **Stamp a stable, opaque id on every release**, per
   [`../phase-2.md`](../phase-2.md#release-identity-and-why-the-api-never-returns-a-url) and
   [D10](../decisions.md#d10--releases-are-identified-by-an-opaque-id-not-a-url):
   `sha256(indexer + "\x00" + title + "\x00" + detailPath|magnet)`, first 8 bytes, hex. Deterministic
   across searches, and it leaks nothing about the source.
6. **Dedup on info hash**, keeping the copy with the most seeders and recording every indexer that
   carried it. Releases without a hash — every unresolved 1337x row — cannot be deduplicated and must
   pass through untouched. A near-duplicate is the honest outcome; guessing by name similarity is not.
7. **Rank by seeders descending**, ties broken by quality rank (the ported `qualityRank`) then name,
   so the order is total and a test can assert it exactly. Not quality first: a 1-seeder 2160p above a
   500-seeder 1080p is the wrong answer to the only question a manual picker is asking. Quality is a
   filter, via the already-ported `FilterQuality`.
8. `ResolveMagnet(ctx, id)` returning the magnet for a stamped id. YTS and TPB releases already carry
   theirs and cost nothing; a 1337x release is fetched from its detail page **at this point and not
   before** — that is the lazy resolution T6 preserved, and a 20-result search must still be one
   protected request rather than 21.
9. An id whose search has expired out of the cache returns a distinguishable sentinel error, so T12
   can answer `410 Gone` rather than a vague 500. Do not silently re-search: the caller asked to
   resolve a specific release, and quietly resolving a different one is worse than saying no.

## Do not

- Import `internal/api`, `internal/store` or `internal/config`. This package searches; wiring is T12.
- Do HTTP here. The indexers already do it.
- Cache anything yourself — compose T10's `Cache` around 1337x and leave YTS and TPB uncached.
- Edit `indexer.go`, `x1337.go`, `minter.go`, `quality.go`, `yts.go`, `tpb.go` or `cache.go`.
- Score releases, monitor anything, or grab automatically ([D5](../decisions.md#d5--manual-search-not-automatic-grabbing)).

## Verify

`go test -race ./internal/indexer` with stub indexers — no network:

- three stubs return releases and all three appear, merged
- **one stub returning an error does not cancel the others** — the other two still land, the failure
  is reported, and the search is a success
- all three failing yields zero releases and three reported failures, still not a fatal error
- a stub that sleeps past the deadline is omitted and reported, and the call returns near the
  deadline rather than waiting it out
- ids are stable across two identical searches, and differ between two different releases
- two indexers carrying the same info hash collapse to one release listing both, keeping the higher
  seeder count; two hashless releases do not collapse
- ranking is asserted exactly, including a low-seeder 2160p sorting below a high-seeder 1080p
- `ResolveMagnet` on a YTS or TPB id makes **no** further request; on a 1337x id it makes exactly one
- an unknown or expired id returns the sentinel error
