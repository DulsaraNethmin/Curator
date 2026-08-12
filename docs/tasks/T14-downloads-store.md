# T14 — Downloads store

**Owns:** `internal/store/downloads.go`, `downloads_test.go`
**Depends on:** nothing — the table exists

## Goal

The queries phase 3 needs against the `downloads` table, plus the one `movies` query that makes a
download possible: recording a film you do not have yet.

## Do

1. Read [`schema.sql`](../../internal/store/schema.sql) and `movies.go` first. Match their style
   exactly — hand-written SQL, no ORM, errors wrapped with context, `sql.ErrNoRows` translated to the
   package's existing `ErrNotFound`.
2. `UpsertWantedMovie(ctx, title string, year int, tmdbID *int64) (Movie, error)`.
   A download needs a `movie_id` and the film is usually **not on disk yet**, so the row is created
   with `status = 'wanted'` and `library_path = NULL`. Return the existing row rather than a second
   one when the same title and year is asked for twice — re-downloading a film must not fork its
   identity. Match on `tmdb_id` when one is given, and on `(title, year)` when it is not.
   **`library_path` is `UNIQUE` but SQLite allows many NULLs**, so any number of wanted movies
   coexist. That is why this is possible at all; say so in a comment.
3. `InsertDownload(ctx, d Download) (Download, error)` — `torrent_hash` is `UNIQUE`, so inserting one
   that already exists must return the existing row, not an error. Re-dispatching after a restart
   should converge.
4. `UpdateDownloadProgress(ctx, hash string, state string, progress float64, completedAt *time.Time)`.
   Setting `completed_at` is the caller's decision, not this function's guesswork.
5. `ListDownloads(ctx) ([]Download, error)` and `GetDownloadByHash(ctx, hash string)`.
6. A `Download` struct mirroring the columns, with `CompletedAt *time.Time` nullable — a download that
   has not finished has no completion time, and zero-valued time in the database is a lie.
7. Hashes are stored **upper-case**, matching `indexer.InfoHash`, and every lookup normalises before
   querying. qBittorrent reports lower-case; if the two ever meet unnormalised, nothing matches and
   the reason is invisible.

## Do not

- Edit `schema.sql`, `store.go` or `movies.go`. The table already exists — **there is no migration in
  this phase**, and adding one would be phase 1's work redone.
- Add columns, or repurpose `movies.status` beyond the values already documented.
- Import `internal/qbit`, `internal/api` or `internal/indexer`. The store stores; state mapping is
  T13's and orchestration is T15's.
- Delete rows. Nothing in phase 3 removes a download.

## Verify

`go test -race ./internal/store`, against a real temporary SQLite database exactly as the phase 1
store tests do:

- `UpsertWantedMovie` creates a row with `status = 'wanted'`, NULL `library_path` and NULL `tmdb_id`
- twice with the same title and year yields **one** row and the same id
- two different wanted movies coexist, proving many NULL `library_path` values are allowed
- a wanted movie later scanned off disk still upserts by path without duplicating — run the phase 1
  path alongside this one
- `InsertDownload` returns the row; a second insert of the same hash returns the first, with no error
  and no duplicate
- `UpdateDownloadProgress` moves state and progress, and sets `completed_at` only when told to
- `GetDownloadByHash` matches a lower-case hash against an upper-case stored one
- a download for a movie that does not exist fails — the foreign key is the point
- `ListDownloads` on an empty table is empty, not an error
