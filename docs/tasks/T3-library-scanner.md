# T3 — Library scanner

**Owns:** `internal/library/`
**Depends on:** T1, T7 (the fixture)

## Goal

Walk the library root, turn each folder into a movie record, and do it idempotently.

## Do

1. `ParseFolder(name string) (title string, year int, ok bool)` — split `Title (Year)`. Every one of
   the 29 real folders matches this; a name that does not is skipped and reported, not guessed at.
2. **Do not "fix" the title.** Return it exactly as it appears on disk. Colons became ` - ` in
   filenames, but `Spider-Man` and `X-Men` contain real hyphens — `Spider-Man - No Way Home (2021)`
   has one of each. Rewriting is T4's problem, and only as a fallback. See
   [`../decisions.md`](../decisions.md) D9.
3. `Scan(root string) ([]Movie, error)` — one entry per directory, carrying `library_path`, `title`,
   `year`, `size_bytes` and `status = "imported"` (it is on disk, so it is imported by definition).
4. `size_bytes` is the **largest video file** in the folder (`.mkv`, `.mp4`, `.avi`, `.m4v`), not the
   folder total — samples, artwork and subtitles sit alongside the feature. Zero if none found; an
   empty folder is not an error.

   > **Amended by [D33](../decisions.md) in [T57](T57-library-way-in.md).** An empty folder is still
   > not an *error* — the scan carries on and reports it — but it is no longer a **movie**: it comes
   > back as a `Skipped` with `NoMedia` set, and the caller removes its row. `size_bytes` is now the
   > feature file's, chosen by `library.FindFeature`, and it can never be zero.
5. Skip hidden entries and non-directories. `.DS_Store` will be there; macOS puts it everywhere.

## Do not

- Call TMDB. That is T4.
- Write to the database. Return values; T5 wires persistence.
- Recurse below one level. The library is flat: `movies/Title (Year)/files`.

## Verify

`go test ./internal/library` against `testdata/library/movies`:

- 29 folders scanned
- all 8 titles containing ` - ` parse with the hyphen **intact** — assert
  `Spider-Man - No Way Home` comes back verbatim, never `Spider Man` or `Spider-Man: No Way Home`
- years parse correctly, including the 2026 releases
- `Interstellar (2014)` reports 4096 bytes — the largest `.mkv`, not the sample and not the total
- a folder with no video files yields size 0 and no error
- scanning twice returns identical results
