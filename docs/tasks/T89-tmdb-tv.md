# T89 — `internal/tmdb` grows the TV half

**Owns** the metadata half of [phase 11](../phase-11.md)
**Blocks** T94, which cannot offer a TV search screen without it

## What it owns

The TV counterparts of the five movie methods, over `/search/tv`, `/tv/{id}`, `/trending/tv/week`
and `/tv/popular`.

**The payload shapes differ, and that is the substance.** TMDB's TV objects use `name` and
`first_air_date` where movie objects use `title` and `release_date`. A TV-shaped struct is decoded
and mapped onto the **same** public `tmdb.Match`, so callers do not branch. `Details` gains the
TV-relevant fields rather than forking.

Every property the movie half already has is preserved: the year-disagreement rejection, the one
retry with `" - "` collapsed to a space ([D9](../decisions.md#d9--query-tmdb-with-the-raw-folder-title),
and CLAUDE.md's title-parsing trap), `ErrUnauthorized` on 401, `ErrNotFound` on 404.

## The one thing that must not be missed

`TestEveryEndpointScrubsTheAPIKey` is **table-driven over every exported method**. A new endpoint
that is not in that table is an endpoint that can leak the API key into a log, and nothing else in
the repo would catch it.

## Out of scope

Wiring. `api.Browser` is T94's to widen.

## Verified against live TMDB, 2026-08-20

The fixtures were written to TMDB's documented television shape rather than
captured from the live API, because the worktree deliberately has no `.env` and
every v3 endpoint needs a key. `TestLiveTV` is the guard on that, and it has now
been run with a real key rather than only skipped:

```
live show: id=1396 title="Breaking Bad" year=2008 poster=/anFx9aTOOYqgS3v7x3R84Kz67ly.jpg
live: Breaking Bad (2008), 5 seasons, 62 episodes, 0 min/ep, [Drama Crime]
live: 20 results, first is "The Office" (2005)
live PopularShows: 20, first is "Reacher" (2022)
live TrendingShows: 20, first is "Lanterns" (2026)
--- PASS: TestLiveTV (2.80s)
```

**`0 min/ep` is TMDB's answer, not a decode bug**, and it is written down here so
nobody files it as one. `GET /tv/1396` returns `"episode_run_time": []` — an
empty array — alongside `first_air_date 2008-01-20`, `last_air_date 2013-09-29`
and `number_of_seasons 5`. The field is taken as `episode_run_time[0]` when the
array has anything in it, because that is what TMDB's own pages show; averaging
the list would invent a number TMDB never states.

## One movie-facing line changed

`tmdb.ErrNotFound`'s message went from `"tmdb: no such movie"` to `"tmdb: no such
title"`, because one sentinel now covers two media types. The sentinel is
deliberately **not** split: `internal/api/movies_match.go:224` and
`internal/api/browse.go:398` both reach it through `errors.Is` and are unchanged.

Nothing in the code asserts the old string — the surviving `"no such movie"`
matches are subtest names and `store.ErrNotFound` checks. One **record** quotes
it, `docs/progress.md:537`, and that is T96's to mark corrected in place rather
than rewrite.
