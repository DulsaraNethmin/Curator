# T20 — The importer

**Owns:** `internal/importer/` — `importer.go`, `importer_test.go`
**Depends on:** T17 (naming + link), T18 (`MarkImported`), T19 (Jellyfin)

## Goal

The one place that knows a completed torrent and a library folder are the same film. It joins the
three packages before it and is the only one that knows anything about **deployment paths**.

## Do

1. Interfaces are **declared here**, not imported as concrete types, so this is tested against fakes
   with no database and no network — the pattern `internal/api` and `internal/download` already use:
   ```go
   type Store interface {
       GetMovie(ctx context.Context, id int64) (store.Movie, error)
       MarkImported(ctx context.Context, hash, libraryPath string, sizeBytes int64, at time.Time) (store.Movie, error)
   }
   type LibraryRefresher interface { RefreshLibrary(ctx context.Context) error }
   ```
   `*store.Store` satisfies `Store`; `*jellyfin.Client` satisfies `LibraryRefresher`. A **nil**
   refresher is a supported state and means "no key configured", exactly as a nil `api.Matcher` and a
   nil `download.TorrentClient` are.
2. `New(st Store, moviesRoot string, paths Paths, refresher LibraryRefresher, log *slog.Logger) *Importer`,
   where `Paths{Curator, QBit string}` is the translation pair.
3. **`Import(ctx, t qbit.Torrent, d store.Download) (store.Movie, error)`** — the whole import, in
   this order, and the order is load-bearing:
   1. `GetMovie(d.MovieID)` for the title and year.
   2. `library.DestFolder(movie.Title, movie.Year)` — this is where an untrusted title is rejected.
   3. **translate** `t.ContentPath` into curator's namespace.
   4. `library.FindFeature(translated, …)` — choose the file.
   5. build the destination folder path and **assert it is inside `moviesRoot`**.
   6. **only now** `os.MkdirAll` the folder.
   7. `library.Link(feature.Path, dest)`.
   8. `MarkImported(d.TorrentHash, folderPath, feature.Size, now())` — the **folder**, not the file.
   9. mark the refresher dirty, and log the import with `Others` if it was not the only video.
   `MkdirAll` after `FindFeature` and not before: a failure earlier would otherwise leave an empty
   `Title (Year)/` that the scanner faithfully records as a zero-size movie.
4. **Translation**, at this boundary and nowhere else:
   - `Paths.Curator` empty → return `ContentPath` **verbatim**. That is the laptop case and the
     shares-the-mount case, and neither should need configuration.
   - otherwise the path must be under `Paths.QBit` (default `/downloads`); rewrite the prefix.
   - **set-but-not-matching is an error, not a pass-through.** Someone configured a translation and
     it did not apply; hardlinking from an untranslated path fails three layers away from the
     mistake. Compare on path boundaries so `/downloads2` does not match `/downloads`.
   - qBittorrent reports POSIX paths whatever curator runs on, so split them with `path`, and join
     the local side with `filepath`.
5. **`TryImport(ctx, t, d)` — no return value at all.** It calls `Import`, logs the outcome, and
   **suppresses a repeat log for the same hash and the same error**, so a permanently failing import
   says so once instead of every ten seconds for ever. A *different* error for that hash logs again,
   and a success clears the entry. This is what the poller calls, and the missing error return is
   what makes "an import cannot fail a poll tick" a fact about the type rather than a comment.
   Guard the map with a mutex: `Run` is one goroutine, but the manual endpoint is another.
6. **`Refresh(ctx)` — also no return value.** No-op when the refresher is nil, and no-op when nothing
   has been imported since the last refresh. `POST /Library/Refresh` is a whole-library scan: one per
   file in a batch of six would queue six scans of the same library, and one every tick for ever
   would be worse. A failure is logged, clears nothing, and is **not** propagated
   ([D15](../decisions.md#d15--the-jellyfin-refresh-is-best-effort-and-its-key-is-optional)).
7. `library.ErrNoVideo` is **passed through unwrapped enough that `errors.Is` still finds it**. The
   caller leaves the row `completed` so the next tick retries, and does **not** mark it `failed` — the
   torrent downloaded exactly what it promised; it simply is not a film we can place.
8. Inject the clock as a `now func() time.Time` field, unset in production, like `store.Store` and
   `download.Poller` already do.

## Do not

- Import `internal/api`. It imports you.
- Import `internal/config`, or read an environment variable. T21 loads config and passes it in.
- Put translation in `internal/qbit` — it translates nothing by contract
  ([D13](../decisions.md#d13--downloads-are-scoped-by-a-qbittorrent-category-with-its-own-save-path))
  — or in `internal/library`, which is a pure package that must not learn about mounts.
- Query the store for work. The poller's torrent list is the work list
  ([D14](../decisions.md#d14--the-importer-is-driven-by-the-pollers-torrent-list-not-by-a-completion-event)).
- Delete, move, rename or `chmod` a source file, on any path including every error path
  ([D8](../decisions.md#d8--import-by-hardlink)).
- Import more than the feature — no subtitles, no `.nfo`, no artwork, no folder copy.
- Add backoff, a retry counter or a "failed import" state. The retry *is* the next tick; only the log
  is suppressed.
- Let a Jellyfin failure change the outcome of an import.

## Verify

`go test -race ./internal/importer` — scope commands to this package, and build every fixture in
`t.TempDir()`.

- **the happy path end to end**, with a fake store and a real temp filesystem: a folder holding a
  60 MB `.mkv` imports to `movies/Interstellar (2014)/Interstellar (2014).mkv`, and the fake store
  records `MarkImported` with the **folder** path and the feature's size
- **the hardlink is proven twice**, as in [T17](T17-library-link.md): `os.SameFile` plus a link count
  of 2 read through a `uint64`-converting helper, **and** bytes written to the source afterwards
  showing up through the destination
- a **single-file** `ContentPath` imports, not just a directory
- a title containing `/`, and a title of `..`, are rejected **before** anything is created — assert
  no directory appeared under the library root
- the resolved destination is asserted to be inside `moviesRoot`; a title that escapes fails the
  containment check even if `DestFolder` were to change
- translation: empty `Paths.Curator` passes through verbatim; a configured pair rewrites
  `/downloads/complete/curator/X` correctly; a non-matching path is an **error**; `/downloads2/…`
  does not match a `/downloads` prefix
- `ErrNoVideo` from an empty content directory, asserted with `errors.Is`, and the store is **not**
  called — the row must stay `completed`
- a 4 MB `sample.mkv` alone is `ErrNoVideo`; beside a 60 MB feature the feature wins and `Others` is
  reported
- **no empty `Title (Year)/` is left behind** when `FindFeature` fails — read the library root back
  and assert it is empty. This is the ordering test.
- a destination that already exists as the source's own link imports as success (the crashed-run
  retry), and the store is still updated
- a store failure after a successful link is an error, and the **link is left in place** — it is a
  fact on disk and the next tick will re-link onto it harmlessly
- `TryImport` never panics and never returns; two consecutive identical failures for one hash produce
  **one** log line, a different error produces a second, and a later success clears the suppression
- `Refresh` with a nil refresher is a no-op; after an import it calls the refresher **once**; called
  twice with no import in between it calls it once in total; a refresher returning an error changes
  nothing observable
- **the source tree is snapshotted before and after every test, failure cases included, and asserted
  unchanged** — names, sizes, contents and link counts
