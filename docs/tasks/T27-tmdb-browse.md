# T27 — Browse the TMDB catalogue, not just match a folder

**Owns:** `internal/tmdb/` — `tmdb.go`, `tmdb_test.go`, `live_test.go`, new fixtures
**Depends on:** nothing

## Goal

Four new reads: many search results instead of one, a film by id, trending, popular. Phase 1's
`SearchMovie` stays exactly as it is — it is the scanner's, and its tests pin behaviour that must not
move.

## Do

1. **Extract the transport first.** `search` is currently the only thing that touches HTTP, which is
   why `scrubURL` cannot be forgotten. Four more endpoints are four more chances to forget it, so
   every one goes through:
   ```go
   func (c *Client) get(ctx context.Context, path string, params url.Values, out any) error
   ```
   `get` owns the `requestTimeout` context, `api_key`, `Accept`, the 200 / 401→`ErrUnauthorized` /
   404→`ErrNotFound` / default switch, `statusMessage`, **`scrubURL`**, and the decode. `search`
   becomes a caller of it and keeps its exact behaviour — the existing tests pin request counts and
   the two-query fallback and must stay green **untouched**.
2. `Match` gains `BackdropPath` and `VoteAverage`. Both are already in the recorded fixtures and
   merely dropped by the decode struct, so they are tested for free. `genre_ids` stays dropped:
   turning ids into names needs `/genre/movie/list` and a second cache, for a line of text the detail
   endpoint gets for free.
3. `Details` embeds `Match` and adds `Tagline`, `Runtime`, `Genres`, `Status`, `ReleaseDate`,
   `OriginalLanguage`, `SpokenLanguages`, `Studios`, `Homepage`, `IMDBID`. Embedding is deliberate:
   a card and a detail page must not be able to disagree about a title or a year.
4. Four methods:
   ```go
   func (c *Client) SearchMovies(ctx context.Context, query string, year int) ([]Match, error)
   func (c *Client) Movie(ctx context.Context, id int) (*Details, error)
   func (c *Client) Trending(ctx context.Context) ([]Match, error)
   func (c *Client) Popular(ctx context.Context) ([]Match, error)
   ```
   `SearchMovies` does **not** call `pick` and does **not** reject a disagreeing year — the caller is
   a human looking at posters, not a scan deciding what to write. It keeps the `collapseSeparator`
   fallback on a genuinely empty result, because pasting a library folder name into the search box is
   a real thing to do while looking at the Library screen.
5. `ErrNotFound` for an id TMDB does not have, distinct from `SearchMovie`'s `(nil, nil)`: an id
   comes from a URL a human can type, so the API layer answers 404 rather than a vague 502.
6. Refactor `pick` to build its `Match` through the same `toMatch(searchResult)` the new methods use,
   so the two cannot drift.

## Do not

- Change `SearchMovie`'s behaviour, its request count, or its fallback. Phase 1 verified it against
  live TMDB and its tests are the record.
- Add `append_to_response`. `/movie/{id}` alone already returns every field the facts panel shows;
  credits would roughly triple the payload for a cast section that is not in the design. `get` takes
  `url.Values`, so adding it later is one parameter.
- Drop results that have no poster. The UI has a `.noposter` fallback and 15 of the 29 real folders
  have no poster — dropping data to make a grid look tidy is the dishonesty this repo legislates
  against.
- Read the environment, cache anything, retry, or log.

## Verify

`go test -race ./internal/tmdb`, and `go test -short` must still touch no network.

- **the leak test is table-driven over every exported method** — a hung server, a 50 ms deadline, and
  for each: `errors.Is(err, context.DeadlineExceeded)` **and** the literal key absent from
  `err.Error()`. A fifth endpoint added later cannot quietly skip it.
- `SearchMovies` returns every result, in TMDB's order, including ones whose year disagrees
- `Movie` decodes runtime, tagline, genres, studios, spoken languages from a recorded
  `/movie/{id}` response
- an unknown id is `ErrNotFound`, asserted with `errors.Is` — not a generic status error
- 401 is still `ErrUnauthorized` on every method
- every existing `SearchMovie` test passes with no edit
