# T100 — a quality filter, on the list already in hand

**Owns** the release table's quality filter and the gate's first TypeScript check · **takes**
[D52](../decisions.md#d52--the-quality-filter-narrows-the-rendered-list-and-its-chips-are-ordered-by-resolution) ·
**completes** [D11](../decisions.md#d11--rank-by-seeders-quality-is-a-filter-not-a-score), which
offered quality as a filter and only ever got the sort · **sits beside**
[D51](../decisions.md#d51--quality-sections-are-ordered-by-their-best-release-not-by-resolution)
without reversing it · **bound by**
[D49](../decisions.md#d49--a-season-narrows-after-the-fetch-and-a-pack-that-contains-the-episode-is-kept-below-it)

## What it owns

```
Quality  [All 91] [2160p 14] [1080p 45] [720p 10] [— 22]
Layout   [Grouped] Flat        Sections in the order of their best-seeded release —
                               the top row is the same either way.

14 of 91 releases for Interstellar (2014) · yts 5 · tpb 88 · eztv not searched
```

`web/lib/quality.ts` — new, and the whole rule: `qualityOptions`, `filterByQuality`,
`resolveQuality`, `qualityOf`.
`web/components/releases.tsx` — the chip row, the filtered list every other derivation reads, and
`Indexers` taking a `shown` count.
`web/lib/sections.ts` — one comment, because a `resolutionOrder` table now exists in TypeScript one
import from the function that must never use one.
`web/scripts/check-lists.mjs` + `testdata/search/` + `make lists` — the gate's first TypeScript
check.

Nothing on the wire changed. `quality` was already per-release, and the filter is a view.

## Why the client, when `?quality=` already works and is already tested

Latency is the obvious half and it is real — quality is not in the search cache key
(`cacheKey`, `internal/indexer/cache.go:55`) and the cache wraps only 1337x, so every click would
re-fetch YTS, TPB and EZTV live at 7.08 s a film.

The half that actually decides it is that **the server cannot express the last chip**.
`FilterQuality` lowercases a wanted spelling and appends a `p` to anything lacking one, so the em
dash is compared as `—p` and matches nothing; an empty quality never satisfies `keep[r.Quality]`
either. `— 22` is the second-largest group in Interstellar's answer, and a server-side control would
have offered four chips out of five and quietly dropped the fifth.

## The chips use D51's rejected table, and that is not a mistake

D51 refuses `QualityRank` for **sections** because section position decides which release is read
first — resolution-ordered sections lead Interstellar with a 262-seeder 2160p and bury the
1194-seeder 1080p. A **chip** buries nothing: the rows behind it stay seeders-ordered whichever one
is lit. What a menu owes its reader instead is to hold still, and sections deliberately move.

The two orders therefore disagree on one screen, which is the point rather than a defect:

```
Interstellar chips     2160p · 1080p · 720p · —
Interstellar sections  1080p · 2160p · 720p · —
```

## MEASURED, 2026-08-21 — live, YTS + TPB, curator 0.4.0 on :8099, no tunnel

```
Interstellar (2014)        91 releases   2160p 14 · 1080p 45 · 720p 10 · — 22
Dune Part Two (2024)       77 releases   2160p 20 · 1080p 43 · 720p 13 · —  1
Silo, season 1             19 releases   1080p 18 · 720p  1
Severance, season 2        74 releases   1080p 55 · 720p 17 · 480p 2
Severance, season 2 ep 5    7 releases   1080p  6 · 720p  1
```

Five searches in **4 s**. `make check` on the first commit: **56.4 s**, in a detached worktree with
no `.env`.

**Interstellar and Dune reproduce T99's counts exactly** — 91 and 77 releases, and Interstellar's
1080p 45 · 2160p 14 · 720p 10 · none 22. Silo and Severance are lower than T99's 102 and 107 because
**EZTV reported `not_applicable`**: it is keyed by IMDb id, has no keyword surface, and declines a
search that carries no `imdb_id`. Nothing was passed here, so these are YTS + TPB. 1337x was switched
off with `INDEXER_1337X=false` to keep the runs short.

## TRAPS

**A filter opens on its TIER's best, not the answer's best.** Filtered to 1080p, Severance S02E05
leads with a **386**-seeder exact episode and puts a **716**-seeder season pack below it. That is
D49 working — the tier is `rank`'s primary key — and it reads exactly like a sorting bug. The first
version of the guard asserted the global maximum and went red on it; the code was right and the
assertion was wrong. `make lists` now pins seeders descending **within a tier**.

**Chips must be built from the unfiltered answer.** Derive them from the visible rows and the row
eats itself: pick 1080p, every other chip vanishes because no visible row carries one, and the only
way back to the full list is a new search. `check-lists.mjs` asserts both halves — that the real
chips survive a selection, and that the naive derivation would collapse to one.

**Do not reset the selection on a new search.** It is resolved against the options in hand rather
than written back, so a 2160p choice stands down to `All` for a film with none and returns by itself
on the next answer that has it. Resetting the state instead makes the preference unrecoverable, and
it is the reset — not the filter — that somebody has to notice and undo.

**Do not recount the per-indexer counts against the filter.** They report what each source returned,
and they were never a count of the rows below: `Count` is `len(s.releases)` at
`internal/indexer/aggregate.go:266`, before `dedupe`, `narrow` and `rank`. Recounting leaves the row
looking the same while meaning something else. Only the total moves, to `14 of 91`.

**`testdata/search/` is trimmed on purpose.** Real captures projected to `id`, `quality`, `seeders`,
`match`. Magnets and release names are dropped because this repository is public and a magnet
carries a tracker list ([T86](T86-a-tracker-name-is-a-leak.md)) — 259 KB untrimmed against 24.6 KB
kept. Do not "restore" the full answers.

## The gate runs TypeScript now

T99 could not pin its rule and said so: *"there is no test that fails when this is done, because the
rule lives in TypeScript and nothing here runs TypeScript tests."* T100 added a second ordering rule
and the table that would break the first, so the comment stopped being enough.

`make lists` imports the **shipped** modules — a transliterated copy would pass while the real thing
broke — and runs them over the five captures. No framework and no dependency: node's
`--experimental-strip-types` runs the `.ts` directly, CI already pins the same 22.21.1 the image
builds with (read from the Dockerfile's `ARG NODE_VERSION`), and it emits nothing on stderr.

It was verified against the mistake it exists for. Sorting `qualitySections` by resolution turns
**four** assertions red, including `sections lead with the best-seeded release, not the best
resolution — a 262-seeder 2160p must not lead a 1194-seeder 1080p`.

The script is `.mjs` so `tsc` never typechecks it; the fixtures carry only the fields the rules read
and are not whole `Release`s.

## Deliberately not done

- **Multi-select.** `?quality=` takes a comma list and `splitQuality` parses one, so "1080p and
  2160p" is expressible on the wire. Every switch in this UI is single-select and a second
  interaction pattern was not worth the one query it answers.
- **Sending the filter upstream.** `AnyQuality` is `''` precisely because that is what the API reads
  as no constraint, so a screen that wants to narrow the *search* can pass the same value. Nothing
  does.
- **An explanation when a stood-down selection has no row to sit in.** A single-quality answer draws
  no chip row, by the rule that hides the layout toggle for a one-section list — so the sentence
  goes with the row. The list is complete and nothing contradicts it; nothing explains it either.
- **Persisting the choice.** Same as D51's toggle: survives a new search, not a reload.
- **Dropping the Quality column when one quality is selected.** It then repeats the chip on every
  row, which is T99's open question about grouping in a second place, and the same answer applies —
  a column set that changes under a control is the bigger jar.
- **Any browser render check.** The markup is buttons and table rows; the logic is what was verified.
