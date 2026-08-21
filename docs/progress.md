# Progress

Where the build actually is. [`roadmap.md`](roadmap.md) says what each phase is *for*; this says
what is **done, verified, and outstanding**. Update it when a phase closes or a decision is made.

**Last updated:** 2026-08-21 · **All ten phases are done, and an eleventh is in progress.** Phases
1–9 built and verified on a laptop; **phase 10, the cutover, executed on the Pi on 2026-08-18** — the
nine \*arr-stack containers removed, curator and minter in their place, and one film taken end to end
from an empty disk. `0.2.0` is tagged, published and running on the box, and
[T51](tasks/T51-documents.md) — which this paragraph called "the last task in the project" for two
days after it shipped — landed with it on 2026-08-18. **Phase 11 is television**, opened by
[D48](decisions.md#d48--television-is-additive-a-show-is-a-row-in-movies-and-the-second-library-root-is-opt-in)
on 2026-08-20 and recorded at the foot of this document.

**T81–T87 are still not a phase, and are still not pretended to be one.** They are a single piece of
work with a single target: [D27](decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket)'s
promise that not one byte of download traffic leaves without the VPN. Audited against the dependency
rather than against our own comment, none of it fully held. It is recorded at the foot of this
document rather than in the table above. This paragraph used to give as its reason that *"the roadmap
has no eleventh phase and inventing one would be tidier than it is true"*; the roadmap has one now
and it is television, which changes nothing about the VPN work — the reason was never the absence of
a slot, it was that a piece of work is not a phase because it was large.

The narrative below is a **record**, kept in the order things were found. Where an entry says
something is missing that now exists — "there is still no Play button" in the T44 section is the one
that catches people — it is a record of what was true when that task ran, not a current status.
The table immediately below is the current status; `make status` derives it from the repository.

---

## Phases

| Phase | What | Status |
|---|---|---|
| **1** | Foundation — skeleton, SQLite, TMDB, library scanner | **done** — verified 2026-08-12 |
| **2** | Indexers — YTS, TPB, then 1337x through minter | **done** — verified 2026-08-12 |
| **3** | Downloads — qBittorrent client, magnet dispatch, state polling | **built** — T13–T16 done; dispatch verified 2026-08-13 against a local qBittorrent 5.1.2, never against the Pi's |
| **4** | Import — completion watcher, hardlink, rename, Jellyfin refresh | **built** — T17–T21 done, verified locally 2026-08-12 against a stub and 2026-08-13 against a real download; never run against the Pi |
| **5** | Interface — Next.js screens embedded via `embed.FS` | **built** — T22–T26, then T27–T31's TMDB-first redesign, verified locally 2026-08-13 |
| **6** | Own the download — the torrent engine and a WireGuard tunnel move inside the binary | **built** — T32–T38 done, verified locally 2026-08-14 against a real NordLynx endpoint; one v4-only crash found afterwards and fixed in T56 |
| **7** | Settings that write — writable config, secrets at rest, optional password | **built** — T39–T42 and T55 done, verified locally 2026-08-14; bcrypt's cost on the Pi is still unmeasured (D25, deferred to phase 10) |
| **8** | Watch it here — direct play, remux, Open in Jellyfin | **done** — T43–T46. The Play button landed in T45; T65 gave it a screen |
| **9** | One command, and a way to watch on the TV — the image, the release pipeline, the bundle | **done** — T47–T51 and T62–T66; `0.2.0` published to ghcr and installable by a stranger, and [T51](tasks/T51-documents.md)'s quickstart run from an empty directory against an anonymous pull before it was written down |
| **10** | Cutover: run alongside, prove parity, remove | **done — executed 2026-08-18.** T52 backed the \*arr configs up, T53 stood curator up on the Pi, T54 removed the nine. D43 voided the parity target by emptying the disk first, so it became "curator works from nothing", and it passed |
| **11** | Television — shows, seasons and episodes, opt-in behind `LIBRARY_TV` | **in progress** — T88–T94 built and merged: store, config, TMDB, indexers, library, Jellyfin, importer, API. Scanned and matched end to end against a real library and a real TMDB key on 2026-08-21; the UI is the outstanding half, and no season pack has yet come through the tunnel |

---

## Phase 1 tasks

Each commit builds, vets, tests and cross-compiles on its own, so a bisect lands on one task.

| Task | Package | Commit | Tests |
|---|---|---|---|
| [T1](tasks/T1-scaffold.md) scaffold | `cmd/curator/`, `internal/config/` | `b209627` | 4 |
| [T2](tasks/T2-store.md) store | `internal/store/` | `849b28c` | 15 |
| [T3](tasks/T3-library-scanner.md) scanner | `internal/library/` | `9bb462d` | 15 |
| [T4](tasks/T4-tmdb-client.md) TMDB | `internal/tmdb/` | `ede2413` | 14 |
| [T6](tasks/T6-absorb-cfprobe.md) absorb cfprobe | `internal/indexer/` | `d2057bb` | 20 |
| [T5](tasks/T5-api.md) API | `internal/api/` | `d7c9111` | 12 |
| [T7](tasks/T7-test-fixture.md) fixture | `testdata/library/` | `44be67e` | — |

`internal/indexer` is **absorbed but unwired** — nothing imports it. That is phase 2's job.

---

## What was verified, and how

Run 2026-08-12 against the real TMDB API and the live fixture.

| Check | Result |
|---|---|
| `POST /api/scan` (cold) | `{"scanned":29,"added":29,"matched":29,"unmatched":0}` in 9.0 s |
| `POST /api/scan` (rescan) | `{"scanned":29,"added":0,"matched":0,"unmatched":0}` in 0.016 s |
| `GET /api/movies` | 29, with 29 distinct `library_path` values |
| `GOOS=linux GOARCH=arm64 go build ./...` | passes |
| Pi library | `ssh pi 'ls /media/storage/media/movies \| wc -l'` → 29 |

The 0.016 s rescan is the evidence that a second scan makes **no TMDB calls**: nothing is left with
a NULL `tmdb_id`.

> **These numbers are a record of that run, not what to expect today.** Since
> [D33](decisions.md#d33--a-folder-with-no-film-in-it-is-not-a-movie-the-row-goes-the-folder-stays)
> a folder with no film in it is not a movie, so the same fixture answers
> `{"scanned":2,...,"empty":27}` — 27 of its 29 folders hold only a `.gitkeep`. Re-verifying phase 1
> from scratch should expect that, and `removed`/`missing` beside it.

**The matches were checked for correctness, not just presence.** Storing the folder title means a
wrong match would be invisible from our own API, so all seven 2026 ids were queried back against
TMDB — every one is the right film with a 2026 release date. `Tom Clancy's Jack Ryan - Ghost War`
→ `1380291`, whose canonical title is `Tom Clancy's Jack Ryan: Ghost War`: a colon exactly where the
folder has ` - `, matched on the raw string with the fallback never firing. That is
[D9](decisions.md#d9--query-tmdb-with-the-raw-folder-title) confirmed on live data.

Also verified end to end: the no-key path scans 29 and reports 29 unmatched; `GET /api/movies/{id}`
returns 404 with `{"error": "..."}`; a non-numeric id returns 400; `SIGTERM` shuts down gracefully.

### Re-verify from scratch

```bash
go build ./... && go vet ./... && go test ./...
GOOS=linux GOARCH=arm64 go build ./...

rm -f curator.db* && set -a && . ./.env && set +a && go run ./cmd/curator &
curl -s -X POST localhost:8090/api/scan | jq                      # 29/29/29/0
curl -s localhost:8090/api/movies | jq 'length'                   # 29
curl -s localhost:8090/api/movies | jq '[.[]|select(.tmdb_id==null)|.title]'   # []
curl -s -X POST localhost:8090/api/scan | jq '.added'             # 0
```

`go test -short` skips the one live TMDB test, which also skips when no key is present.

---

## What phase 2 verified, and how

Run 2026-08-12 against the live indexers and the running minter, on port 8095.

| Check | Result |
|---|---|
| `GET /api/search?title=Interstellar&year=2014` | `200`, 111 releases, all three indexers reporting: yts 12→5, tpb 88, 1337x 20 |
| Ranking | seeders descending — 1386 · 994 · 616 · 528 …, and `PROPER.IMAX.1080p.UHD` correctly ranked as **1080p**, not 2160p |
| Dedup on info hash | 113 rows from three indexers → 111, two collapsed to `["yts","tpb"]` keeping the higher seeder count |
| First search | 13.4 s — a browser cleared a real Cloudflare challenge |
| Second search, same hour | **0.73 s**, no browser. `interstellar%20` (case + trailing space) also hits the same entry |
| Lazy magnet, 1337x | `magnet: null` on all 20 rows until picked; resolving one took 13.2 s and returned a 40-char hash with 16 trackers |
| Lazy magnet, YTS/TPB | **0.010 s** — already carried, no request made |
| Expired/unknown id | `410` with `{"error": "release … is no longer available: search again"}` |
| minter unreachable | `200`, 57 releases from the healthy two, 1337x `ok:false` carrying `connection refused` |
| `?quality=2160p` and `?quality=4k` | both 19 releases, all `2160p` |
| Missing/blank `title`, non-numeric `year` | `400` with the phase 1 `{"error": "..."}` shape |
| Phase 1 endpoints | `/healthz`, `/api/movies`, `/api/movies/{id}` unaffected |
| `GOOS=linux GOARCH=arm64 go build ./...` | passes |

The 13.4 s → 0.73 s drop is the evidence that a repeat search launches no browser. It is not 0.00 s
because YTS and TPB are deliberately **not** cached: they cost under a second and caching them would
only make results an hour stale.

A cross-check worth keeping: the 1337x magnet resolved to `89599BF4…B486`, the same info hash apibay
returned for its copy of that release. The two rows did **not** dedupe, which is correct — a 1337x
release has no hash until it is resolved, so it cannot be deduplicated against, and a near-duplicate
row is the honest outcome rather than a bug.

### Re-verify from scratch

```bash
go build ./... && go vet ./... && go test -race ./...
GOOS=linux GOARCH=arm64 go build ./...

go run ./cmd/curator &
curl -s 'localhost:8090/api/search?title=Interstellar&year=2014' | jq '.indexers'
time curl -s -o /dev/null 'localhost:8090/api/search?title=Interstellar&year=2014'   # seconds
time curl -s -o /dev/null 'localhost:8090/api/search?title=Interstellar&year=2014'   # instant
```

`go test -short` skips the live YTS, TPB and TMDB tests; they also skip when the network is
unreachable, but a decode failure or a bad status **fails**, so a dead base URL cannot stay green.

---

## Open threads

Neither blocking, both decided rather than forgotten.

- **`cfprobe` is not deleted**, though [T6](tasks/T6-absorb-cfprobe.md) step 6 says to. That repo has
  **no git remote** — all three commits are local-only, so the directory is the only copy — and T6
  deliberately does not port `cmd/fp` or the uTLS transport. Deleting it would destroy the tooling
  that produced [D2](decisions.md#d2--fetch-pages-through-the-browser-do-not-reuse-cookies)'s JA3 /
  JA4 / HTTP-2 measurements. The numbers survive here; the ability to re-run them would not. Push it
  somewhere before deleting.
- **`/opt/docker/backups/` on the Pi is still empty.** Backing up
  `/opt/docker/configs/{radarr,sonarr,prowlarr}` — roughly 230 MB, currently on an SD card with no
  copy anywhere — is the rollback path for phase 6. Deferred because it writes to the Pi, and the
  *arr stack is meant to be untouched until cutover.

## Phase 2 tasks

Ownership here was per **file**, not per directory — T8, T9 and T10 shared `internal/indexer/` with
phase 1's ported code, which was read-only to them. T8, T9 and T10 ran in parallel, in isolated
copies of the repo so that three agents writing into one Go package could not compile each other's
half-written files; each copy was diffed against the origin before its owned files were merged back.

| Task | Owns | Tests | Status |
|---|---|---|---|
| [T8](tasks/T8-yts-indexer.md) YTS | `internal/indexer/yts.go` | 12 | done |
| [T9](tasks/T9-tpb-indexer.md) TPB | `internal/indexer/tpb.go` | 14 | done |
| [T10](tasks/T10-search-cache.md) cache | `internal/indexer/cache.go` | 14 | done |
| [T11](tasks/T11-aggregator.md) aggregator | `internal/indexer/aggregate.go` | 16 | done |
| [T12](tasks/T12-search-api.md) API | `internal/api/search.go`, config, wiring | 12 + 3 config | done |

### What phase 2 learned

- **`yts.mx` is gone.** NXDOMAIN on every resolver and from the Pi. The live API is
  `https://movies-api.accel.li/api/v2`, which the API's own payload names as its migration target —
  [D12](decisions.md#d12--yts-is-reached-at-movies-apiaccelli-not-ytsmx).
- **apibay's empty-search sentinel is real** and is rejected **structurally**, on the info hash being
  absent or all-zeros, rather than by matching the string `"No results returned"`. A reworded sentinel
  cannot resurrect it, and a genuine release that happens to be named that is still kept.
- **Every apibay field is a JSON string**, `size` included (bytes, not `"2.3 GB"` — so `parseSize` is
  the wrong tool there). A test unmarshals the fixture into an `int64` struct and asserts it *fails*,
  so the day apibay switches to real numbers, a test says so instead of the parser silently zeroing.
- **A `Cache` is deliberately not a `MagnetResolver`.** Go method sets are not conditional, so
  forwarding `ResolveMagnet` would make a cache around YTS claim to resolve magnets it never has. The
  aggregator therefore unwraps decorators before asserting — without that, wrapping 1337x in the
  cache, which is exactly what ships, silently disables lazy resolution. It is a test, not a comment.
- **The year must not go in YTS's `query_term`.** `Interstellar 2014` works only because the year is
  in `title_long`; `Interstellar 9999` returns nothing. YTS filters on `movie.year` instead — unlike
  1337x, where `searchQuery` deliberately does append the year.
- **YTS clamps `seeds` at 100**, so its rows plateau in a seeder-ranked list.
- **`errgroup.WithContext` was avoided**, as specified: each goroutine records its own error and
  returns nil, so one downed indexer cannot cancel its siblings.

### Still live going out

- **minter runs natively here, not in Docker** (a `python3` process on 8191), so `docker stop minter`
  in the verification block does not apply on this laptop. Degradation was verified instead by
  pointing a second instance at a dead port, which is the same connection-refused from curator's side.
- **TPB's magnets carry some dead trackers.** `tpb.go`'s list is a faithful copy of The Pirate Bay's
  own `print_trackers()`, and four of its hosts no longer answer a BEP-15 connect. Harmless: it is a
  superset of the six live trackers YTS's magnets use, and clients ignore the rest. Left as-is because
  matching the site's own list is defensible; worth pruning if a magnet ever looks slow to start.
- **`NewTPB(nil)` falls back to `http.DefaultClient`, which has no timeout**, unlike `NewYTS` and
  `tmdb.New`. Not reachable from `cmd/curator`, which passes a shared client with a 15 s timeout.
- **apibay caps a response at 100 rows** with no pagination, so a broad title is truncated at source.

---

## What phase 3 verified, and how

Run 2026-08-12 against the running binary and the live indexers. **One check is
outstanding** and is called out below rather than quietly omitted.

| Check | Result |
|---|---|
| Unconfigured `POST /api/downloads` | `503` — `{"error":"downloads are not configured: set QBIT_USER and QBIT_PASS"}` |
| Unconfigured startup | serves normally, logs the warning, and **starts no poller** |
| `GET /api/downloads` empty | `[]`, never `null` |
| Blank `title`, malformed body | `400`, and the dispatcher is never reached |
| Unknown or expired `release_id` | `410` — "search again and pick from the new results" |
| Real release, qBittorrent unreachable | **`502`**, and `GET /api/downloads` still returns **zero rows** |
| Poller under a dead qBittorrent | 18 consecutive failed ticks logged, loop still running |
| `SIGTERM` | `download poller stopped` then `shutting down`; port released |
| Phase 1 and 2 endpoints | `/api/movies`, `/api/search` unaffected (77 releases for Dune) |
| `go test -race ./...` | passes; 105 tests across the packages phase 3 touched |
| `GOOS=linux GOARCH=arm64 go build ./...` | passes |

The 502-with-zero-rows result is the one worth keeping. A real release was picked out of a live
search, dispatched against a qBittorrent that was not there, and the downloads table stayed empty —
the ordering guarantee observed end to end rather than only against a fake.

**Outstanding: a dispatch against the real qBittorrent.** It needs the Web UI password for `nethmin`,
which is not in `.env`. Everything up to the add is verified; what remains unproven is that
qBittorrent accepts our magnet under the `curator` category, that the confirmation-by-hash finds it,
and that the poller then moves a real download's progress. Until that runs, **phase 3 is built, not
verified.**

```bash
# with QBIT_USER and QBIT_PASS in .env
id=$(curl -s 'localhost:8090/api/search?title=Interstellar&year=2014' | jq -r '.releases[0].id')
curl -s -X POST localhost:8090/api/downloads \
  -d "{\"release_id\":\"$id\",\"title\":\"Interstellar\",\"year\":2014}" | jq
curl -s localhost:8090/api/downloads | jq '.[] | {release_name, state, progress}'
```

---

## Phase 3 tasks

Specified 2026-08-12, no code yet. Spec in [`phase-3.md`](phase-3.md). The `downloads` table has
existed since [T2](tasks/T2-store.md), so this phase needs **no migration**.

| Task | Owns | Tests | Status |
|---|---|---|---|
| [T13](tasks/T13-qbit-client.md) qBittorrent client | `internal/qbit/` | 22 | done |
| [T14](tasks/T14-downloads-store.md) downloads store | `internal/store/downloads.go` | 20 | done |
| [T15](tasks/T15-download-poller.md) dispatch + poller | `internal/download/` | 16 | done |
| [T16](tasks/T16-downloads-api.md) API | `internal/api/downloads.go`, config, wiring | 12 | done |

**Measured on the Pi while specifying, so the implementation does not have to guess:**

- **qBittorrent 5.1.2** (`lscr.io/linuxserver/qbittorrent`), Web API v2, sharing gluetun's network
  namespace — it has no ports of its own, so it is `gluetun:8080` in Docker and `192.168.1.26:8080`
  from a laptop.
- **Authentication is required on every endpoint** — an unauthenticated call is `403`, not `401`.
  The session is an `SID` cookie, and an expired session is indistinguishable from a wrong password.
- **Mount is `/media/storage/media/downloads` → `/downloads`**, so every path qBittorrent reports is
  in its own namespace. Phase 3 stores them verbatim; the translation belongs in phase 4's hardlink.
- **Existing categories are `radarr` and `sonarr`.** Curator gets its own with its own save path
  ([D13](decisions.md#d13--downloads-are-scoped-by-a-qbittorrent-category-with-its-own-save-path)).

**Traps the tasks call out as tests rather than comments:**

- **`torrents/add` returns `200 Ok.` whether or not it accepted the magnet**, and never returns a
  hash. The add is confirmed by looking the torrent up by `indexer.InfoHash` afterwards — the same
  class of lie as minter's 200-carrying-a-failure.
- **Nothing is written to the database until qBittorrent confirms.** A row written first would claim
  a download no client has heard of, every time qBittorrent is down.
- **qBittorrent's hashes are lower-case and `indexer.InfoHash` is upper-case.** Unnormalised, every
  lookup silently misses.
- **`imported` stays phase 4's to set.** A completed torrent is a file, not a library entry.

## Phase 4 tasks

Specified and built 2026-08-12. Spec in [`phase-4.md`](phase-4.md). **No migration** — the
importer is driven by the poller's existing torrent list, so it needs no new column and no second
loop ([D14](decisions.md#d14--the-importer-is-driven-by-the-pollers-torrent-list-not-by-a-completion-event)).

| Task | Owns | Commit | Tests | Status |
|---|---|---|---|---|
| [T17](tasks/T17-library-link.md) naming + hardlink | `internal/library/link.go` | `a9f8b22` | 20 | done |
| [T18](tasks/T18-import-store.md) import transaction | `internal/store/imports.go` | `3db068e` | 10 | done |
| [T19](tasks/T19-jellyfin-client.md) Jellyfin client | `internal/jellyfin/` | `7a330df` | 10 | done |
| [T20](tasks/T20-importer.md) importer | `internal/importer/` | `6459044` | 16 | done |
| [T21](tasks/T21-import-wiring.md) wiring | config, `cmd/curator`, poller hook, API | `bae7aca` | 20 | done |

Two decisions were settled while specifying:
[D14](decisions.md#d14--the-importer-is-driven-by-the-pollers-torrent-list-not-by-a-completion-event)
(state, not transition — the crash-safe trigger) and
[D15](decisions.md#d15--the-jellyfin-refresh-is-best-effort-and-its-key-is-optional) (the refresh is
best-effort and its key optional).

### Measured on the Pi while specifying, so the implementation does not have to guess

Read-only, 2026-08-12. Nothing was written to the Pi.

| Measurement | Value |
|---|---|
| qBittorrent's file modes | **`0644`**, owner `nethmin:nethmin`, directories `0755` |
| The library's own file modes | **identical** — `0644 nethmin:nethmin` on the *arr stack's hardlinks |
| Downloads and library filesystem | both device **`2049`**, so [D8](decisions.md#d8--import-by-hardlink)'s hardlink still holds |
| `Session\TempPath` | `/downloads/incomplete/` — but `Session\TempPathEnabled` is **absent**, so it takes qBittorrent's default of false; the directory is empty and inert |
| Categories on the Pi | `radarr` → `/downloads/complete/movies`, `sonarr` → `/downloads/complete/tv`. **No `curator` category exists yet** |
| Jellyfin | `jellyfin/jellyfin:10.10.7`, healthy, 28 h uptime |

**The modes are the one that mattered.** A hardlink is a second name for one inode and has no
permission bits of its own, so it inherits the source's — and a `chmod` on the link changes the file
qBittorrent is still seeding. Had qBittorrent been writing `0600`, every import would have produced a
library entry Jellyfin cannot open: a film visible in the UI that silently does not play, with the
obvious fix being the one that corrupts the seeding copy. It writes `0644`, which is already exactly
what the library's existing files are, so **the importer needs no `chmod` and must not add one.**

**Two of the three measurements could not be taken.** The real `content_path` and `save_path` for a
`curator` torrent need `torrents/info?category=curator`, which needs a session, which needs
`QBIT_USER` and `QBIT_PASS` — still absent from `.env`. The category does not exist on the Pi yet
either, because nothing has ever been dispatched into it. Rather than guess, phase 4 is designed not
to depend on the answer: `content_path` is treated as **either a file or a directory**, the importer
only ever looks at torrents whose state maps to `completed` (and phase 3 already maps `moving` to
`downloading`, so a torrent mid-relocation never qualifies), and a wrong path leaves the row
`completed` for the next tick to retry.

### What phase 4 verified, and how

Run 2026-08-12, entirely locally. **Nothing in this phase has ever touched the Pi**, which is the
point: it is the first phase that writes to disk and the *arr stack keeps serving until phase 6.

The real binary was driven end to end against a **stub qBittorrent and a stub Jellyfin**, with a real
60 MB file in a real directory — the honest substitute for a live run, since the credentials phase 3
is still waiting on are the same ones this would need.

| Check | Result |
|---|---|
| A completed torrent, unaided | became `movies/Interstellar (2014)/Interstellar (2014).mkv` |
| Hardlink, proof 1 | source and destination share **inode 57419359**, link count **2** |
| Hardlink, proof 2 | bytes written through the **source** read back through the destination |
| Mode | `0644` on both names — the link inherits it, and nothing chmods |
| `downloads` row | `state: "imported"`, `completed_at` set |
| `movies` row | `status: "imported"`, `library_path` at the folder, `imported_at` set, `size_bytes` 62914560 |
| Path translation | `/downloads/complete/curator/…` → the local root, applied at the importer's boundary |
| Jellyfin | refreshed **once per import**, three times across ~40 ticks |
| `POST /api/downloads/{hash}/import` | `200` with the movie row; `404` for an unknown hash |
| **Rescan after the import** | `{"scanned":1,"added":0}` and **one** movie row |
| Phase 1–3 endpoints | `/healthz`, `/api/movies`, `/api/movies/{id}`, `/api/downloads` unaffected |
| `go test -race ./...` | passes |
| `GOOS=linux GOARCH=arm64 go build ./...` and `go vet ./...` | both pass |
| Every commit alone, in a detached worktree | builds, vets, tests and cross-compiles |

**The rescan is the result worth keeping.** `added: 0` with one movie row is
[D9](decisions.md#d9--query-tmdb-with-the-raw-folder-title)'s folder-name round trip holding on live
data rather than only over the fixture: the importer wrote a folder name that `UpsertMovieByPath`
then matched against the row it had just created. Had the colon rule been wrong by so much as a
double space, this would have said `added: 1` and the library would show the film twice.

**`df` unchanged was not checked**, deliberately. It is a weak signal on a copy-on-write macOS temp
dir, where the numbers move for unrelated reasons, so the laptop substitute is equal-inode-plus-link-
count-2 above and the `df` half of the roadmap's "done when" defers to the Pi at phase 6.

### Still live going out

- **A permanently failing import retries every poll interval, for ever.** That is
  [D14](decisions.md#d14--the-importer-is-driven-by-the-pollers-torrent-list-not-by-a-completion-event)
  working as designed — the failures that actually happen (a torrent still moving, a full disk, an
  unmounted library) all fix themselves. What is suppressed is the repeat **log**, per hash and per
  distinct message; there is no backoff and no retry counter, because both would add state and a
  timer to solve what is only noise.
- **`adoptTwin`'s third-row `tmdb_id` check is unreachable today.** `tmdb_id` is `UNIQUE`, so a
  wanted row holding an id is itself proof nothing else holds it. The guard is kept as defence in
  depth — the failure it prevents is an import whose hardlink is already on disk failing on a
  constraint, then failing identically for ever — and `TestATMDBIDCannotBeContested` asserts the
  constraint that makes it unreachable, so the day `tmdb_id` stops being `UNIQUE` a test says so.
- **The live Jellyfin test is written and skipped.** A refresh mutates Jellyfin. Phase 6 enables it
  by deleting one statement.
- **A torrent whose content path holds several videos imports only the largest**, and logs how many
  others it saw. A genuine double feature is therefore visible but half-imported by design; nothing
  in the schema can represent two files for one movie row.
- **`movies.quality` is still the scanner's column.** The importer does not guess it from a release
  name.

### Phase 3's outstanding verification was not run

`QBIT_USER` and `QBIT_PASS` are still not in `.env` — it carries `TMDB_API_KEY`, `LIBRARY_MOVIES` and
`PORT` and nothing else. The live dispatch block in [`phase-3.md`](phase-3.md#verification) is
therefore still unrun and **phase 3 remains built, not verified**. Nothing was added to the `radarr`
or `sonarr` categories and nothing was deleted.

## Phase 5 tasks

Specified 2026-08-13, no code yet. Spec in [`phase-5.md`](phase-5.md). Phases 1–4 are merged to
`main` and pushed.

| Task | Owns | Commit | Status |
|---|---|---|---|
| [T22](tasks/T22-embed-ui.md) embed and serve | `internal/web/`, the mount in `cmd/curator` | `1121beb` | done |
| [T23](tasks/T23-settings-api.md) settings endpoint | `internal/api/settings.go` | `c44ec2d` | done |
| [T24](tasks/T24-ui-shell.md) shell and build wiring | `web/` scaffold, API client, nav | `87cbb8f` | done |
| [T25](tasks/T25-search-screens.md) Search → Releases | `web/app/search/` | `a13b29c` | done |
| [T26](tasks/T26-library-screens.md) Library, Activity, Settings | the other three routes | `90c6d4b` | done |

Two decisions were settled while specifying:
[D16](decisions.md#d16--the-ui-is-embedded-with-all-and-a-committed-placeholder-keeps-go-build-honest)
(`all:` on the embed, a committed placeholder, and the build output stays out of git) and
[D17](decisions.md#d17--settings-is-read-only-and-the-settings-table-stays-unused) (Settings is
read-only and no secret reaches the browser).

**Two choices confirmed rather than assumed.** Next.js `output: 'export'` was kept as the roadmap
planned it, over the lighter alternatives, and the Settings screen is read-only status rather than an
editor. The `settings` table created in [T2](tasks/T2-store.md) has still **never been read or
written** by anything, and stays that way.

### What phase 5 costs, stated up front

`GOOS=linux GOARCH=arm64 go build ./...` stops being the whole build: `npm --prefix web run build`
has to run first, or the binary carries the placeholder page. That is a real regression against
phase 1's one-command deploy and it is accepted in exactly one documented place
([D16](decisions.md#d16--the-ui-is-embedded-with-all-and-a-committed-placeholder-keeps-go-build-honest)).
`go build ./...` alone still compiles, still passes every test, and still serves a complete API.

### Measured while specifying

- **Node v22.21.1, npm 11.12.1** on this laptop. Nothing on the Pi needs either — the binary ships
  built.
- **Nine endpoints exist**, and the five screens need exactly one more: `GET /api/settings`.
  Everything else is already there and was verified against live services in phases 1–4.
- **`.gitignore` already carried `/web/out/`** from phase 1, which is now the wrong path: `go:embed`
  cannot reach outside its own package, so the export lands in `internal/web/dist/` instead.

### What phase 5 verified, and how

Run 2026-08-13 against the real binary with the fixture library, the live indexers and the running
minter.

| Check | Result |
|---|---|
| Every screen by URL | `/`, `/search/`, `/library/`, `/activity/`, `/settings/` all **200**, with and without the trailing slash |
| `_next/` assets | **200**, `Cache-Control: public, max-age=31536000, immutable` |
| HTML documents | `Cache-Control: no-cache` |
| Unknown path | **404** carrying the export's own page, **not** the app |
| `/healthz`, `/api/*` | unaffected by the UI mounted at `/` |
| Cold scan through the UI's endpoint | `{"scanned":29,"added":29,"matched":29,"unmatched":0}` — see the D33 note above; it is 2 films and 27 empty now |
| Library data as rendered | 29 movies, 29 with posters, **27 zero-size**, 0 unmatched — those 27 rows no longer exist |
| Live search | **111 releases in 14.6 s** — yts 5, tpb 88, 1337x 20 |
| Ranking | seeders descending, 1386 · 1085 · 616 |
| Lazy magnets | **20 of 111** come back `null`, all 1337x, exactly as D10 intends |
| `GET /api/settings?probe=1` | tmdb reachable, minter reachable, qbittorrent and jellyfin unconfigured with no `reachable` field |
| Fresh clone (`dist/` moved aside) | `go build`, `go test` and the arm64 cross-compile all still pass |
| `git status` after `npm run build` | clean — the export is ignored, the `.gitkeep` survives |
| `go test -race ./...` | passes |

**Not verified: how it looks.** Everything above is the served markup and the API beneath it —
headings, nav links, copy and status codes. Nobody has actually looked at the rendered pages in a
browser, so layout, spacing, colour and the dark-mode palette are unreviewed. That is the one part
of this phase a test genuinely cannot cover, and it wants human eyes.

### What phase 5 learned

- **`//go:embed dist` would have shipped a blank page.** `go:embed` drops names beginning with `_`,
  and Next.js puts every script and stylesheet under `_next/`. The binary compiles and serves
  `index.html` either way, so the failure is invisible until it is deployed. `all:dist` is asserted
  by reading the source, because on a fresh clone there is no export to test against.
- **The 15.x Next line carries security advisories.** npm flagged the version this started on, and
  the whole line turned out to be affected — three high-severity findings across `next`, `postcss`
  and `sharp`, fixed only in 16.x. It is on **`next@16.3.0`** and `npm audit` reports zero. These are
  build-time only and nothing but static files reach the Pi, but starting a toolchain on known
  advisories is a bad habit to form on the phase that introduces the toolchain.
- **Next 16's type checker found a real bug on the first build.** `error && <X/>` where `error` is
  `unknown` has type `unknown`, which is not a `ReactNode`. `typescript.ignoreBuildErrors` stays
  false.
- **`-race` found a real race in a test**, not in the handler: the settings probe counters were
  plain ints incremented from the concurrent probe goroutines.
- **Search and Releases are one route, not two.** The task file named
  `web/app/releases/` as its own screen; a release id is only valid while its search is cached, so a
  separate page would have to re-search and get different ids, or stash results and pretend. Recorded
  as a deviation rather than built as a route that lies about where its data came from.

## The TMDB-first redesign — T27–T31

Specified and built 2026-08-13, after the first real download recorded
`title='avengers', year=0` and produced three failures from one row. Spec in
[`phase-5.md`](phase-5.md#the-tmdb-first-redesign--t27t31). **No migration** — `movies.tmdb_id` has
been nullable and unique since [T2](tasks/T2-store.md), and `POST /api/downloads` has always
accepted a `tmdb_id` it had never been sent.

| Task | Owns | Commit | Status |
|---|---|---|---|
| [T27](tasks/T27-tmdb-browse.md) TMDB browsing | `internal/tmdb/` — `get`, `Details`, four methods | `7141bdd` | done |
| [T28](tasks/T28-query-title.md) query normalisation | `internal/indexer/query.go`, one line of `aggregate.go` | `9bb462d` | done |
| [T29](tasks/T29-library-by-tmdb.md) library index | `internal/store/movies.go` | `600dafd` | done |
| [T30](tasks/T30-browse-api.md) browse endpoints | `internal/api/browse.go`, wiring | `a97b3cb` | done |
| [T31](tasks/T31-browse-ui.md) the screens | `web/` — Discover, cards, movie page | `b591997` `2009e1d` `9a0130b` `c5fd94d` `fbe7dbc` | done |

T31 is five commits rather than one. The first is the `<Releases>` extraction, with no behaviour
change, so that the movie page and the release-name fallback could not grow two implementations of
dispatch; the last is two fixes found by driving the result. Each of the five builds, vets, tests
and cross-compiles on its own, checked in five detached worktrees.

Two decisions were settled while specifying:
[D20](decisions.md#d20--the-film-comes-from-tmdb-the-search-box-only-finds-it) (the film comes from
TMDB, and the canonical title is not the query title) and
[D21](decisions.md#d21--the-movie-page-is-movieid-because-the-ui-is-a-static-export) (the movie
page is `/movie/?id=`, forced by the static export).

### What the redesign verified, and how

Driven in a browser 2026-08-13 against the live TMDB, the live indexers, the running minter and a
local **qBittorrent 5.1.2** in Docker — the same version the Pi runs. The Pi was not touched.

| Check | Result |
|---|---|
| `/search/?q=avengers` | 20 cards with **Doomsday among them rather than silently chosen** — the measurement D20 turns on |
| Library badges on cards | Endgame **downloading**, Infinity War **in library** — both derived, neither read off `movies.status` |
| Card click | reaches `/movie/?id=299536`, and that URL is **reloadable in a fresh tab** — the static export resolving `/movie/` with the query intact, `internal/web` unchanged |
| Movie page load | **no indexer request**: `/logs` carries only the three startup lines |
| `?id=` missing, `abc`, `NaN` | empty state, and **zero `/api/` requests** on the network panel — no `/api/tmdb/movies/NaN` |
| `?id=999999999` | `404` — "tmdb movie 999999999: no such movie", the API's own message. **Corrected in place 2026-08-21 (T96):** the sentinel behind that sentence is now `"tmdb: no such title"` — T89 retitled it because one error covers two media types. The status, the reason and the reading are unchanged; only the words are, and this row is left as it was written because it is a record of what was seen that day |
| **Find releases** on `Avengers: Endgame` | **124 releases · yts 7 · tpb 100 · 1337x 20** — the canonical colon title, and 1337x answering 20 rather than 0 |
| One cold run exceeded `SEARCH_TIMEOUT` | 1337x `timed out after 30s`; 104 releases rendered anyway and the partial-results banner named the failure |
| Dispatch | `movies` row 30 — `tmdb_id 299534`, `title "Avengers: Endgame"`, `year 2019` |
| Import | folder **`Avengers - Endgame (2019)/Avengers - Endgame (2019).mp4`**, single-spaced |
| Hardlink | inode **57988283**, link count **2**, shared with the download |
| A second dispatch for the same film | reused `movie_id 30` rather than making a twin |
| No `TMDB_API_KEY` | Search **opens in release mode and says why**, its film toggle disabled; Discover renders "Not configured"; the counters still read 30 / 1 / 0 |
| A film with no release date (tmdb `1495483`) | no year, no runtime, no poster — the fallback rather than a broken image |
| Light and dark palettes | both legible over a backdrop; the hero scrim is the page's own background at 90%, so the flip needs no second rule |
| Every commit alone, in a detached worktree | builds, vets, tests and cross-compiles |

**The folder is the result worth keeping.** `Avengers - Endgame (2019)` with `movies.tmdb_id` set is
the exact row whose absence caused all three of D20's failures: the yearless import that retried for
ever, the scan that matched **Avengers: Doomsday (2026)**, and a folder named after a search box.
The colon reached `DestFolder` intact because nothing on the client stripped it — `NormaliseQuery`
strips it server-side, above the cache, for the indexers only.

This also runs the check [phase 3](phase-3.md#verification) listed as outstanding: qBittorrent
accepted the magnet under the `curator` category, confirmation-by-hash found it, and the poller
moved a real download from `queued` through `downloading` to `imported`. Against a local container,
not the Pi's — phases 3 and 4 stay "never run against the Pi" until cutover.

### Still live going out

- **A dead magnet sits in qBittorrent as `metaDL`, and its row stays `queued` in Activity for ever.**
  The 1337x `GalaxyRG` release advertises 2,508 seeders and its magnet carries only long-dead
  trackers (`coppersurfer.tk`, `leechers-paradise.org`, `zer0day.to`); qBittorrent is healthy beside
  it with 362 DHT nodes and a second torrent that finished at 2.2 MB/s. Nothing in curator is wrong
  and nothing in curator will time it out — there is no notion of a download that never starts.
- **The year-0 download button was not driven.** Its sentence is a prop on `<Releases>` and the
  branch is shared with the release-name path, but no undated film that also has releases turned up
  to press it against. Rare by construction, and unproven.
- **Nobody has looked at the pre-redesign screens since.** Library, Activity, Settings and Logs were
  glanced at rather than reviewed; the Library grid was confirmed still correct after `.movie`
  became a link for the TMDB grids, and nothing else was re-checked.
- **The counters strip, the rails and the hero are one reviewer's taste.** Phase 5's "not verified:
  how it looks" is now half-answered — the new screens have been seen — and the rest still wants
  human eyes.

## Phase 6 tasks

Specified 2026-08-13, built 2026-08-13/14. Spec in [`phase-6.md`](phase-6.md). Phases 1–5 are merged
to `main` and pushed; this is on `phase-6-own-the-download` and is not.

| Task | Owns | Commit | Status |
|---|---|---|---|
| T32 spike: the engine | throwaway, deleted | — | done, gate passed |
| T33 spike: the tunnel | throwaway, deleted | — | done, gate passed |
| [T34](tasks/T34-torrent-type.md) backend-neutral torrent | `internal/torrent/` | `ccca4c9` | done |
| [T35](tasks/T35-embedded-engine.md) the embedded engine | `internal/engine/` | `209b218` | done |
| [T36](tasks/T36-resume-stall.md) resume and stall | `internal/engine/`, `internal/download/` | `175aa52` | done |
| [T37](tasks/T37-tunnel.md) the tunnel | `internal/vpn/` | `d74a5a5` | done |
| [T38](tasks/T38-wiring-subtraction.md) wiring and subtraction | `cmd/curator`, `internal/config` | `26dc937` | done, **less the `Add` collapse** |

Two decisions were written while specifying:
[D22](decisions.md#d22--the-torrent-engine-moves-inside-the-binary-and-qbittorrent-becomes-the-second-backend)
(the engine moves in, qBittorrent stays as a second backend with a sunset criterion) and
[D27](decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket) (the VPN is mandatory,
curator owns the socket, and what the external path can and cannot promise).

## What phase 6 verified, and how

Driven on the laptop 2026-08-14, against a payload seeded over loopback and the local
qBittorrent 5.1.2 container. Never against the Pi.

| Check | Result |
|---|---|
| dispatch → complete → **hardlink** | both films imported; **inode shared with the download, link count 2** |
| the library copy's mode | **`-r--r--r--`** — the engine finishes files `0444` and a hardlink carries the source's bits |
| restart with **no peers and no network** | complete in **55 ms, 0 bytes re-downloaded**, from the persisted metainfo |
| a file placed in the data directory by hand | in no row and in no library folder — structural, because the engine only holds what curator added |
| `TORRENT_BACKEND=embedded` start-up | engine up, no tunnel, warned about it, `/api/settings` says "curator downloads it itself, in this process" |
| `TORRENT_BACKEND=qbittorrent` dispatch | **refused, 503**, naming the shared exit address `187.14.240.8`. The wording was corrected afterwards: equality proves the client leaves by curator's own route, not that it has no VPN — this laptop is itself on NordVPN, so that container *was* tunnelled, just not by anything curator chose ([D27](decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket)) |
| an unknown `TORRENT_BACKEND` | start-up error naming both valid values |
| peak RSS / peak Go heap on a real 755 MB download | **817.6 MB / 33.4 MB**, heap flat throughout ([D22](decisions.md#d22--the-torrent-engine-moves-inside-the-binary-and-qbittorrent-becomes-the-second-backend)) |
| every commit alone | builds, vets, tests with `-race`, and cross-compiles to `linux/arm64` |

Then the tunnel, 2026-08-14, against a real NordVPN (NordLynx) endpoint in Singapore — chosen in a
different country from the one this laptop's own NordVPN app was using, so the two exits could not
be confused for each other:

| Check | Result |
|---|---|
| handshake with `sg701.nordvpn.com` | **up**, rx 92 B / tx 180 B, in well under a second |
| exit address **through** the tunnel vs the host's | **187.15.102.106** vs **187.14.240.8** — it changes where traffic leaves from |
| DNS and HTTPS inside the tunnel | both, against a real Linux endpoint — none of T33's Docker-Desktop checksum artifact |
| the **engine** over the tunnel | metadata in **6.0 s**, then **9.5 MB from 5 peers in 12 s**, with `DisableTCP`, `DisableUTP` and `NoDHT` set — the client has no socket of its own to leak through |
| the binary, `TORRENT_BACKEND=embedded` + `VPN_CONFIG_FILE` | `vpn tunnel up` then `torrent engine started tunnelled=true`; `/api/settings?probe=1` reports **torrents and vpn both reachable** |

**The first run of that crashed, and it was our bug.** `ListenPacket("udp", ":0")` handed netstack a
`net.UDPAddr` with a nil IP — what ":0" means everywhere else in Go — and gvisor does not reject
that, it **panics**: `invalid protocol number = 0`, four frames inside its transport layer. It is
the exact call the engine makes at start-up, so the embedded-plus-tunnel path would have panicked on
boot for anybody who configured one. An unspecified host now becomes the tunnel's own address.
Nothing hermetic could have caught it: there was no tunnel to bind to.

**The hardlink is the result worth keeping.** It is phase 4's proof — equal inode, link count 2 —
re-run against a backend phase 4 never saw, through the real store, the real poller and the real
importer. The `0444` in that table is the measurement that retires
[`phase-4.md`](phase-4.md)'s "qBittorrent writes 0644, so the importer needs no chmod": the mode
changed, the conclusion (no chmod needed) survives, because a readable file is all Jellyfin wants.

**The first run of that check failed, and the failure was the right one.** The payloads were 1–2 MB,
and `library.FindFeature` refused them: `DefaultMinFeatureBytes` is 50 MiB, the sample-file guard
phase 4 built. It fired against the new backend exactly as designed. The check was re-run with
60 MB and 55 MB payloads.

### Still live going out

- **"Kill the tunnel mid-download and confirm traffic stops" is still unrun**, and T82-T84 have
  changed what it would be evidence for rather than answering it. The claim used to rest on
  construction alone — `DisableTCP`, `DisableUTP` and `NoDHT` leave the client no socket of its own,
  measured as zero in T33. It now also rests on three paths that measurement never covered
  (WebTorrent, the DHT's bootstrap DNS and UPnP), on a fourth that opened host sockets whenever
  `VPN_REQUIRED` was on with no tunnel, and on a watchdog that holds downloads when the tunnel stops
  proving out. All of that is verified by tests and by reading anacrolix's source; none of it is a
  packet capture, and D27's promise is about packets. It is now
  [T85](tasks/T85-the-capture-that-settles-it.md) rather than a line in this list.
- **Only one provider, one server, one run.** NordLynx against a Singapore endpoint, once. Nothing
  says how this behaves on a provider with a different MTU, an IPv6-only address, or a config
  carrying `Table`/`PostUp` directives the parser ignores.
  **[T56](tasks/T56-udp-tracker-panic.md) is the first evidence that this reaches further than
  expected**: the v4-only `Address` line was not just untested breadth, it was a crash on the normal
  download path, and it survived two phases because the only magnet ever run live carried an
  `http://` tracker.
- **Dispatch from the UI was not re-driven end to end.** The release-id path from search to
  `POST /api/downloads` is phase 2 and 3 code, unchanged here, and driving it to completion would
  mean downloading a real film. Everything downstream of the row — resume, engine, poller, importer,
  hardlink — was driven with a seeded payload instead.
- **The `Add` collapse is not done.** T38's fifth step, a pure refactor of dispatch's
  add-then-confirm into one call, is written up in its task file as outstanding rather than dropped.
- **`TORRENT_BACKEND` defaults to `embedded`, which changes an existing `.env`.** A deployment with
  `QBIT_USER` set now starts the engine instead, and refuses to dispatch until `VPN_CONFIG` is set
  or `VPN_REQUIRED=false` is typed. Intended, and worth knowing before the first Download press.
- **Nothing has run on the Pi**, on purpose. Phase 10, after T52 backs up the *arr configs.

## Phase 7 tasks

Specified and built 2026-08-14. Spec in [`phase-7.md`](phase-7.md). Each task's own file carries what
it shipped beyond its sketch and what it measured live — those sections are the detail, and they are
not repeated here.

| Task | Owns | Status |
|---|---|---|
| [T39](tasks/T39-settings-store.md) the settings store | `internal/settings/`, `internal/store/settings.go`, `migrate.go` | **done** — `092968c` |
| [T40](tasks/T40-settings-api.md) settings becomes read/write | `internal/api/settings.go`, wiring | **done** — `e01a0fc` |
| [T41](tasks/T41-auth.md) optional authentication | `internal/api/auth.go`, wiring | **done** — `6966d2a` |
| [T42](tasks/T42-settings-screens.md) the Settings screens | `web/app/settings/`, `web/lib/api.ts` | **done** — `cb20a4d` |
| [T55](tasks/T55-stall-reason.md) the stall reason reaches the screen | `torrent`, `store`, `download`, `api`, `web` | **done** — `e56561a` |
| [T56](tasks/T56-udp-tracker-panic.md) a `udp://` tracker stops taking the process down | `internal/engine/network.go` | **done** — `beb5147`, verified live with both binaries against one row |

Three decisions were written while specifying:
[D25](decisions.md#d25--authentication-is-optional-and-off-by-default) (authentication is optional
and off by default, which rewrites the roadmap's out-of-scope bullet that D17, D18 and D19 all
cite), [D28](decisions.md#d28--settings-are-writable-secrets-are-encrypted-at-rest-and-write-only-across-the-api)
(settings are writable, secrets encrypted at rest and write-only across the API — D17's threat model
survives, its environment-only conclusion does not) and
[D29](decisions.md#d29--a-written-setting-applies-at-the-next-start-the-password-applies-at-once)
(a written setting applies at the next start; the password applies at once).

**T55 is numbered outside the plan on purpose.** The plan reserved T43–T54 for phases 8–10, so a
task found afterwards takes the next free number rather than displacing one other documents cite. It
is [T36](tasks/T36-resume-stall.md)'s deferred stall *reason*, which needs a column, which is what
makes it the first passenger of the repo's first migration.

**T56 is phase 6's bug, found by phase 7's live run.** T55's first attempt never reached the
listener: a `udp://` tracker in a resumed magnet panicked the process at boot, because anacrolix
splits one `udp://` announce into `udp4://` **and** `udp6://` and a NordLynx tunnel has no v6
address to give the second one — and curator's error for that went to a `panicif` on a function that
returns nothing. **Every real indexer magnet carries `udp://` trackers**, so the case that crashed
was the normal one; phase 6 missed it for two phases because `live_test.go`'s Debian magnet carries
a single `http://` tracker, the one scheme that never asks for a packet socket.
[D30](decisions.md#d30--a-tunnel-announces-only-in-the-families-it-has-and-never-hands-the-library-an-error-it-panics-on)
records both halves: announce only in the families the tunnel has, and never hand the library an
error it panics on.

**This phase needs a migration, and it is the first.** Five phases shipped without one on a real
argument — `schema.sql` is all `IF NOT EXISTS` and every column phases 3–6 needed already existed.
`downloads.reason` does not, and `CREATE TABLE IF NOT EXISTS` silently does nothing to a table that
already exists. The mechanism arrives now rather than in phase 9 because from phase 9 there are
databases this repo has never seen, and a mechanism introduced with one nullable column is cheaper
than one introduced under pressure.

### Still live going out

- **bcrypt's cost on the Pi has never been measured.** Tests use `MinCost`, production uses the
  default, and [D25](decisions.md#d25--authentication-is-optional-and-off-by-default) defers the
  number to phase 10 on purpose — tuning a cost against a laptop is how you ship one that is wrong
  for the hardware. The serialising mutex means the number is a login latency, not a throughput
  ceiling.
- **The real NordLynx `.conf` has never gone through the settings textarea.** What was pasted was a
  structurally identical file with random keys. The parser is the same one `VPN_CONFIG_FILE` uses,
  so this is breadth rather than a gap in the mechanism.
- **A stall reason CLEARING has never been watched live.** Proven hermetically in three places; the
  live run saw a reason appear and not a torrent recover from one.
- **The writer still refuses a secret when the codec is nil** — a database restored without its key.
  The screen renders `unreadable` with a repair sentence, and no real unreadable row has ever been
  seen.
- **The documented dev workflow does not work in a browser.** `next dev` on `:3000` against the API
  on `:8090` is cross-origin and there is **no CORS header anywhere in the Go code**, so every UI
  check has to run against the embedded build. It is not to be fixed with a permissive CORS block:
  the API now carries a session cookie, which makes that a CSRF surface and a decision record of its
  own.

---

## Phase 8 tasks

Specified 2026-08-14. Spec in [`phase-8.md`](phase-8.md).

| Task | Owns | Status |
|---|---|---|
| [T43](tasks/T43-stream.md) the stream endpoint | `internal/api/stream.go`, `internal/library/contain.go`, the ticket in `auth.go` | **built** 2026-08-14 |
| [T44](tasks/T44-remux.md) remux | `internal/remux/`, the ffmpeg build | **built** 2026-08-14 |
| [T45](tasks/T45-player.md) the player and the Jellyfin link | `web/app/movie/`, `internal/jellyfin` | **built** 2026-08-15 |
| [T46](tasks/T46-subtitles.md) the subtitle sidecar | `internal/importer`, `internal/api/stream.go`, `internal/library/subtitles.go` | **built** 2026-08-15 |

Two decisions were written while specifying:
[D24](decisions.md#d24--playback-remuxes-and-never-transcodes) (playback remuxes and never
transcodes — the container is what actually breaks, and a Pi 5 does not re-encode 1080p in software
in real time) and
[D31](decisions.md#d31--the-stream-is-behind-the-same-password-and-a-ticket-is-how-a-player-carries-it)
(the stream is behind the same password as everything else, and a ticket — signed with the cookie's
own key, valid for one path, dead when the password changes — is how VLC carries it).

**The one thing measured while specifying, because it is a bug that would only appear in phase 9.**
Go's builtin MIME table (`mime/type.go:53-70`) contains **no video extension at all**. On this Mac
`mime.TypeByExtension(".mkv")` answers `video/x-matroska`, and it does so only by reading
`/etc/apache2/mime.types`; phase 9's image is `FROM scratch`, where none of the four files Go looks
in exists, every answer becomes `""`, and `http.ServeContent` falls back to sniffing. **Sniffing an
MKV gives `video/webm`**, measured, because Matroska and WebM share the EBML magic — so a stream
endpoint that let Go decide would work perfectly on this laptop and mislabel every file in the
image. T43 carries its own four-entry table.

**Phase 8 is where the plan's parallel branch finally runs.** It has depended on nothing since day
one — only the library, which has existed since phase 1 — and what phases 6 and 7 changed is not what
it builds but what it must be careful about: a password now sits in front of `/api/*`, and a
`<video>` carries a cookie where VLC does not.

### T44, and the number phase 9's image budget is built on

**The minimal ffmpeg is 2,232,152 bytes — 2.2 MB, stripped, static, linux/arm64.** Budget was 20 MB
and 40 MB was the abort line, so the fallback of shipping direct play alone never came near being
needed. ffmpeg 8.1.1, built by [`scripts/build-ffmpeg.sh`](../scripts/build-ffmpeg.sh) in an Alpine
container: `--disable-everything`, then the `matroska`/`mov`/`avi` demuxers, the `mp4` muxer, the
`file` and `pipe` protocols, and the parsers and bitstream filters a stream copy needs. **No
encoders, no filters, no network protocols** — `--disable-network` is in the flags, so the binary
curator ships is incapable of opening a socket.

For scale: Homebrew's general-purpose ffmpeg 8.1.1 on this Mac is a 441 KB launcher against ~40 MB of
shared libav\*; the static 78 MB general-purpose build is what D24 refused. 2.2 MB is what is left
when the only thing a binary can do is copy streams between four containers.

**Measured against the real film, not a fixture.** `Backrooms (2026).mp4` is h264 + aac, so it direct
plays and is not a test case — the case had to be built, and it was built out of the film itself:
`ffmpeg -c copy -t 600 -f matroska` gives a 241,659,312-byte `.mkv` with the same streams inside a
container browsers refuse, which is exactly what D24 says the remux is for. Through the arm64 binary,
`-c copy` produced 241,767,352 bytes — **0.04 % container overhead, h264 and aac untouched** — and the
whole 10 minutes remuxed in about 4 seconds of wall clock. `-ss 300` on the 600-second file yielded
301 seconds of output, so the `?start=` seam T45 needs is real and is effectively instant.

**ffmpeg is not quiet at `-loglevel warning`, and that is why the tail is bounded.** One remux of that
`.mkv` wrote **274,201 bytes of stderr** — Matroska stores no DTS, so every B-frame produces an
`Invalid DTS … replacing by guess` or a `Non-monotonic DTS` line. Unbounded and at info level that is
the whole of `/api/logs` filled by one film ([D18](decisions.md#d18--the-log-tail-is-readable-without-authentication-so-it-is-redacted-at-the-source)
made that buffer a product surface). curator keeps the last 4 KB and logs it **only on a non-zero
exit**: across a live session of about ten remuxes, three of them refused by the cap, **curator wrote
8 log lines in total**.

**Verified live on 8097, against the embedded build.** `POST .../playback` returned `remux_url`;
`GET .../remux` answered `200` with `Accept-Ranges: none`, `Transfer-Encoding: chunked` and **no
`Content-Length`**; `?start=` refused `abc`, `-5`, `NaN`, `1e999` and `90 -c:v libx264` with `400`
apiece while an empty one still played. **The subprocess dies with the request**: ffmpeg was observed
running under a reading client, and gone within three seconds of that client being killed, with no
log line — a closed tab is the ordinary case and is not a failure. Three slow readers held all three
slots, the fourth got `503` with `Retry-After: 30`, and killing the three gave the slots back. **VLC
3.0.23 decodes the live stream**: `mp4 demux`, `videotoolbox decoder: Using Video Toolbox to decode
'h264'`, playing from `?start=300`.

**One defect the live run found that no test had.** The cap's `503` went through `s.fail`, which logs
anything past 500 at **ERROR** — so a cap doing exactly its job put a red line in the tail. It is a
`Warn` of its own now, and a test asserts no `ERROR` entry is produced by a refusal.

**What T44 did NOT establish, on purpose.** *A browser has not played a remux.* Chrome 151 renders its
native media player for the `/remux` URL and answers `probably` to
`canPlayType('video/mp4; codecs="avc1.64001f, mp4a.40.2"')`, but the automated tab is `hidden` and
Chrome throttles media preload in a hidden tab, so no frame was decoded there. That check belongs
with T45 anyway — **there is still no Play button**, and the `error`-event fallback chain it verifies
does not exist yet. Two things to carry into it: `canPlayType('video/x-matroska')` answers **`maybe`**
in Chrome 151, which is precisely why phase 8 refuses to keep a codec-support matrix; and a hidden
`<video>` pointed at an `.mkv` fired `loadstart, stalled` and then **nothing at all in 20 seconds — no
`error` event** — so a fallback chain that waits on `error` alone can wait for ever.

### T46, and the release that had been waiting since phase 4

**The subtitles for Backrooms have been sitting in the downloads folder since phase 4 and are now in
the library.** The importer hardlinked the feature and nothing else; it links the sidecars too, and
one run of the real importer against the real content path placed all five, **every inode equal to
its source and every link count 2** — the same proof phase 4 gave for the feature, because it is the
same `library.Link` and [D8](decisions.md#d8--import-by-hardlink) binds it identically.

```
Backrooms.2026.1080p.WEBRip.x264.AAC5.1-[YTS.GG - YTS.BZ].srt  →  Backrooms (2026).srt
Subs/English.srt                                               →  Backrooms (2026).en.srt
Subs/SDH.eng.HI.srt                                            →  Backrooms (2026).en.sdh.srt
Subs/Latin American.spa.srt                                    →  Backrooms (2026).es.srt
Subs/Saudi Arabia.ara.srt                                      →  Backrooms (2026).ar.srt
```

**The rename is the feature, not a tidiness.** `Subs/2_English.srt` is associated with nothing until
it is named off the film; once it is, three players that are not curator read it without being told.
Measured in one of them: **VLC 3.0.23 auto-detected all five**, the exact-stem one at priority 4 and
the four language-suffixed ones at priority 3, all loaded as SPU streams.

**`hi` is both a flag and a language, and the real release ships both readings.** YTS's folder holds
`Subs/SDH.eng.HI.srt`, where `hi` marks hearing-impaired — and `hi` is Hindi's ISO 639-1 code, which
is how a Hindi subtitle is named everywhere. No table settles that; the token in front of it does.
It is a flag when a language precedes it and the language otherwise, and it normalises to `sdh`.

**The task file's own verify fixture collides**, and the collision is the right answer.
`Movie.en.srt` beside `Subs/2_English.srt` both name English, so both want `Title (Year).en.srt` —
the first wins and the second is a warning, exactly as the same task file's other bullet requires.
Both behaviours have a test; the "two sidecars land" one uses a French second file.

**The containment argument is a closed table, and that is worth stating precisely.** A sidecar's
destination is the *feature's* stem plus an ISO code out of curator's table plus a flag out of
another plus a known extension, so no part of a filename a release group chose reaches the path.
`AssertInside` stays as the guard that keeps that true rather than as what the safety rests on, and
the test asserts the property head-on: a sidecar named `x..%2F..%2Fetc%2Fpasswd.en.srt` lands as
`Title (Year).en.srt` with nothing written outside the library.

**Verified live on 8097, against the embedded build, in a visible Chrome 151.** Five `<track>`
elements with the right labels and `srclang`s; `text/vtt; charset=utf-8`; **Chrome's own WebVTT
parser accepted 1,826 cues** out of the converted `.en.sdh.srt` — the whole file, not a prefix — and
the caption rendered over the film at 30:06 of 1:50:28 on direct play. 119,695 bytes on disk became
111,680 served, which is the cue numbers going away. **The subtitle path wrote nothing to
`/api/logs` across the whole session.**

**Subtitles survive the fallback to remux**, observed rather than designed: the track list comes off
the playback response and not out of the container, so the same five stay attached when
`<video src>` re-points at `/remux`.

**The visible-tab trap caught this task too, and it is worth repeating because it looks like a
bug.** Play pressed while the tab was `hidden` ran the whole chain to the remux — Chrome throttled
the direct-play preload, the player's 12-second cap fired, and the fallback did exactly what it
should. The same click in a visible tab direct-plays a 6,628-second film with no banner. **A
throttled preload is not a codec refusal**, and it still does not close
"direct-fail-then-remux-succeeds in one run", which needs a browser that genuinely refuses a
container.

**What could not be verified, and why.** *Jellyfin has not seen these files and cannot.* Jellyfin's
library is the Pi's `/media/storage/media/movies` and curator's local library is this laptop's
`~/curator-local/movies` — different disks. Checked read-only: **the Pi's library holds zero sidecar
subtitles**, so there is nothing there to compare against either. Putting a file on the Pi is phase
10, after T52. VLC is the substitute measurement and it is the same convention.

## T57–T60 — the library is a list of films you can watch

Out-of-band work, in the T55/T56 convention: no phase owns it, and **T47–T54 stay unclaimed** for
phases 9 and 10. Four commits on `t57-library-way-in`, each green under `make check` alone.

| | |
|---|---|
| [T57](tasks/T57-library-way-in.md) | a folder with no film in it is not a movie — the scanner uses `FindFeature`, and every scan removes the rows that no longer describe one ([D33](decisions.md)) |
| [T58](tasks/T58-delete-outside-root.md) | a `library_path` outside the root stops blocking its own deletion |
| [T59](tasks/T59-already-have.md) | `POST /api/downloads` refuses a film curator already has, with 409 |
| [T60](tasks/T60-library-way-in-web.md) | the card opens the film, and the film's page leads with watching |

### What the live run measured

Port **8097**, embedded build, against a **copy** of `./curator.db` — the first change that deletes
rows automatically. `LIBRARY_MOVIES=~/curator-local/movies`, which holds one film.

| Check | Result |
|---|---|
| First scan | `{"scanned":1,"added":0,"matched":0,"unmatched":0,"empty":0,"removed":28,"missing":0}` in **13 ms** |
| The film that is really there | kept its row — `added: 0`, `id` unchanged at 3, size and `tmdb_id` intact |
| The 28 stale rows | removed, each logged with `why="its library_path is outside LIBRARY_MOVIES, so it can never be served"` |
| `testdata/library/movies/` | **still 29 directories** afterwards. Rows only |
| Second scan | `removed: 0` — idempotent |
| A film whose file was deleted between scans | `{"scanned":1,"empty":1,"removed":1}`, logged `why="there is no film in that folder"`, **and the folder is still on disk** |
| `POST /api/downloads` with `tmdb_id` 1083381 | **409** — *"curator already has this film, at …/Backrooms (2026) — delete it first to replace it"* |
| The same for a film curator does not have | past the gate; 503 from the unconfigured downloader, which is the pre-existing answer |
| Library screen | one card, "1 on disk", the card's root is an `<a href="/movie/?id=1083381">` with Delete as its **sibling** — no `<button>` inside an `<a>` |
| Clicking the card | opens `/movie/?id=1083381` |
| The hero | `▶ Watch here` and `Open in Jellyfin` side by side, and **no "Find releases"**; the Releases section names the path and links to the Library |
| `▶ Watch here` | **direct play**, first frame in ~9 s, `1924×1040`, `readyState` 4, five subtitle tracks, on `/api/movies/3/stream` — the remux never entered it |

**The tab was visible for the playback measurement, and that took work.** `document.hidden` was
**true** on the first two checks even with `document.hasFocus()` true, because the tab was in a
window whose *active* tab was something else. Chrome throttles media preload in a hidden tab and the
player's 12 s cap then falls the chain through to remux, which looks exactly like a codec refusal —
so the reading was retaken after selecting the tab with `osascript` and asserting
`document.hidden === false`. Activating the application is not enough; the tab has to be the active
one in its window.

**The database copy has to include the WAL.** The first attempt copied `curator.db` alone and ran
against a stale snapshot — 29 `testdata/` rows and no live one — which produced a plausible-looking
`removed: 29` for the wrong reason. `curator.db-wal` was 951 KB and two days newer than the main
file. Copy `curator.db`, `curator.db-wal` and `curator.db-shm`, or the run is against a database
nobody has.

### Not this task's, found while running it

**`go test -race ./...` has an intermittent data race in `TestLiveEngineOverTunnel`** — third-party,
in `wireguard/tun/netstack.(*netTun).Close()` racing gvisor's `WriteNotify` on the same channel when
a packet is in flight as the tunnel closes. Seen once under a full-suite run and **not reproduced in
six targeted runs**, three on this branch and three on `main` at `e211dc0`, so it is pre-existing and
load-dependent rather than anything these commits introduced. It only fires when `.env` supplies a
`VPN_CONFIG_FILE`, because that is what un-skips the live test.

## Corrections made to the docs

Recorded so the reasoning is not lost.

| What | Where |
|---|---|
| Six → **seven** 2026 releases in the fixture | `CLAUDE.md`, `decisions.md` D9, `T4`, `T7` |
| Phases 3–4 no longer blocked — `gluetun` and `qbittorrent` are healthy | `CLAUDE.md`, `roadmap.md` |
| `.gitignore`'s unanchored `curator` also matched the `cmd/curator/` **directory**, silently excluding the command from git | `.gitignore` |
| T45 was still listed as `specified` in the phase 8 table after it had been built and merged | `progress.md` |
| T46's verify fixture — `Movie.en.srt` beside `Subs/2_English.srt` — produces one sidecar and not two, because both name English | `tasks/T46-subtitles.md` |

---

## T82–T85 — a kill switch that can be seen, and proved

The requirement D27 wrote down was never only that downloads are protected. It is that **not one
byte** of download traffic leaves without the VPN, that downloads stop when the tunnel fails, and
that somebody can **see** which of those is true. Audited against `anacrolix/torrent@v1.61.0` rather
than against D27's own comment, none of the three fully held.

**T33's "zero OS sockets" was true and is still true.** It measured the peer, tracker and DHT
sockets, and those have never leaked. It was never a statement about the paths anacrolix opens
without asking a dialer, and nobody had gone looking for those.

| What | Where it went instead | Carried |
|---|---|---|
| WebTorrent / WebRTC | its own `websocket.Dialer`, then WebRTC data channels | **payload** |
| DHT bootstrap DNS | the host resolver, eight names, refreshed every 5 min for the life of the process | metadata |
| UPnP | SSDP multicast out of every real interface | an announcement |
| The engine itself, with no tunnel and `VPN_REQUIRED` on | host sockets and a **DHT node on the household connection**, every boot | **payload** |

**One cause, and it is the useful sentence: `cfg.hermetic` hardened the TEST configuration in ways
the production one never got.** `internal/engine` even documents the DHT behaviour and fixes it for
tests only. No existing test could have failed, because every test was already behind the fix. See
[D47](decisions.md#d47--every-torrent-network-operation-is-tunnel-bound-or-disabled).

### What is now true

- Every torrent network operation is tunnel-bound or disabled, and the DHT bootstrap **resolves
  through the tunnel** rather than being switched off, so cold-start peer discovery survives.
- `VPN_REQUIRED` with no tunnel builds **no engine at all**, rather than one bound to the host.
- A `Sentinel` re-proves the tunnel on a timer — 15 s locally, and an exit check every 5 minutes only
  while something is downloading or the last verdict was bad, so an idle box makes none.
- A failing verdict **holds** every torrent, without dropping it, and the Activity screen names the
  tunnel instead of blaming the swarm. `Resume` at boot asks the guard first.
- The **VPN screen** shows the verdict as an AND of four independently-read facts, and the exit
  address is withheld unless a password is in front of it.
- `VPN_REQUIRED` applies at once. Its one limit is stated on the screen: it applies to the check and
  cannot conjure an engine that was never started.

### Measured on this laptop, against a real NordVPN tunnel

| Check | Result |
|---|---|
| boot with the handshake not yet complete | downloads **held**, stated rather than warned about |
| the handshake landing | the next 15 s tick **released** them |
| `inside_tunnel` | **true**, from two independent reads — the engine's socket `10.5.0.2` and the tunnel's own address |
| the exit address with no password | **absent from the body**; the page draws `203.0.113.x` |
| `PUT vpn_required=false` | applied **without a restart**; `/api/settings` reports `restart_required: false` and nothing pending |
| `VPN_REQUIRED` set in the environment | the switch draws **read-only** and names the variable, rather than answering 409 |

### T85 — the capture, run, and what it found

Run on the Pi on 2026-08-19 against a real 2.4 GB download over a real tunnel, beside the pinned
0.2.0 on its own port. Jellyfin untouched.

| Reading | Result |
|---|---|
| sockets held by the process, before vs during a download | **identical** — the WireGuard socket, the listener, and the exit check. 70 MB arrived between the two and **no new socket appeared** |
| 45 s on the wire during a fast stretch | **101,043 packets, 99,563 of them (98.5%) WireGuard to the endpoint** |
| DNS on the host resolver | **eight tracker hostnames**, which is [T86](tasks/T86-a-tracker-name-is-a-leak.md) |
| after T86, three minutes of downloading | **not one tracker or DHT name** — only the exit check |

**A fifth leak, and only the capture could have found it.** The first four were read out of
`anacrolix.ClientConfig`. This one is `net.ResolveUDPAddr` inside a dependency, on a path whose socket
was correctly bound the whole time — so the config was right, the socket was right, and the name went
out in clear. `LookupTrackerIp` is anacrolix's hook for it and is declared and called nowhere in
v1.61.0.

**Two facts about the Pi the handoffs did not carry.** There is a **`nordlynx` interface at
`100.108.251.229/10`** left over from the retired gluetun stack — not the default route, `nordvpn
status` says *Disconnected*, and the host is **not** tunnelled, which is the only reason a capture on
`wlan0` means anything. And **every NordVPN Singapore server publishes the same WireGuard public
key**, so a second config beside the Pi's needs only a different `Endpoint` line.

### Still live going out

- ~~**The tunnel has still never been torn down under a live download.**~~ **Done** — this is
  [T87](tasks/T87-the-tunnel-torn-down-under-load.md), below. It was carried unrun through four
  phases and is the last thing D27 was owed.
- **`internal/remux`'s `TestTheCapRefusesTheNextOneAndFreesItsSlot` is flaky on this laptop**, and it
  is not this work's doing: measured at roughly one failure in three on `56ec0f3`, before any of it.
  It waits ten seconds for a cancelled ffmpeg to return. `make check` is therefore not reliably green
  here on the first run, which is worth knowing before anybody reads a red gate as a regression.

---

## T87 — the tunnel torn down under a live download

The acceptance test [D27](decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket) has
owed since phase 6. Line 640 of this document carried *"kill the tunnel mid-download and confirm
traffic stops"* unrun through four phases, and [T85](tasks/T85-the-capture-that-settles-it.md) said
in its own "Not done here" that the capture was not it. Full detail in
[T87](tasks/T87-the-tunnel-torn-down-under-load.md).

**How you kill a peer, since the first two ways do not work.** Repointing the peer's endpoint at a
black hole does nothing — an endpoint is where we *send*, and inbound packets match on key index
rather than source address, so the far end went on streaming and the device counter climbed
**39 MB in the thirty seconds after the repoint**. Removing the peer works but leaves a device that
has never handshaken, so the verdict is `waiting` and the 180 s the guarantee is written in is never
exercised. **Swapping the device's private key** is the one that reproduces reality: the peer stays,
every keypair expires, and `lastHandshakeNano` is left alone — a tunnel up, configured, handshaken in
the past and carrying nothing, which is exactly a VPN server that has stopped answering.

### Measured, on a real NordVPN Singapore endpoint

| Reading | Result |
|---|---|
| bytes across 20 s **before** the kill | **61,770,060** |
| bytes across 20 s **after** the kill | **0** |
| held at | **3m0s**, naming the tunnel: *"the last handshake with 187.15.103.112:51820 was 3m2s ago, longer than the 3m0s a peer will accept a keypair for"* |
| released, and resumed | at **0.0623**, having been held at 0.0623 |

**The floor is a few KB rather than zero, and that is the point rather than a concession.** Both ends
go on trying to rekey a session that is gone and WireGuard's own protocol traffic counts as received —
320 B across 20 s on one run, 0 B on another. A floor of "nothing at all" fails on protocol noise; a
percentage would grow with the leak it exists to catch.

It ran on a **second config**, one `Endpoint` line changed, which is T85's finding that every NordVPN
Singapore server publishes the same WireGuard public key. Four minutes, because 180 s is
`REJECT_AFTER_TIME` and shortening it would measure something else.

### One thing a person reads, which was wrong

A dead tunnel blamed **cloudflare**. `Verdict.Detail` carried `err.Error()` from the exit check, and
that string is not a diagnostic — `Sentinel` hands it to `Engine.Hold` and the Activity screen renders
it verbatim, so a VPN server that stopped answering produced *"…asking
https://www.cloudflare.com/cdn-cgi/trace through the tunnel: context deadline exceeded"* under the
stalled row. It named a third party as the culprit, put the check URL on a screen with no
authentication in front of it ([D18](decisions.md#d18--the-log-tail-is-readable-without-authentication-so-it-is-redacted-at-the-source)),
and withheld the diagnosis `Check` had already reached two lines earlier. T83's claim is that a held
download names the tunnel instead of blaming the swarm; this named cloudflare instead, which is the
same failure wearing a different coat. **Only the live teardown could have found it** — every unit
test in that package asserts on `State`, not on the sentence.

### The three tests the T82–T84 plan specified and never shipped

Fourteen of seventeen were written and nothing recorded the other three as dropped. All three are
here: the in-process teardown (the seam is the `PacketConn`, not the `Conn` — `DialContext` carries
trackers and webseeds and no payload at all, so a fake that closed dials would have proved nothing),
what the client *ends up holding* rather than what it was asked for, and a `/proc/self/fd` snapshot
that matches every socket gained **by inode** against what the `Network` handed out.

All three were verified by **reintroducing the leak they are aimed at**: with `cc.DisableTCP = false`
restored in `bindConfig`, the whole 1 MB arrives over TCP with the tunnel carrying not one byte. The
`/proc` test is Linux-gated and was measured in a `golang:1.25` container — **sixteen executions, no
failure**.

### Still live going out

- **A socket inventory cannot retire T85.** A snapshot sees only sockets still open when it is taken,
  and the fifth leak — a tracker name through `net.ResolveUDPAddr` — opens and closes a UDP socket
  inside one stdlib call. That whole class stays invisible to any inventory, which is why looking at
  the wire remains its own instrument.
- **The dialer half is not assertable.** anacrolix keeps `cl.dialers` unexported with no accessor, so
  the teardown test is the only thing that would catch a fallback dialer.

---

## Phase 11 — television, additive and opt-in

[D48](decisions.md#d48--television-is-additive-a-show-is-a-row-in-movies-and-the-second-library-root-is-opt-in)
landed **2026-08-20** and spent the hook [D6](decisions.md#d6--tmdb_id-is-nullable) left in the
schema in phase 1 — *"a `media_type` column defaulting to `'movie'` is included from the start so TV
is additive later"*. Nothing had ever written anything but `'movie'` to that column until now.

D43 is **not** overturned by this and is not edited. Its own sentence is why it could not have been:
*"the series are deleted and television is retired deliberately, not because the dependency analysis
changed… there is no longer a sonarr to protect."* It retired a **stack**, not a **capability**.

### Measured while building it, so nobody pays for these twice

| | |
|---|---|
| TMDB id sequences **overlap** | Severance is **tv id 95396**, and a film holds **movie id 95396**. `tmdb_id` is `UNIQUE` at table level and `migrate.go` cannot relax a constraint, so `tmdb_tv_id` is a second nullable column with its own UNIQUE index — the first thing the migration mechanism has grown that is not a column |
| `apibay.org/q.php?q=severance&cat=205,208` (2026-08-20) | **100 rows**, season packs *and* single episodes. `Severance - Season 1 - Mp4 x264 AC3 1080p` **844 seeders**; `Severance S02E05 … WEB-DL` **381**. 98 of the 100 state no year at all |
| Narrowing that query | `q=severance s02` → **8 rows**; `q=severance season 2` → a different **4**; and the **727-seeder** `Severance - Season 2` pack is in **neither**. apibay matches keywords against the release name, so the two spellings are two subsets of one thing — the season is read back off each name instead |
| The live TV check inside `TestTPBLive` | **100 television releases, 99 naming a season.** The hundredth is a *Seasons 1 and 2 Complete* box set, correctly parsing as no single season |
| `TestLiveTV` against real TMDB (2026-08-20) | `id=1396 "Breaking Bad" (2008)`, **5 seasons, 62 episodes, 0 min/ep**, search 20 results first *The Office* (2005), popular 20 first *Reacher* (2022), trending 20 first *Lanterns* (2026) — **2.80s** |
| `0 min/ep` is **TMDB's answer, not a decode bug** | `GET /tv/1396` returns `"episode_run_time": []` beside `first_air_date 2008-01-20` and `number_of_seasons 5`. Written down here so nobody files it |
| Pi `/media/storage/media/tv` (2026-08-20) | exists, **empty**, `1000:1000`, a **sibling** of `movies` and `downloads` inside one bind — so it is one filesystem and D8's `link()` works there per episode |
| Pi Jellyfin `/Library/VirtualFolders` | already holds **`Shows` · `tvshows` · `/tv`**, left in place by the T54 cutover. Nothing to provision on that box |
| `SearchMovie` in the tree, before T90 | **29 references outside tests across 12 files, 201 inside** — and two different methods shared the name, `tmdb.Client`'s and `indexer.Indexer`'s |

### The end-to-end run, 2026-08-21 — a real process, a real library, a real key

Not a test. `go run ./cmd/curator` over one film, three episodes across two shows, and one show folder
holding nothing, all above the production 50 MiB floor:

```
POST /api/scan
{"scanned":1,"added":1,"matched":1,"empty":0,"removed":0,"missing":0,
 "shows":2,"shows_added":2,"shows_matched":2,"shows_unmatched":0,
 "shows_empty":1,"episodes":3}

/api/shows   'Star Wars - Andor' (2022)  tmdb_id=None  tmdb_tv_id=83867   62914560
             'Severance'         (2022)  tmdb_id=None  tmdb_tv_id=95396  125829120
/api/movies  'Interstellar'      (2014)  tmdb_id=157336  tmdb_tv_id=None
```

**`Star Wars - Andor` matched**, which is CLAUDE.md's ` - ` colon-substitution trap met by the
television matcher and not only by the film one. The **two id columns are cleanly separated**, each
row NULL in the other's. And the **sizes are summed per show**: 125829120 is 2 × 60 MiB, 62914560
is 1.

Then the scenario every existing install performs on the next image — the same database restarted
with `LIBRARY_TV` **unset**:

```
POST /api/scan
{"scanned":1,"removed":0,"missing":2,"shows":0,"shows_empty":0,"episodes":0}

GET /api/shows  ->  503  {"error":"television is not configured: set LIBRARY_TV"}

INFO scan: rows kept without being considered, because nothing walked their
     library root  media_type=tv rows=2
     why="LIBRARY_TV is unset, so television is off and nothing under it was scanned"
```

`removed: 0`, one log line about the **root** rather than one per show, and turning television back on
recovered both rows untouched — `shows=2 shows_added=0 episodes=3 removed=0 missing=0`, `tmdb_tv_id`
still 83867 and 95396.

### The two that would have shipped as silent damage

Neither is hypothetical, and both are the price of one table rather than two.

**A movie scan would have deleted every TV row.** `prune`'s switch puts `case outside` — computed as
`AssertInside(libraryRoot, row.LibraryPath) != nil` — *before* `case recorded[key]`. A show row is
not merely unfound by a movie scan; it is affirmatively deleted, with a log line reading *"its
library_path is outside LIBRARY_MOVIES, so it can never be served"*, taking its downloads with it
through the foreign key. **The first movie scan after the first TV import would have emptied the TV
library.**

**A show would have quietly taken a film's identity.** `MoviesMissingMetadata` selects
`WHERE tmdb_id IS NULL`, and a show's `tmdb_id` is NULL by construction — so every show lands on the
matching pass's work list on **every** scan, is looked up against `/search/movie`, and is written
back with `SetTMDBMetadata`, which overwrites unconditionally by design. For **Fargo, Watchmen,
Hannibal, Westworld, Dune and Snowpiercer** that lookup *succeeds*. No error, no log, and it re-fires
every scan.

The guard is that every media-scoped read takes a **required** media type with no value meaning
"both", and the `MoviesMissingMetadata` half is **mutation-tested**: dropping the `media_type`
predicate fails the Fargo test rather than passing it.

### Corrections made to the docs — T96

Every one of these was true when it was written.

| What | Where |
|---|---|
| *"**No television.** Movies only."* — a public claim on a public repository, and the bullet that made it | `README.md` |
| The `sequenceDiagram` filing only into `movies/Title (Year)/`, the `Indexer` fence whose one method was `SearchMovie(ctx, title, year)`, an `erDiagram` with no `tmdb_tv_id`, and the **EZTV row that had read *"TV, a later phase"* since phase 2** | `docs/architecture.md` |
| *"**TV.** Retired by choice in D43, not deferred"*, and a phase table that stopped at ten | `docs/roadmap.md` |
| `LIBRARY_TV` absent from the file a stranger curls, from the Pi's overlay, and from the developer's | `compose.yaml`, `compose.pi.yaml`, `.env.example` |
| An environment table and a layout block that did not know `tv/` existed | `CLAUDE.md` |
| A link to `decisions.md#d42` — **not an anchor GitHub resolves**; D42's heading is long and needs its full slug | `docs/tasks/T77-a-dead-host-fails-loudly.md` |

**Marked corrected in place rather than rewritten**, which is the posture T51 used for the container
arithmetic: line 537 of this document quotes the API's 404 as `"tmdb movie 999999999: no such
movie"`, and T89 retitled that sentinel to `"tmdb: no such title"` because one error now covers two
media types. The row still says what was seen that day, with the correction beside it.

**EZTV's row was kept rather than deleted.** The phase it had been waiting for arrived and chose
TPB's `cat=205,208` and 1337x's keyword search instead, and a row that says so is worth more than an
absence somebody re-proposes in a year.

### Not this task's, found while running its gate

**`internal/remux`'s `TestTheCapRefusesTheNextOneAndFreesItsSlot` is still failing**, and it is worth
one more data point rather than a second entry. Measured here on a branch whose entire diff is
Markdown and YAML comments — `git diff --stat main...HEAD` names no Go file, so it cannot be this
work's doing. The rate on this laptop: **three red out of six `make check` runs**, and **one red out
of four** runs of the test on its own, which is consistent with the *roughly one in three* recorded
above rather than a worsening.

**The failing run is visible in its duration**, which is the useful half: the test takes **10.9 s and
11.08 s on the runs that fail** and **1.9 s on the runs that pass** — it is waiting out the ten
seconds it gives a cancelled ffmpeg to return, every time. So a red `make check` here reads as a
regression and is not one. Re-run it before believing it, and do not take timings at all while
another agent is building, because `$GOCACHE` and the test cache are machine-global.

### Still live going out

- ~~**The UI is the outstanding half.** T95 was in flight beside T96 and is the only phase-11 task with
  no commit.~~ **Closed 2026-08-21** — T95 landed and phase 11 reads 9/9 in `make status`.
- **Three of `phase-11.md`'s eight verification steps have no recorded run**, and they are the ones
  that touch real hardware: a **real season pack from the UI through the tunnel** with `stat` showing
  link count 2 per episode; a **single episode into a show that already has that season**, proving
  `library.Link`'s same-inode-is-success path rather than an overwrite; and **Jellyfin's `Shows`
  library picking the episodes up** from the refresh curator already sends. The importer's behaviour
  is covered by tests in `t.TempDir()`; none of it has met a real download.
- **Nothing has been deployed.** `compose.pi.yaml` now carries `LIBRARY_TV: /media/tv`, which is a
  file in this repository and not a running container — no `up -d` has been run with it, and the Pi's
  `/media/storage/media/tv` is still the empty directory measured on 2026-08-20. `phase-11.md` says
  the Pi is a separate decision and that nothing in this phase deploys; that is still true.
- **T74's flake fired in front of the v0.4.0 publish, and its diagnostic finally spoke.** Release run
  `32447250968`'s embedded `check` failed on `TestDeleteTorrentRefusesAnotherCategory` at 60.07 s.
  The identical commit had passed `check` on `main` eight minutes earlier (`32447242921`), so this is
  nondeterminism again and not the version bump, which changed one string constant. What is new is
  the dump. T74's signature was metadata in, peers up and **zero payload**; this one moved payload
  and then stopped — `read data=180224`, `pieces complete=3` of 32, one piece left `M`, alongside
  `active=2 seeders=2`, `piece0 priority=normal ok=true`, `wasted-chunks=0 bad-pieces=0`. All four of
  swarmState's cases are excluded by those readings, which is precisely the state T74 built
  `stallReport` for and left unwired. It is wired into `await` now, so the next occurrence carries the
  per-peer `flags` segment that says whether the peer is choking us. Measured by rendering it
  mid-flight rather than assuming the format survived anacrolix v1.61.0:
  `reqq: 0+0/(1/250):0/1024, flags: :e,v1:c` — and that trailing `c` is the answer. 180 lines and
  6,833 bytes, on the failure path only. **The mechanism is still not named**; the deadline, a retry
  and `-count` all remain refused for T74's reasons, and it did not reproduce locally in 12 full-package
  runs at `GOMAXPROCS=2` any more than it did in T74's 20.
- **`Progress:0` sat beside `pieces complete=3` in that same dump, and the two cannot both be current**
  — `BytesCompleted` and `PiecesComplete` read the same `_completedPieces` bitmap. So either the
  poller's view was stale, which would mean `TorrentByHash` was **blocking** and the download was
  healthy all along, or the client's accounting disagrees with itself. Those are different bugs in
  different places and the old dump could not separate them. `await` now prints its own poll count and
  the age of its last poll: the healthy baseline, measured here, is **2,868 polls over 60.01 s with
  the last one 10 ms before the deadline**. A count in the tens next time means the observation
  starved, not the swarm.
- **One laptop, one endpoint, once**, as with every live VPN measurement since phase 6.

---

## T97, T98 — a fourth indexer, and a picker that shows what exists

Two things Nethmin asked for after using the television screens: **more sources**, and **season and
episode as clickable rows** rather than a select and a number field. He also asked whether **DHT**
could widen the results — it cannot, and the reason is in
[T97](tasks/T97-eztv.md#deliberately-not-in-scope): BEP 5 maps `infohash → peers` and has no keyword
index, so *"Silo S03E08"* is not a question it can be asked. curator already runs a DHT node on the
tunnel socket, doing the only job it can do.

### Measured live, 2026-08-21 — do not re-derive

| | |
|---|---|
| `eztvx.to/api/get-torrents?imdb_id=14688458` | **270 torrents** for Silo across three pages, one page 87 KB in ~2.5 s. apibay caps at 100 |
| `limit=300` | answers `"limit":30` with **thirty** rows — above the cap the value is discarded, not clamped, so asking for more returns **fewer** |
| `&season=3&episode=8` | **ignored**, the identical page 1 comes back. Narrowing stays where D49 put it |
| ordering | **newest first**. Silo page 1 is seasons 2 and 3 only; season 1 first appears on page 2 |
| no `imdb_id` | HTTP 200, `torrents_count: 1075625` — the newest uploads across every show on the site |
| unknown id | HTTP 200, `torrents_count: 0`, **no `torrents` key at all** — `ytsData`'s `movie_count` trap again |
| catalogue sizes | Game of Thrones 146 · Stranger Things 196 · Silo 270 · The Simpsons **2,425** |
| `eztv.re` | **301**; `eztvx.to` is the live front door. `architecture.md` named the wrong one from phase 2 until now |
| TMDB `/tv/{id}?append_to_response=external_ids` | `imdb_id` **and** `seasons[]` in **one** request, 0.22 s. Live: `imdb=tt0903747, seasons[]=6` |
| Silo's `seasons[]` | `number_of_seasons: 4` against **10, 10, 10 and 0** episodes — the fourth is announced and unaired |
| live `TestEZTVLive` | **270 releases, 270 stating a season**, in 2.05 s |

### The end-to-end run, on 8095 against real services

| | |
|---|---|
| `?title=Silo&media=tv&season=3&episode=8&imdb_id=tt14688458` | `yts NA · tpb 100 · eztv 270` → **5 exact** |
| the same without `imdb_id` | `yts NA · tpb 100 · eztv NA` → 2. Nothing failed and nothing is red |
| `&season=1&imdb_id=…` | 102 releases, **93 of them from eztv** — every one off page 2 or 3, which is the whole argument for a three-page budget over one |
| rendered `/show/?id=125988` | season row **Every 1 2 3** — the unaired fourth absent — then **All 1..10**, top result carrying `tpb, eztv` because the same torrent was found through both and merged on its info hash |

### What was decided rather than discovered

**The page budget is a cap, not a promise.** Three pages, 300 rows, ~7.5 s of a 30 s
`SEARCH_TIMEOUT` now shared by four sources. Twenty-five pages would cover The Simpsons and eat the
whole budget; one page would answer a season-1 search with a hundred rows `narrow()` then drops. The
truncation is real and lives in `eztvMaxPages`' own comment.

**Specials gets no button.** `?season=0` is what the API reads an absent season as, so a Specials
button would silently search every season. Drawn disabled was the alternative and is worse — a
control that cannot be pressed invites somebody to make it pressable without finding the reason
first. Recorded as a known gap in
[D50](decisions.md#d50--an-indexer-may-decline-a-query-it-cannot-answer-and-that-is-not-a-failure).

### Still live going out

- **The Pi is still on 0.3.0** and nothing here changes that. `compose.pi.yaml` remains staged at
  `:0.4.0` with `LIBRARY_TV: /media/tv`, un-run.
- **Three of `phase-11.md`'s eight verification steps still have no recorded run** — the three that
  need real hardware, listed above and unchanged by this task.
- **Quality sections are deferred**, taking **D51**: a Grouped/Flat toggle defaulting to grouped,
  nesting inside D49's tiers as *tier → quality → seeders*. **Done by
  [T99](tasks/T99-quality-sections.md) on 2026-08-21**, and D51 settled the one thing this line left
  open the other way round: the sections are ordered by their best-seeded release, not by
  resolution, because ordering them by resolution is D11 reversed.
- **`internal/remux`'s `TestTheCapRefusesTheNextOneAndFreesItsSlot`** is still unfiled, and this task
  narrowed it without fixing it. It fired twice here — 11.14 s and 10.57 s, the documented failing
  duration against ~0.9 s passing — and both times it was **under a full parallel `go test ./...`**.
  In isolation it passed **4 of 4 on this branch and 4 of 4 on `main`**, and this branch has **zero
  commits touching `internal/remux`**. That points at contention starving the ten seconds the test
  gives a cancelled ffmpeg to return, rather than at the leak the failure message names — which
  contradicts the T96 handoff's "failed 1 of 3 in isolation" and is worth re-measuring before
  anybody files it as a slot leak. `make check` was green at all five commits.
- **A fourth live indexer test is a fourth chance of CI reddening on an outage**, knowingly accepted.
  `TestEZTVLive` shares `classifyLiveFailure` with the other two, and the control arrangement is now
  a **cycle** — TPB←YTS←EZTV←TPB — pinned by a table rather than by two hand-written comparisons.

---

## T99 — quality sections, ordered by their best release

The release table draws in **quality sections** by default, with a `Grouped | Flat` toggle above it.
A section is a **(tier, quality)** pair, nesting inside D49's tiers, and the sections come out in the
order their first release appeared in the ranked list — ordered by their best-seeded member, not by
resolution. [D51](decisions.md#d51--quality-sections-are-ordered-by-their-best-release-not-by-resolution)
is the decision; [T99](tasks/T99-quality-sections.md) is the task.

Nothing on the wire changed. `quality` and `match` were already per-release, and the section order is
derived from the server's own ranking rather than from a rule a client could get wrong — which is
why this needed no new field despite D49's posture of sending the tier rather than re-deriving it.

### Measured live, 2026-08-21 — do not re-derive

YTS + TPB + EZTV merged, five searches in **7.08 s**:

| search | releases | sections | |
|---|---|---|---|
| Interstellar (2014) | 91 | 4 | 1080p 45 (best **1194**) · 2160p 14 (**262**) · 720p 10 (130) · none 22 (5) |
| Dune Part Two (2024) | 77 | 4 | 1080p 43 (best **1142**) · 2160p 20 (**518**) |
| Silo S01 | 102 | 4 | 1080p 52 (858) · 720p 31 (523) · **none 9 (6)** · **480p 10 (4)** |
| Severance S02 | 107 | 5 | |
| Severance S02E05 | 9 | 5 | under 2 tier dividers |

**The bolded pairs are the decision.** Ordering sections best-resolution-first leads Interstellar
with a 262-seeder 2160p and buries the 1194-seeder 1080p — which is D11's own rejected sentence, and
putting it behind a section header does not make it a different answer. So the best row orders the
sections: D11 applied one level up.

**Silo puts `no resolution in the name` (best 6) above `480p` (best 4)**, which is the same rule
visible in the one place it disagrees with a resolution table. `QualityRank` orders a tie-break
inside `rank` and deliberately does not order these.

**The top row is the same in Grouped and in Flat**, verified on all three dumped answers rather than
argued.

### How a browserless repo checked a browser change

There is no JS test framework here and T99 did not add one. Instead a live search was dumped in the
API's release shape and the **shipped module** was run over it — imported, not transliterated:

```
node --experimental-strip-types render.mjs      # imports web/lib/sections.ts
```

That is why the logic lives in `web/lib/sections.ts` rather than beside the component: it is the only
part of the change that can be checked without a browser, and it was. **No browser render was
checked** — the markup is table rows; the logic is what was verified.

### What was decided rather than discovered

**No row-count threshold.** Severance S02E05 is seven rows of chrome over nine rows of list, and
three of its five sections hold one release under a header naming the quality that release's own
badge already states. Any number chosen to suppress that would be a number nobody measured. The
answers are the toggle — one click, and it does not reset when a new search arrives — and the rule
that a list with **one** section is never grouped at all, because there the two modes render
identically.

**The header names its tier only when it is not `exact`.** Interstellar and Silo are 100% one tier,
so a film search and a season-only search get bare resolution headers; the qualifier appears on the
only screen that has more than one tier on it, where `1080p · 45 releases` and
`1080p · season packs · 2 releases` would otherwise both read `1080p`.

**D49's tier divider stays above the section headers.** It carries the sentence that explains a tier
the first time you meet it; a section header names a section. Neither replaces the other.

### The Go half of a TypeScript invariant

`TestRankKeepsATierContiguous` (`internal/indexer/tier_test.go`) pins that a tier arrives in one run
and never two. That holds only because the tier is `rank`'s primary key, so nobody set out to provide
it — and the thing it protects is in `web/components/releases.tsx`. Demoting the tier below seeders
makes a tier reopen partway down the list and draw its header a second time, with nothing in the Go
diff to suggest a screen broke. Verified failing for exactly that reason before it was kept.

### Still live going out

- **The Pi is still on 0.3.0** and nothing here changes that. `compose.pi.yaml` remains staged at
  `:0.4.0` with `LIBRARY_TV: /media/tv`, un-run.
- **Three of `phase-11.md`'s eight verification steps still have no recorded run** — the three that
  need real hardware, unchanged by this task.
- **No quality filter.** `?quality=` exists in the API and `FilterFound` implements it; no screen
  sends it. Sections make the same information browsable without narrowing, so a control that
  actually filters is still unbuilt.
- **Grouped mode leaves the Quality column in place**, where it repeats its own section header on
  every row. Dropping it under grouping would reclaim the width and make the column set change under
  a toggle, which is the bigger jar — not traded blind.
- **`internal/remux`'s `TestTheCapRefusesTheNextOneAndFreesItsSlot`** is still unfiled and still
  unmeasured since T98 narrowed it to contention rather than a slot leak.
- **`?season=0` is still overloaded**, and the picker still draws no Specials button.
