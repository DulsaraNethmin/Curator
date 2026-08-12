# T1 — Scaffold

**Owns:** `cmd/curator/`, `internal/config/`
**Depends on:** nothing. Everything else waits on this.

## Goal

A binary that starts, reads its configuration from the environment, and serves `/healthz`.

## Do

1. `internal/config/config.go` — a `Config` struct read once from the environment, with the defaults
   in [`../phase-1.md`](../phase-1.md#configuration). Return an error for anything invalid (a
   non-numeric `PORT`), not a panic. `TMDB_API_KEY` being empty is **not** an error here — scanning
   works without it, only matching does not.
2. `cmd/curator/main.go` — load config, build the HTTP server, serve `GET /healthz` returning
   `{"ok": true, "version": "..."}`. Use stdlib `net/http` with Go 1.22 pattern routing.
3. Graceful shutdown on `SIGINT`/`SIGTERM` with a bounded timeout. This will run in a container and
   an abrupt exit mid-write is how SQLite files get corrupted.
4. A `version` constant in one place, so it never drifts the way it did in minter.

## Do not

- Add a router, framework or logging library. Stdlib `net/http` and `log/slog`.
- Wire a database, scanner or API handlers — those are T2, T3 and T5.

## Verify

```bash
go build ./... && go vet ./...
GOOS=linux GOARCH=arm64 go build ./...     # must pass from the very first commit
go run ./cmd/curator &
curl -s localhost:8090/healthz | jq
PORT=9999 go run ./cmd/curator &            # config actually reads the environment
curl -s localhost:9999/healthz | jq
```
