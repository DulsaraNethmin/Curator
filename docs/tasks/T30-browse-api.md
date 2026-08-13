# T30 — The browse endpoints, and what they do without a key

**Owns:** `internal/api/browse.go`, `browse_test.go`, the `Store` interface and `browser` field in
`api.go`, the fake in `api_test.go`, wiring in `cmd/curator/main.go`
**Depends on:** T27, T29

## Goal

Three routes that turn TMDB into cards the UI can render, annotated with what curator already has.

## Do

1. Declare `Browser` **here**, as a second interface over the same `*tmdb.Client`:
   ```go
   type Browser interface {
       SearchMovies(ctx context.Context, query string, year int) ([]tmdb.Match, error)
       Movie(ctx context.Context, id int) (*tmdb.Details, error)
       Trending(ctx context.Context) ([]tmdb.Match, error)
       Popular(ctx context.Context) ([]tmdb.Match, error)
   }
   ```
   **Not four more methods on `Matcher`.** `Matcher` is phase 1's scan-time lookup; widening it would
   force every phase 1 fake to implement four methods it never calls, for a feature it does not test.
   Attach with `WithBrowser`, mount with `RegisterBrowse`, exactly as `WithSearch`/`RegisterSearch`.
2. Routes, all under `/api/tmdb/` — and the prefix is the rule: **if it is under `/api/tmdb/`, it
   goes dark without a key.** `/api/movies` stays the library and cannot be confused with it.
   ```
   GET /api/tmdb/discover
   GET /api/tmdb/search?query=&year=
   GET /api/tmdb/movies/{id}
   ```
3. One card shape shared by discover and search, carrying
   `library: {movie_id, state, library_path} | null` from `LibraryByTMDBID`. `state` is
   `imported` / `downloading` / `wanted` — `store.Status*` values, not a parallel vocabulary.
4. `discover` answers `{rows:[{id,title,ok,error,results}]}` — **the `indexers[]` shape,
   deliberately.** One rail failing is a success carrying the other, and a failing source that is
   invisible is minter's 200-carrying-a-failure wearing a third hat. Both rails failing is still
   `200` with two named failures, including a rejected key whose message is what the operator needs.
5. `failTMDB`, mirroring `failDispatch`:

   | condition | status |
   |---|---|
   | `s.browser == nil` | **503** naming `TMDB_API_KEY` |
   | `tmdb.ErrUnauthorized` | **502** carrying TMDB's own message |
   | `tmdb.ErrNotFound` | **404** |
   | anything else from the client | **502** |
   | empty `query`, non-numeric `{id}` | **400** |

   `ErrUnauthorized` is **502, not 503**: 503 in this codebase means "you did not set the variable",
   which would be a lie when it is set and wrong.
6. Wire in `main.go` keeping the nil-interface trap the file already documents — one `*tmdb.Client`
   assigned to both `matcher` and `browser`, and the warning extended to say Discover is unavailable
   and Search falls back.

## Do not

- Cache the library annotation. It is the one field that must be fresh — a film dispatched thirty
  seconds ago has to show as downloading.
- Send the API key to the browser.
- Touch `GET /api/search`. It is the fallback and the no-key path.
- Widen `Matcher`.

## Verify

`go test -race ./internal/api`, plus against a running binary:

- a card for a film in the fixture library carries `library.state == "imported"`; one that is not
  carries `library: null`
- `/api/tmdb/movies/999999999` → **404**, not 502
- `/api/tmdb/search?query=` → 400
- a **wrong** key → 502 carrying "Invalid API key", **not** 503
- with no key → all three 503 naming the variable, while `/api/search`, `/api/movies` and `/api/scan`
  are unaffected
- one rail failing still answers 200 with the other rail's results and a named error
- `curl /api/tmdb/discover | grep -c "$TMDB_API_KEY"` is 0, and so is the same grep over `/api/logs`
