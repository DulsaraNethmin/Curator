# Working in this repo

`curator` replaces the \*arr layer on a Raspberry Pi with one Go binary and an embedded Next.js UI —
and **since phase 6 it is being rebuilt into a product anyone runs with one `docker run`**, which
changes the goal rather than the code written so far. The container arithmetic has moved twice and
the current number is
[D43](docs/decisions.md#d43--the-pi-is-a-clean-slate-television-is-retired-and-curator-is-the-only-downloader)'s:
**thirteen services become five** — jellyfin, portainer, watchtower, homepage and curator, which is
five services and six containers because curator's bundle is curator and minter. T51 cleared the
"13 containers become 6" passages on 2026-08-19; where one survives it is inside a **record** — a
phase document or a task file — and is marked as corrected in place. None of that is the product's
number: for anyone who is not this Pi, curator is one container plus optional profiles.

Read [`docs/architecture.md`](docs/architecture.md) for the system,
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
[D27](docs/decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket)). **Phase 7 is built
and verified locally**: settings are writable, secrets are encrypted at rest and write-only across
the API, and one optional password gates it
([D28](docs/decisions.md#d28--settings-are-writable-secrets-are-encrypted-at-rest-and-write-only-across-the-api),
[D25](docs/decisions.md#d25--authentication-is-optional-and-off-by-default)). **Phase 8 is built** —
direct play, a remux for the containers a browser refuses, and Open in Jellyfin. **Phase 9 is built**, T51
included: the repository and its ghcr package are public, `0.2.0` is published, and the README's
quickstart was run from an empty directory against an anonymous pull before it was written down.
**Phase 10, the cutover, is done** — see below, because it changes what may be touched.
Tasks live in [`docs/tasks/`](docs/tasks/). Pick one, read its file, do only what it owns.
`make status` derives the phases, the tasks and the decision gaps — read it rather than this
paragraph for the current counts.

### Phase 4 writes to disk, and only ever locally

The importer hardlinks; it never moves, copies over, renames or deletes a source file
([D8](docs/decisions.md#d8--import-by-hardlink)). It is proven in `t.TempDir()` — equal inode plus a
link count of 2, and bytes written through the source appearing through the destination. `df
unchanged` is a weak signal on a copy-on-write macOS temp dir, so that check defers to the Pi at
phase 6.

Two traps worth naming. `syscall.Stat_t.Nlink` is `uint16` on darwin and `uint64` on linux, so read
it through a converting helper or `GOOS=linux GOARCH=arm64 go vet ./...` breaks on a line that looks
fine here. And **a folder does not imply a file.** That rule was learned from the Pi, where 29 folders
held only 16 films; the Pi's library is now empty
([D43](docs/decisions.md#d43--the-pi-is-a-clean-slate-television-is-retired-and-curator-is-the-only-downloader)),
so `testdata/library/movies/` is the only place the shape survives — **29 directories, 3 with a video
file and 26 carrying just a `.gitkeep`.** Since
[D33](docs/decisions.md#d33--a-folder-with-no-film-in-it-is-not-a-movie-the-row-goes-the-folder-stays)
the consequence is the opposite of what it used to be: such a folder is **not a movie**, every scan
removes its row, and the directory itself is left exactly where it is.

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

Since T77 a base URL that has gone NXDOMAIN **fails the live test loudly** rather than skipping, so
this should not go unnoticed for a week again — but only once a control name proves DNS works
([D42](docs/decisions.md#d42--a-dead-base-url-fails-the-build-but-only-once-a-control-name-proves-the-machine-is-online)).
See the population note under *Fanning work out*.

### The Pi rule has changed — phase 10 started

**This section used to say "change nothing on the Pi", and that expired on 2026-08-18.** Phases 1–9
built the replacement on a laptop and touched nothing; phase 10 is the cutover and it has begun.

What has already happened: the media disk was **emptied** — 363 GB deleted, 870 GB free,
`media/{movies,tv,downloads}` all present and empty — and television was retired
([D43](docs/decisions.md#d43--the-pi-is-a-clean-slate-television-is-retired-and-curator-is-the-only-downloader),
which reverses [D26](docs/decisions.md#d26--television-keeps-its-stack-the-cutover-removes-only-what-curator-replaces-for-movies)).
The \*arr configs were backed up and one was restored to prove it
([T52](docs/tasks/T52-arr-config-backup.md)); the backup lives outside this repository and holds the
NordVPN credentials.

**The cutover is done.** [T54](docs/tasks/T54-remove-what-is-replaced.md) removed the nine on
2026-08-18 — gluetun, qbittorrent, prowlarr, radarr, sonarr, seerr, flaresolverr, byparr,
recyclarr — and the box now runs **six containers**: jellyfin, portainer, watchtower and homepage
from `/opt/docker`, plus curator and minter from `/opt/curator`. Five *services*, six containers;
D43's "thirteen become five" counts curator's bundle as one, and minter is the second half of it.

**Jellyfin stays and must not be touched**; curator adopts that server rather than replacing it.
`ssh pi` reaches the box. The rollback is `/opt/docker/configs/` — 627 MB of the removed services'
configuration, deliberately left on disk — plus `compose.yml.pre-t54` beside the new file and the
full thirteen-service original in the T52 backup off the box.

---

## Conventions

- **Stdlib `net/http`** with Go 1.22 pattern routing (`GET /api/movies/{id}`). No router, no framework.
- **Hand-written SQL**, no ORM. Three tables; queries live in `internal/store/`.
- **Errors wrap with context**: `fmt.Errorf("scan %s: %w", path, err)`. Return them; do not log-and-continue
  in library code.
- **Comments explain why, not what.** If a line is non-obvious because of something we measured,
  say what we measured.
- Config from environment, read once into `internal/config`.

## Fanning work out

A session may run subagents in parallel. The win is real, and it is a **reading**
win rather than a building one — which is worth knowing before the first one is
launched, because the building half of this repo is where the collisions are.

**Where it pays.** The context a task starts from is ~3,700 lines before its own
task file is opened: `docs/decisions.md` is 1,180, `internal/api/jellyfin.go` is
1,093, `docs/phase-9.md` is 496. One agent per document is the cheapest speed-up
available here. So are grep-and-audit sweeps across `docs/`, and independent file
edits once the facts are agreed.

**Where it does not, and the ceiling is lower than it looks.** Coupled prose is
one lane and one mind. Measured on [T51](docs/tasks/T51-documents.md):
`README.md` and `docs/architecture.md` are about 60% of that task and describe
**the same pipeline in two notations** — an ASCII fence and a mermaid
`sequenceDiagram` — so splitting them produces two different stories about where
the VPN sits. The realistic ceiling on a documentation task here is ~2x, not Nx.

**Delegated reading returns quotes, not summaries.** A handoff is worth its
length because nothing in it is reconstructable, and an agent that reports "the
probe worked" has destroyed the only expensive thing it was sent for. Ask for
verbatim output, exit codes and byte counts.

### One worktree per agent, and never two builds in one tree

`git worktree add` gives each agent its own `curator.db`, `curator.key`,
`downloads/`, and — the one that actually bites — its own `internal/web/dist/`.
`web/scripts/embed.mjs:17` does `rm -rf` on that directory and rebuilds it in
place, while `//go:embed all:dist` is a **compile-time** directive. A second
`make check` in the same tree therefore walks a directory being deleted under it,
and the outcomes run from a binary carrying half a UI to `internal/web` failing
to compile outright. It is the sharpest edge in the repo and a worktree removes
it completely.

**Do not copy `.env` into a worktree.** A worktree without one is exactly the
state CI runs in, which is why all ten `TestLive*` take their skip there
(`.github/workflows/check.yml`). Copying it back in re-imports every shared
resource below — the tunnel, qBittorrent's `curator` category, the real library.

**The ten are not the whole population**, and T73 found that out the hard way.
`TestTPBLive` (`internal/indexer/tpb_test.go`) and `TestYTSLiveSearchInterstellar`
(`internal/indexer/yts_test.go`) are live tests for the two indexers that need no
credential, so there is no missing variable for them to skip on. They gate on
`-short` and `make check` does not pass it, which means **two tests do reach the
public internet from a runner** — and one of them failed the first three workflow
runs this project ever had, because apibay answers 403 to a GitHub address range.
A third `-short`-gated test outside the ten, `TestFindMovieGivesUpOnAWedgedJellyfin`
(`internal/jellyfin/client_test.go`), is slow rather than networked: it waits out
a timeout against an `httptest` server on loopback.

Since T76 those two share one rule, in `internal/indexer/live_test.go`:
`classifyLiveFailure` decides skip-or-fail for a failed live **search**, not for
a probe, and both tests ask it. A refused caller (403/401/429) or a transport
failure skips; every other non-200, and any decode failure, still fails loudly.
Do not add a third live indexer test with its own private rule — that divergence
is what T76 existed to remove.

**T77 closed the gap T76 measured**: a base URL that has gone NXDOMAIN used to
take the transport branch and skip, so D12's own failure went green under the
guard D12 paid for. It now **fails** — but only once a control name has proved
the machine is online, because `IsNotFound` alone cannot be trusted here. Go maps
`EAI_NONAME`/`EAI_NODATA` to "no such host" (`net/cgo_unix.go:189`) and macOS
answers that way with no network at all, so on a laptop with the WiFi off a dead
host and an offline machine are the **same error value**. The control is the
other indexer's host — `TestTPBLive` asks `movies-api.accel.li` and
`TestYTSLiveSearchInterstellar` asks `apibay.org` — which adds no third party and
makes each test the other's cross-check. A refused host still resolves, so T73's
403 does not disarm it. See
[D42](docs/decisions.md#d42--a-dead-base-url-fails-the-build-but-only-once-a-control-name-proves-the-machine-is-online)
and [`docs/tasks/T77-a-dead-host-fails-loudly.md`](docs/tasks/T77-a-dead-host-fails-loudly.md).

### What a worktree does not fix, because it is global to the machine

- **`docker compose`, anything.** `compose.yaml:30` sets `name: curator`, so a
  second `up -d` reconciles the first agent's containers instead of starting its
  own, and either `down` destroys both. **`COMPOSE_PROJECT_NAME` and `-p` do not
  rescue this**: the volumes are declared `name: curator-data` and
  `name: curator-media` at `compose.yaml:242-244`, and an explicit volume name is
  absolute — it ignores the project prefix. Two agents share one database, one
  key and one library whatever the project is called.
- **Ports.** `defaultPort` is 8090 (`internal/config/config.go:173`) and `Addr()`
  binds the wildcard, not loopback. 8090 and 8099 are already held by
  long-running instances that are not a session's to kill. `PORT` is read from
  the environment only (`config.go:514`), so giving each agent its own is free
  and is the fix.
- **Docker image tags.** `curator-ffmpeg:8.1.1`
  (`scripts/build-ffmpeg.sh:25`) and `ghcr.io/dulsaranethmin/curator:latest` are
  daemon-global names: a build in one agent re-points the tag another is about to
  run.
- **`make clean`.** `Makefile:66` runs `go clean -cache -testcache`, which is
  every agent's cache and not this one's.
- **The tunnel, qBittorrent, and the Pi.** One `wg0.conf` is one private key and
  one peer session, so two `make live-tunnel`s make the endpoint flap — but the
  flap is about the ENDPOINT, not the key. NordVPN issues one NordLynx key per
  account, and a WireGuard session is per *(server, client key)* pair, so two
  boxes on two different servers are two independent sessions. The Pi runs on
  `187.15.101.96` (Singapore #647) precisely because the laptop holds
  `187.15.102.104`, and NordVPN's own recommendation API returns the laptop's
  server first — so a second config has to CHOOSE its endpoint, not accept one.
  The qBittorrent category is `curator` for everyone.
  **The Pi is no longer read-only**: T53 deployed curator to it on 2026-08-18.

### Any timing measured while agents run in parallel is noise

`$GOCACHE` and the test cache are machine-global and content-keyed, so one
agent's `go test ./...` can return another's cached results in milliseconds and
look like a fast run. **Take timings serially or not at all.** A wrong number in
a handoff is worse than a missing one, because the next session builds on it
instead of re-measuring.

**The same cache used to make CI lie**, which is why `make race` passes
`-count=1` (T75). `actions/setup-go` restores `$GOCACHE` between runs, so a
re-run of a green `check` returned `(cached)` for thirteen of twenty packages —
`internal/engine` among them — and passed in 1m58s without executing them. Do
not remove that flag to make `make check` faster; `make test` is the fast one
and is deliberately still cached.

## Git workflow

**Branch first, never commit to `main` directly.** Create the branch before the first commit of a
piece of work and name it for the work — `phase-2-indexers`, not `wip`. Commits land on `main`
through a merge, never by committing to it.

**Nethmin is the author of every commit, always.** Not a co-author — the author. No
`Co-Authored-By` trailer, on any commit, ever, and nothing that credits a model in a message. The
fourteen such trailers in this history are from 2026-08-12, before the rule existed; they are
pushed, and rewriting them is Nethmin's call rather than a session's.

**Merge into `main` when the work is done and the gate is green. Do not push.** The merge is now a
session's to make; `origin` is still Nethmin's alone, and nothing reaches it without him. This
changed on 2026-08-18 — the rule before it was "do not push, and do not merge", and a session
reading an older document will hold branches back for no reason.

Merge with `--no-ff`, so the branch name survives as a merge commit and the task's commits stay
grouped under it. A fast-forward loses the shape of the work.

**A branch may carry as many commits as the work has moves.** The old rule was one commit per task,
and it is retired: splitting a task into "the fix" and "the test that pins it" is clearer than
wedging both into one message, and a task that turns out to be three things should read as three
things. What has not changed is that **every commit stands on its own** — the gate passes at each
one, not just at the tip.

**Say what was merged, and what is waiting to be pushed.** The question at the end of a long day is
no longer "what do I merge" but "what am I about to push", and it is the same shape:

```
merged: t79-download-button, t80-update-from-the-app — main is 4 commits ahead of origin
hold:   nothing else outstanding
```

`git log --oneline origin/main..main` is the answer, and it is the one thing a session must never
resolve on Nethmin's behalf.

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
9. **The git workflow**, in two lines: branch first, the gate per commit, merge with `--no-ff` when
   it is green, **never push**, and Nethmin is the author of every commit — no `Co-Authored-By`.

Written in the imperative, to the next session, not about it. A handoff that says "I implemented the
registry" wastes a line that could have said "the registry holds no defaults, and here is why".

**Every commit builds, vets, tests and cross-compiles on its own**, so a bisect lands on a working
tree rather than on a half-finished phase. That is the gate per commit, not per branch — this
paragraph used to say "one commit per task" and that rule was retired above on 2026-08-18. `make
check` is the gate, and it runs the UI export as well, because since phase 5 the binary embeds it:

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
| Library | `/media/storage/media/movies` — **empty since D43**; 916 GB USB disk with 870 GB free |
| Downloads | `/media/storage/media/downloads` — same filesystem, so imports hardlink |
| TMDB | `TMDB_API_KEY` env var, free key from themoviedb.org |
| minter | `MINTER_URL`, default `http://127.0.0.1:8191` — IPv4 only, so not `localhost` |
| Search | `SEARCH_TIMEOUT` default `30s`, `SEARCH_CACHE_TTL` default `1h` |
| qBittorrent | `QBIT_URL` default `http://127.0.0.1:8080`; `gluetun:8080` in Docker — it has no ports of its own |
| Downloads | `QBIT_USER`/`QBIT_PASS` (unset = downloads off, not a startup error), `QBIT_CATEGORY` default `curator` |
| Import | `DOWNLOADS_PATH` (empty = use `content_path` verbatim), `QBIT_DOWNLOADS_PATH` default `/downloads` |
| Jellyfin | 10.10.7 at `192.168.1.26:8096`. `JELLYFIN_URL`, `JELLYFIN_API_KEY` (unset = no refresh, not a startup error) |

## Known broken upstream

**`qbittorrent` on the Pi is dead and is staying dead.** Exit 255 at `2026-08-18T01:38:35Z` with
`RestartCount` 0, and the cause is structural rather than a fault to fix: it runs
`network_mode: "service:gluetun"`, and **`depends_on` binds `docker compose up`, not the Docker
daemon's restart-on-boot.** The Pi rebooted, the daemon started qbittorrent before gluetun existed,
it could not join a namespace that was not there, and nothing retried — measured, qbittorrent
finished at `01:38:35.131Z` and gluetun started at `01:38:40.298Z`. It is the only `network_mode` in
the file. [D43](docs/decisions.md#d43--the-pi-is-a-clean-slate-television-is-retired-and-curator-is-the-only-downloader)
retires that whole seam rather than repairing it, because curator's tunnel is in-process and has no
equivalent.

The older NordVPN `AUTH_FAILED` that blocked phases 3–4 is long resolved. Container state drifts, so
confirm with `ssh pi 'docker ps'` before relying on any of this.
