# T69 — a film matched to the wrong film can be pointed at the right one

**Owns:** `PUT /api/movies/{id}/match`, `store.CorrectMatch`, and the link from `/movie/` that is the
way in
**Depends on:** [T67](T67-manual-match.md), which built the picker and refused this case by name —
[D36](../decisions.md#d36--a-row-tmdb-could-not-match-is-matched-by-hand-and-the-scan-never-takes-it-back)
states it as *"a different feature and needs its own way in"* — and
[T68](T68-tmdb-year.md), which gave the row somewhere to put TMDB's year

## Goal

A library row matched to the wrong film can be repointed at the right one, and the wrong film's
poster and overview go with it.

[T67](T67-manual-match.md) covered the row TMDB never matched. The row TMDB matched **badly** was
the one line of its table marked *not reachable, and deliberately so*: there was no way to reach the
picker for a matched row, and building the write without one would have shipped a code path with no
caller. This builds the way in first and the write second.

## The way in was the design question, and it is one link

A matched card routes to `/movie/?id=<tmdb_id>` and never reaches `/library/film/`, which is where
the picker lives. That is [D35](../decisions.md#d35--a-library-row-with-no-tmdb-id-is-addressed-by-curators-own-id-at-libraryfilmid)
working correctly and it is not what changes.

What changes is that `/movie/` gains a link back. It costs nothing to build because the id is already
on that page — `details.library.movie_id` is what the player is handed — and `/library/film/` already
accepts a matched row on purpose, its own comment saying that refusing one *"would be an invented
rule"*. The seam was there; only the inbound link was missing.

**`/movie/` is also the right place rather than merely the cheap one.** It is where somebody arrives
holding the evidence: they clicked a card in their Library and got a page about a film that is not
the one in that folder. Every other thing on that screen asserts the match is right — the poster, the
title, *"curator already has this film"* — so the control that doubts it belongs beside them.

## Clearing the match first is the obvious implementation and it is broken

The shape that suggests itself is *clear `tmdb_id`, `overview`, `poster_path` and `tmdb_year`, then
reuse `MatchMovie`*. **Measure what a NULL `tmdb_id` means before writing it:**

```go
// internal/store/movies.go
MoviesMissingMetadata → `WHERE tmdb_id IS NULL`
```

That is the scan's work list. Every row on it is handed to `SetTMDBMetadata`, which overwrites
unconditionally, and the search it overwrites from is **the folder name** — which is the thing that
produced the wrong match in the first place. So a scan landing between the clear and the match
restores precisely the film being corrected away from. `adoptTwin` (`internal/store/imports.go`) is a
second writer with the same opening: it sets `tmdb_id` on a row whose own is NULL, so a completing
import could repoint it too.

Neither needs a slow human. The clear and the match are two HTTP requests, and a scan is one button.

So the correction is **one statement inside one transaction**: the row goes from the wrong id to the
right one and is never NULL in between. This costs a second store method rather than a reuse, and
that is the price of the window not existing.

## Do

1. **`store.CorrectMatch(ctx, id, TMDBMatch) (Movie, error)`, beside `MatchMovie` and not inside it.**
   It shares the `UPDATE` — one `writeMatch` helper, so a corrected row and a hand-matched row cannot
   drift into different shapes — and differs in exactly two refusals:

   - the row has no `tmdb_id` → **`ErrNotMatched`**, the exact inverse of `ErrAlreadyMatched`
   - another row holds the target id → `ErrTMDBIDTaken`, **with `AND id != ?`**

   That last clause is the one refusal that is deliberately not inverted. Without it, re-picking the
   film the row already holds collides with itself and the user is told their film conflicts with
   their own row. With it, re-picking is a refresh: a new overview, poster and `tmdb_year`.

2. **`PUT /api/movies/{id}/match`, beside the `POST`.** Same path, same body, same response — the
   resource is the row's match and what differs is whether one is being established or replaced,
   which is what a method is for. A body flag (`{"replace":true}`) loses on the property that
   matters: a client that forgets a flag gets the destructive behaviour, where a client that sends
   the wrong method gets a 409 naming the right one.

   Everything either method does around the write is identical — the key check, the body, the TMDB
   re-read, the response — so it is shared, and that is what keeps a corrected row and a matched row
   indistinguishable afterwards.

3. **Fix the sentence T67's 409 answers with.** It reads *"…so there is nothing to correct here"*,
   which was true while a wrong match had no remedy and is false the moment one exists. Both 409s now
   name the other method. A message that describes the impossible is worse than a terse one, because
   somebody reads it and stops looking.

4. **The picker decides which job it is from the row, not from a prop.** It already takes the whole
   `MovieRow`, so `movie.tmdb_id !== null` answers it, and a page cannot open it in the wrong mode.
   It decides three things: the sentence under the heading, the endpoint, and whether the film the
   row already holds counts as a collision — `film.library.movie_id !== movie.id`, the same line the
   store draws.

5. **The lede changes with it.** *"curator searched TMDB … and found nothing it was sure of"* is a
   plain falsehood on a row that is matched — something did resolve, it was simply wrong.

## Do not

- **Clear the match and reuse `MatchMovie`.** See above; it hands the row back to the scan, which
  re-matches it from the folder name that was wrong. The measurement lives in
  [D38](../decisions.md#d38--a-wrong-match-is-corrected-by-overwriting-it-never-by-clearing-it-first),
  in `store.CorrectMatch`'s doc comment, and in
  `TestClearingAMatchWouldHandTheRowBackToTheScan`, which performs the clear with raw SQL precisely
  because no exported method does it and none should.
- **Add an "unmatch" action.** Same reason, and it has no caller: nothing in the product wants a row
  that names no film. A row whose film left the disk is
  [D33](../decisions.md#d33--a-folder-with-no-film-in-it-is-not-a-movie-the-row-goes-the-folder-stays)'s,
  not this one's.
- **Widen `MatchMovie` to overwrite instead.** Establishing a match and replacing one are different
  intents, and its refusal is the guard that a client cannot destroy an answer somebody already gave.
  T67 refused to widen `SetTMDBMetadata` for the same reason one layer down.
- **Write TMDB's year onto `year`.** Unchanged from [D37](../decisions.md#d37--year-is-the-folders-tmdbs-year-gets-a-column-of-its-own):
  the folder's year is half a directory name, `tmdb_year` is the film's, and a correction moves the
  second one only.
- **Overwrite the title with TMDB's.** [D9](../decisions.md#d9--query-tmdb-with-the-raw-folder-title),
  exactly as both existing writes already decline to.
- **Route the picker's failures through `<Failure>`.** `states.tsx` titles every 409 *"Not finished
  yet"*, which is right for the import path and wrong for all four of the others. The picker renders
  `cause.message` inline, so that stays latent — fixing the title is a task of its own and is not
  smuggled in here.
- **Touch the Library grid or `GET /api/movies`.** A matched card still opens the catalogue page;
  that is D35 and nothing about it changes because a match can now be wrong.
- **Offer the correction for a row that is not on disk.** A wanted row has a `tmdb_id` because a
  dispatch gave it one, not because a folder name was resolved, so there is no folder to be wrong
  about.

## Verify

`make check` — `next build`'s type check is the UI's whole guarantee, so the new call and its types
go in `web/lib/api.ts` where `tsc` catches a screen that forgot them.

Hermetic, Go side, over `t.TempDir()`:

- correcting a matched row replaces `tmdb_id`, `overview`, `poster_path` and `tmdb_year` together,
  and leaves `title`, `year` and `library_path` exactly as they were
- correcting a row with **no** match is `ErrNotMatched` and writes nothing
- correcting onto an id another row holds is `ErrTMDBIDTaken`, and **both** rows are unchanged
- correcting onto the id the row already holds is **allowed**, and refreshes the columns
- three rescans after a correction change neither year and neither id
- **a corrected row is never on `MoviesMissingMetadata`'s list, and a cleared one is** — the second
  half is the measurement above made executable, and it is the test that stops the design being
  simplified back into the bug

Over the API, on the fake store:

- `PUT` at an unmatched row and `POST` at a matched one are both **409**, each naming the method that
  would have worked, and neither spends a TMDB request finding out
- the already-matched 409 no longer claims there is nothing to correct
- `PUT` with no key in force is **503** and names `TMDB_API_KEY`

Then live, against the real TMDB and the real 10.10.7: take the folder T67 and T68 both measured on,
`Zzz Nonexistent Home Movie Xyq (2011)`, matched to Iron Man (1726). Open it from the Library, follow
*Not this film?* off the catalogue page, correct it to a different film, and confirm the poster, the
overview, `tmdb_year` and `jellyfin_url` all move together — then rescan and confirm none of them
moves back.
