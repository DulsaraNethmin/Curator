# Progress

Where the build actually is. [`roadmap.md`](roadmap.md) says what each phase is *for*; this says
what is **done, verified, and outstanding**. Update it when a phase closes or a decision is made.

**Last updated:** 2026-08-12 · **Phases 1 and 2 complete** · phase 3 built, one verification
outstanding · **phase 4 built and verified locally**

---

## Phases

| Phase | What | Status |
|---|---|---|
| **1** | Foundation — skeleton, SQLite, TMDB, library scanner | **done** — verified 2026-08-12 |
| **2** | Indexers — YTS, TPB, then 1337x through minter | **done** — verified 2026-08-12 |
| **3** | Downloads — qBittorrent client, magnet dispatch, state polling | **built** — T13–T16 done; live dispatch pending qBittorrent credentials |
| **4** | Import — completion watcher, hardlink, rename, Jellyfin refresh | **built** — T17–T21 done, verified locally 2026-08-12; never run against the Pi |
| 5 | Interface — Next.js screens embedded via `embed.FS` | |
| 6 | Cutover — run alongside, confirm parity, remove seven containers | back up the *arr configs first |

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

## Corrections made to the docs

Recorded so the reasoning is not lost.

| What | Where |
|---|---|
| Six → **seven** 2026 releases in the fixture | `CLAUDE.md`, `decisions.md` D9, `T4`, `T7` |
| Phases 3–4 no longer blocked — `gluetun` and `qbittorrent` are healthy | `CLAUDE.md`, `roadmap.md` |
| `.gitignore`'s unanchored `curator` also matched the `cmd/curator/` **directory**, silently excluding the command from git | `.gitignore` |
