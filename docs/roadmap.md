# Roadmap

Ten phases. Each ends somewhere useful.

The plan grew from six to ten while it was being built, and the growth is the interesting part rather
than an overrun. Phases 1–5 were the original arithmetic — replace the \*arr layer on one Raspberry Pi
— and phase 6 was going to be the cutover. It became **Own the download** instead, because
[D22](decisions.md#d22--the-torrent-engine-moves-inside-the-binary-and-qbittorrent-becomes-the-second-backend)
moved the torrent engine inside the binary and changed what curator is: not a manager that drives
other containers, but the thing that does the work. Everything after that follows from it — a tunnel
to own (6), settings to write from a browser rather than a `.env` (7), somewhere to watch (8), and an
install a stranger can run (9). The cutover moved to the end, where it belongs, as phase 10.

| Phase | What | Status |
|---|---|---|
| **1** | Foundation — skeleton, SQLite, TMDB, library scanner | **done** |
| **2** | Indexers — YTS, TPB, then 1337x through minter | **done** |
| **3** | Downloads — magnet dispatch, state polling | **done** |
| **4** | Import — completion watcher, hardlink, rename, Jellyfin refresh | **done** |
| **5** | Interface — Next.js screens, static export embedded via `embed.FS` | **done** |
| **6** | Own the download — the torrent engine and a WireGuard tunnel move inside the binary | **done** |
| **7** | Settings that write — writable config, secrets at rest, one optional password | **done** |
| **8** | Watch it here — direct play, remux, Open in Jellyfin | **done** |
| **9** | One command, and a way to watch on the TV — the image, the release, the bundle | **done** |
| **10** | Cutover: run alongside, prove parity, remove | **done** — executed 2026-08-18 |

`make status` derives this table from the repository rather than from a list somebody has to remember
to update. Where the two disagree, `make status` is right.

## The container arithmetic, which moved twice

The original promise was **"thirteen containers become six"**, and it is quoted in enough places to be
worth stating plainly: *it was wrong twice, in the same direction.*

| | Claim | Why it changed |
|---|---|---|
| Original | 13 → 6 | kept qBittorrent and gluetun as dependencies |
| [D26](decisions.md#d26--television-keeps-its-stack-the-cutover-removes-only-what-curator-replaces-for-movies) | 13 → 10 | television was still in use, so sonarr's stack had to stay |
| [D43](decisions.md#d43--the-pi-is-a-clean-slate-television-is-retired-and-curator-is-the-only-downloader) | **13 → 5** | television retired by choice; D22's engine made qBittorrent and gluetun removable |

**What actually happened on 2026-08-18**: [T54](tasks/T54-remove-what-is-replaced.md) removed nine —
gluetun, qbittorrent, prowlarr, radarr, sonarr, seerr, flaresolverr, byparr, recyclarr — leaving
jellyfin, portainer, watchtower and homepage, plus curator. **Five services, six containers**, because
curator's bundle is curator and minter. It beat the original promise rather than missing it.

None of that is the product's number. A container count on one particular Raspberry Pi is not a
metric anybody else can check — for a stranger with Docker, curator is **one container**, plus
Jellyfin if they want it. That is what `README.md` says and it is the number that generalises.

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

Two decisions were settled while specifying this phase: releases are identified by an opaque id
rather than a URL ([D10](decisions.md#d10--releases-are-identified-by-an-opaque-id-not-a-url)), and
ranking is by seeders with quality as a filter rather than part of a score
([D11](decisions.md#d11--rank-by-seeders-quality-is-a-filter-not-a-score)).

**Done when** `/api/search?title=Interstellar&year=2014` returns ranked releases with working
magnets, a second search inside the hour launches no browser, and stopping minter degrades search to
the other indexers rather than erroring.

**Done, verified 2026-08-12.** One correction outlived the phase: YTS is reached at
`https://movies-api.accel.li/api/v2`, because `yts.mx` went NXDOMAIN
([D12](decisions.md#d12--yts-is-reached-at-movies-apiaccelli-not-ytsmx)). `yts.rs` and `yts.hn`
resolve and look plausible while being clone sites running a re-implemented API.

## Phase 3 — Downloads

qBittorrent Web API client, add-magnet under the `curator` category, state polling into the
`downloads` table. Spec in [`phase-3.md`](phase-3.md); tasks T13–T16 in [`tasks/`](tasks/).

**Done when** an API call puts a torrent into qBittorrent and progress is visible in the database.

Scoping is by category rather than only a tag ([D13](decisions.md#d13--downloads-are-scoped-by-a-qbittorrent-category-with-its-own-save-path)),
with its own save path so that the side-by-side run cannot have two importers writing one directory.

**Done, verified 2026-08-13** against a real qBittorrent 5.1.2. Phase 6 then made this the *second*
backend rather than the only one; it is kept as a migration path, not removed.

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

**Done.** The `df` half deferred to real hardware, because it is a weak signal on a copy-on-write
macOS temp dir — and it was paid on the Pi in phase 10: both paths one inode with `links=2`, and `df`
reporting 802 MB used for an 837 MB file.

## Phase 5 — Interface

Next.js with `output: 'export'`, built to static files and embedded via `embed.FS`. One artifact, one
container, one process. Spec in [`phase-5.md`](phase-5.md); tasks T22–T26 in [`tasks/`](tasks/), then
T27–T31's TMDB-first redesign.

**Done when** the whole flow is drivable from a browser with no hand-written API calls.

Two decisions were settled while specifying: the embed needs `all:` because Next.js hides every asset
under `_next/`, a committed placeholder keeps `go build` working on a fresh clone, and the build
output stays out of git ([D16](decisions.md#d16--the-ui-is-embedded-with-all-and-a-committed-placeholder-keeps-go-build-honest));
and Settings began read-only, so no secret ever reached an unauthenticated LAN page
([D17](decisions.md#d17--settings-is-read-only-and-the-settings-table-stays-unused)) — which phase 7
then reversed deliberately, with encryption underneath it.

> **Shipping becomes two commands.** `npm --prefix web run build` must run before
> `GOOS=linux GOARCH=arm64 go build ./...`, or the binary carries the placeholder. That is a real
> regression against phase 1's single command, accepted in one place and documented in D16.

## Phase 6 — Own the download

The phase that changed what curator is. The torrent engine moves **inside the binary** — pure-Go,
no cgo, so the single cross-compile still ships — and a WireGuard tunnel comes up in-process beside
it. Spec in [`phase-6.md`](phase-6.md); tasks T32–T38.

**Done when** a magnet dispatched from the UI downloads through curator's own engine, over a tunnel
curator brought up itself, and hardlinks into the library — with dispatch refused while the tunnel
is down.

[D22](decisions.md#d22--the-torrent-engine-moves-inside-the-binary-and-qbittorrent-becomes-the-second-backend)
makes qBittorrent the second backend rather than a dependency, and
[D27](decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket) makes the VPN mandatory:
curator can only promise a tunnel for the engine whose socket it owns, which is exactly the argument
for owning it. Only the torrent engine is routed through it — the UI, TMDB, the indexers and Jellyfin
stay on the host's connection, so a tunnel that drops stops downloads instead of locking you out.

**Done, verified 2026-08-14** against a real NordLynx endpoint; one v4-only crash found afterwards
and fixed in T56.

## Phase 7 — Settings that write

The `settings` table [T2](tasks/T2-store.md) created and nothing had ever read finally gets a use:
the backend, the tunnel, the indexers and Jellyfin all become configurable from the UI. Spec in
[`phase-7.md`](phase-7.md); tasks T39–T42 and T55.

Secrets are **encrypted at rest and write-only across the API** — they go in and are never read back
out ([D28](decisions.md#d28--settings-are-writable-secrets-are-encrypted-at-rest-and-write-only-across-the-api)),
which is what makes reversing D17 safe. One optional password gates the whole app, off by default
([D25](decisions.md#d25--authentication-is-optional-and-off-by-default)).

**Done, verified 2026-08-14.** This is the phase that makes the compose file nearly empty: anything
writable from the browser has no business being in a YAML file a stranger is told to edit.

## Phase 8 — Watch it here

The library stops being a list of files and becomes something you can press play on. Direct play
where the browser can, a **remux** where it cannot — rewrapping H.264/AAC out of a container Chrome
refuses, never re-encoding — and Open in Jellyfin for a player that is not a browser. Spec in
[`phase-8.md`](phase-8.md); tasks T43–T46.

**Done.** No transcoding, deliberately: that is Jellyfin's job, and phase 9 makes Jellyfin one
profile away.

## Phase 9 — One command, and a way to watch on the TV

The phase where curator stops being something only we can install. A `FROM scratch` image running as
uid 1000 ([T47](tasks/T47-image.md)), a release pipeline that publishes it
([T48](tasks/T48-release-pipeline.md)), a compose bundle whose first line is the whole product
([T63](tasks/T63-compose.md)), a first-run wizard ([T50](tasks/T50-first-run.md)), and Jellyfin
either provisioned or adopted ([T64](tasks/T64-jellyfin-provisioner.md),
[T66](tasks/T66-adopt-jellyfin.md)). Spec in [`phase-9.md`](phase-9.md).

**Done when** a stranger with Docker runs `docker compose up -d`, opens a browser, and gets from
nothing to watching a film on a device that is not that browser.

Jellyfin and minter are **opt-in profiles**, because a media server nobody asked for arriving with
`up -d` is the shape this bundle was built to refuse
([D34](decisions.md#d34--curator-provisions-a-jellyfin-it-brought-up-and-never-rewrites-one-somebody-is-already-watching)).

**Done.** [T51](tasks/T51-documents.md) — the documents, including this line — was this document's
own honesty check, and it shipped on 2026-08-18: the repository and its ghcr package are public,
`0.2.0` is published, and the quickstart was run from an empty directory against an anonymous pull
**before** it was written down. It was called "the last task in the project" here for two days after
it landed, which is the failure mode a document about honesty is most exposed to.

## Phase 10 — Cutover: run alongside, prove parity, remove

The last phase, and the one every earlier phase deferred to. Back up the \*arr configs first
([T52](tasks/T52-arr-config-backup.md)), stand curator up on the Pi
([T53](tasks/T53-run-alongside.md)), then remove what it replaces
([T54](tasks/T54-remove-what-is-replaced.md)). Spec in [`phase-10.md`](phase-10.md).

**Done when** the box runs curator and nothing has regressed.

**Executed 2026-08-18.** The parity target it was specified around did not survive:
[D43](decisions.md#d43--the-pi-is-a-clean-slate-television-is-retired-and-curator-is-the-only-downloader)
emptied the media disk by choice, so "curator agrees with radarr about 29 films and 16 files" had no
radarr left to agree with. It became *"curator works on the Pi from nothing"* — stand it up, search,
download, import, play — which is a weaker assertion honestly labelled rather than a missing one, and
it passed: one film taken end to end from an empty disk, hardlinked, and played.

---

## Deliberately out of scope

- **TV.** Retired by choice in D43, not deferred. The schema carries `media_type`, so it stays
  additive if that ever changes.
- **Automatic grabbing** — see [D5](decisions.md#d5--manual-search-not-automatic-grabbing). Manual
  search is the design, not a stepping stone.
- **Users and roles** — one optional password, D25. Anything more is a surface nobody has to
  maintain.
- **Transcoding** — remux only. Jellyfin is a profile away and is better at it.
- **The Knaben aggregator** — see [D7](decisions.md#d7--do-not-adopt-the-knaben-aggregator). Recorded,
  not adopted.
