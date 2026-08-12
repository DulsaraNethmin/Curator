# T2 — Store

**Owns:** `internal/store/`
**Depends on:** T1

## Goal

SQLite persistence: schema applied at startup, and the queries phase 1 needs.

## Do

1. Open the database with **`modernc.org/sqlite`** — pure Go. Never `mattn/go-sqlite3`; see
   [`../decisions.md`](../decisions.md) D4. The cross-compile depends on it.
2. Apply the schema from [`../phase-1.md`](../phase-1.md#schema) at startup, idempotently
   (`CREATE TABLE IF NOT EXISTS`). All three tables, even though phase 1 only writes `movies`.
3. Enable `PRAGMA foreign_keys = ON` and `journal_mode = WAL` on connect. Foreign keys are off by
   default in SQLite, so the `downloads` reference is decorative without it.
4. Queries:
   - `UpsertMovieByPath` — insert or update keyed on `library_path`, so rescans are idempotent
   - `ListMovies` — newest first
   - `GetMovie(id)`
   - `MoviesMissingMetadata` — rows with `tmdb_id IS NULL`, for the matching pass
5. `tmdb_id`, `overview`, `poster_path`, `imported_at` are nullable. Use `sql.NullInt64` /
   `sql.NullString` or pointers — do not paper over NULL with zero values, because "unmatched" and
   "matched to TMDB id 0" must stay distinguishable.

## Do not

- Add an ORM or query builder. Hand-written SQL.
- Import `internal/library` or `internal/tmdb`. The store knows about rows, not about disks or APIs.

## Verify

`go test ./internal/store` against a temp-file database:

- schema applies twice without error
- `UpsertMovieByPath` twice with the same path produces **one** row, with the second call's values
- a movie with `tmdb_id = NULL` round-trips as NULL, not 0
- `MoviesMissingMetadata` returns only the unmatched rows
