# T106 — a poster for a row curator wrote itself

**Owns** `MoviesMissingArtwork`, `SetTMDBArtwork`, the scan's `artworkPass` and the dispatch's own
fill · **applies** [D10](../decisions.md#d10--releases-are-identified-by-an-opaque-id-not-a-url)
(the server does not accept facts it already holds) and
[D9](../decisions.md#d9--query-tmdb-with-the-raw-folder-title-disambiguate-by-year) (record NULL and
surface it, never guess) to a hole neither foresaw · **bound by**
[D48](../decisions.md#d48--television-is-additive-a-show-is-a-row-in-movies-and-the-second-library-root-is-opt-in) ·
**decides nothing** — see *Why no decision record* at the foot.

## What was actually wrong

The library screen drew no thumbnails, and it was not the library screen. `GET /api/movies` on the
Pi, 2026-08-22:

```
Deadpool (2016)               tmdb_id    293660   poster_path null   overview null
The Avengers (2012)           tmdb_id     24428   poster_path null   overview null
The Fast and the Furious      tmdb_id      9799   poster_path null   overview null
Dracula Untold (2014)         tmdb_id     49017   poster_path null   overview null
Prison Break (2005)           tmdb_tv_id   2288   poster_path null   overview null
```

Five of five, and every one of them was created by pressing Download. The chain:

- `store.UpsertWanted` inserts `(tmdb_id|tmdb_tv_id, title, year, media_type, status, added_at)` and
  nothing else (`internal/store/downloads.go:155-158`). At that layer the id is all there is; a
  poster is not something a dispatch knows.
- `MoviesMissingMetadata` — the work list of the pass that would fill it in — selects
  `WHERE media_type = ? AND <tmdbcol> IS NULL` (`internal/store/movies.go:555-566`). **A row born
  with its id is excluded from it for ever.** Not for a while: permanently, on every scan that will
  ever run.
- `Server.matchPass` → `Server.match` → `store.SetTMDBMetadata` is the only writer of `poster_path`
  in normal operation, and the scan's `UpsertMovieByPath` is explicitly forbidden from touching it —
  *"TMDB owns tmdb_id, overview and poster_path, and an upsert never touches them."*
- There was **no way out through the UI either.** A Library card for a row that has a TMDB id links
  to `/movie/?id=<tmdb_id>` (`web/app/library/page.tsx:450-456`), not `/library/film/?id=`, so the
  `MatchPicker` → `PUT /api/movies/{id}/match` → `CorrectMatch` path — which *does* write
  `poster_path` (`movies.go:496`) — is unreachable for exactly the rows that need it.

`Poster` renders `<div class="noposter">{title}</div>` when `src` is null
(`web/components/movie-card.tsx:32-34`), and `.noposter` gets the same 2/3 box as the image
(`globals.css:1253`). So a row with no poster and a poster that failed to load look nearly identical
on screen, which is why this read as a broken image rather than as missing data.

## Ruled out, so nobody re-checks

`images: {unoptimized: true}` is set (`web/next.config.mjs`) and nothing uses `next/image`. There is
no CSP header or `<meta http-equiv>` anywhere in Go or the app shell. The database stores a bare
`/abc.jpg`, not a full URL. `posterURL()` (`web/lib/api.ts:1556`) builds
`https://image.tmdb.org/t/p/w342${path}` correctly, and that host answered **200 in 0.36 s**.

## What it owns

**`store.MoviesMissingArtwork`** — rows of one media type TMDB *has* matched that carry no poster.
A second query rather than a widening of `MoviesMissingMetadata`, because that one is not a work
list, it is a **meaning**: three readers take it as "TMDB could not match this row" — the unmatched
badge, the manual matcher, and the scan's matched/unmatched counts. A matched row appearing on it
would be handed to `/search/movie` to be re-guessed from its folder name.

**`store.SetTMDBArtwork`** — its write half, and the difference from `SetTMDBMetadata` is `COALESCE`.
That one assigns `overview = ?` unconditionally, which is correct for the matching pass: it resolved
the row from nothing, so what TMDB said is the whole truth about it. This caller fills gaps in a row
whose identity is already settled, so a title TMDB has no overview for **must not blank one the row
already carries** — reachable, not theoretical, since a hand-matched row can hold an overview and no
poster and the missing poster is precisely why it is on the list. It also leaves both id columns
alone, so it can never trip their UNIQUE indexes.

**`Server.artworkPass` + `Server.fillArtwork`** — the pass runs after `matchPass` (a row matched a
moment ago already has its poster, so running first would ask TMDB twice) and asks **by id**. D9
does not apply to a by-id lookup: the id came off the row, it *is* the title, and `/movie/{id}`
either knows it or 404s. So a yearless wanted row is fine here and is refused by `matchPass`.

The television half is deliberately **not** behind `tvConfigured()`. A show row exists whether or not
`LIBRARY_TV` is set today — a dispatch creates one — and a root switched off is a reason not to
*walk* it, not a reason to leave a row's poster blank.

**The dispatch fills its own row**, using the same `fillArtwork`, after the torrent is added and
best-effort. Read from TMDB and never taken from the request body: the client had to be told the id
in order to send it, and letting it hand back the poster too is how a request starts describing
itself (D10) — the line `alreadyHave` already draws eight lines above.

## MEASURED — do not re-derive

Against **live TMDB**, on a throwaway binary at `:8097` with the Pi's five rows reproduced exactly:

```
POST /api/scan   {"artwork":4, ... ,"shows_artwork":1}      HTTP 200 in 1.766 s
second scan      {"artwork":0, ... ,"shows_artwork":0}      idempotent
```

Five TMDB calls in 1.77 s, and the posters are real files rather than plausible strings:

```
w342/3E53WEZJqP6aM84D8CckXx4pIHw.jpg   200   29 074 bytes   Deadpool
w342/RYMX2wcKCBAr24UyPD7xwmjaTn.jpg    200   57 038 bytes   The Avengers
w342/gqY0ITBgT7A82poL9jv851qdnIb.jpg   200   44 472 bytes   The Fast and the Furious
w342/m5h3NtZ2ZfryIHl1MvatmANvIqQ.jpg   200   41 977 bytes   Dracula Untold
w342/wnmNPaLvhnMeOqnWlhNkYCZxtda.jpg   200   48 876 bytes   Prison Break
```

### The D48 collision, caught live rather than reasoned about

**TMDB id 2288 is Prison Break as a show and *Closer* (2004) as a film.** The Pi's one show row
holds `tmdb_tv_id = 2288`, so it is a real instance of the overlap D48 is about, and it was sitting
in the test data by accident:

```
/tv/2288      name: Prison Break     poster: /wnmNPaLvhnMeOqnWlhNkYCZxtda.jpg
/movie/2288   title: Closer          poster: /fGGaokx4k00S0J603VG53Qlr9jz.jpg
curator kept  title: Prison Break    poster: /wnmNPaLvhnMeOqnWlhNkYCZxtda.jpg
```

Had `fillArtwork` chosen the endpoint any way other than from the row's own `media_type`, Prison
Break would have acquired Mike Nichols' poster — **with no error and no log**, exactly as D48
predicts. `TestAShowsArtworkComesFromTheTVEndpoint` stages that film deliberately and fails if it is
the one that lands.

## TRAPS found the hard way

- **A row born with a TMDB id is invisible to `MoviesMissingMetadata` for ever.** That `IS NULL` is
  correct and must stay; what it is not is a general "needs metadata" predicate. Anybody widening it
  to fix a missing poster is reintroducing D9's guess and D48's contamination in one edit. This is
  the note that belongs beside that `WHERE` clause, and it is why the fix is a second query.
- **`SetTMDBMetadata` blanks.** Its `overview = ?` is assignment, not merge. Calling it from a
  gap-filler nulls an overview the row already had. `SetTMDBArtwork` exists for that one reason and
  `TestSetTMDBArtworkOnlyEverAdds` pins it.
- **`Browser` spans both id spaces**, unlike `Matcher`/`ShowMatcher`, which are two interfaces
  precisely so the wrong one is a compile error. `fillArtwork` is the one place in the package where
  that split is made by hand, and therefore the only place a test can catch it.

## Still outstanding

- **The dispatch half is verified by test, not against a live dispatch.** `POST /api/downloads`
  needs a resolvable release id and a torrent backend, and the throwaway instance ran with
  `QBIT_USER` blank, so it would answer 503. What is pinned is
  `TestADispatchSurvivesTMDBBeingDown` (201 with the row in the body, poster absent, row left on the
  pass's list) and `TestADispatchFillsTheNewRowsArtwork`. The first real dispatch on the Pi is what
  proves it end to end.
- **The Pi's five rows are still blank.** This ships the fix; a scan on the box is what applies it.
- No `artwork_failed` counter. A row that fails stays on the list and the log names it, which is the
  same record without a second JSON key to keep true.

## Why no decision record

It reverses nothing and establishes no rule — it applies D10 and D9 to a hole neither foresaw. The
finding that is worth keeping, *a row born with a tmdb id is excluded from `MoviesMissingMetadata`
for ever*, is a fact about one `WHERE` clause and belongs in this file beside the query it is about,
where somebody editing that clause will meet it.

## Verification

```bash
# reproduce the Pi's rows on a throwaway instance, then:
curl -s -X POST localhost:8097/api/scan | jq '{artwork, shows_artwork}'
curl -s localhost:8097/api/movies | jq -r '.[] | [.title, .poster_path] | @tsv'
curl -s localhost:8097/api/shows  | jq -r '.[] | [.title, .poster_path] | @tsv'
curl -sI "https://image.tmdb.org/t/p/w342<path>" | head -1     # expect HTTP/2 200

# on the Pi, once this is deployed:
ssh pi 'curl -s -X POST localhost:8090/api/scan | jq "{artwork, shows_artwork}"'
ssh pi 'curl -s localhost:8090/api/movies | jq -r ".[] | [.title, .poster_path] | @tsv"'
```
