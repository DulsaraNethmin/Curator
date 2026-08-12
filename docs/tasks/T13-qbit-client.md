# T13 — qBittorrent client

**Owns:** `internal/qbit/` — `client.go`, `client_test.go`, `state.go`, `state_test.go`
**Depends on:** nothing

## Goal

A Web API v2 client for qBittorrent 5.1.2: log in, add a magnet, list our torrents, and translate
qBittorrent's states into ours. No database, no HTTP handlers, no polling loop — T15 owns the loop.

## Do

1. `New(baseURL, username, password string, c *http.Client) *Client`, base overridable so an
   `httptest.Server` can stand in — the same shape as `internal/tmdb` and `internal/indexer`'s YTS.
2. **Session authentication.** `POST /api/v2/auth/login` with `username` and `password` as **form
   values**, which returns an `SID` cookie and the body `Ok.`. Hold the cookie and send it on every
   later call. A `net/http/cookiejar` is stdlib and is the least code that is still correct.
3. **Re-authenticate exactly once on a 403, then retry.** An expired session and a wrong password are
   the same status code, so a second 403 is a real failure and must be reported as one — not retried
   in a loop. Say in the error which of the two it looks like.
4. `AddMagnet(ctx, magnet, category string) error` → `POST /api/v2/torrents/add`, form-encoded, with
   `urls`, `category` and `tags`.
   **`torrents/add` answers `200 Ok.` whether or not it accepted the magnet, and never returns a
   hash.** So this method does not attempt to report one; confirming the add is the caller's job with
   `TorrentByHash`, and the doc comment should say so.
5. `Torrents(ctx, category string) ([]Torrent, error)` → `GET /api/v2/torrents/info?category=…`, and
   `TorrentByHash(ctx, hash string) (*Torrent, error)` returning `(nil, nil)` when it is simply not
   there — absence is a normal answer, not an error.
6. `Torrent` carries at least `Hash`, `Name`, `State`, `Progress`, `Size`, `ContentPath`, `Category`,
   `SavePath`. **Hashes are lower-case hex from qBittorrent and upper-case from
   `indexer.InfoHash`** — normalise on the way in and compare case-insensitively, or every lookup
   fails for a reason nobody will guess.
7. `state.go`: map qBittorrent's states to `queued | downloading | completed | failed`, per the table
   in [`../phase-3.md`](../phase-3.md#state-mapping). **Anything unrecognised maps to `downloading`,
   never `failed`** — a state we have not seen is far more likely to be a new qBittorrent than a
   broken torrent. `pausedUP` is `completed`; `pausedDL` is `queued`.

## Do not

- Poll, loop, or start a goroutine. T15 owns that.
- Touch the database, `internal/api`, or `internal/config`.
- Delete, pause, resume or reprioritise anything. This phase adds and reads, nothing else — the *arr
  stack is live on the same qBittorrent until phase 6.
- Log in before every request. The session lasts; a round trip per call to prove it does not is waste.
- Add a dependency. Stdlib only.

## Verify

`go test -race ./internal/qbit` against an `httptest.Server` — no Pi, no network:

- login sends form values and captures the `SID` cookie; later calls carry it
- a 403 triggers exactly one re-login and one retry, and the retry's result is returned
- a second consecutive 403 returns an error naming qBittorrent and saying credentials or session
- `AddMagnet` posts `urls`, `category` and `tags`, and form-encodes correctly
- `Torrents` filters by category, and an empty list is `(nil, nil)`
- `TorrentByHash` finds an upper-case `indexer.InfoHash` against qBittorrent's lower-case hash, and
  returns `(nil, nil)` for one that is absent
- a 5xx and a non-JSON body both return errors naming qBittorrent
- every state in the mapping table, plus an invented one, which must be `downloading`

Then one live check against the Pi's qBittorrent, skipped under `-short` and whenever `QBIT_USER` is
unset: log in, list the `curator` category, assert no error. **Read-only — add nothing in a test.**
