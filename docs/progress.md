# Progress

Where the build actually is. [`roadmap.md`](roadmap.md) says what each phase is *for*; this says
what is **done, verified, and outstanding**. Update it when a phase closes or a decision is made.

**Last updated:** 2026-08-12 · **Phases 1 and 2 complete** · phase 3 next

---

## Phases

| Phase | What | Status |
|---|---|---|
| **1** | Foundation — skeleton, SQLite, TMDB, library scanner | **done** — verified 2026-08-12 |
| **2** | Indexers — YTS, TPB, then 1337x through minter | **done** — verified 2026-08-12 |
| **3** | Downloads — qBittorrent client, magnet dispatch, state polling | **next** — unblocked (was NordVPN `AUTH_FAILED`) |
| 4 | Import — completion watcher, hardlink, rename, Jellyfin refresh | |
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

## Corrections made to the docs

Recorded so the reasoning is not lost.

| What | Where |
|---|---|
| Six → **seven** 2026 releases in the fixture | `CLAUDE.md`, `decisions.md` D9, `T4`, `T7` |
| Phases 3–4 no longer blocked — `gluetun` and `qbittorrent` are healthy | `CLAUDE.md`, `roadmap.md` |
| `.gitignore`'s unanchored `curator` also matched the `cmd/curator/` **directory**, silently excluding the command from git | `.gitignore` |
