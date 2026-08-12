# T4 — TMDB client

**Owns:** `internal/tmdb/`
**Depends on:** T1

## Goal

Turn a folder title and year into TMDB metadata, and be honest when it cannot.

## Do

1. `Client` constructed with an API key and an `*http.Client` (injectable, so tests do not hit the
   network). Base `https://api.themoviedb.org/3`.
2. `SearchMovie(ctx, title string, year int) (*Match, error)` using `/search/movie` with
   `query` and `primary_release_year`.
3. **Query with the raw folder title first.** TMDB's search is fuzzy enough to match
   `Avengers - Infinity War`. Only if that returns zero results, retry with ` - ` collapsed to a
   single space. Never do a bare `-` → `:` replacement — it corrupts `Spider-Man` and `X-Men`. See
   [`../decisions.md`](../decisions.md) D9.
4. **Return no match rather than a wrong one.** If the year disagrees or nothing comes back, return a
   nil match and no error — "not found" is a normal outcome, not a failure. Seven titles are 2026
   releases where a confident-but-wrong match is plausible.
5. `Match` carries `TMDBID`, `Title`, `Year`, `Overview`, `PosterPath`.
6. Respect `ctx`, set a timeout, and treat a 401 as a configuration error worth surfacing clearly —
   a bad API key should say so, not look like "no results".

## Do not

- Download images. `poster_path` is a string; the UI builds the URL later.
- Write to the database or read the disk.
- Retry aggressively. One attempt, plus the ` - ` fallback.

## Verify

`go test ./internal/tmdb` with an `httptest.Server` serving recorded fixtures:

- a normal title matches
- a ` - ` title matches on the first query, with no fallback needed
- a title that only matches after collapsing ` - ` exercises the fallback path
- zero results returns `(nil, nil)`, not an error
- a wrong-year result is rejected
- 401 returns a clear, distinguishable error

Then one live check with a real key, asserting `Interstellar` (2014) resolves to TMDB id `157336`.
