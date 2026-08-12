# T18 — The import transaction

**Owns:** `internal/store/imports.go`, `internal/store/imports_test.go` — two new files, plus one
stale comment in `internal/store/downloads_test.go`
**Depends on:** nothing

## Goal

One store method that turns "the file is now in the library" into a consistent database, atomically.
It is one method and one transaction because the half-done version of it is a library that shows a
film twice.

## Do

1. **`MarkImported(ctx, hash, libraryPath string, sizeBytes int64, at time.Time) (Movie, error)`** —
   returns the surviving `movies` row. `hash` goes through the existing `normaliseHash` on the way
   in, like every other method in `downloads.go`.
2. `libraryPath` is the **folder**, not the file: `Scan` sets `LibraryPath` to
   `filepath.Join(root, folderName)` and `library_path` is its identity key. Storing the `.mkv` path
   here would mean no future scan ever matches this row.
3. Everything below happens **inside one transaction**. `_txlock=immediate` means it holds the write
   lock from `BeginTx`, so the lookups cannot race a concurrent scan.
4. Look the download up by hash. No row → an error wrapping the existing `ErrNotFound`.
5. Look up **the twin**: `SELECT id FROM movies WHERE library_path = ?`. This is the case the method
   exists for. A `movies` row with `status = 'wanted'` and `library_path = NULL` was created at
   dispatch; if a scan ran between the hardlink and this call, `UpsertMovieByPath` has already
   inserted a **second** row for the same folder.
6. **No twin, or the twin *is* the download's own movie row** — update that row: `library_path`,
   `size_bytes`, `status = 'imported'`, and
   `imported_at = COALESCE(imported_at, ?)`.
7. **A twin that is a different row** — reconcile, in this order, because it is the only order the
   foreign key permits:
   1. repoint **every** `downloads.movie_id` that points at the wanted row to the twin — not just
      this download's. A film can have two attempts against it.
   2. carry `tmdb_id` forward **only if** the twin's is NULL **and** no third row already holds that
      id. `tmdb_id` is `UNIQUE`, and a blind carry turns a successful import into a constraint
      violation.
   3. delete the wanted row. This must come **after** the repoint (the reference is `NOT NULL` and
      foreign keys are on) and **before** the `tmdb_id` write (the row about to be deleted is itself
      the `UNIQUE` conflict).
   4. update the twin: `status = 'imported'`, `size_bytes`, `imported_at = COALESCE(…)`. Its
      `library_path` is already right — that is how it was found.
8. **Keep the twin, delete the wanted row.** Never the reverse. The twin is what every future
   `UpsertMovieByPath` finds, because `library_path` is the identity key; keeping the wanted row
   instead would have the next scan re-insert the twin and the problem repeats for ever.
9. Do **not** overwrite the twin's `title`. It came off the folder name, which is what the library is
   actually called; the wanted row's title came from a client.
10. Finally, `UPDATE downloads SET state = 'imported' WHERE torrent_hash = ?`, using the existing
    `DownloadImported` constant.
11. `COALESCE` on `imported_at` is deliberate: a re-import keeps the **first** moment. The film
    entered the library once, whatever a re-added torrent later does.
12. **Update the stale comment on `TestWantedMovieDoesNotDisturbTheScanUpsert`** in
    `downloads_test.go`. Its assertions are all still correct and must not be touched — the comment
    is what is now wrong, because it reads as though the wanted row and the scanned row stay separate
    for ever, and this task is what reconciles them. Say that phase 4 does the reconciling, and point
    at the sibling test.

## Do not

- Add a column, an index or a migration. `downloads.state` has carried `imported` since
  [T2](T2-store.md) and `movies` already has `library_path`, `size_bytes` and `imported_at`. This
  repo has never run a migration and this task does not start
  ([D14](../decisions.md#d14--the-importer-is-driven-by-the-pollers-torrent-list-not-by-a-completion-event)).
- Add a `DownloadsAwaitingImport` / "completed but not imported" query. The work list is the poller's
  torrent list; a second query would be a second, divergent source of truth — same decision.
- Modify `UpsertMovieByPath`, `store.go`, `movies.go` or `schema.sql`. Phase 4 does not change the
  scan upsert, and `TestWantedMovieDoesNotDisturbTheScanUpsert`'s **assertions** all still hold.
- Touch the filesystem or check that `libraryPath` exists. The caller has already made the link; this
  package knows about rows and nothing else.
- Write `imported` from anywhere else, or let the poller reach it.

## Verify

`go test -race ./internal/store` — scope commands to this package, a sibling task is mid-write in the
same tree.

- **the plain path**: wanted row + download → `MarkImported` → the movie has that `library_path`,
  `status = 'imported'`, a non-nil `imported_at` and the size; the download reads `imported`
- **the twin path, which is the reason this is a transaction**: dispatch (wanted row) → scan the same
  folder (`UpsertMovieByPath` inserts the twin) → `MarkImported`. Assert **exactly one** movie row
  survives, that it is the **twin's** id, that the download now points at it, and that
  `ListMovies` returns one row for the film and not two.
- that same order as a **sibling test next to `TestWantedMovieDoesNotDisturbTheScanUpsert`'s
  subject** — wanted → scan → import — so the guard test and the reconciliation test read together.
- `tmdb_id` carried from the wanted row onto a twin whose id is NULL
- `tmdb_id` **not** carried when the twin already has one
- `tmdb_id` **not** carried when a third movie row already holds that id — and the import still
  succeeds rather than failing on the `UNIQUE` constraint. This is the test that proves the check is
  not decorative.
- **two downloads pointing at the wanted movie** are both repointed at the twin; neither is orphaned,
  which a foreign key would otherwise refuse
- re-importing the same hash is idempotent and `imported_at` **does not move** — assert the same
  timestamp, having advanced the store's clock in between
- an unknown hash returns `ErrNotFound`, asserted with `errors.Is`, and **writes nothing**
- a failure part-way leaves the database untouched: no movie deleted, no download flipped. Prove the
  rollback rather than assuming `BeginTx` does it.
- foreign keys are genuinely on for this path, so the delete-before-repoint ordering is load-bearing
  rather than stylistic
