# T17 — Destination naming and the hardlink

**Owns:** `internal/library/link.go`, `internal/library/link_test.go` — two new files
**Depends on:** nothing

## Goal

The two pure pieces of an import: what a movie's folder and file are **called**, and how a file gets
a second name **on disk**. No database, no HTTP, no environment, no knowledge that qBittorrent exists.

## Do

1. **`DestFolder(title string, year int) (string, error)`** — `Title (Year)`, the exact inverse of
   `ParseFolder`.
   - Replace `":"` with `" - "`, because that is what the library did when colons turned out to be
     illegal in filenames ([D9](../decisions.md#d9--query-tmdb-with-the-raw-folder-title)). Skip this
     and a rescan creates a second folder for a film that is already there.
   - **Reject, do not sanitise-and-continue:** a title containing `/`, `\` or NUL, and a title that
     is `.` or `..` or empty after trimming. `movies.title` arrives from a client via
     `POST /api/downloads`, and this function is the first thing that turns it into a path. An error
     here is a failed import; a silent rewrite is a file written outside the library.
   - Reject a year outside 1000–9999. `Title (0)` does not parse back, so it would break the round
     trip and produce a folder `Scan` skips.
2. **`DestName(title string, year int, ext string) (string, error)`** — `Title (Year).ext`, built on
   `DestFolder` so the two cannot drift. `ext` arrives as `filepath.Ext` gives it (`".mkv"`), is
   lower-cased, and an empty or non-video extension is an error.
3. **`FindFeature(root string, opts FeatureOpts) (Feature, error)`** — the exported recursive search.
   `root` **may be a file or a directory**: qBittorrent's `content_path` is the file itself for a
   single-file torrent and the folder for a multi-file one, and the caller does not know which.
   - `Feature{Path string; Size int64; Others int}`. `Others` is how many other qualifying videos
     were seen, so a double feature is visible in a log instead of silently half-imported.
   - Depth-capped (`FeatureOpts.MaxDepth`, default 3). Skip hidden entries — reuse the **existing
     unexported `isHidden`** — and skip directories named `Sample`, `Extras`, `Featurettes`, `Subs`,
     case-insensitively.
   - A file qualifies if the **existing unexported `isVideo`** accepts it and it is at least
     `FeatureOpts.MinBytes` (default `50 << 20`). The floor is why a 4 MB `sample.mkv` in the root
     cannot win. `MinBytes` is an option purely so tests do not have to write 50 MiB.
   - Nothing qualifying → a **named `ErrNoVideo`**, wrapped with the path. The caller keeps the row
     `completed` and retries; it does **not** mark it failed, because the download did not fail.
   - A symlinked directory is not descended into. A torrent directory is not a place to follow links
     out of.
4. **`Link(src, dst string) error`**, and **`LinkWith(link Linker, src, dst string) error`** where
   `type Linker func(oldname, newname string) error`. `Link` is `LinkWith(os.Link, …)`; the seam is
   what makes the `EXDEV` fallback testable without a second filesystem.
   - `src` must exist and be a **regular file**.
   - `dst` exists and `os.SameFile` says it is `src` → **success, nil**. That is a previous run's own
     link, and it is what makes a retry converge instead of failing for ever.
   - `dst` exists and is a **different** file → error. **Never overwrite**, never remove.
   - `EXDEV` → copy instead. Write to `dst + ".curator-tmp"`, `fsync`, then `os.Rename`. A crash
     mid-copy must leave an obvious temp file, not a half-written `.mkv` that passes every existence
     check and then stops playing forty minutes in. Remove the temp file on any error before the
     rename.
   - It does **not** create the destination directory. The caller creates it only once a source file
     has been chosen, so a failed import cannot leave an empty `Title (Year)/` behind for the scanner
     to record as a zero-size movie.
5. Everything here is in **package `library`**, so `isVideo`, `isHidden` and `videoExtensions` are
   already in scope. Call them; do not copy them.

## Do not

- **Touch `scan.go`.** Not `largestVideo`, not `videoExtensions`, not `Scan`. `TestScanFixture`
  asserts exactly 29 folders and `TestScanSizeIsLargestVideoFile` hardcodes byte sizes and "27
  zero-size folders". `FindFeature` is a *separate* exported function that happens to look similar;
  the flat, non-recursive, no-floor `largestVideo` is what the scanner needs and it stays as it is.

  > **Superseded by [D33](../decisions.md) in [T57](T57-library-way-in.md).** This rule was right
  > while a disagreement between the two pickers produced a wrong `size_bytes`. It stopped being
  > right once the same disagreement decided whether a row *exists*: `largestVideo` is gone and the
  > scanner calls `FindFeature`, so there is exactly one answer to which file is the film. The
  > second rule below still stands.
- **Add anything to `testdata/library/movies/`** — same two tests, same reason. Build every fixture
  this task needs in `t.TempDir()`.
- Import `internal/store`, `internal/qbit`, `internal/config` or anything else. This package is pure
  and stays pure — it has no idea what a download is.
- Read the environment or take a deployment path. Path translation is T20's, at the importer's
  boundary.
- `chmod` anything. A hardlink is a second name for one inode and has no mode of its own; changing it
  changes the file qBittorrent is still seeding. Measured on the Pi: qBittorrent writes `0644`, which
  is already what the library's own files are, so there is nothing to fix.
- Delete a source file, ever, under any error path ([D8](../decisions.md#d8--import-by-hardlink)).

## Verify

`go test -race ./internal/library` — scope commands to this package, a sibling task is mid-write in
the same tree.

**The round trip, over the real fixture.** Read the 29 directory names out of
`testdata/library/movies/` and assert that for every one of them
`ParseFolder(name)` → `DestFolder(title, year)` reproduces the name **byte for byte**. That single
test covers the eight ` - ` titles, `Spider-Man`, `X-Men Origins`, `Deadpool & Wolverine` and
`Tom Clancy's Jack Ryan - Ghost War` without anyone needing to remember they are special.

Naming:
- `DestFolder("Avengers: Infinity War", 2018)` → `Avengers - Infinity War (2018)`
- `/`, `\`, NUL, `.`, `..`, `""` and `"   "` each rejected, each asserted separately
- year `0`, `999`, `10000` rejected
- `DestName` → `Interstellar (2014).mkv`, and `.MKV` lower-cased to `.mkv`

`FindFeature`:
- a bare file path returns that file
- nested one and two levels deep, largest wins, `Others` counts the rest
- a 4 MB `sample.mkv` beside a 60 MB feature loses; a directory holding **only** the 4 MB file is
  `ErrNoVideo`, asserted with `errors.Is`
- `Sample/`, `Extras/`, `Featurettes/`, `Subs/` and `.hidden/` are not descended into, proven by
  putting a *larger* video inside each and asserting it does not win
- an empty directory and a directory of `.srt`/`.nfo` files are both `ErrNoVideo`
- depth cap: a video below `MaxDepth` is not found

`Link`:
- **the hardlink is proven twice, independently** — (a) `os.SameFile` **and** a link count of 2, and
  (b) write new bytes into the **source** after linking and read them back through the destination.
  Either check alone passes for a plain copy or for the wrong file.
- read `Nlink` through a helper that converts to `uint64`. `syscall.Stat_t.Nlink` is `uint16` on
  darwin and `uint64` on linux, and an inline comparison compiles here and breaks
  `GOOS=linux GOARCH=arm64 go vet ./...`.
- re-linking onto its own existing link is nil, and the destination is untouched
- a destination that is a *different* file errors, and that file's contents are **unchanged**
- a `Linker` returning `syscall.EXDEV` produces a byte-identical copy, no `.curator-tmp` left behind,
  and — since a copy is a different inode — link count 1 and `os.SameFile` false
- a `Linker` returning EXDEV where the copy then fails leaves **no** `.curator-tmp` and no partial
  destination
- a missing source, and a source that is a directory, both error before anything is created
- **the source tree is snapshotted before and after every single test, failure cases included, and
  asserted unchanged** — names, sizes and contents. That is [D8](../decisions.md#d8--import-by-hardlink)
  turned into a test instead of a promise.
