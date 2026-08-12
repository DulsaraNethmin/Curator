# T21 — Wiring: config, the poller hook, and a manual import

**Owns:** `internal/config/config.go` + `config_test.go`, `cmd/curator/main.go`,
`internal/download/poller.go` + `poller_test.go`, `internal/download/service.go` + `service_test.go`,
`internal/api/imports.go` + `imports_test.go`, `.env.example`, `CLAUDE.md`
**Depends on:** T20

## Goal

Make the importer run. Four settings, a hook in the poll tick, one endpoint for the case a tick
cannot serve.

## Do

1. **Config** — four new variables, defaults exactly as the phase spec's table:

   | Variable | Default | Notes |
   |---|---|---|
   | `DOWNLOADS_PATH` | *(empty)* | Empty means use `content_path` verbatim — the laptop case needs no config |
   | `QBIT_DOWNLOADS_PATH` | `/downloads` | qBittorrent's side of the mount |
   | `JELLYFIN_URL` | `http://127.0.0.1:8096` | |
   | `JELLYFIN_API_KEY` | *(empty)* | Empty disables the refresh and is **not** a startup error |

   Add `JellyfinConfigured() bool` beside the existing `DownloadsConfigured()`, and keep the comment
   style: say *why* the default is what it is, not what it does.
2. **The poller hook.** Add a nilable `importer Importer` field and a `WithImporter` builder, so
   `NewPoller`'s signature is unchanged and **every existing poller test passes untouched**. The
   interface is declared in `internal/download`:
   ```go
   type Importer interface {
       TryImport(ctx context.Context, t qbit.Torrent, d store.Download)
       Refresh(ctx context.Context)
   }
   ```
   Neither method returns an error — an import cannot fail a tick, by type
   ([D15](../decisions.md#d15--the-jellyfin-refresh-is-best-effort-and-its-key-is-optional)).
3. **Trigger on the state, never on the transition**
   ([D14](../decisions.md#d14--the-importer-is-driven-by-the-pollers-torrent-list-not-by-a-completion-event)).
   Inside the existing loop, after `state` and `completedAt` are computed:
   - the current `if state == row.State && … { continue }` short-circuit must become an `if` around
     the **update** instead of a `continue` past the rest of the iteration. Left as a `continue`, a
     completed-and-unchanged torrent skips the import on every tick after the first, and the retry
     path — which is the whole design — never runs.
   - then, when `p.importer != nil && state == store.DownloadCompleted`, call
     `p.importer.TryImport(ctx, t, row)`. Rows already reading `imported` were skipped further up by
     the guard phase 3 already has.
   - after the loop, `p.importer.Refresh(ctx)` — **once per tick, not once per import**.
4. **Change nothing else in `poller.go`**: not the state mapping, not the `completed_at` stamping,
   not the no-row warning, not the failed-tick logging.
5. **`Service.Import(ctx, hash) (store.Movie, error)`** in `service.go`, with a `WithImporter`
   builder for the same nilable-field reason:
   - nil client or nil importer → `ErrUnconfigured`
   - `GetDownloadByHash` → `store.ErrNotFound` passes through
   - `TorrentByHash` fails → wrap `ErrClient`; a **nil** torrent is also `ErrClient`, naming the hash
   - the torrent's mapped state is not `completed` → a new named `ErrNotCompleted`
   - otherwise call the importer's **`Import`** — the erroring one, because a caller who asked
     synchronously deserves the reason
6. **`POST /api/downloads/{hash}/import`** in a new `internal/api/imports.go`, registered from
   `RegisterDownloads`. Extend the existing `Dispatcher` interface rather than adding a second one,
   and answer `200` with the movie row. Status codes, in the shape `failDispatch` already
   establishes:

   | | |
   |---|---|
   | unknown hash | `404` |
   | qBittorrent unreachable, or no such torrent | `502` |
   | not finished | `409` |
   | `ErrNoVideo`, or a title that cannot be a folder | `422` |
   | unconfigured | `503` |
7. **`cmd/curator/main.go`** — build a `*jellyfin.Client` only when `JellyfinConfigured()`, declared
   as the interface so a nil stays a **nil interface** and not an interface holding a nil pointer
   (the trap the `Matcher` and `TorrentClient` comments already call out). Build the importer, attach
   it to the poller with `WithImporter` and to the service, and log a warning when the key is unset —
   the same shape as the existing `TMDB_API_KEY` and `QBIT_USER` warnings. Share `indexerHTTP`.
8. Update `.env.example` with the four variables and their reasoning, and `CLAUDE.md`'s **Layout**
   (`internal/importer/`, `internal/jellyfin/`) and **Environment** table.

## Do not

- Start a second goroutine, a second ticker or a second loop. The importer runs inside the tick that
  already exists.
- Change `NewPoller` or `NewService`'s signature. `WithImporter` exists so the phase 3 tests are not
  touched at all — if a phase 3 test needed editing, the hook is in the wrong place.
- Make an unset `JELLYFIN_API_KEY` a startup error, or any other unset integration. A service that
  refuses to boot because one of six integrations is unconfigured is worse at being partially useful.
- Let a failing import or a failing refresh end a tick or fail a request that was not about them.
- Add a second import code path for the endpoint. It calls the same `Importer`.
- Migrate anything.

## Verify

`go build ./... && go vet ./... && go test -race ./...`, then
`GOOS=linux GOARCH=arm64 go build ./...` and `GOOS=linux GOARCH=arm64 go vet ./...`.

- **every phase 3 poller test passes unmodified** — `git diff` shows no assertion changed in
  `poller_test.go`; only new tests were added
- a poller with a **nil** importer behaves exactly as phase 3 did, including for a completed torrent
- a completed torrent calls `TryImport` **once per tick**, with the torrent and the row
- **the retry path**: a second tick with nothing changed still calls `TryImport`. This is the test
  the `continue`-to-`if` change exists for, and it fails against the phase 3 control flow.
- a torrent already `imported` is skipped before the importer is reached
- `Refresh` is called **once per tick**, after the loop, even when three torrents imported — and on a
  tick where nothing was completed
- a `TryImport` that would have failed does not stop the tick; the remaining torrents are still
  reconciled
- config: defaults, overrides, and `JellyfinConfigured()` false with an empty key
- the endpoint: `200` with the movie row; `404`, `409`, `422`, `502`, `503` each asserted against a
  fake, in the `{"error": "..."}` shape phase 1 established
- `POST /api/downloads/{hash}/import` with downloads unconfigured is `503` and reaches no importer
- phase 1, 2 and 3 endpoints are unaffected

Nothing here is run against the Pi. The *arr stack keeps serving until phase 6.
