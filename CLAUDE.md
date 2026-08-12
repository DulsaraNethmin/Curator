# Working in this repo

`curator` replaces the seven-container *arr layer on a Raspberry Pi with one Go binary and an
embedded Next.js UI. Read [`docs/architecture.md`](docs/architecture.md) for the system,
[`docs/progress.md`](docs/progress.md) for where we are right now,
[`docs/roadmap.md`](docs/roadmap.md) for where we are going, and
[`docs/decisions.md`](docs/decisions.md) before overturning anything — several decisions here
reversed an earlier plan for reasons that were expensive to establish.

**Phases 1 and 2 are done** (both verified 2026-08-12). **Phase 3 is built**, with one verification
outstanding — a dispatch against the real qBittorrent, which needs its Web UI password. **Phase 4 is
built and verified locally**; it has never run against the Pi, on purpose. Tasks live in
[`docs/tasks/`](docs/tasks/). Pick one, read its file, do only what it owns.

### Phase 4 writes to disk, and only ever locally

The importer hardlinks; it never moves, copies over, renames or deletes a source file
([D8](docs/decisions.md#d8--import-by-hardlink)). It is proven in `t.TempDir()` — equal inode plus a
link count of 2, and bytes written through the source appearing through the destination. `df
unchanged` is a weak signal on a copy-on-write macOS temp dir, so that check defers to the Pi at
phase 6.

Two traps worth naming. `syscall.Stat_t.Nlink` is `uint16` on darwin and `uint64` on linux, so read
it through a converting helper or `GOOS=linux GOARCH=arm64 go vet ./...` breaks on a line that looks
fine here. And **a folder does not imply a file** — 15 of the 29 library folders on the Pi are empty,
and only 14 video files exist in total.

---

## Things that will bite you

### The title parsing trap

All 29 library folders are `Title (Year)`. Colons are illegal in filenames, so they were replaced
with ` - `:

```
Avengers - Infinity War (2018)              →  Avengers: Infinity War
Spider-Man - No Way Home (2021)             →  Spider-Man: No Way Home
X-Men Origins - Wolverine (2009)            →  X-Men Origins: Wolverine
```

**A `-` → `:` replacement corrupts `Spider-Man` and `X-Men`.** Only ` - ` (space-hyphen-space) is a
substitution, and even that is not guaranteed to be a colon rather than a real dash.

The rule: query TMDB with the **raw folder title** and disambiguate by year — TMDB's search is fuzzy
enough. Only on an empty result, retry with ` - ` collapsed to a space. On failure record
`tmdb_id = NULL` and surface it; never guess. Seven titles are 2026 releases where a confident-but-wrong
match is entirely plausible.

8 of 29 titles contain ` - `. They are all in `testdata/library/movies/`, so the parser meets the
real strings in tests.

### SQLite must be pure Go

`modernc.org/sqlite`, **never** `mattn/go-sqlite3`. The cgo driver would make
`GOOS=linux GOARCH=arm64 go build` require a cross-compilation toolchain; the pure-Go one makes
deploying to the Pi a single command. This is load-bearing, not preference.

### minter's contract

[`minter`](https://github.com/DulsaraNethmin/Minter) (`ghcr.io/dulsaranethmin/minter`) is a companion
service that gets past Cloudflare. `POST /fetch {"url": "..."}` returns rendered HTML.

**Cookie reuse does not work.** Cloudflare binds `cf_clearance` to the exit IP, User-Agent *and* TLS
fingerprint, and no uTLS profile reproduces minter's patched Firefox 151 — measured, all three
fingerprints differ. Do not try to be clever and skip the browser. 1337x goes through it; YTS and TPB
do not need it.

**Use `http://127.0.0.1:8191`, not `localhost`.** minter binds IPv4 only, so `localhost` resolves to
`::1` first and the connection fails. That is `MINTER_URL`'s default. Inside Docker the name `minter`
resolves correctly. Before calling a 1337x failure a code bug, check minter is actually up.

### The indexer domains move

`yts.mx` is NXDOMAIN — YTS is reached at `https://movies-api.accel.li/api/v2`
([D12](docs/decisions.md#d12--yts-is-reached-at-movies-apiaccelli-not-ytsmx)). `yts.rs` and `yts.hn`
resolve and look plausible but are clone sites running a re-implemented API. Check the base URL before
concluding the parser broke.

### Do not touch the Pi's running stack

The *arr containers on `npi` keep serving until phase 6 cutover. Read from the Pi freely; change
nothing. `ssh pi` reaches it. The library is `/media/storage/media/movies`.

---

## Conventions

- **Stdlib `net/http`** with Go 1.22 pattern routing (`GET /api/movies/{id}`). No router, no framework.
- **Hand-written SQL**, no ORM. Three tables; queries live in `internal/store/`.
- **Errors wrap with context**: `fmt.Errorf("scan %s: %w", path, err)`. Return them; do not log-and-continue
  in library code.
- **Comments explain why, not what.** If a line is non-obvious because of something we measured,
  say what we measured.
- Config from environment, read once into `internal/config`.

## Git workflow

**Branch first, never commit to `main`.** Create the branch before the first commit of a piece of
work and name it for the work — `phase-2-indexers`, not `wip`.

**Do not push, and do not merge.** Both are Nethmin's, including the merge into `main` and anything
that reaches `origin`. Leave the finished branch local and say it is ready.

**One commit per task**, each of which **builds, vets, tests and cross-compiles on its own**, so a
bisect lands on one task and not on a half-finished phase:

```bash
go build ./... && go vet ./... && go test -race ./... && GOOS=linux GOARCH=arm64 go build ./...
```

Verify that per commit rather than only at the end — a temporary worktree per commit
(`git worktree add --detach`) is how phase 2's five were checked. A dependency belongs in the commit
that first imports it: `golang.org/x/sync` landed with T11's `errgroup`, not earlier.

Commit messages explain **why**, in the body, at the length the reasoning needs. The subject is
`T<n> <area>: <what>`. If a commit encodes a decision, say what the alternative was and why it lost —
`git log` is where the reasoning is looked for once the docs have moved on.

## Layout

```
cmd/curator/         wiring, config, HTTP server
internal/config/     env-driven settings
internal/store/      SQLite: schema, queries
internal/library/    disk scan, Title (Year) parsing
internal/tmdb/       metadata lookup
internal/api/        HTTP handlers
internal/indexer/    release search — YTS, TPB, 1337x behind one interface, merged and ranked
internal/qbit/       qBittorrent Web API v2 — adds and reads, never deletes
internal/download/   dispatch a picked release, poll its progress into the database
internal/importer/   hardlink a completed download into the library; the only package that knows deployment paths
internal/jellyfin/   ask the media server to rescan, and nothing else
testdata/library/    29 fixture dirs mirroring the real library
web/                 Next.js UI (phase 5)
```

## Commands

```bash
go build ./... && go test ./...
GOOS=linux GOARCH=arm64 go build ./...      # must always pass — it is how this ships
go run ./cmd/curator                         # http://localhost:8090

LIBRARY_MOVIES=./testdata/library/movies go run ./cmd/curator   # scan the fixture
```

Mermaid diagrams in `docs/` must render before committing — `click` is a reserved word in flowcharts
and a bad node name fails silently on GitHub:

```bash
npx -y @mermaid-js/mermaid-cli@11 -i docs/diagram.mmd -o /tmp/out.svg
```

## Environment

| | |
|---|---|
| Pi | `ssh pi` → 192.168.1.26, Pi 5, arm64, Debian 13 |
| Library | `/media/storage/media/movies` — 29 folders on a 916 GB USB disk |
| Downloads | `/media/storage/media/downloads` — same filesystem, so imports hardlink |
| TMDB | `TMDB_API_KEY` env var, free key from themoviedb.org |
| minter | `MINTER_URL`, default `http://127.0.0.1:8191` — IPv4 only, so not `localhost` |
| Search | `SEARCH_TIMEOUT` default `30s`, `SEARCH_CACHE_TTL` default `1h` |
| qBittorrent | `QBIT_URL` default `http://127.0.0.1:8080`; `gluetun:8080` in Docker — it has no ports of its own |
| Downloads | `QBIT_USER`/`QBIT_PASS` (unset = downloads off, not a startup error), `QBIT_CATEGORY` default `curator` |
| Import | `DOWNLOADS_PATH` (empty = use `content_path` verbatim), `QBIT_DOWNLOADS_PATH` default `/downloads` |
| Jellyfin | 10.10.7 at `192.168.1.26:8096`. `JELLYFIN_URL`, `JELLYFIN_API_KEY` (unset = no refresh, not a startup error) |

## Known broken upstream

Nothing outstanding. `gluetun` was exited and `qbittorrent` had never started — an unresolved NordVPN
`AUTH_FAILED` that blocked phases 3–4 verification. Both have been running healthy since 2026-08-12,
so nothing is blocked. Container state drifts, so confirm with `ssh pi 'docker ps'` before relying
on it.
