# T88 — store and config learn there are two media types

**Owns** the schema half of [D48](../decisions.md#d48--television-is-additive-a-show-is-a-row-in-movies-and-the-second-library-root-is-opt-in)
**Spends** the hook [D6](../decisions.md#d6--tmdb_id-is-nullable) left in phase 1 — *"a `media_type`
column defaulting to `'movie'` is included from the start so TV is additive later"*
**Blocks** every other task in [phase 11](../phase-11.md), which is why it landed alone

## What it changes, and what it deliberately does not

Nothing curator does today changes. There is still no way to create a TV row — no scanner writes
one, no endpoint accepts one, no screen shows one. What changes is that the **eighteen places where
films and shows would otherwise have quietly mixed** are now fixed, or a compile error, or a
deferral written down against the task that owns them.

## The column that could not be reused

`tmdb_id` is `UNIQUE` at table level and TMDB's movie and tv id sequences overlap — Severance is tv
id **95396**, and a film holds movie id 95396. Putting both in one column is not a rare loud
collision; it is routine silent corruption, and D48 has the table. `migrate.go` cannot drop a column
constraint — its entire mechanism is `addColumn` via `pragma_table_info` — so the answer is a second
nullable column, `tmdb_tv_id`, and `store.tmdbColumn` as the one place a media type becomes a column
name.

**The index is the first thing the migration mechanism has grown that is not a column**, and where
it lives is not a style choice:

```go
// store.go
_, err := db.ExecContext(ctx, schemaSQL)   // ← runs first
...
err = s.migrate(ctx)                        // ← then this
```

`schema.sql` is execd **before** `migrate`, so `CREATE UNIQUE INDEX … ON movies(tmdb_tv_id)`
declared there would be created against a column that does not exist yet and fail with `no such
column` — on exactly the existing databases the mechanism exists to serve. `addIndex` therefore sits
in `migrate.go`, and it asks the database nothing, because `CREATE UNIQUE INDEX IF NOT EXISTS` is
already idempotent by construction — which is the property `migrate`'s own doc comment demands of
every step, arrived at directly rather than through a shape inspection.

A UNIQUE index over a **nullable** column is the right instrument, and the test proves both halves:
SQLite treats NULLs as distinct, so every film — all of which have `tmdb_tv_id` NULL — coexists
under it without noticing, and only two rows claiming the same show are refused.

## The three that would have shipped as silent damage

Traced by hand through the callers rather than by grepping for a column name.

**1. `MoviesMissingMetadata` — the worst, and not close.** A show's `tmdb_id` is NULL by
construction, so an unscoped `WHERE tmdb_id IS NULL` puts every show on the matching pass's work
list **on every scan**. `handleScan` feeds that list to TMDB's `/search/movie` and writes the answer
back with `SetTMDBMetadata`, which overwrites unconditionally by design. For **Fargo, Watchmen,
Hannibal, Westworld, Dune and Snowpiercer** the lookup *succeeds*: the show acquires a film's id,
overview and poster, with no error, no log, and a repeat every scan. The `media_type` predicate is
the load-bearing half here, not the column choice.

**2. `prune`.** `case outside` sits **before** `case recorded[key]` in the switch, so a TV row is not
merely unfound by a movie scan — it is affirmatively deleted, with the reason *"its library_path is
outside LIBRARY_MOVIES, so it can never be served"*, cascading its downloads through the foreign key.

**3. `ScannedMovie.MediaType` defaulting to `movie`.** Correct while there was one media type. A
loaded gun with two, because `UpsertMovieByPath` **rewrites** `media_type` from that field on every
pass — so one construction site that forgot it silently relabels a show as a film, and then (2)
deletes it.

## Required arguments, not optional filters

Every media-scoped read takes a **required** `mediaType`, and there is no value meaning "both".

That is the whole point. Making it required turned all four `LibraryByTMDBID` call sites into compile
errors and nine `ScannedMovie` construction sites into loud test failures, each of which somebody had
to answer on purpose. A default reading `"movie"` would have read fine at every one of them and been
wrong at exactly one — `alreadyHave`, the dispatch guard, where a colliding id refuses a TV grab with
a sentence naming a film the user never asked about.

The writes go the other way round. `tmdbIDWrite` is a `CASE` over the row's **own** `media_type`:

```sql
tmdb_id    = CASE media_type WHEN 'tv' THEN tmdb_id ELSE ? END,
tmdb_tv_id = CASE media_type WHEN 'tv' THEN ? ELSE tmdb_tv_id END
```

so a caller cannot put a tv id in the movie column by passing the wrong argument, because there is no
argument to pass. **The row decides.**

`MoviesOnDisk` is the deliberate exception: it grows `MediaType` on the **row**, not a filter on the
query. That list is what a prune may *consider*, and a prune deletes on a positive finding — so a row
filtered out of it is a row that cannot be *kept* either. Every row present with the caller deciding
which root owns it is what [D33](../decisions.md#d33--a-folder-with-no-film-in-it-is-not-a-movie-the-row-goes-the-folder-stays)'s
asymmetry needs, and it means a scan that walks only the movies root leaves shows in the `default:`
arm — kept, and logged as unaccounted for, which is the safe answer.

## `UpsertWantedMovie` became `UpsertWanted(store.Wanted)`

Not cosmetic. It hard-coded `MediaTypeMovie`, and **both** its identity probes were unscoped — the
`tmdb_id` one dangerously so, because this function's contract is to return an existing row
*untouched*, so a probe that found the film holding 95396 would hand it back and the season pack
would be attached to a film.

A struct rather than five positional arguments because `MediaType` and `Title` are both strings and
would have been adjacent: a caller that swapped them compiles, inserts a row whose media type is a
film's name, and is only noticed when the scan refuses to prune it.

## `LIBRARY_TV`, and why it has no default

`LIBRARY_MOVIES` defaults to the fixture so a fresh clone does something useful. `LIBRARY_TV`
defaults to nothing, and **empty means television is off**. A default would point curator at a
directory nobody asked it to write to, and would turn television on for every existing install on the
next image. `config.TVConfigured()` is the one place to ask.

Two documents had to move with it, and **both are enforced rather than remembered**:
`internal/settings` has a test asserting its registry count matches
[`phase-7.md`](../phase-7.md#the-settings-catalogue), and `cmd/curator` has one asserting every
registry setting has an effective value on the settings screen. The second caught this — a setting
somebody can write and cannot see the current value of is a form that lies.

## Verified

- **Mutation-tested, not just green.** Dropping the `media_type` predicate from
  `MoviesMissingMetadata` fails the Fargo test rather than passing — so the test measures the guard
  rather than restating it.
- The collision itself: a film with movie id 95396 and a show with tv id 95396 in one library, both
  written, both read back, each found only by its own media type's index.
- Two rows claiming the same show are refused; two films with NULL `tmdb_tv_id` are not.
- A media type outside the enum is refused by every scoped read — including `'; DROP TABLE movies; --`,
  since `tmdbColumn` interpolates rather than binds and `validMediaType` is the gate.
- The migration from both directions on a phase-10-shaped database, and twice, because every start
  applies it.
- `make check` green at each of the four commits, in a detached worktree with no `.env` — which is
  exactly the state CI runs in.

## Not done here

- **The importer still files one file per download.** `FindFeature` returns the largest single video
  and warns about the rest; `link.go`'s comment already names *"a season pack that arrived in the
  movie category"* as the case it reports. That is T93.
- **`RemoveFromLibrary` still knows one root**, so deleting a show would refuse with `ErrOutsideRoot`
  and — per [D19](../decisions.md#d19--deleting-a-movie-removes-the-file-and-asks-qbittorrent-to-remove-its-own)
  — take the rows and leave the folder. Also T93.
- **`MarkImported` writes one file's size**, so importing season 2 clobbers season 1's summed size
  until a rescan, and `imported_at` keeps season 1's moment for ever. Both defensible; T93 settles
  them on purpose rather than by accident.
- **`jellyfin.FindMovie` still pins `IncludeItemTypes=Movie`**, so Open in Jellyfin degrades silently
  for a show. T92.
- **`JellyfinSetup.LibraryPath` is single-valued** and provisioning generates *"add a Movies library
  at exactly this path"*. A fresh-install gap, irrelevant on the Pi where a `Shows` library already
  exists — and therefore a written deferral rather than a discovered one. T92.
- **`POST /api/downloads` has no `media_type` field**, so `internal/api` states
  `store.MediaTypeMovie` explicitly at the one construction site. T94 makes it a body field, and it
  is a stated line to change rather than a default to find.
