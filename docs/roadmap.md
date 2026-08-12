# Roadmap

Six phases. Each ends somewhere useful, and the *arr containers on the Pi keep working untouched
until the last one.

| Phase | What | Status |
|---|---|---|
| **1** | Foundation — skeleton, SQLite, TMDB, library scanner | **in progress** |
| 2 | Indexers — YTS, TPB, then 1337x through minter | next |
| 3 | Downloads — qBittorrent client, magnet dispatch, state polling | blocked *(see below)* |
| 4 | Import — completion watcher, hardlink, rename, Jellyfin refresh | |
| 5 | Interface — Next.js screens, static export embedded via `embed.FS` | |
| 6 | Cutover — run alongside, confirm parity, remove seven containers | |

---

## Phase 1 — Foundation

Go skeleton, SQLite schema, TMDB client, library scanner. Spec in
[`phase-1.md`](phase-1.md); tasks in [`tasks/`](tasks/).

**Done when** `GET /api/movies` returns all 29 movies scanned off disk with metadata attached, and
`GOOS=linux GOARCH=arm64 go build ./...` passes.

## Phase 2 — Indexers

The `Indexer` interface with YTS and TPB (both plain JSON), then 1337x through minter. Concurrent
search with `errgroup`, results merged and ranked by seeders and quality preference. A failing
indexer is omitted, never fatal.

Most of the 1337x implementation already exists in `internal/indexer/`, absorbed from `cfprobe` in
phase 1 but not yet wired.

**Done when** `/api/search?title=Interstellar&year=2014` returns ranked releases with working
magnets, a second search inside the hour launches no browser, and stopping minter degrades search to
the other indexers rather than erroring.

## Phase 3 — Downloads

qBittorrent Web API client, add-magnet tagged `curator`, state polling into the `downloads` table.

**Done when** an API call puts a torrent into qBittorrent and progress is visible in the database.

> **Blocked.** `gluetun` is exited on the Pi and `qbittorrent` has never started — an unresolved
> NordVPN `AUTH_FAILED`. Nothing before this phase is affected, but this must be fixed before phase 3
> can be verified.

## Phase 4 — Import

Completion watcher, hardlink into `movies/Title (Year)/`, Jellyfin refresh. The only part that touches
the filesystem, so it is deliberately conservative: tag-scoped so it never touches torrents added by
hand, and it never deletes source files.

**Done when** a download completes and the file appears in the library and in Jellyfin unaided, with
`stat` showing link count 2 and `df` unchanged.

## Phase 5 — Interface

Next.js with `output: 'export'`, built to static files and embedded via `embed.FS`. One artifact, one
container, one process. Screens: Search, Releases, Library, Activity, Settings.

**Done when** the whole flow is drivable from a browser with no hand-written API calls.

## Phase 6 — Cutover

Run alongside the *arr stack, confirm parity on real searches, then remove the seven containers.

**Before this phase:** back up `/opt/docker/configs/{radarr,sonarr,prowlarr}` — roughly 230 MB of
indexer definitions, quality profiles and history, currently on an SD card with no copy anywhere.
That is the rollback path.

**Done when** 13 containers are 6 and nothing has regressed.

---

## Deliberately out of scope

- **TV** until the movie path works end to end. The schema carries `media_type` so it is additive.
- **Automatic grabbing** — see [D5](decisions.md). Manual search is the design, not a stepping stone.
- **Authentication** — LAN-only, same posture as the stack it replaces.
- **The Knaben aggregator** — see [D7](decisions.md). Recorded, not adopted.
