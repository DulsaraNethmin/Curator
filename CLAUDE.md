# Working in this repo

`curator` replaces the seven-container *arr layer on a Raspberry Pi with one Go binary and an
embedded Next.js UI. Read [`docs/architecture.md`](docs/architecture.md) for the system,
[`docs/progress.md`](docs/progress.md) for where we are right now,
[`docs/roadmap.md`](docs/roadmap.md) for where we are going, and
[`docs/decisions.md`](docs/decisions.md) before overturning anything — several decisions here
reversed an earlier plan for reasons that were expensive to establish.

**Phase 1 is done** (verified 2026-08-12). **Current phase: 2 (indexers).** Tasks live in
[`docs/tasks/`](docs/tasks/). Pick one, read its file, do only what it owns.

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
fingerprints differ. Do not try to be clever and skip the browser. Needed for 1337x in phase 2.

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

## Layout

```
cmd/curator/         wiring, config, HTTP server
internal/config/     env-driven settings
internal/store/      SQLite: schema, queries
internal/library/    disk scan, Title (Year) parsing
internal/tmdb/       metadata lookup
internal/api/        HTTP handlers
internal/indexer/    release search — absorbed from cfprobe, NOT wired until phase 2
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
| minter | `MINTER_URL`, default `http://localhost:8191` (phase 2) |

## Known broken upstream

Nothing outstanding. `gluetun` was exited and `qbittorrent` had never started — an unresolved NordVPN
`AUTH_FAILED` that blocked phases 3–4 verification. Both have been running healthy since 2026-08-12,
so nothing is blocked. Container state drifts, so confirm with `ssh pi 'docker ps'` before relying
on it.
