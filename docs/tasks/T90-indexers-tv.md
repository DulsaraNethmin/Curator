# T90 — the indexer seam generalises, and YTS declines TV

**Owns** the search half of [phase 11](../phase-11.md) · the widest lane in it

## What it owns

`Indexer.SearchMovie(ctx, title, year)` becomes `Search(ctx, Query{Title, Year, Media, Season})`.
The rename breaking every fake is the point: it forces a visit to each implementation instead of
letting one silently answer a TV query with movie behaviour. Encoding the media type in the title
string does not work — the discriminator is TPB's `cat=` and 1337x's query construction, neither
reachable from a title.

**Capability is an optional interface**, `MediaCapable{ Handles(media string) bool }`, mirroring how
`MagnetResolver` already sits beside `Indexer`. Absent means "handles everything", so only YTS
declares anything: `/list_movies.json` is its whole surface and it has no TV endpoint.

`Release` gains `Season` and `Episode`. **`releaseID` does not change** — it is media-independent on
purpose, because the same magnet found two ways is the same release
([D10](../decisions.md#d10--releases-are-identified-by-an-opaque-id-not-a-url)).

## Three traps, all measured

**YTS would have lied rather than failed.** A TV search reaches `yts.SearchMovie`, which returns an
empty slice with a nil error — its own doc says that means "YTS does not have this film". The
aggregator reports `ok:true, count:0`, which [D20](../decisions.md#d20--the-film-comes-from-tmdb-the-search-box-only-finds-it)
documents as indistinguishable from "nobody uploaded it". Hence `Outcome.NotApplicable`, following
`Unconfigured`'s own argument for its own reason.

**Skipping the `g.Go` reports a timeout.** `slots[i].reported` stays false and the reporting
switch's first arm is `case !s.reported:` → `"timed out after 30s"`. A precomputed `skip []bool` and
a `case skip[i]:` arm **before** it. And the slot is not omitted: `Outcomes` is positional, so
omitting yields a nameless `{"name":"","ok":false}` on the screen.

**TPB's year filter cannot help for TV, only harm.** `tpbNameAllowsYear` drops a release whose name
states a year different from the one searched. A show's searched year is its **first air** year,
while an episode's name carries its **own** air year — so it can never confirm a TV row and only
ever fires on rows stating their correct year. `Year: 0` for TV disables it and also makes
`searchQuery(title, 0)` return the bare title at 1337x, getting all three sources right with no new
branches.

**The cache key must gain media and season.** Without media, a movie search for "Severance" serves
the film's releases to the TV search with `ok:true` — in the 1337x slot, whose documented failure
mode is already `ok:true, count:0`, so nothing would show.

## Measured 2026-08-20

`https://apibay.org/q.php?q=severance&cat=205,208` → 100 rows, season packs *and* single episodes.
`Severance - Season 1 - Mp4 x264 AC3 1080p` 844 seeders; `Severance S02E05 … WEB-DL` 381 seeders.

## Binding

CLAUDE.md's *Fanning work out*: `TestTPBLive` and `TestYTSLiveSearchInterstellar` reach the public
internet from CI and share **one** rule, `classifyLiveFailure`. A live TV test uses that helper.
**Do not add a third live indexer test with its own private skip rule** — removing that divergence
is what T76 existed to do. [D42](../decisions.md#d42--a-dead-base-url-fails-the-build-but-only-once-a-control-name-proves-the-machine-is-online)
still binds.

## What shipped, and the plan it overturned

**The season does not go in TPB's query string.** This task file said to put it
there, reasoning that a season pack would otherwise compete with a hundred single
episodes for apibay's 100-row cap. That reasoning was sound and the conclusion
was wrong, and the difference is a measurement. Live against apibay, 2026-08-20:

| query | rows |
|---|---|
| `q=severance&cat=205,208` | **100** — 98 cat 208, 2 cat 205 |
| `q=severance s02` | 8, all `Severance.S02.*` |
| `q=severance season 2` | 4, a different four |

and the 727-seeder `Severance - Season 2 - Mp4 x264 AC3 1080p` **is not among the
eight**. apibay matches keywords against the release name, so `S02` and
`Season 2` are two spellings of one thing and narrowing costs the best pack the
show has. The query stays the bare title and the season is read back off each
name instead — the same posture the year already had: narrowing happens where
"ambiguous" is allowed to mean "keep". 1337x is the opposite and does take
`<title> S02`.

**Television searches with no year**, so `Release.Year` is 0 on a TV release. The
alternative — threading media into `tpbToReleases` so only `tpbNameAllowsYear` is
gated — costs a branch in three places to preserve a fact about the *show*
stamped onto an *episode*. On the same 100 rows, 98 state no year at all and the
two that do state their own.

**The media constants are declared in `internal/indexer`, not imported from
`internal/store`**, with a single test importing both packages to pin that they
are equal. `internal/indexer` importing `internal/store` would drag the SQLite
driver into the search path.

The live television check is an extra call inside `TestTPBLive` rather than a
third live test — CLAUDE.md is explicit that a third private skip rule is what
T76 existed to remove. Real run: **100 television releases, 99 naming a season**;
the hundredth is a *Seasons 1 and 2 Complete* box set, correctly parsing as no
single season.
