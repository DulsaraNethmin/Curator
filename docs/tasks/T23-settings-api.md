# T23 — The settings endpoint

**Owns:** `internal/api/settings.go`, `internal/api/settings_test.go`
**Depends on:** nothing

## Goal

One endpoint that answers "what is configured, and can curator reach it" — so every other screen can
answer "can I press this button" without guessing.

## Do

1. **`GET /api/settings`**, returning integrations, paths and intervals.
2. **No secret is ever in the response**, not even masked, not even a length
   ([D17](../decisions.md#d17--settings-is-read-only-and-the-settings-table-stays-unused)). Each
   integration reports `configured: true|false` and nothing else about its credentials. There is no
   authentication in front of this endpoint and there is not going to be, so the only safe design is
   one where the secret is never in the payload to leak.
3. Shape, roughly — names matching the environment variables so the screen can tell someone what to
   set:
   ```json
   {
     "version": "0.1.0",
     "integrations": [
       {"name": "tmdb", "env": "TMDB_API_KEY", "configured": true,  "reachable": true},
       {"name": "qbittorrent", "env": "QBIT_USER", "configured": false, "detail": "downloads are disabled"}
     ],
     "paths": {"library_movies": "...", "downloads_path": "", "qbit_downloads_path": "/downloads"},
     "intervals": {"download_poll": "10s", "search_timeout": "30s", "search_cache_ttl": "1h"}
   }
   ```
4. **Reachability is optional, bounded and never fatal.** `?probe=1` may actually call each
   configured integration with a short timeout; without it the endpoint answers from configuration
   alone and returns instantly. The screen loads fast and probes on demand. A probe that fails sets
   `reachable: false` with the reason — it never fails the request, because "Jellyfin is down" is
   exactly what the page exists to display.
5. An **unconfigured** integration is never probed, and reports `reachable` as absent rather than
   false. "Not set up" and "set up but broken" are different facts and the screen says different
   things about them.
6. Read from `*config.Config`, passed in — `internal/api` does not read the environment.

## Do not

- Write anything. No `POST`, no `PUT`, and no use of the `settings` table (D17).
- Return `QBIT_PASS`, `TMDB_API_KEY` or `JELLYFIN_API_KEY` in any form, including truncated, hashed
  or masked. A masked secret still confirms its length and its existence to anyone on the LAN.
- Change any existing handler, or `config.Load`.
- Probe on the unqualified `GET`. A settings page that takes 13 seconds because it woke a browser is
  a settings page nobody opens.
- Make an unreachable integration an error status. `200` carrying bad news is the whole point.

## Verify

`go test -race ./internal/api`:

- the response lists every integration with the right `configured` value for a given config
- **no secret value appears anywhere in the marshalled body** — set every key to a distinctive
  sentinel, marshal, and assert the sentinel is absent. This is the test the task exists for.
- an unconfigured integration reports no `reachable` field at all
- `?probe=1` calls only the **configured** ones — asserted against fakes, with a stub that would fail
  the test if an unconfigured one were probed
- a probe that errors yields `200`, `reachable: false` and a reason
- a probe that hangs is bounded by the timeout and still returns `200`
- paths and intervals reflect the config that was passed in, including an **empty** `DOWNLOADS_PATH`,
  which is a meaningful value and not a missing one
