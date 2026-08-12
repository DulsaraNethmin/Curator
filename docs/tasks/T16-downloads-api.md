# T16 — Downloads API

**Owns:** `internal/api/downloads.go`, `downloads_test.go`; the phase 3 additions to
`internal/config/`; the wiring in `cmd/curator/`
**Depends on:** T15

## Goal

Expose dispatch and download state over HTTP, and start the poller with the process. The task that
makes phase 3 real.

## Do

1. Endpoints, per [`../phase-3.md`](../phase-3.md#api-surface):
   - `POST /api/downloads` — body `{"release_id", "title", "year", "tmdb_id"}`. `201` with the row
   - `GET /api/downloads` — every download with state and progress
2. A `Dispatcher` interface **declared in `internal/api`**, satisfied by T15's service, so handlers
   are tested with fakes. Same pattern as phase 1's `Store` and phase 2's `Searcher`.
3. Status codes that mean something:
   - expired or unknown `release_id` → **`410`**, forwarding phase 2's `indexer.ErrReleaseExpired`
     rather than inventing a second sentinel
   - qBittorrent unreachable, or an add that could not be confirmed → **`502`**, because the request
     was fine and a dependency was not. A `500` would blame curator for qBittorrent's bad day
   - blank `title`, non-numeric `year`, missing `release_id` → `400`
   - re-dispatch of a release already downloading → `200` with the existing row, never a duplicate
   - errors keep phase 1's `{"error": "..."}` shape
4. Config additions, defaults from [`../phase-3.md`](../phase-3.md#configuration): `QBIT_URL`
   (`http://127.0.0.1:8080`), `QBIT_USER`, `QBIT_PASS`, `QBIT_CATEGORY` (`curator`),
   `DOWNLOAD_POLL_INTERVAL` (`10s`). Extend the existing config tests.
   **An unset `QBIT_USER` must not fail startup.** It matches the unset-`TMDB_API_KEY` path: the
   library still scans, search still works, and only dispatch reports itself unconfigured — as a
   `503` naming the missing variable, not a panic and not a silent nil.
5. Wire it in `cmd/curator`: build the qBittorrent client, the download service and the poller, and
   **start the poller with the server and stop it on `SIGTERM`** through the context already used for
   graceful shutdown. Reuse the shared `*http.Client`.
6. Skip starting the poller entirely when downloads are unconfigured. A poller that logs an
   authentication failure every ten seconds forever is worse than no poller.

## Do not

- Add authentication. LAN-only, same posture as the stack this replaces.
- Touch `internal/library`, `internal/tmdb`, or phase 2's `search.go`.
- Write files, hardlink, or tell Jellyfin anything. Phase 4.
- Return a `200` carrying a failure, or a `500` carrying qBittorrent's.
- Delete anything from qBittorrent, ever — the *arr stack shares that client until phase 6.

## Verify

`go test -race ./internal/api` with a fake `Dispatcher`:

- a dispatch returns `201` and the row
- an expired release id returns `410` with a JSON body
- an unreachable qBittorrent returns `502` with a JSON body
- unconfigured downloads return `503` naming the variable, and the server still serves
  `/api/search`, `/api/movies` and `/healthz`
- blank title, non-numeric year and missing release id each return `400`
- `GET /api/downloads` returns `[]` and not `null` when there is nothing
- phase 1 and phase 2 endpoints all still pass

Then end to end against the Pi's qBittorrent, per
[`../phase-3.md`](../phase-3.md#verification): dispatch a real release, confirm it appears **under the
`curator` category**, watch progress move, restart curator and confirm progress survives, and confirm
an unreachable qBittorrent gives `502` while writing no row.

**Add nothing to the `radarr` or `sonarr` categories, and delete nothing.** Everything this phase
touches is scoped to `curator`.
