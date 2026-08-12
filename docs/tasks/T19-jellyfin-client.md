# T19 — Jellyfin client

**Owns:** `internal/jellyfin/` — `client.go`, `client_test.go`, `live_test.go`
**Depends on:** nothing

## Goal

Ask Jellyfin to rescan its library. One endpoint, one header, one expected status code. It is a small
package on purpose: everything else Jellyfin can do is somebody else's phase.

## Do

1. `New(baseURL, apiKey string, httpClient *http.Client) *Client`, same shape as `qbit.New` and
   `tmdb.New`: an empty base URL falls back to `DefaultBaseURL`, and a nil client gets one **with a
   timeout**, because `http.DefaultClient` has none and would hang for ever.
2. `DefaultBaseURL = "http://127.0.0.1:8096"`. Jellyfin is **10.10.7 at 192.168.1.26:8096** on the
   Pi, reached as `http://jellyfin:8096` inside Docker; the constant is the laptop default, which is
   where it gets typed most.
3. **`RefreshLibrary(ctx) error`** — `POST /Library/Refresh` with the key in an **`X-Emby-Token`**
   header. Expect **`204 No Content`**. A `200` is accepted too; anything else is an error naming the
   status and a capped snippet of the body.
4. **`401` → a named `ErrUnauthorized`**, testable with `errors.Is`. It is a configuration fault — a
   revoked or mistyped key — and callers must not treat it as "Jellyfin is down" and keep trying.
   `403` maps to it as well: both mean the token, not the request.
5. The API key rides in a **header, never in the query string**, so it stays out of `*url.Error`
   messages, access logs and any proxy in between. The same reasoning as `qbit`'s password in the
   form body.
6. **This method returns an error, and that is correct.** The "cannot fail the tick" guarantee
   ([D15](../decisions.md#d15--the-jellyfin-refresh-is-best-effort-and-its-key-is-optional)) is put
   in the type at **T20's** seam, where the swallowing is deliberate and happens exactly once. A
   client that hid its own failures could not be tested and could not have a live test that fails on
   a bad status.
7. **`live_test.go` follows `internal/tmdb/live_test.go`'s contract**: skip under `-short`, skip when
   the URL or key is absent, skip when the host is unreachable — but **fail** on a bad status, so a
   dead URL cannot sit there green.
8. **Leave the live test skipped this phase.** A refresh *mutates* Jellyfin, and the Pi is off limits
   until phase 6 cutover. Guard it with one clearly-named early skip that says exactly that, placed
   so that phase 6 enables it by deleting a single statement.

## Do not

- Add anything else Jellyfin can do — no item queries, no `/Items/{id}/Refresh`, no user or session
  endpoints, no playback control. The narrowest possible client is the cheapest guarantee that
  nothing here can disturb a media server the household is watching. This is the same posture
  `internal/qbit` takes towards delete and pause.
- Read the environment or hold a config struct. T21 passes the URL and the key in.
- Import `internal/store`, `internal/importer` or `internal/download`. Nothing here knows what a
  download is.
- Run the live test against the Pi in this phase, or otherwise send it a request.
- Log. Return errors; the caller decides what is worth saying
  ([CLAUDE.md](../../CLAUDE.md#conventions)).

## Verify

`go test -race ./internal/jellyfin` — scope commands to this package, sibling tasks are mid-write in
the same tree.

Against an `httptest.Server`, which is how every client in this repo is tested:

- a `204` is success, and the request really was `POST /Library/Refresh`
- the `X-Emby-Token` header carries the key, asserted on the server side, and the key appears
  **nowhere** in the request URL or query string
- `200` is also accepted
- `401` and `403` each return `ErrUnauthorized`, asserted with `errors.Is`
- `500` is an error that is **not** `ErrUnauthorized` — the distinction is the whole point of the
  sentinel
- a connection-refused base URL returns an error mentioning the URL, and does not panic
- a cancelled context returns promptly
- a base URL with a trailing slash produces exactly one `/` in the path — a doubled slash is a 404
  from a server that would otherwise have worked
- an HTML error page from a reverse proxy is reported as a capped, single-line snippet rather than
  pasted whole into the error
- the live test **skips**, with a message naming phase 6, and skips under `-short` too
