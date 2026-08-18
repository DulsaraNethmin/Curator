# T8 — YTS indexer

**Owns:** `internal/indexer/yts.go`, `yts_test.go`, `testdata/yts-*.json`
**Depends on:** nothing — `Release` and `Indexer` already exist

## Goal

The YTS API behind the `Indexer` interface. The easy one: structured JSON, quality and size as
fields, no name parsing and no browser.

## Do

1. `NewYTS(*http.Client)` and a `SearchMovie(ctx, title, year)` satisfying
   [`Indexer`](../architecture.md#indexers). Base `https://yts.mx/api/v2`, overridable so an
   `httptest.Server` can stand in — same shape as `internal/tmdb`. **That host is now NXDOMAIN**: the
   base shipped in the code is `https://movies-api.accel.li/api/v2`
   ([D12](../decisions.md#d12--yts-is-reached-at-movies-apiaccelli-not-ytsmx)), and the overridability
   this line asked for is what made the correction one constant.
2. **Capture a real response as a fixture and build the parser against that.** The endpoint is
   `list_movies.json` with a `query_term`, and each movie carries a list of torrents with a hash,
   a quality, a size and a seed count. Do not trust the field names in this sentence over what the
   API actually returns; check, then write them down in the fixture.
3. One movie yields one `Release` **per torrent** — YTS lists 1080p, 2160p and web variants of the
   same film separately, and they are genuinely different downloads.
4. Take `Quality` and `SizeBytes` from the API's own fields. Do **not** run `parseQuality` over the
   name here: YTS states the quality, and re-deriving it from a constructed string could only make
   it worse.
5. Build the magnet from the hash, with a display name and a **public tracker list**. A bare
   `urn:btih:` with no trackers leans entirely on DHT, and phase 2 is done when magnets *work*.
   Keep the tracker list in one named variable with a comment saying why it is there.
6. Filter by `year` when one is given, against the movie's own year field. A search with no year
   keeps everything.
7. `Indexer` field on every `Release` set to `"yts"`.

## Do not

- Parse quality out of release names. That is 1337x's problem, and `parseQuality` already solves it.
- Edit `indexer.go`, `x1337.go`, `minter.go` or `quality.go` — phase 1's ported code, read-only here.
- Rank, dedup or cache. T11 and T10 own those.
- Add a dependency. Stdlib and the existing `goquery` are all this package has.

## Verify

`go test ./internal/indexer` against the recorded fixture:

- a normal search yields one `Release` per torrent with quality, size, seeders and a magnet
- every magnet parses back through the ported `InfoHash` to 40 characters
- a `year` that matches keeps the film; a year that disagrees drops it
- an empty result set is `(nil, nil)` — "nothing found" is a normal outcome, not an error
- a 5xx, and a body that is not JSON, both return errors that name YTS

Then one live check, guarded so it skips without network and under `-short`, asserting a real search
for `Interstellar` (2014) comes back with at least one 1080p release.
