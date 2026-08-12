# Roadmap

Six phases. Each ends somewhere useful, and the *arr containers on the Pi keep working untouched
until the last one.

| Phase | What | Status |
|---|---|---|
| **1** | Foundation — skeleton, SQLite, TMDB, library scanner | **done** |
| **2** | Indexers — YTS, TPB, then 1337x through minter | **done** |
| **3** | Downloads — qBittorrent client, magnet dispatch, state polling | **built** |
| **4** | Import — completion watcher, hardlink, rename, Jellyfin refresh | **built** |
| 5 | Interface — Next.js screens, static export embedded via `embed.FS` | **next** |
| 6 | Cutover — run alongside, confirm parity, remove seven containers | |

---

## Phase 1 — Foundation

Go skeleton, SQLite schema, TMDB client, library scanner. Spec in
[`phase-1.md`](phase-1.md); tasks in [`tasks/`](tasks/).

**Done when** `GET /api/movies` returns all 29 movies scanned off disk with metadata attached, and
`GOOS=linux GOARCH=arm64 go build ./...` passes.

**Done, verified 2026-08-12** — 29/29 scanned and matched, rescan idempotent, arm64 build passes.
See [`progress.md`](progress.md).

## Phase 2 — Indexers

The `Indexer` interface with YTS and TPB (both plain JSON), then 1337x through minter. Concurrent
search with `errgroup`, results merged and ranked. A failing indexer is omitted, never fatal.
Spec in [`phase-2.md`](phase-2.md); tasks T8–T12 in [`tasks/`](tasks/).

Most of the 1337x implementation already exists in `internal/indexer/`, absorbed from `cfprobe` in
phase 1 but not yet wired.

Two decisions were settled while specifying this phase: releases are identified by an opaque id
rather than a URL ([D10](decisions.md#d10--releases-are-identified-by-an-opaque-id-not-a-url)), and
ranking is by seeders with quality as a filter rather than part of a score
([D11](decisions.md#d11--rank-by-seeders-quality-is-a-filter-not-a-score)).

**Done when** `/api/search?title=Interstellar&year=2014` returns ranked releases with working
magnets, a second search inside the hour launches no browser, and stopping minter degrades search to
the other indexers rather than erroring.

## Phase 3 — Downloads

qBittorrent Web API client, add-magnet under the `curator` category, state polling into the
`downloads` table. Spec in [`phase-3.md`](phase-3.md); tasks T13–T16 in [`tasks/`](tasks/).

**Done when** an API call puts a torrent into qBittorrent and progress is visible in the database.

Scoping is by category rather than only a tag ([D13](decisions.md#d13--downloads-are-scoped-by-a-qbittorrent-category-with-its-own-save-path)),
with its own save path so that phase 6's side-by-side run cannot have two importers writing one
directory.

> **No longer blocked.** `gluetun` and `qbittorrent` were down on an unresolved NordVPN
> `AUTH_FAILED`; both have been running healthy since 2026-08-12 — confirmed again while specifying
> this phase: qBittorrent 5.1.2, sharing gluetun's network namespace, reachable at `gluetun:8080`
> and requiring authentication on every endpoint.

## Phase 4 — Import

Completion watcher, hardlink into `movies/Title (Year)/`, Jellyfin refresh. The only part that touches
the filesystem, so it is deliberately conservative: category-scoped so it never touches torrents added
by hand, and it never deletes source files. Spec in [`phase-4.md`](phase-4.md); tasks T17–T21 in
[`tasks/`](tasks/).

**Done when** a download completes and the file appears in the library and in Jellyfin unaided, with
`stat` showing link count 2 and `df` unchanged.

Two decisions were settled while specifying: the importer is driven by the poller's existing torrent
list and triggers on a **state** rather than a completion transition, which is what makes it crash
safe ([D14](decisions.md#d14--the-importer-is-driven-by-the-pollers-torrent-list-not-by-a-completion-event)),
and the Jellyfin refresh is best-effort with an optional key
([D15](decisions.md#d15--the-jellyfin-refresh-is-best-effort-and-its-key-is-optional)).

> **Verified locally, never on the Pi.** This is the first phase that writes to disk, and the *arr
> stack keeps serving until phase 6. Hardlinks are proven in `t.TempDir()` by equal inode plus link
> count 2 — `df` unchanged is a weak signal on a copy-on-write macOS temp dir, so that half of the
> "done when" defers to cutover.

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
