# T5 — API

**Owns:** `internal/api/`
**Depends on:** T2 (store), T3 (scanner), T4 (TMDB)

## Goal

Wire the pieces together and expose them over HTTP. This is the task that makes phase 1 real.

## Do

1. A `Server` holding the store, scanner and TMDB client as **interfaces defined in this package**,
   so handlers can be tested with fakes rather than a live database and network.
2. Endpoints, per [`../phase-1.md`](../phase-1.md#api-surface):
   - `POST /api/scan` — walk the library, upsert each folder, then match anything with
     `tmdb_id IS NULL`. Returns `{scanned, added, matched, unmatched}`.
   - `GET /api/movies` — newest first.
   - `GET /api/movies/{id}` — 404 when absent.
3. **Scanning proceeds without TMDB.** If no API key is configured, or TMDB errors, still record what
   is on disk and report `unmatched`. The disk is the source of truth; metadata is an enrichment.
4. One TMDB failure must not abort the scan. Log it, leave that row unmatched, carry on.
5. JSON errors in a consistent shape (`{"error": "..."}`) with correct status codes. Never return a
   200 carrying a failure — minter shipped exactly that bug and it made every failure look like
   success.

## Do not

- Add authentication. LAN-only, same posture as the stack this replaces.
- Make scanning a background job. 29 folders is fast; synchronous is simpler and honest about when
  it finishes.
- Serve the UI yet. That is phase 5.

## Verify

`go test ./internal/api` with fakes:

- `POST /api/scan` on a fixture with 29 folders reports `scanned: 29`
- a second scan reports `added: 0` — idempotent
- a TMDB error leaves the row present with `tmdb_id: null` and increments `unmatched`
- no API key configured still scans, reporting everything unmatched
- `GET /api/movies/{id}` returns 404 for an unknown id, with a JSON body

Then end to end:

```bash
TMDB_API_KEY=... go run ./cmd/curator &
curl -s -X POST localhost:8090/api/scan | jq
curl -s localhost:8090/api/movies | jq 'length'                              # 29
curl -s localhost:8090/api/movies | jq '[.[]|select(.tmdb_id==null)|.title]'  # ideally []
```
