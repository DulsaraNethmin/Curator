# T93 — the importer files a whole season

**Owns** the write-to-disk half of [phase 11](../phase-11.md) · **needs** T91

## What it owns

`Import` branches on the linked row's `media_type`. For television: `FindEpisodes`, then hardlink
**each** episode to `Show (Year)/Season NN/Show (Year) - SxxEyy.ext`, `AssertInside` per
destination, and `MarkImported` with the **show folder**.

Today it files one file per download: `FindFeature` returns the largest single video and warns about
the rest — and `link.go`'s comment already names *"a season pack that arrived in the movie
category"* as the case it reports. That warning is the bug this task closes.

## What it inherits

[D8](../decisions.md#d8--import-by-hardlink) holds per file: never move, copy over, rename or delete
a source. `library.Link`'s contract gives deduplication free — same inode is success, a *different*
file at the destination is an error and never an overwrite — so re-grabbing a season is idempotent
and a conflicting release refuses loudly.

`MarkImported`'s twin reconciliation keys on `library_path` and works unchanged with the show
folder: one row per show, N downloads pointing at it, converging on the folder as identity. That is
the argument [D48](../decisions.md#d48--television-is-additive-a-show-is-a-row-in-movies-and-the-second-library-root-is-opt-in)
makes for one table.

## Two things to settle on purpose rather than by accident

- **`MarkImported` writes one file's size.** Importing season 2 clobbers season 1's summed size
  until a rescan. The scan is already the source of truth for size; decide and write it down.
- **`imported_at = COALESCE(imported_at, ?)`** keeps season 1's moment for ever. Defensible; say so.

And one fix: `RemoveFromLibrary` calls `RemoveMovieFolder(im.moviesRoot, …)`, so deleting a show
refuses with `ErrOutsideRoot` — which [D19](../decisions.md#d19--deleting-a-movie-removes-the-file-and-asks-qbittorrent-to-remove-its-own)
treats as a refusal rather than a failure, taking the rows and leaving the whole show on disk.
