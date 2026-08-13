# T28 — Strip the colon before asking an indexer

**Owns:** `internal/indexer/query.go`, `query_test.go`, one line of `aggregate.go`
**Depends on:** nothing

## Goal

The title the indexers answer is not the title curator stores. Make that difference one function,
with the measurement in its comment.

## Do

1. `func NormaliseQuery(title string) string` — removes `":"`, collapses the whitespace that leaves,
   trims. **Nothing else.**
2. Call it **once, at the top of `Aggregator.SearchMovie`**, before the fan-out, so every indexer is
   asked the same normalised string.
3. Carry the measurement in the comment, because it is the whole justification:

   | query | 1337x |
   |---|---|
   | `Avengers: Endgame` | 0 |
   | `Avengers Endgame` | 20 |
   | `Avengers: Infinity War` | 0 |
   | `Avengers Infinity War` | 20 |
   | `Spider-Man: No Way Home` | 20 either way |
   | `Dune: Part Two` | 20 either way |

   YTS and TPB are unaffected. Stripping never lost a result in any case measured.

## Do not

- Strip the hyphen. `Spider-Man` and `X-Men` contain real ones and D9 exists because of that.
- Strip the ampersand or the apostrophe. Unmeasured — this function is a record of a measurement,
  not a guess.
- Put it in `x1337.searchQuery`. `indexer.Cache` wraps 1337x and keys on the title it is handed, so
  normalising below the cache leaves two entries for one identical minter fetch and each path pays
  its own browser launch. Above the cache, the key is the string actually queried.
- Touch `cacheNormaliseTitle`. It still refuses to fold punctuation itself, for the reason its
  comment gives — and after `NormaliseQuery` the two strings are literally the same anyway.
- Change what the API echoes. The canonical title, colon included, is what dispatch stores and what
  becomes the folder.

## Verify

`go test -race ./internal/indexer`:

- a table over the measured titles plus the traps: `Spider-Man: No Way Home` keeps its hyphen,
  `X-Men Origins: Wolverine` keeps its, `Deadpool & Wolverine` is unchanged, `Interstellar` is
  unchanged, `":"` alone becomes empty
- a fake `Indexer` records the title it was handed and sees `Avengers Endgame` while the caller's
  value still has the colon
- and in `internal/api`: `?title=Avengers%3A+Endgame` still echoes the colon in the response, which
  is the half that protects the folder name
