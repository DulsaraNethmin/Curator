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

## Run end to end, 2026-08-21

Not a test — `go run ./cmd/curator` against a real library on disk, with a real
TMDB key, because everything above this line is a fake answering a fake.

A library of one film and three episodes over two shows, plus one show folder
holding nothing, all above the production 50 MiB floor:

```
POST /api/scan
{"scanned":1,"added":1,"matched":1,"empty":0,"removed":0,"missing":0,
 "shows":2,"shows_added":2,"shows_matched":2,"shows_unmatched":0,
 "shows_empty":1,"episodes":3}
```

```
/api/shows   'Star Wars - Andor' (2022)  tmdb_id=None  tmdb_tv_id=83867   62914560
             'Severance'         (2022)  tmdb_id=None  tmdb_tv_id=95396  125829120
/api/movies  'Interstellar'      (2014)  tmdb_id=157336  tmdb_tv_id=None
```

Three things that could each have been wrong and were not. **`Star Wars - Andor`
matched** — the ` - ` colon substitution CLAUDE.md's title-parsing trap is built
on, met by the television matcher rather than only by the film one. **The two id
columns are cleanly separated**, each row NULL in the other's. And **the sizes
are summed per show**: 125829120 is 2 × 60 MiB, 62914560 is 1.

### The data-loss scenario, on a real process

The same database, restarted with `LIBRARY_TV` **unset** — television turned off
under a library that already holds shows, which is what every existing install
does on the next image.

```
POST /api/scan
{"scanned":1,"removed":0,"missing":2,"shows":0,"shows_empty":0,"episodes":0}

GET /api/shows  ->  503  {"error":"television is not configured: set LIBRARY_TV"}

INFO scan: rows kept without being considered, because nothing walked their
     library root  media_type=tv rows=2
     why="LIBRARY_TV is unset, so television is off and nothing under it was scanned"
```

`removed: 0`. One log line about the **root**, not one per show. And turning
television back on recovers both rows untouched — `shows=2 shows_added=0
episodes=3 removed=0 missing=0`, with `tmdb_tv_id` 83867 and 95396 still on
them, so nothing was lost and nothing was re-matched.
