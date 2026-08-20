# T94 — the API grows a parallel tree

**Owns** the HTTP half of [phase 11](../phase-11.md) · **needs** T88–T92

## What it owns

`GET /api/shows` and `/api/shows/{id}`, with `/api/movies` staying movies-only.
`GET /api/tmdb/shows/{id}`, and `media=tv` on `/api/tmdb/search` and `/api/tmdb/discover`.
`media` and `season` on `GET /api/search`. `media_type` and `season` on `POST /api/downloads` —
where `internal/api` currently states `store.MediaTypeMovie` explicitly, so this is a stated line to
change rather than a default to find.

An unconfigured `LIBRARY_TV` answers **503 naming the variable**, per
[D40](../decisions.md#d40--a-refusals-sentence-is-written-at-the-boundary-that-answers-it).

## The scan, which is where the data loss lives

`POST /api/scan` walks **both roots in one pass and prunes once**, with `outside` computed against
the root that owns each row's media type.

Splitting it into two scoped prunes is the tempting shape and it is wrong: every show would land in
the `default:` arm of a movie scan, logging a warning per show for ever. One prune over both
found-sets is what keeps [D33](../decisions.md#d33--a-folder-with-no-film-in-it-is-not-a-movie-the-row-goes-the-folder-stays)'s
asymmetry intact — and `MoviesOnDisk` deliberately returns both kinds so no row is invisible to it.

**This gets a test in both directions**: a movie scan must not remove a show row, and a show scan
must not remove a film.

## Out of scope, deliberately

**Playback.** `/api/movies/{id}/stream` serves one file per row and a show is not one file. Episodes
play in Jellyfin. No guard is needed — `stream.go`'s `AssertInside(s.libraryRoot, …)` already
refuses a row under the TV root by construction, and that refusal is correct.
