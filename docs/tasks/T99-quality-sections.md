# T99 — quality sections, ordered by their best release

**Owns** the release table's grouping · **takes**
[D51](../decisions.md#d51--quality-sections-are-ordered-by-their-best-release-not-by-resolution) ·
**nests inside**
[D49](../decisions.md#d49--a-season-narrows-after-the-fetch-and-a-pack-that-contains-the-episode-is-kept-below-it)'s
tiers · **bound by**
[D11](../decisions.md#d11--rank-by-seeders-quality-is-a-filter-not-a-score), which is the reason the
sections are ordered the way they are and not the obvious way

## What it owns

```
Quality  [Grouped] Flat   Sections in the order of their best-seeded release —
                          the top row is the same either way.

## 1080p · 45 releases
   1194  1080p  Interstellar (2014) (2014) 1080p BrRip x264 - YIFY
    580  1080p  Interstellar.2014.PROPER.IMAX.1080p.UHD.BluRay.x265…
## 2160p · 14 releases
    262  2160p  Interstellar.2014.2160p.PROPER.IMAX.REMUX.DV.HDR10+…
## 720p · 10 releases
## no resolution in the name · 22 releases
```

`web/lib/sections.ts` — new, and the whole rule: `sectionKey`, `sectionLabel`, `qualitySections`.
`web/components/releases.tsx` — the toggle, and a table that walks an ordered list of
`{release, section}` instead of `result.releases`.
`internal/indexer/tier_test.go` — `TestRankKeepsATierContiguous`, the Go half of the invariant.

Nothing on the wire changed. `quality` and `match` were both already per-release, and the section
order is derived from the server's own ranking rather than from a rule a client could get wrong —
which is why this needed no new field despite D49's posture of sending the tier rather than
re-deriving it.

## The ordering is the decision, and it was measured

D51 has the argument. The short version is that ordering sections best-resolution-first — which is
what makes 2160p sit in the same place every time — leads Interstellar with a **262**-seeder 2160p
and buries the **1194**-seeder 1080p, and that is the sentence D11 already refused.

## Why the logic is in `lib` and not beside the component

Because it is the only part of this that can be checked without a browser, and it was. There is no
JS test framework in this repo and T99 did not add one; instead a live search was dumped in the
API's release shape and the **shipped module** was run over it — imported, not transliterated:

```
node --experimental-strip-types render.mjs      # imports web/lib/sections.ts
```

That run is the source of every number below, and it is what confirmed the top-row invariant on real
data rather than on reasoning.

## MEASURED, 2026-08-21 — live, YTS + TPB + EZTV merged

```
Interstellar (2014)    91 releases →  4 sections   1080p 45 (best 1194) · 2160p 14 (262)
                                                   · 720p 10 (130) · none 22 (5)
Dune Part Two (2024)   77 releases →  4 sections   1080p 43 (best 1142) · 2160p 20 (518)
Silo S01              102 releases →  4 sections   1080p 52 (858) · 720p 31 (523)
                                                   · none 9 (6) · 480p 10 (4)
Severance S02          107 releases →  5 sections
Severance S02E05         9 releases →  5 sections, under 2 tier dividers
```

Five searches across three indexers: 7.08 s.

**The top row is the same in Grouped and in Flat**, verified on all three dumped answers rather than
argued.

**Silo puts `no resolution in the name` (best 6) above `480p` (best 4).** That is the ordering rule
visible in the one place it disagrees with a resolution table, and it is correct: `QualityRank`
orders a tie-break inside `rank` and does not order these.

**Severance S02E05 is the cost.** Seven rows of chrome over nine rows of list, and three of its five
sections hold one release under a header naming the quality that release's own badge already states.
D51 declines to add a row-count threshold for it and says why.

## TRAPS

**Do not reorder the sections by `QualityRank`.** It looks like a tidy-up, it puts 2160p in a
predictable place, and it is D11 reversed — see the 262-against-1194 above. There is no test that
fails when this is done, because the rule lives in TypeScript and nothing here runs TypeScript
tests; the guard is this file and `qualitySections`' own comment.

**Do not remove `TestRankKeepsATierContiguous` as a test of something obvious.** It is not obvious —
it holds only because the tier is `rank`'s primary key, and the thing it protects is in
`web/components/releases.tsx`. Demote the tier below seeders and a tier reopens partway down the
list, drawing its header a second time, with nothing in the Go diff to suggest a screen broke.

**Grouped is a re-ordering, not decoration.** Within a tier the ranked list is seeders-descending,
so its qualities interleave; a header drawn wherever `quality` changed would open a section every
few rows. `qualitySections` collects, it does not scan for transitions.

## Deliberately not done

- **A quality filter.** `?quality=` exists in the API and `FilterFound` implements it; no screen
  sends it, and sections make the same information browsable without narrowing. A control that
  actually filters is still unbuilt.
- **Dropping the Quality column under grouping.** It repeats its section header on every row. The
  trade is a column set that changes under a toggle, which is the bigger jar.
- **Persisting the choice.** Nothing in this repo stores a view preference — the Movies | Shows
  switch rides in the query string so the URL is the only memory. The toggle survives a new search
  and not a reload.
- **Any browser render check.** The markup is table rows; the logic is what was verified.
