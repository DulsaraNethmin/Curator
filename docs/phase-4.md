# Phase 4 — Import

Turn a finished download into a movie the library can see, without moving a byte and without
deleting anything.

**Done when** a completed download is hardlinked into `movies/Title (Year)/Title (Year).ext`, the
`downloads` row reads `imported`, the `movies` row has a `library_path` and an `imported_at`,
Jellyfin has been asked to refresh, and `GOOS=linux GOARCH=arm64 go build ./...` still passes.

This is the first phase that **writes to disk**, and the *arr stack keeps serving until phase 6. It
is therefore deliberately conservative, and it is verified on a laptop against `t.TempDir()`, not on
the Pi.

---

## Tasks

| Task | Owns | Depends on |
|---|---|---|
| [T17](tasks/T17-library-link.md) hardlink + naming | `internal/library/link.go`, `link_test.go` | — |
| [T18](tasks/T18-import-store.md) import transaction | `internal/store/imports.go`, `imports_test.go` | — |
| [T19](tasks/T19-jellyfin-client.md) Jellyfin client | `internal/jellyfin/` | — |
| [T20](tasks/T20-importer.md) the importer | `internal/importer/` | T17, T18, T19 |
| [T21](tasks/T21-import-wiring.md) wiring | config, `cmd/curator`, poller hook, API | T20 |

T17, T18 and T19 own disjoint packages and are independent — run them in parallel. T20 joins them and
is the only package that knows about deployment paths. T21 exposes them.

Ownership is per **file** again, as in phase 2: T17 and T18 add new files to packages phase 1 owns.

---

## What must not change

Stated first, because most of the risk in this phase is collateral damage.

- **`internal/library/scan.go` is not modified.** `TestScanFixture` asserts exactly 29 folders and
  `TestScanSizeIsLargestVideoFile` hardcodes sizes and "27 zero-size folders", so **nothing may be
  added to `testdata/library/movies/` either**. `link.go` is in package `library` and can call the
  existing unexported `isVideo` and `isHidden` directly; the importer's recursive search is a
  **separate exported function**, not a change to `largestVideo`.
- **`UpsertMovieByPath` is not modified**, so
  `TestWantedMovieDoesNotDisturbTheScanUpsert`'s assertions all still hold. Only its comment is
  stale — it says the wanted row and the scanned row stay separate for ever, and phase 4 is what
  eventually reconciles them. Update the comment, add a sibling test for the full
  wanted → scan → `MarkImported` path, and do not rewrite a working guard.
- **`poller.go` keeps its state mapping, its `completed_at` stamping and its no-row warning.** The
  only change is a nilable importer field, so every existing poller test passes unchanged.
- **No migration.** `downloads.state` has carried `imported` since [T2](tasks/T2-store.md) and
  `movies` already has `library_path`, `size_bytes` and `imported_at`. See
  [D14](decisions.md#d14--the-importer-is-driven-by-the-pollers-torrent-list-not-by-a-completion-event).
- **No source file is ever deleted** ([D8](decisions.md#d8--import-by-hardlink)). Cleanup stays
  qBittorrent's business under its own seeding rules. Every test in this phase — the failure cases
  included — snapshots the source tree before and after and asserts it is unchanged.

---

## Measured, so the implementation does not have to guess

Read off the Pi 2026-08-12. Nothing was written to it.

| | |
|---|---|
| Download modes | qBittorrent writes files **`0644`** and directories **`0755`**, owner `nethmin:nethmin` |
| Library modes | identical — `0644 nethmin:nethmin` on every feature file the *arr importer hardlinked |
| Same filesystem | `/media/storage/media/movies` and `…/downloads` both report device **`2049`** |
| Categories | `radarr` → `/downloads/complete/movies`, `sonarr` → `/downloads/complete/tv`, no `curator` yet |
| Temp path | `Session\TempPath=/downloads/incomplete/` is set, but `Session\TempPathEnabled` is **absent**, so it takes qBittorrent's default of false — the directory is inert and empty |

**The modes are the measurement that mattered.** A hardlink has no mode of its own: it is a second
name for one inode, so it carries the source's permission bits, and `chmod` on the link changes the
source file too. Had qBittorrent been writing `0600`, every import would have produced a library
entry Jellyfin cannot open — a film that appears in the UI and silently will not play — and the
obvious fix would have quietly changed the file qBittorrent is still seeding. It writes `0644`, the
same mode the *arr stack's own hardlinks already have in the library, so **the importer needs no
`chmod` at all** and must not add one.

**Still unmeasured, and designed around rather than guessed at:** the live `content_path` and
`save_path` for a `curator` torrent. Both need a real dispatch, which needs `QBIT_USER` and
`QBIT_PASS`, which are not in `.env` — the same credential gap that leaves phase 3 built rather than
verified. The category does not exist on the Pi yet, so there is nothing to read.

What that costs us is nothing, because the importer does not depend on the answer:

- `ContentPath` **may be a file or a directory** and is treated as either, so
  single-file-vs-folder torrent is not a branch the configuration has to get right.
- Complete-vs-incomplete cannot bite. The importer only ever looks at a torrent whose state maps to
  `completed`, and phase 3 already maps qBittorrent's `moving` to `downloading` — so a torrent being
  relocated never reads as importable.
- If the path is wrong anyway, the import fails, the row stays `completed`, and the next tick
  retries. That is [D14](decisions.md#d14--the-importer-is-driven-by-the-pollers-torrent-list-not-by-a-completion-event)'s
  recovery path doing exactly its job.

---

## What the import does

One completed torrent, in order:

1. **Read the movie row** behind `downloads.movie_id` — the title and year the dispatcher recorded.
2. **Build the destination folder name**, `Title (Year)`, sanitising the title first.
3. **Translate `ContentPath`** out of qBittorrent's namespace into curator's.
4. **Choose the file**: walk the content path and take the largest video.
5. **Create the destination folder** — and not one step earlier.
6. **Hardlink** the file to `Title (Year)/Title (Year).ext`.
7. **Record it**, in one transaction: `MarkImported`.

Then, once per tick and only if something was imported, ask Jellyfin to refresh.

### Folder naming is the inverse of D9

The library replaced illegal colons with ` - ` (8 of 29 folders), so the importer **must do the same
substitution** — `Avengers: Infinity War` → `Avengers - Infinity War (2018)`. Get this wrong and a
rescan finds `Avengers: Infinity War (2018)` beside the folder that is already there and records a
second movie for one film.

This is asserted as a **round trip over the real fixture**: for all 29 folder names,
`ParseFolder(name)` → `DestFolder(title, year)` must reproduce the name **exactly**. That test meets
the eight ` - ` titles, `Spider-Man`, `X-Men` and `Deadpool & Wolverine` without anyone having to
remember they are special. See [D9](decisions.md#d9--query-tmdb-with-the-raw-folder-title).

### The title is untrusted input

`movies.title` reaches the database from a client, through `POST /api/downloads`, and phase 4 is the
first code that turns it into a **path**. A title of `../../etc/cron.d` writes outside the library.

So `DestFolder` rejects `/`, `\`, NUL, and the names `.` and `..` outright, and the importer
separately asserts that the resolved destination is still **inside** `LIBRARY_MOVIES` before it
creates anything. Nothing else in the repo treats a title as untrusted today, and this is the one
security-relevant line of the phase.

A year outside 1000–9999 is rejected for a duller reason: `Title (0)` does not parse back, so it
would break the round trip and produce a folder the scanner skips.

### Path translation lives in the importer

qBittorrent reports `/downloads/complete/curator/…` because its mount is host
`/media/storage/media/downloads` → container `/downloads`
([D13](decisions.md#d13--downloads-are-scoped-by-a-qbittorrent-category-with-its-own-save-path)).

| Variable | Default | Meaning |
|---|---|---|
| `DOWNLOADS_PATH` | *(empty)* | Where curator sees the downloads. Empty means **use `ContentPath` verbatim** |
| `QBIT_DOWNLOADS_PATH` | `/downloads` | The same directory in qBittorrent's namespace |

Empty by default because that is the laptop case and the container-shares-the-mount case, both of
which need no configuration at all. When `DOWNLOADS_PATH` **is** set and a content path does not
start with `QBIT_DOWNLOADS_PATH`, that is an **error, not a silent pass-through**: someone has
configured a translation and it did not apply, and quietly hardlinking from an untranslated path is
how you get a "file not found" three layers from the mistake.

It is applied at `internal/importer`'s boundary — **not** in `internal/qbit`, which translates
nothing by contract ([D13](decisions.md#d13--downloads-are-scoped-by-a-qbittorrent-category-with-its-own-save-path)),
and **not** in `internal/library`, which is a pure package with no deployment knowledge.

### Choosing the file

`ContentPath` may be a file or a directory, and `torrents/files` is deliberately not implemented in
the client, so the importer walks the path itself:

- depth-capped, so a pathological torrent cannot walk a filesystem
- hidden entries skipped, and directories named `Sample`, `Extras`, `Featurettes`, `Subs` skipped
- the **largest** video wins, with a **~50 MiB floor** so a 4 MB `sample.mkv` sitting in the root
  cannot be mistaken for the feature
- nothing qualifying is a **named `ErrNoVideo`** — the row stays `completed` so the next tick
  retries, and it is **not** marked `failed`, because nothing failed: the torrent downloaded exactly
  what it said it would, and it just is not a film we can place
- **one file only.** No `.nfo`, no subtitles, no artwork. If a folder holds several videos the
  largest is imported and the log says how many others were seen, so a double feature is visible
  rather than half-imported

A relevant fact about the real library: **15 of the 29 folders on the Pi are empty** — 14 video files
exist in total. A folder does not imply a file, here or in the download directory.

### Link first, database second

The hardlink is the fact; the row is the record of it. Doing it the other way round means a row
claiming a file that is not there.

- A destination that already exists and is `os.SameFile` as the source is **success** — it is a
  crashed run's own link, and this is what makes a retry converge.
- A destination that exists and is a *different* file is an **error**. Never an overwrite.
- `EXDEV` falls back to a copy, through an **injected link seam** so the fallback is testable without
  a second filesystem.
- The copy is written to a `.curator-tmp` file and then renamed, so a crash mid-copy leaves an
  obvious temp file rather than a half-written `.mkv` that passes every existence check and plays for
  forty minutes.
- `MkdirAll` for the destination folder happens **after** the source file has been chosen. Creating
  it earlier means a failed import leaves an empty `Title (Year)/`, which the scanner faithfully
  records as a zero-size movie.

### One store method, one transaction

`MarkImported(hash, libraryPath, size, at)` — everything or nothing.

The complication it exists for: the film may already be in the library **twice**. A `movies` row with
`status = 'wanted'` and `library_path = NULL` was created at dispatch, and if a scan ran between the
hardlink and this write, `UpsertMovieByPath` has already inserted a **twin** row for the same folder.

Inside the transaction, after looking the twin up by `library_path`:

- **no twin** — update the wanted row: `library_path`, `size_bytes`, `status = 'imported'`,
  `imported_at`
- **a twin** — repoint **every** `downloads.movie_id` from the wanted row to the twin, carry
  `tmdb_id` forward only if the twin's is NULL *and* no third row already holds it, delete the wanted
  row, then update the twin

**The twin is kept and the wanted row is deleted**, never the other way round. The twin is what every
future `UpsertMovieByPath` finds, because `library_path` is its identity key; keeping the wanted row
instead would leave the next scan inserting the twin all over again.

`imported_at` is `COALESCE`d, so a re-import keeps the first moment rather than resetting the film's
history every time a torrent is re-added.

### Jellyfin

`POST /Library/Refresh` with an `X-Emby-Token` header, expecting `204`. `401` is a named
`ErrUnauthorized`. Best-effort, once per tick, key optional — see
[D15](decisions.md#d15--the-jellyfin-refresh-is-best-effort-and-its-key-is-optional).

---

## Configuration

Added to phase 3's table:

| Variable | Default | Purpose |
|---|---|---|
| `DOWNLOADS_PATH` | *(empty)* | Downloads root as curator sees it. Empty = use `ContentPath` verbatim |
| `QBIT_DOWNLOADS_PATH` | `/downloads` | Downloads root as qBittorrent sees it |
| `JELLYFIN_URL` | `http://127.0.0.1:8096` | Jellyfin. `http://jellyfin:8096` in Docker |
| `JELLYFIN_API_KEY` | *(empty)* | Empty disables the refresh; it is not a startup error |

`LIBRARY_MOVIES` is reused as the import destination. It is already the scanner's root, and an
importer writing anywhere else would produce movies the scanner never sees.

---

## API surface

| Endpoint | Behaviour |
|---|---|
| `POST /api/downloads/{hash}/import` | Import that download now, without waiting for a tick. `200` with the movie row |

It exists for the case the poller cannot serve: a download that failed to import for a reason since
fixed, where waiting up to `DOWNLOAD_POLL_INTERVAL` to find out whether the fix worked is worse than
asking. It runs the identical code path — there is no second importer.

- unknown hash → `404`
- qBittorrent unreachable, or no torrent with that hash → `502`
- the torrent has not finished → `409`
- no video in the content path, or a title that cannot be a folder → `422`
- downloads unconfigured → `503`

---

## Verification

Local, against `t.TempDir()`. **Nothing in this phase is verified against the Pi**, which is
read-only until phase 6.

```bash
go build ./... && go vet ./... && go test -race ./...
GOOS=linux GOARCH=arm64 go build ./...
GOOS=linux GOARCH=arm64 go vet ./...      # catches Nlink's uint16/uint64 split

# the fixture still scans as it did in phase 1 — nothing was added to testdata
go test ./internal/library -run 'TestScan'
```

**The hardlink is proven twice, independently**, because either check alone can be satisfied by a
plain copy or by an unrelated file:

1. `os.SameFile(srcInfo, dstInfo)` **and** a link count of 2
2. modify the **source's bytes** after linking, then read the destination and see the change

`syscall.Stat_t.Nlink` is `uint16` on darwin and `uint64` on linux. Read it through a helper that
converts, or the arm64 cross-compile of the tests breaks on a line that looks fine on a laptop.

**`df` unchanged is a weak signal on a macOS temp dir** — APFS is copy-on-write and the numbers move
for unrelated reasons — so the laptop substitute for the roadmap's "`df` unchanged" is
**equal inode plus link count 2**, and the `df` check itself defers to the Pi at phase 6 cutover.

End to end, with the fixture library and a fake download directory:

```bash
# a completed download imports on the next tick
curl -s localhost:8090/api/downloads | jq '.[] | {release_name, state}'   # imported
curl -s localhost:8090/api/movies | jq '.[] | select(.status=="imported") | {title, library_path, imported_at}'
ls "$LIBRARY_MOVIES/Interstellar (2014)"                                  # Interstellar (2014).mkv
stat -f '%l %i' "$LIBRARY_MOVIES/Interstellar (2014)/Interstellar (2014).mkv"   # link count 2
```

---

## Out of scope

- **TV.** `media_type` carries it, the importer does not.
- **Deleting, moving or renaming a source file**, and any seeding policy
  ([D8](decisions.md#d8--import-by-hardlink)).
- **Importing anything but the feature** — no subtitles, no `.nfo`, no artwork.
- **Retry with backoff.** A failing import retries every tick by design
  ([D14](decisions.md#d14--the-importer-is-driven-by-the-pollers-torrent-list-not-by-a-completion-event));
  what is suppressed is the repeat log, not the retry.
- **Quality parsing from the release name.** `movies.quality` stays the scanner's column; guessing it
  from a torrent name is a phase 5 nicety at best.
- **Running any of this against the Pi.** Phase 6.
