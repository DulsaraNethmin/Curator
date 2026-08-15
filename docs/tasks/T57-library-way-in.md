# T57 — a folder with no film in it is not a movie

**Owns:** `internal/library/scan.go`, one query in `internal/store/movies.go`, `Scanner` and
`handleScan` in `internal/api/api.go`, [D33](../decisions.md)
**Depends on:** [T17](T17-library-link.md) (`FindFeature`), whose "do not touch `scan.go`" rule this
supersedes
**Followed by:** [T58](T58-delete-outside-root.md), [T59](T59-already-have.md),
[T60](T60-library-way-in-web.md)

## Goal

The library stops recording films that are not there.

`internal/library/scan.go` records **every** folder it finds as an imported movie regardless of what
is inside it: `largestVideo` answers 0 for an empty directory and the scanner writes the row anyway.
`status` reads `imported`, which is what the movie page uses to decide whether to draw Play — so
Play appears for a film that does not exist and 404s when `<video>` fetches. 15 of the 29 folders on
the Pi are empty, so over half the library is that row.

Two changes, and neither works without the other: **the scanner stops creating them**, and **every
scan removes the rows that are already there**. Pruning alone is not stable — the next
`POST /api/scan` puts every one straight back.

## What is decided, and is not this task's to revisit

1. **An entry with no media loses its ROW. The directory on disk is never touched.** At the phase 10
   cutover those directories belong to Radarr, and curator deleting them is curator fighting the
   \*arr stack for the library.
2. **It happens on every scan**, and the response reports the counts, so nothing vanishes silently.
   That is [D6](../decisions.md#d6--tmdb_id-is-nullable)'s principle — surface what needs attention —
   applied to a different fact.
3. **A row whose `library_path` is outside `LIBRARY_MOVIES` loses its row too.** `AssertInside`
   already refuses to serve it, so it is dead weight by definition. Stated consequence: repointing
   `LIBRARY_MOVIES` drops the rows that pointed at the old one.
4. **A folder curator cannot read is KEPT, never pruned.** That is what an unplugged USB disk looks
   like, and losing a library list to a loose cable is not a trade worth making.

## Do

1. **One answer to "which file is the film."** Replace `largestVideo` with `library.FindFeature`, the
   picker `internal/importer/importer.go` and `internal/api/stream.go` already share.
   [`docs/phase-8.md`](../phase-8.md) is explicit that two answers to this question is a bug that
   only appears on the folders where it matters — and after this task those two answers no longer
   disagree about a *size*, they disagree about whether a row **exists**.

   Pass `FeatureOpts{}` verbatim in production. Lowering the floor or the depth here re-creates the
   bug in the form that deletes: the stream endpoint would find a film the scanner did not, and the
   prune would remove its row.

2. **`Skipped` grows a machine-readable discriminator.** Exactly one kind of skip removes a row, and
   a substring match on prose is not a contract:

   ```go
   type Skipped struct {
   	Name    string
   	Path    string // filepath.Join(root, Name) — the key the pruner joins rows on
   	NoMedia bool   // read successfully, and holds no feature file
   	Reason  string // for a human and for the log
   }
   ```

3. **An unreadable folder is a `Skipped`, not an aborted scan.** The current code already argues for
   this posture eight lines above contradicting it — "a dangling symlink is a fact about the disk,
   not a reason to abandon the other 28 folders" — and then returns `largestVideo`'s error. The
   asymmetry that stays: **a failing `os.ReadDir(root)` is still fatal**, and that is the whole
   protection behind decision 4.

4. **`ScanWithSkipped` is absorbed into `Scan`**, which existed only to discard the skipped list and
   nothing wants that any more. `ScanWith(root, opts)` is the same function with the picker's options
   supplied, so a test can lower the 50 MiB floor instead of committing 50 MiB.

5. **`MoviesOnDisk` in the store** — the prune candidates, with the guard inside the query:

   ```go
   // OnDisk{ ID int64; Title string; Year int; LibraryPath string; Downloading bool }
   func (s *Store) MoviesOnDisk(ctx context.Context) ([]OnDisk, error)
   ```

   A `wanted` row has `library_path` NULL and is not a candidate at all. `Downloading` is an EXISTS
   over `downloads` with `state NOT IN ('imported','failed')` — **the same definition
   `LibraryByTMDBID` uses**, so the badge on a poster and this can never disagree about what "already
   downloading" means.

6. **`handleScan` classifies, as a pure join** over what the scan just returned. No re-stat: the scan
   has walked the whole root and every fact is already in hand. Both sides normalised through
   `filepath.Abs`; a path that will not `Abs` is kept.

   | condition | action |
   |---|---|
   | `Downloading` | **keep, always** — checked first, overrides every prune below |
   | `AssertInside` errors | **prune** — it can never be served |
   | recorded this run | keep; the upsert already refreshed its size |
   | a `Skipped` with `NoMedia` | **prune** — this is "no media on it" |
   | anything else — absent, unreadable, unparseable | keep |

   Only two branches delete, and both are *positive findings*. A root that mounts empty returns no
   movies and no skips, every row falls to "mentioned nowhere", and nothing is removed.

7. **Prune with `store.DeleteMovie`** — the store method, which removes the `downloads` rows then the
   `movies` row for the foreign key and has never touched a disk. Order inside the handler: scan,
   upsert, **prune**, then TMDB, so no lookup is ever spent on a row about to go. A prune failure is
   a 500 for the same reason a failing upsert is.

8. **The response carries the counts**, and the log carries a line per row: `empty` (folders with no
   film in them), `removed` (rows dropped), `missing` (rows kept because this scan could not read
   their folder). `scanned` narrows to "folders that hold a film", which is why `empty` ships beside
   it rather than later.

## Do not

- **Use `download.Service.DeleteMovie`.** It removes files and talks to qBittorrent. The scan's
  cleanup is rows-only; `store.DeleteMovie` is the one that means that.
- **Delete the directory.** Decision 1. It is the first improvement the next reader will propose,
  which is why D33 says why not, next to the rule.
- **Prune a row with a download in flight**, even when its folder is empty. The importer creates the
  destination folder and only then hardlinks into it, so there is a window where the folder
  legitimately holds nothing.
- **Give the scanner its own floor or depth.** See Do 1.
- **Let a failing `os.ReadDir(root)` prune anything.** It must 500 with nothing removed.
- **Commit a 50 MiB fixture.** `docs/phase-4.md` and [T17](T17-library-link.md) both say nothing may
  be added to `testdata/library/movies/`; that rule survives this task. The floor is proven over
  sparse files in `t.TempDir()`, which is what `internal/api/stream_test.go` already does.
- Touch the UI, the delete path or dispatch. Those are T58, T59 and T60.

## Verify

Hermetic, `internal/library`:

- `Scan(fixtureRoot)` at the **real** floor → **0 movies, 29 skips**, every one `NoMedia`. The
  fixture's largest video is 4096 B; saying that out loud is what stops the next reader "fixing" it.
- `ScanWith(fixtureRoot, FeatureOpts{MinBytes: 1})` → 2 movies (4096, 2048), 27 `NoMedia`, and all 29
  folder names come back byte-for-byte across the two slices.
- Every returned `Movie` has `SizeBytes >= opts.minBytes()`, and `FindFeature(m.LibraryPath, opts)`
  agrees with it — the one-answer property asserted head-on.
- Over `t.TempDir()` with sparse files: a 60 MiB feature beside a 6 MiB `sample.mkv` scans as the
  feature; a folder whose only video **is** the sample is `NoMedia`; the same for `Extras/`.
- A feature one level down is now **found** — `TestScanDoesNotRecurse` inverts, and its replacement
  says the reversal is deliberate.
- A `chmod 000` folder is a `Skipped` with `NoMedia` **false**, `err == nil`, and the folder beside
  it still scans. This is the branch that decides whether a row lives.
- A symlinked feature file inside a library folder is `NoMedia` — the accepted cost, with a test.
- A missing root still errors, and its comment says the prune is why.

Hermetic, `internal/api`:

- a row for an empty fixture folder is removed **and `os.Stat` says the directory is still there**
- **an empty root with rows seeded removes nothing** and reports them all `missing` — the unplugged
  disk, and the most important test in this task
- a row outside the root is removed, and that directory survives too
- a `wanted` row is untouched; a row with a download in flight survives an empty folder
- a failing `ReadDir` of the root is a 500 that prunes **nothing**
- a second scan reports `removed: 0`
- the JSON carries `empty`, `removed` and `missing`

Hermetic, `internal/store`: `MoviesOnDisk` excludes NULL paths, excludes a movie with a
`queued`/`downloading`/`stalled`/`completed` download, and includes one whose downloads are all
`imported`/`failed`.

Then live, against the embedded build on 8097 with its own database and a copy taken first — this is
the first change that deletes rows automatically. `LIBRARY_MOVIES=~/curator-local/movies`,
`POST /api/scan`: the stale rows go, the one real film stays, the empty directories are still on
disk, and a second scan removes nothing.
