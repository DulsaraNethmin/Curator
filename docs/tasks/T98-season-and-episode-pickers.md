# T98 — season and episode pickers that show what exists

**Owns** the control above the release table · **needs** [T97](T97-eztv.md)'s `season_list` ·
**completes** [D48](../decisions.md#d48--television-is-additive-a-show-is-a-row-in-movies-and-the-second-library-root-is-opt-in)'s
season selector and [D49](../decisions.md#d49--a-season-narrows-after-the-fetch-and-a-pack-that-contains-the-episode-is-kept-below-it)'s
episode narrowing, neither of which had a control that could show what it was narrowing

## What it owns

```
Season    Every  1  2  [3]
Episode   All  1 2 3 4 5 6 7  [8]  9 10
```

`web/components/releases.tsx` — `SeasonPicker`, replacing a `<select>` and a number input.
`web/app/show/page.tsx` passes `season_list` rather than the count, and `onEpisode` becomes a search
rather than a `setState`.

It draws `.switch`, the class the **Movies | Shows** toggle already uses, rather than a second way
of drawing a group of buttons where one is active. That class gains `flex-wrap`: a 22-episode season
is ordinary and would otherwise push the page sideways.

## Both rows show what exists, which is the point

The season control was built from TMDB's season **count**, so Silo — which reports four seasons and
has aired three — offered a Season 4 that could only ever return nothing. And an episode row was
impossible at all: a count cannot say how many episodes a season has, which is why the episode
control was a free number field.

T97's `season_list` carries `episode_count` per season. Both rows fall out of it.

## Clicking searches, which undoes a compromise

The number field could not. It fires per keystroke and a cold television search costs up to thirteen
seconds behind minter, so typing `12` would have launched a search for episode 1 and abandoned it —
hence Enter, and hence a caption that had to explain Enter. A button is a discrete choice that fires
once, exactly as the season select already did, so the input, its `onKeyDown` and the sentence
explaining them go away together.

## Two seasons are left out, for completely different reasons

**`episode_count === 0`** is announced and unaired. There is nothing to search for, and offering it
is precisely what the count-based control did wrong.

**`number === 0`** is Specials, and it is dropped because it **cannot be asked for**. `?season=0` is
what the API reads an absent season as (`parseSeason`), so a Specials button would silently search
every season and return something that looks like an answer.

Rendering Specials **disabled** was the alternative and it is worse: a control that cannot be
pressed invites somebody to make it pressable without finding the reason first. The overload is a
known gap recorded in [D50](../decisions.md#d50--an-indexer-may-decline-a-query-it-cannot-answer-and-that-is-not-a-failure);
fixing it means giving `Query.Season` a separate "unset", which is a contract change.

## Verified 2026-08-21, rendered

Silo (`/show/?id=125988`) against the live services: season row **Every 1 2 3** — the unaired fourth
absent — then **All 1..10**, then five exact `S03E08` releases, the top one carrying `tpb, eztv`
because the same torrent was found through both and merged on its info hash. Caption reads
*"Narrowed to season 3 episode 8"* with no mention of Enter.

## Deliberately not in scope — quality sections

Deferred by choice, and the approach is settled so it is not re-litigated: **a Grouped/Flat toggle
defaulting to grouped**, nesting inside D49's tiers as *tier → quality → seeders*. Grouping alone
puts a 1-seeder 2160p above a 500-seeder 1080p, which
[D11](../decisions.md#d11--rank-by-seeders-quality-is-a-filter-not-a-score) decided against. It
takes **D51**.
