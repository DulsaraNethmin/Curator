# Progress

Where the build actually is. [`roadmap.md`](roadmap.md) says what each phase is *for*; this says
what is **done, verified, and outstanding**. Update it when a phase closes or a decision is made.

**Last updated:** 2026-08-12 · **Phase 1 complete** · phase 2 specified, not started

---

## Phases

| Phase | What | Status |
|---|---|---|
| **1** | Foundation — skeleton, SQLite, TMDB, library scanner | **done** — verified 2026-08-12 |
| **2** | Indexers — YTS, TPB, then 1337x through minter | **next** — [spec](phase-2.md) and tasks written, no code yet |
| 3 | Downloads — qBittorrent client, magnet dispatch, state polling | unblocked (was NordVPN `AUTH_FAILED`) |
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

Specified 2026-08-12, no code yet. Spec in [`phase-2.md`](phase-2.md). Ownership here is per **file**,
not per directory — T8, T9 and T10 share `internal/indexer/` with phase 1's ported code, which is
read-only to them.

| Task | Owns | Depends on | Status |
|---|---|---|---|
| [T8](tasks/T8-yts-indexer.md) YTS | `internal/indexer/yts.go` | — | not started |
| [T9](tasks/T9-tpb-indexer.md) TPB | `internal/indexer/tpb.go` | — | not started |
| [T10](tasks/T10-search-cache.md) cache | `internal/indexer/cache.go` | — | not started |
| [T11](tasks/T11-aggregator.md) aggregator | `internal/indexer/aggregate.go` | T8, T9, T10 | not started |
| [T12](tasks/T12-search-api.md) API | `internal/api/search.go`, config, wiring | T11 | not started |

T8, T9 and T10 are independent — run them in parallel, as phase 1 did.

**Decided while specifying, so phase 2 does not have to stop and think:**

- **Release identity** — [D10](decisions.md#d10--releases-are-identified-by-an-opaque-id-not-a-url).
  The detail path stays unexported and server-side; releases carry an opaque deterministic id.
  Exporting it would have meant the API accepting a URL from the client and handing it to minter to
  fetch, which is request forgery through a service built to fetch convincingly.
- **Ranking** — [D11](decisions.md#d11--rank-by-seeders-quality-is-a-filter-not-a-score). Seeders
  descending, quality as a filter rather than part of a score.

**Still live going in:**

- **`MINTER_URL`'s old default is a trap on this machine.** minter binds IPv4 only, so
  `http://localhost:8191` resolves to `::1` first and fails while `http://127.0.0.1:8191` works. The
  phase 2 default is the literal IP.
- **`errgroup` fails fast, which is backwards here.** `errgroup.WithContext` cancels every sibling on
  the first error, so one downed indexer would silently empty an otherwise good search. Each
  goroutine records its own error and returns nil. Called out in T11 as a test, not a comment.
- **The absorbed indexer is unwired**, so phase 2 starts from a tested `parseSearch`, `parseQuality`,
  `InfoHash` and minter client rather than a blank file.

---

## Corrections made to the docs

Recorded so the reasoning is not lost.

| What | Where |
|---|---|
| Six → **seven** 2026 releases in the fixture | `CLAUDE.md`, `decisions.md` D9, `T4`, `T7` |
| Phases 3–4 no longer blocked — `gluetun` and `qbittorrent` are healthy | `CLAUDE.md`, `roadmap.md` |
| `.gitignore`'s unanchored `curator` also matched the `cmd/curator/` **directory**, silently excluding the command from git | `.gitignore` |
