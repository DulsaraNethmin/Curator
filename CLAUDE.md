# Working in this repo

`curator` replaces the seven-container *arr layer on a Raspberry Pi with one Go binary and an
embedded Next.js UI — and **since phase 6 it is being rebuilt into a product anyone runs with one
`docker run`**, which changes the goal rather than the code written so far. Where a document still
describes "13 containers become 6", it is stale: [`docs/phase-6.md`](docs/phase-6.md) is the target,
and T51 clears the rest. Read [`docs/architecture.md`](docs/architecture.md) for the system,
[`docs/progress.md`](docs/progress.md) for where we are right now,
[`docs/roadmap.md`](docs/roadmap.md) for where we are going, and
[`docs/decisions.md`](docs/decisions.md) before overturning anything — several decisions here
reversed an earlier plan for reasons that were expensive to establish.

**Phases 1 and 2 are done** (both verified 2026-08-12). **Phase 3 is built**, and its outstanding
dispatch has now run against a real qBittorrent 5.1.2 — a local container, not the Pi's. **Phase 4
is built and verified locally**, including one real download hardlinked into the library; it has
never run against the Pi, on purpose. **Phase 5 is built** — seven screens embedded in the binary,
including the TMDB-first redesign of T27–T31, where the film comes from TMDB and releases hang off
it ([D20](docs/decisions.md#d20--the-film-comes-from-tmdb-the-search-box-only-finds-it)). **Phase 6 is
built and verified locally**, tunnel included: the torrent engine and a WireGuard tunnel now live
inside the binary
([D22](docs/decisions.md#d22--the-torrent-engine-moves-inside-the-binary-and-qbittorrent-becomes-the-second-backend),
[D27](docs/decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket)). **Phase 7 is
specified**: [`docs/phase-7.md`](docs/phase-7.md) makes settings writable, which is what a
`docker run` with no `.env` needs and what a private key at rest forces
([D28](docs/decisions.md#d28--settings-are-writable-secrets-are-encrypted-at-rest-and-write-only-across-the-api)).
Tasks live in [`docs/tasks/`](docs/tasks/). Pick one, read its file, do only what it owns.

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

The *arr containers on `npi` keep serving until the cutover, which is **phase 10** — phase 6 builds
the replacement on a laptop and changes nothing on the Pi. Read from the Pi freely; change nothing.
`ssh pi` reaches it. The library is `/media/storage/media/movies`.

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

**Say what to merge, at the end of every task.** Not at the end of a phase — every task, as soon as
its commit passes `make check`. Last line of the message, in this shape:

```
merge: phase-7-settings-that-write — 2 commits (caa24ae, <next>), main does not have them
hold:  nothing else outstanding
```

Name the branch, count the commits `main` is missing, and say plainly whether it is **ready** or
**waiting** — and on waiting, what for. `git log --oneline main..HEAD` is the answer and
`git branch --no-merged main` is the one `make status` reports. It is the question that actually gets
asked at the end of a long day, and it is not one to reconstruct from a scrollback.

**Then write the handoff, in the same message.** One task is one session: the next one starts in a
fresh context that knows nothing, and what it is given is the difference between an hour of
re-derivation and starting. Print it as a single copyable block, after the merge line, and write
**what the repo cannot say for itself** — `make status` derives the phases, the tasks and the
decision gaps, and repeating them wastes the only thing the block is for.

The shape, in this order:

1. **Where `main` is** — the SHA, whether it is clean and pushed, and any unmerged branch.
2. **Read first, in this order** — `CLAUDE.md`, the plan, `docs/phase-N.md`, the task file, the
   decisions that bind it. Name the files; do not summarise them.
3. **What the last task shipped**, in a paragraph — and what it deliberately did not.
4. **MEASURED — do not re-derive.** Numbers that were paid for: timings, sizes, exit addresses,
   modes, what a live service actually answered. This is the most valuable section and the one that
   is never reconstructable.
5. **TRAPS found the hard way**, with the guard that now exists, so nobody removes it as dead code.
6. **STILL OUTSTANDING** — the honest list, including the parts of a "done" task that are not.
7. **NEXT** — the task, its file, and the one design question it will hit first.
8. **ENVIRONMENT** — what is running, what is in `.env`, what must not be touched.
9. **The git workflow**, in two lines: branch first, one commit per task, `make check` per commit,
   no push, no merge, no `Co-Authored-By`.

Written in the imperative, to the next session, not about it. A handoff that says "I implemented the
registry" wastes a line that could have said "the registry holds no defaults, and here is why".

**One commit per task**, each of which **builds, vets, tests and cross-compiles on its own**, so a
bisect lands on one task and not on a half-finished phase. That is `make check`, which runs the UI
export as well, because since phase 5 the binary embeds it:

```bash
make check     # npm run build, go build, go vet, go test -race, arm64 cross-compile
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
internal/web/        the embedded UI — //go:embed all:dist, and the all: is load-bearing
testdata/library/    29 fixture dirs mirroring the real library
web/                 Next.js UI — static export, embedded from internal/web/dist
```

## Commands

`make` lists them; `make status` says where the build is, derived from the repo rather than from a
list somebody has to remember to update.

```bash
go build ./... && go test ./...
GOOS=linux GOARCH=arm64 go build ./...      # must always pass — it is how this ships
go run ./cmd/curator                         # http://localhost:8090

# The UI is a static export embedded with //go:embed. Shipping is TWO commands
# since phase 5, and the order matters — see decisions.md D16.
npm --prefix web install                     # once
npm --prefix web run build                   # writes internal/web/dist/
GOOS=linux GOARCH=arm64 go build ./...       # embeds it

# Working on the UI: the two halves run separately, because output:'export'
# disables rewrites and there is no dev proxy to configure.
NEXT_PUBLIC_API_BASE=http://localhost:8090 npm --prefix web run dev   # :3000

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
