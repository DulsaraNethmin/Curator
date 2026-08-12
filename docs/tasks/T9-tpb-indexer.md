# T9 — The Pirate Bay indexer

**Owns:** `internal/indexer/tpb.go`, `tpb_test.go`, `testdata/tpb-*.json`
**Depends on:** nothing — `Release` and `Indexer` already exist

## Goal

apibay behind the `Indexer` interface. JSON like YTS, but the quality has to be read out of the
release name, and the API has a trap in how it says "nothing found".

## Do

1. `NewTPB(*http.Client)` and a `SearchMovie(ctx, title, year)` satisfying
   [`Indexer`](../architecture.md#indexers). Base `https://apibay.org`, overridable for an
   `httptest.Server`.
2. **Capture a real response as a fixture and build the parser against it.** The endpoint is
   `q.php` with a query and a category; rows carry a name, an info hash, a size, and seeder and
   leecher counts. Verify the actual field names and types — several are **strings holding
   numbers**, which is exactly the sort of thing that silently parses to zero.
3. **Check what an empty search returns before assuming it is an empty array.** apibay is known to
   answer with a single sentinel row rather than nothing. Whatever it does, a search with no matches
   must come back as zero releases — shipping a fake row named "No results returned" as a download
   candidate is the failure this task exists to avoid. Pin the real behaviour in a fixture.
4. Quality comes from the ported `parseQuality` over the release name. It already handles the
   `1080p.UHD` trap; do not write a second one.
5. Build the magnet from the info hash, with a display name and the same public tracker list
   reasoning as [T8](T8-yts-indexer.md).
6. Restrict to the movie categories rather than filtering after the fact, and filter by `year` when
   given — apibay has no year field, so match on the release name and keep anything ambiguous.
   A year is a hint here, not a key.
7. `Indexer` field on every `Release` set to `"tpb"`.

## Do not

- Edit `indexer.go`, `x1337.go`, `minter.go` or `quality.go` — read-only here.
- Reimplement `parseQuality`, `FilterQuality` or `InfoHash`.
- Rank, dedup or cache. T11 and T10 own those.
- Drop a row because one field failed to convert. A zero seeder count is information; a missing row
  is not.

## Verify

`go test ./internal/indexer` against the recorded fixtures:

- a normal search yields releases with name, quality, size, seeders and a magnet
- **the empty-search fixture yields zero releases**, whatever shape the API returns it in
- `1080p.UHD` in a real apibay name still resolves to 1080p, not 2160p
- string-typed numeric fields convert, and a malformed one yields a zero rather than dropping the row
- every magnet parses back through `InfoHash` to 40 characters
- a 5xx, and a body that is not JSON, both return errors that name TPB

Then one live check, guarded so it skips without network and under `-short`: a real search returns
results, and a deliberately absurd query returns zero.
