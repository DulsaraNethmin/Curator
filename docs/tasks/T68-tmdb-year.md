# T68 — a hand-matched film gets the deep link, and its folder keeps its own year

**Owns:** `movies.tmdb_year`, `store.Movie.MatchYear`, and which year a Jellyfin lookup is narrowed by
**Depends on:** [T67](T67-manual-match.md), which built the hand match and measured the failure this
fixes —
[D36](../decisions.md#d36--a-row-tmdb-could-not-match-is-matched-by-hand-and-the-scan-never-takes-it-back)
closes by naming it *"a decision of its own rather than a rider on this one"*

## Goal

A film matched by hand opens in Jellyfin on the film's own page, not on a search for its name — and
it still does after the next scan.

[T67](T67-manual-match.md) gave an unmatched row a poster, an overview and a catalogue page. The one
thing it could not give it was the Jellyfin deep link, and its own file says why: the link is
narrowed by `years=`, a hand-matched row is the only row whose year can disagree with TMDB's, and
writing TMDB's year onto the row reverted on the next scan.

## The column was doing two jobs, which is the whole finding

`movies.year` is **the folder's year**: `ParseFolder` reads it out of `Title (Year)`,
`library.DestFolder` writes it back, and `link_test.go` asserts the round trip. It is also **the
film's year**, which is what
[D32](../decisions.md#d32--the-jellyfin-link-is-keyed-on-the-tmdb-id-not-on-the-path) narrows a
Jellyfin lookup with, on the stated premise that *"both sides take the year from TMDB"*.

Those are the same number for every row the scan matched, because `SearchMovie` rejects a candidate
whose year disagrees (`internal/tmdb/tmdb.go`, `pick`). Nothing could tell the two meanings apart
until T67 made a row a human had matched, and then the disagreement had to land on one of them.

**D36 proposed freeing `year` for matched rows. Measure the importer before doing that:**

```go
folder, err := library.DestFolder(movie.Title, movie.Year)   // internal/importer/importer.go
```

The folder comes from the **row**, not from the download. A hand-matched row keeps a `/movie/` page;
a release dispatched from it reaches `UpsertWantedMovie`, which matches on `tmdb_id` and returns the
existing row untouched. So a matched row whose `year` had moved to TMDB's would import its next
release into a second folder beside the first, and the next scan would record that folder as a
second row. Freeing the column fixes a link by breaking an import.

## Do

1. **`movies.tmdb_year`, nullable, in `schema.sql` and in `migrate.go`** — both, exactly as
   `downloads.reason` is. `CREATE TABLE IF NOT EXISTS` does nothing to a table that already exists,
   so a fresh clone would have the column and every existing database would not.

   **NULL is a statement, not a gap.** It says the folder's year *is* TMDB's, which is true by
   construction for every row the scan matched — so this backfills to the right answer with no
   backfill.

2. **`TMDBMatch.Year *int`, written only by `store.MatchMovie`.** `SetTMDBMetadata` does not write
   it and must not: it serves the scan, whose matches agree with the folder already, so it would
   only ever store a number equal to the one beside it. `year` is still not in `MatchMovie`'s `SET`
   list, and `TestMatchMovieKeepsTheFoldersYear` still passes unchanged — that is the check that the
   two facts are both true rather than one replacing the other.

3. **`Movie.MatchYear()` is the rule, in one place.** TMDB's year when it is known to differ, the
   folder's otherwise. Everything that identifies the film to something outside curator asks it:

   | call site | year it needs |
   |---|---|
   | `GET /api/movies/{id}` (`api.go`) | `MatchYear()` |
   | `POST /api/movies/{id}/match` (`movies_match.go`) | `MatchYear()` |
   | `checkFilm` (`jellyfin.go`) — proves a key against a library row | `MatchYear()` |
   | `GET /api/tmdb/movies/{id}` (`browse.go`) | already TMDB's, unchanged |
   | `internal/importer` | `Year` — the folder it already wrote |

4. **A film TMDB has no release date for leaves the column NULL rather than storing 0.** That is
   what `FindMovie` does with a zero year too: skip the narrowing rather than narrow to a year
   nothing was released in.

## Do not

- **Free `year` for matched rows**, which is what D36 sketched. See above — it costs a second folder
  on the next import. The measurement is in
  [D37](../decisions.md#d37--year-is-the-folders-tmdbs-year-gets-a-column-of-its-own) so it is not
  re-proposed as an obvious simplification.
- **Write `tmdb_year` from `SetTMDBMetadata`.** It is the scan's write and its rows agree already.
  T67 refused to widen that contract for a phase 9 feature and the refusal still holds.
- **Backfill the column.** Every existing row is a scan match, and NULL is already correct for it.
- **Show it on screen.** Nothing reads it; the type in `web/lib/api.ts` exists so `tsc` describes the
  row the server actually sends, not because a screen wants the number.
- **Build "correct a wrong match".** Still D36's open case, still needs a way in before it needs an
  endpoint, and still not this.

## Verify

`make check`.

Hermetic, Go side, over `t.TempDir()`:

- a hand match writes `tmdb_year` and leaves `year`, `title` and `library_path` exactly as they were
- **three rescans afterwards change neither year** — this is the property the task exists for, and it
  is the assertion T67 could not make
- an unmatched row and a scan-matched row both have `tmdb_year` NULL, and `MatchYear()` answers the
  folder's year for both
- a match with no TMDB release date leaves the column NULL
- the Jellyfin lookup for a hand-matched row is narrowed by **TMDB's** year, and the response still
  reports the folder's
- the migration, from both directions: an older database gains the column and serves a row through
  it, and a fresh one has it from `schema.sql`

Then live, against the real TMDB and the real 10.10.7: a folder whose year disagrees with the film's,
matched by hand, answers `#/details?id=…` rather than `#/search.html?query=…`, and still does after a
rescan. `Zzz Nonexistent Home Movie Xyq (2011)` matched to Iron Man (1726, 2008) is the case T67
measured failing, so it is the one to re-run.
