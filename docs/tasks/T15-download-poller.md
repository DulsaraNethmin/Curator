# T15 — Dispatch and poller

**Owns:** `internal/download/` — `service.go`, `service_test.go`, `poller.go`, `poller_test.go`
**Depends on:** T13 (qbit client), T14 (store)

## Goal

The part that decides things: dispatch a picked release into qBittorrent in an order that cannot lie,
and reconcile qBittorrent back into the database on an interval.

## Do

1. A `Service` holding interfaces **declared here** — a qBittorrent client, a store, and a magnet
   resolver satisfied by phase 2's aggregator — so this is tested with fakes and no network. Same
   pattern as `internal/api`'s `Store` and `Searcher`.
2. `Dispatch(ctx, req)` where `req` carries the release id, title, year and optional TMDB id.
   **The order is load-bearing, and it is not the obvious one:**
   1. resolve the magnet — an expired search fails here, having written nothing
   2. add to qBittorrent
   3. **confirm by looking the torrent up by hash**, because `torrents/add` returns `200 Ok.` for a
      magnet it ignored just as readily as for one it accepted, and returns no hash
   4. `UpsertWantedMovie`, then `InsertDownload`
   Writing the row before the client confirms would mean a database claiming downloads that no
   torrent client has heard of, every single time qBittorrent is down.
3. The hash comes from `indexer.InfoHash(magnet)`, which is already ported and tested. An empty hash
   means the magnet is malformed and the dispatch fails **before** anything is added.
4. Re-dispatching a release that is already downloading returns the existing row and adds nothing.
   Both the client's add and the store's insert are idempotent by design; this is the behaviour those
   two choices exist to produce.
5. `Poller` with an interval and an injected clock-free loop: `Start(ctx)` runs until the context is
   cancelled and `Stop()`/context cancellation ends it. **It must not outlive its owner** — the leak
   [T10](T10-search-cache.md) refused to create for the cache is the same leak here.
6. One tick is one request: `Torrents(ctx, category)`, then update each matching row's state and
   progress. Set `completed_at` on the transition into `completed`, once, and never clear it.
7. **A torrent in our category with no row in the database is reported, never adopted.** It is either
   a leftover from a wiped database or somebody else using the category, and inventing a `movies` row
   to hang it off would fabricate a film nobody asked for. Log it and move on.
8. **`imported` is phase 4's state and this must never set it.** A completed torrent is a file on
   disk, not a movie in the library; overwriting that distinction leaves phase 4 nothing to observe.
9. A tick that fails is logged and the next one still runs. qBittorrent restarting must not kill the
   poller — that is the whole reason it is a loop rather than a one-shot.

## Do not

- Serve HTTP or read the environment. T16 owns both.
- Import `internal/api`. It imports you.
- Delete, pause or resume torrents, or write files. Phase 3 adds and observes.
- Retry a failed download automatically, or pick a different release ([D5](../decisions.md#d5--manual-search-not-automatic-grabbing)).
- Poll in a tight loop, or with a ticker that keeps firing while a slow tick is still running.

## Verify

`go test -race ./internal/download` with fakes — no qBittorrent, no database, no network:

- a happy dispatch calls resolve, add, confirm, upsert and insert **in that order**, asserted
- **qBittorrent unreachable on add → no store write at all.** Assert the fake store recorded nothing;
  this is the test the task exists for
- an add that "succeeds" but whose torrent is not found by hash → error, and still no store write
- an expired release id returns something the API layer can turn into a 410
- a malformed magnet fails before the client is called at all
- a second dispatch of the same release returns the first row and adds nothing
- one poll tick maps states and progress onto rows, sets `completed_at` exactly once on completion,
  and does not clear it on a later tick
- a torrent with no matching row is skipped, not adopted, and the run still succeeds
- a tick whose client call fails does not stop the poller — the next tick runs
- cancelling the context stops the poller promptly, and the test proves the goroutine exited
