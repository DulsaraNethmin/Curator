# T12 — Search API

**Owns:** `internal/api/search.go`, `search_test.go`; the phase 2 additions to `internal/config/`;
the wiring in `cmd/curator/`
**Depends on:** T11

## Goal

Expose search over HTTP and wire the indexers into the running binary. The task that makes phase 2
real.

## Do

1. Endpoints, per [`../phase-2.md`](../phase-2.md#api-surface):
   - `GET /api/search?title=&year=&quality=` — ranked releases plus the per-indexer outcomes.
     `title` is required; `year` and `quality` are optional.
   - `GET /api/releases/{id}/magnet` — `{"magnet": "...", "info_hash": "..."}`.
2. A `Searcher` interface **declared in `internal/api`**, satisfied by T11's aggregator, so handlers
   are tested with fakes. Same pattern as phase 1's `Store`, `Scanner` and `Matcher`.
3. Status codes that mean something:
   - missing or blank `title` → `400`
   - a `year` that is not a number → `400`
   - an expired or unknown release id → `410 Gone`, carrying the sentinel error from T11 and telling
     the caller to search again
   - every indexer failing → still `200`, with zero releases and three reported failures. **The
     request succeeded; the sources failed, and the response says which.** A 500 here would be a lie
     about whose fault it is.
   - errors keep phase 1's `{"error": "..."}` shape
4. Config additions, defaults from [`../phase-2.md`](../phase-2.md#configuration): `MINTER_URL`
   (default `http://127.0.0.1:8191` — minter binds IPv4 only, so `localhost` resolves to `::1` and
   fails), `SEARCH_TIMEOUT` (`30s`), `SEARCH_CACHE_TTL` (`1h`). Invalid values are errors at startup,
   not panics — extend the existing tests.
5. Wire it in `cmd/curator`: build YTS, TPB and 1337x, wrap **1337x only** in T10's cache, hand them
   to T11's aggregator, register the routes. One `*http.Client` with a timeout, shared.
6. Search stays synchronous, like the phase 1 scan. It is one round of concurrent requests with a
   deadline, and the caller wants the results.

## Do not

- Add authentication. LAN-only, same posture as the stack this replaces.
- Touch `internal/store`, `internal/library` or `internal/tmdb`. Nothing about search writes to the
  database — releases are not rows until a download starts in phase 3.
- Persist search results. The cache is T10's and it is in memory.
- Serve the UI. Phase 5.
- Return a 200 carrying a failure, or a 500 carrying an indexer's bad day.

## Verify

`go test ./internal/api` with a fake `Searcher`:

- a normal search returns ranked releases and the per-indexer block
- a blank or missing `title` returns 400 with a JSON body
- a non-numeric `year` returns 400
- every indexer failing returns **200**, zero releases, and the failures listed
- an unresolved release has `"magnet": null`, never an empty string
- resolving a known id returns the magnet and its info hash
- an expired id returns 410 with a JSON body
- phase 1's endpoints still pass — `/healthz`, `/api/scan`, `/api/movies`, `/api/movies/{id}`

Then end to end, with minter running (`127.0.0.1:8191`, not `localhost`), the block in
[`../phase-2.md`](../phase-2.md#verification): every indexer reporting, ranking by seeders, the
second search inside the hour launching no browser, lazy magnet resolution, and `docker stop minter`
degrading search to the other two rather than erroring.
