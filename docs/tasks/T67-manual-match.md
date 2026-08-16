# T67 — an unmatched film can be matched by hand

**Owns:** `POST /api/movies/{id}/match`, `store.MatchMovie`, and the picker on `/library/film/`
**Depends on:** [T61](T61-unmatched-film-has-no-way-in.md), which built the page this action lives on
and left the seam for it —
[D35](../decisions.md#d35--a-library-row-with-no-tmdb-id-is-addressed-by-curators-own-id-at-libraryfilmid)
closes with *"When it is built it takes a number of its own and cites this one."*

## Goal

A film whose folder name TMDB could not resolve can be pointed at the right film by a human, once,
and it stays pointed there.

[T61](T61-unmatched-film-has-no-way-in.md) gave that row a page and a way to watch it. It is still a
row with no poster, no overview and no deep link, and the page's own sentence — *"TMDB had no match
for this folder name"* — is the end of the conversation rather than the start of one. This is T61's
**option 3**, which its own file called *"the only option that also fixes the poster, the overview
and the Jellyfin link"*.

## Who this actually serves, which is narrower than it sounds

D35 is explicit that manual matching *"cannot help the keyless population at all (there is no key to
search with, which is why the rows are unmatched)"*. That population is the guaranteed one and it
heals on a restart. So the population **this** task serves is the other one:

| the row's state | does this help? |
|---|---|
| keyless install, every row NULL | **no** — no key to search with. It heals on restart (D35) |
| a key is in force, the folder name did not resolve | **yes** — this is the whole population |
| a key is in force, TMDB has never indexed the film | **no** — there is nothing to match it to |
| a key is in force and the scan matched the **wrong** film | **not reachable**, and deliberately so — see *Do not* |

That is a real but small population, and it is the honest size of this task. It is worth building
because for each of those rows the poster, the overview and the Jellyfin deep link are missing for
ever, and no rescan will ever change that — the match pass only ever reads
`WHERE tmdb_id IS NULL`, so a row that stays NULL is retried for ever while a folder name that will
never resolve is retried for ever with it.

## The measurement that says a manual match is worth writing

**A manually-set `tmdb_id` survives every rescan**, by three independent constructions, all verified
by reading rather than assumed:

1. `internal/store/movies.go:138-142` — the rescan `UPDATE`'s `SET` list is `title, year,
   media_type, status, quality, size_bytes`. `tmdb_id`, `overview` and `poster_path` are not in it.
2. `store.ScannedMovie` (`movies.go:57-67`) has **no** TMDB field, so the upsert could not carry one
   if it wanted to. Its doc comment states the rule: *"must not overwrite a match an earlier run
   established."*
3. The scan's match pass reads `MoviesMissingMetadata` — `WHERE tmdb_id IS NULL`
   (`movies.go:209`) — so a matched row is never visited and `SetTMDBMetadata` is never re-run
   against it. `internal/api/api.go:413` is the only call site in the repository.

The one way a manual match is lost is `prune` deleting the row when the film leaves the disk. That is
[D33](../decisions.md#d33--a-folder-with-no-film-in-it-is-not-a-movie-the-row-goes-the-folder-stays)
working as designed, not a hole in this feature.

## Do

1. **`store.MatchMovie(ctx, id, TMDBMatch) (Movie, error)`, and it is not `SetTMDBMetadata`.**
   `SetTMDBMetadata` overwrites unconditionally and must keep doing so — it serves the scan, where
   the row is already known to be NULL because that is how it was selected. This one is reached from
   a request and takes three named refusals instead:

   - no such row → `ErrNotFound`, as `GetMovie` already does
   - the row already has a `tmdb_id` → **`ErrAlreadyMatched`**
   - another row already holds that `tmdb_id` → **`ErrTMDBIDTaken`**

   All three are decided by `SELECT` inside the same transaction rather than by parsing the driver's
   UNIQUE violation. `adoptTwin` (`internal/store/imports.go:157-166`) already does exactly that for
   the same column, and a `strings.Contains` on a `modernc.org/sqlite` message is a guard that breaks
   silently on a driver upgrade.

   It returns the updated `store.Movie` so the caller does not re-read it.

2. **`POST /api/movies/{id}/match`, registered beside the other `/api/movies/{id}/…` routes**, body
   `{"tmdb_id": 1083381}`. It answers the same `movieBody` `GET /api/movies/{id}` does, so the page
   can swap the row it is holding with no reload — and `jellyfin_url` is recomputed with it, because
   `jellyfinLinkFor` stops taking the nil-`tmdbID` search branch (`browse.go:352`) and starts taking
   the deep-link lookup (`browse.go:359`).

   **That lookup only lands when the folder's year agrees with TMDB's** — see *The year, and why it
   is not written* below. Measured both ways against the real 10.10.7: a 2014 folder matched to
   Gone Girl answered `#/details?id=cfda2a8b…&serverId=004aee63…`, and a 2011 folder matched to Jaws
   (1975) answered D32's search link.

3. **The server re-fetches the film from TMDB rather than trusting the body.** `Browser.Movie(id)`
   is already on the interface. It costs one request and buys two things: a `tmdb_id` that does not
   exist is a 404 instead of a row pointing at nothing, and `overview`/`poster_path` are written from
   the same source the scan writes them from, so a matched-by-hand row and a matched-by-scan row are
   indistinguishable afterwards.

   **The title is not overwritten**, exactly as `s.match` declines to (`api.go:389-392`, `402`):
   `TMDBMatch.Title` stays nil, and `library_path` is the row's identity anyway.

4. **The refusals map to statuses that say whose problem it is**, reusing `failTMDB` for the TMDB
   half:

   | condition | status |
   |---|---|
   | no browser configured | 503, `errTMDBUnconfigured` — names `TMDB_API_KEY` |
   | `tmdb_id` missing or not a positive number | 400 |
   | no such movie row | 404 |
   | `tmdb.ErrNotFound` — TMDB has no such film | 404 |
   | `ErrAlreadyMatched` | 409 |
   | `ErrTMDBIDTaken` | 409 |
   | `tmdb.ErrUnauthorized` | 502, never 503 — see `browse.go:388-392` |

5. **The picker is seeded from the row, and is not a box the user retypes.** The page already holds
   `movie.title` and `movie.year` (`library/film/page.tsx:123-124`), so the first search runs from
   them the moment the picker opens. The query stays editable, because a folder name that TMDB could
   not resolve is exactly the string most likely to need one word changed.

   `GET /api/tmdb/search` needs **no change**: `api.tmdbSearch(query, year)` already exists
   (`web/lib/api.ts:683`), and `SearchMovies`' own doc comment already anticipates this
   (`internal/tmdb/tmdb.go:396-397`) — *"pasting a library folder name … into the search box is a
   real thing to do while looking at the Library screen."*

6. **It refuses to open at all with no key in force, and `integrations` is what it asks.**
   `integrations[].configured` is `cfg.TMDBAPIKey != ""` — the **running** value. `settings[].configured`
   is true the moment a key is stored, while the process still has `browser == nil` until a restart
   (D35's load-bearing restart). Gating the picker on the settings row would offer an action that
   answers 503, which is the failure D35 spent a paragraph on.

   The same swap fixes the sentence above it. Today the lede branches on the settings row, so a
   stored-but-not-yet-in-force key renders *"TMDB had no match for this folder name"* — which is the
   small lie T61 wrote that branch to avoid, arriving through the other door. On `integrations` both
   states read as "no key in force", and *"add one, restart, and scan again"* is the correct remedy
   for both.

7. **A film curator already holds is shown as already held, before it is clicked.** Every card from
   `/api/tmdb/search` carries `library: LibraryState | null` (`browse.go:284-311`), which is the
   `ErrTMDBIDTaken` collision made visible one step earlier.

## The year, and why it is not written — built, measured, reverted

**This was implemented, run against a real library, and taken back out. Do not re-add it without
reading this.**

> **Settled since, by [T68](T68-tmdb-year.md) and
> [D37](../decisions.md#d37--year-is-the-folders-tmdbs-year-gets-a-column-of-its-own).** Everything
> below still holds — `year` is still the folder's, and writing TMDB's onto it still reverts. What
> changed is that TMDB's year now has a column of its own, `movies.tmdb_year`, so the deep link
> lands without this column moving. The closing paragraph's *"moving `year` out of the scan's
> authority"* is the one thing here that was **not** done: the importer builds the library folder
> out of the row's own year, so freeing it costs a second folder on the next import.

A hand-matched row is the only way to produce a row whose `year` disagrees with TMDB's: the scan
cannot, because `SearchMovie` rejects a match whose year disagrees. That disagreement costs the
Jellyfin deep link, because [D32](../decisions.md#d32--the-jellyfin-link-is-keyed-on-the-tmdb-id-not-on-the-path)
narrows its lookup with `years=` and says so outright — *"the narrowing is safe because both sides
take the year from TMDB"*, which is exactly the premise a hand-matched row breaks.

Writing TMDB's year onto the row fixes it, and [D20](../decisions.md#d20--the-film-comes-from-tmdb-the-search-box-only-finds-it)
appears to authorise it: a human picking a film from a grid of posters makes *"title, year and
`tmdb_id` … from TMDB … authoritative"*. **It does not survive.** `UpsertMovieByPath`'s `SET` list
includes `year` (`internal/store/movies.go:138-142`), so the next scan rewrites it from the folder
name. Measured, on the real TMDB and the real 10.10.7:

| | `tmdb_id` | `year` | `jellyfin_url` |
|---|---|---|---|
| after matching a 2019 folder to a 2008 film | 1726 | 2008 | `#/details?id=0a938432…` |
| after one rescan | 1726 | **2019** | **search link** |

`overview` and `poster_path` survived both. Only `year` reverted, because only `year` is a column the
scan owns.

**A value that reverts on the next scan is worse than one never written**, so the year stays the
folder's and the link falls back to D32's search, which always lands somewhere useful and never 404s.
Making it stick means moving `year` out of the scan's authority for matched rows — a change to phase
1's documented division of authority, which is a task and a decision of its own and not a rider on
this one. `TestMatchMovieKeepsTheFoldersYear` and `store.TMDBMatch`'s comment both carry this so it is
not rediscovered.

## Do not

- **Offer this for a row that already has a `tmdb_id`.** `adoptTwin` decided this exact question for
  this exact column (`internal/store/imports.go:139-144`): *"the twin is already matched. A match the
  scanner established from the folder title is not worth overwriting with one a client sent."*
  Correcting a **wrong** match is a different feature — it needs its own way in, because a matched
  card routes to `/movie/` and never reaches this page — and building the server half now would ship
  a code path with no caller.
- **Write `tmdb_id` from the request body without asking TMDB.** See 3. It is one request, and
  without it a typo writes a row that points at a film that does not exist.
- **Overwrite the title with TMDB's canonical one.** `Avengers - Infinity War` becoming
  `Avengers: Infinity War` undoes the very substitution that identifies the folder
  ([D9](../decisions.md#d9--query-tmdb-with-the-raw-folder-title)), and `api.go:389-392` already
  refuses it for the scan.
- **Write TMDB's year onto the row.** It reverts on the next scan. See above — it was built and
  measured, and the table is there so it is not built again.
- **Add a `WHERE tmdb_id IS NULL` guard to `SetTMDBMetadata`.** It is the scan's write, its callers
  select on that column already, and narrowing it would be a change to phase 1's contract for a
  phase 9 feature.
- **Classify the UNIQUE violation by matching on the driver's error string.** See 1.
- **Give the picker its own poster-grid component.** `MovieCard` exists and the search grid already
  renders it.
- **Touch `/movie/`, the Library grid, or `GET /api/movies`.** D35 settled all three and none of them
  changes because a row acquired an id.

## Verify

`make check` — `next build`'s type check is the UI's whole guarantee, so the new call and its types
go in `web/lib/api.ts` where `tsc` catches a screen that forgot them.

Hermetic, Go side, over `t.TempDir()`:

- matching a NULL row writes `tmdb_id`, `overview` and `poster_path`, leaves `title`, `year` and
  `library_path` exactly as they were, and answers the row back
- a **rescan afterwards leaves the match alone** — this is the property the whole task rests on, and
  it is asserted rather than argued
- matching a row that already has an id is **409**, and the stored id is unchanged
- matching to an id another row holds is **409**, and both rows are unchanged
- a `tmdb_id` TMDB does not know is **404** and nothing is written
- with `browser == nil` the route is **503** and names `TMDB_API_KEY`

Then live, against the embedded build on a free port (not `next dev` — cross-origin, no CORS): a
library folder whose name TMDB cannot resolve shows the picker seeded with its own title, picking the
right film redraws the page **without a reload** — the `unmatched` badge, the explanatory sentence and
the picker all go, and the catalogue link appears — and a second scan does not undo it.

The Jellyfin deep link is verified on a row whose folder year **agrees** with TMDB's, because that is
the only case where it can land; see *The year, and why it is not written*.
