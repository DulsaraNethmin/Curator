# T29 — Index the library by tmdb_id

**Owns:** `internal/store/movies.go`, `store_test.go`
**Depends on:** nothing

## Goal

The annotation behind "already in your library" on a poster.

## Do

1. ```go
   type LibraryState struct {
       MovieID     int64
       Status      string
       LibraryPath *string
       Downloading bool
   }
   func (s *Store) LibraryByTMDBID(ctx context.Context) (map[int64]LibraryState, error)
   ```
2. Return the **whole** set rather than taking a list of ids. The library is 29 films and will be
   hundreds; one query with no placeholders beats building an `IN` clause per request and never meets
   SQLite's variable limit.
3. `WHERE m.tmdb_id IS NOT NULL` — those rows are exactly the ones no TMDB card can match.
4. **`Downloading` is an `EXISTS` over `downloads`, not `movies.status`.** `store.StatusDownloading`
   is declared and never written: `UpsertWantedMovie` inserts `wanted` and the importer writes
   `imported`, so a film whose torrent is at 60% would be labelled "wanted" on a card. The state that
   is true lives in `downloads.state`, and `imported`/`failed` are the two that do not count.

## Do not

- Return `Movie`. A card needs four facts, and reading thirteen columns for every row to annotate
  forty posters is a read nobody should make twice.
- Add a column, an index or a migration. `tmdb_id` is already `INTEGER UNIQUE`.
- Invent a vocabulary. `Status` is `movies.status`; the API maps it.

## Verify

`go test -race ./internal/store`:

- a row with `tmdb_id NULL` is absent from the map
- a wanted row with a live download reports `Downloading: true`
- the same row once the download reads `imported` reports `Downloading: false` and `Status:
  "imported"` — the transition that would be wrong if this read `movies.status`
- a `failed` download does not count as downloading
- an empty database returns an empty, non-nil map
