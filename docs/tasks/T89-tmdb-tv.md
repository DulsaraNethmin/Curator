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
