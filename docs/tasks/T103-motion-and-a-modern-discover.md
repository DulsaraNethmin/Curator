# T103 — motion, skeletons, and an icon set of its own

**Owns** the UI's loading states, its entrance motion, its icons and the two hover gestures ·
**takes** [T102](T102-more-rails-and-a-visual-refresh.md)'s token layer and extends it with icon
sizes and two entrance curves · **does not overturn** globals.css's opening rule — no utility
framework and no webfont — and the same reasoning is why there is no icon package either: seven
outline paths are drawn in `web/components/icons.tsx`, geometry after Lucide's, and the npm package
that ships them is one more dependency that has to still install in two years.

## What it owns

Four moves, in the order they paid off:

1. **Skeletons sized to the content they become** (`web/components/skeleton.tsx`). Ten screens said
   "Loading…" as a paragraph; on Discover the pitch rendered where the billboard would land, so the
   trending rail's arrival shoved the whole page down. The stand-ins carry the real components'
   geometry — the billboard's clamp and break-out, the rail's 10.5rem columns, the grid's cells, the
   hero panel — so data replaces them without a shift. The shimmer is the one infinite animation in
   the stylesheet, allowed because it is a loading indicator.
2. **Entrance motion.** Rails stagger in on load and reveal on scroll (`web/components/reveal.ts`,
   an IntersectionObserver at the preset's "top 90%"); panels, heroes, the strip and the log view
   take a subtle 300ms fade; grid cards ramp at .03s capped at the ninth; posters fade in on decode
   (`Poster` in `movie-card.tsx`). Opacity and transform only, everywhere.
3. **SVG instead of glyphs.** ★ ‹ › → ▶ are gone from the markup; what remains matching a grep is
   comments and prose naming menu paths. Ratings are a ring beside the number
   (`web/components/rating.tsx`), never instead of it, banded accent/warn/error at 7 and 5.
4. **The two big gestures.** The billboard rotates through the trending rail's top five backdrops —
   still zero extra requests — with dots, a pause control, a hold on hover, and a full stop under
   reduced motion. Cards grow a hover preview (`web/components/preview.tsx`): a 350ms rest opens a
   portal with the backdrop, ring and three clamped lines, scaled from the card on the overshoot
   curve. Both are additions a touch or keyboard user loses nothing by missing.

The topbar now sits transparent while the page is at rest and picks up panel, border and blur once
anything scrolls under it — `web/components/topbar.tsx`, one passive listener, moved out of the
layout so the layout stays a server component.

## MEASURED — do not re-derive

**The motion presets**, carried in from the handoff's database pull and encoded as tokens. Use these
numbers, do not invent new ones:

```
standard entrance   380ms  cubic-bezier(.34, 1.56, .64, 1)   stagger .06s, capped   opacity 0, y 16, scale .92
subtle entrance     300ms  cubic-bezier(.25, .46, .45, .94)  stagger .03s, capped   opacity 0, y 8
scroll reveal       trigger at "top 90%" = IntersectionObserver rootMargin '0px 0px -10% 0px'
```

The attached DON'Ts are load-bearing: never more than .1s of stagger per item, never the overshoot
curve on dense informational UI (tables, panels and grids take the subtle curve), and reduced motion
renders the FINAL state immediately — implemented by putting every hiding rule inside
`@media (prefers-reduced-motion: no-preference)`, so under `reduce` there is nothing to undo.

**Contrast: all 54 pairs green in both themes** (`make contrast`). The four new pairs are the rating
ring's warn and error bands as graphics (3:1, WCAG 1.4.11):

```
                     LIGHT   DARK
warn  on bg           5.77   9.43
warn  on panel        6.23   8.68
error on bg           6.61   7.57
error on panel        7.15   6.97
```

The high band is `--accent`, already pinned by the focus-ring pairs. The billboard overrides all
three bands with the dark palette's literals because that surface is dark in both themes; it is an
image and not checkable.

**The rotation was verified by watching it**: five dots on a fresh key's trending rail, `active`
moving 0 → 1 at the 8-second interval, two `.billboard-bg` layers stacked during the crossfade, and
the pause button present. **No horizontal scroll at any of 375 / 768 / 1024 / 1440, both themes**:
`scrollWidth - clientWidth = 0` on Discover at all eight combinations, plus the movie page at 375
and the library at 768.

**Gate.** `make check` green per commit, all five commits, in a worktree without `.env` — the CI
state, where the ten credentialed live tests skip. No live-test flake was hit in these runs.

## TRAPS found the hard way

**A hidden tab kills a re-armed timeout for ever.** The billboard's first draft advanced with a
`setTimeout` that skipped its work when `document.hidden` — and a skipped timeout never re-arms, so
one backgrounded tab ended the rotation until some unrelated re-render. It is an interval stepping
one state object functionally now. If the rotation ever "just stops", look here first.

**A cached poster fires `load` before React attaches the handler.** `onLoad` alone leaves
`data-loaded` false for ever on a warm cache and the poster sits at opacity 0. The `Poster`
component's ref callback checks `el.complete && el.naturalWidth > 0` at attach time; it is not
decoration and removing it breaks exactly the reload case nobody tests.

**Nothing inside `.rail` may take an entrance transform.** The strip is an overflow container and a
transform in there is shaved off top and bottom — T102's hover-lift trap, same physics. The
rail-SECTION animates; the hover preview escapes the container entirely by being a portal on
`document.body`, fixed-positioned from the card's measured rect, and closing on any scroll (caught
with `capture: true`, because the rail's own scroll does not bubble).

**Headless Chrome does not hover on a single `Input.dispatchMouseEvent`.** One `mouseMoved` straight
onto the element produces no mouseover transition and the preview never opens; a second move — any
nearby point first, then the target — does. `scratchpad`-style CDP test scripts need two moves, or
they will report a working feature as missing.

**`[data-reveal]`'s hidden state must live inside the `no-preference` media block.** Written as a
bare rule it hides below-fold content for reduced-motion users until the observer fires, which
violates "final state immediately" in the way that looks fine on every machine the developer owns.

`movie-card.tsx` and `preview.tsx` import each other (the card uses the hook, the preview reuses
`LibraryBadge`). The cycle is benign — both are functions resolved at render time, not at module
init — but it is there, and moving `LibraryBadge` out is the fix if a bundler ever objects.

## What it deliberately did not do

- **View Transitions for the poster → detail morph.** Assessed and skipped, per the handoff's
  verify-first instruction: the App Router flag is experimental on a static export, and the detail
  page draws a skeleton hero until TMDB answers — a morph would land on a placeholder and read as a
  glitch, not a shared element. The CSS-only fake was judged not worth its complexity in the same
  breath. Revisit only when the flag is stable AND the hero can render from data the card already
  has.
- **No Inter, self-hosted or otherwise.** globals.css's no-webfont rule was left standing; the
  system stack stays. Self-hosting a subset into the binary is the only acceptable route if this is
  ever wanted, and it should arrive with its size measured and a decision record.
- **No per-card stagger inside rails** — the overflow trap above.
- **Release-name search keeps its Working banner.** Thirteen seconds of a real browser clearing
  Cloudflare needs an explanation; the film mode's ~150ms TMDB call got the skeleton instead.
- **Still outstanding from T102, untouched here:** per-rail streaming (SSE), the missing D-number
  for the no-Tailwind/no-shadcn ruling, and the Pi, which has seen none of this.
  **Streaming is done in [T104](T104-discover-streams.md)** ([D54](../decisions.md#d54--discover-streams-and-the-browser-reads-it-with-fetch-rather-than-eventsource)),
  and it kept this task's CLS win intact — 0.00025, measured, because a rail's skeleton strip and the
  strip it becomes are the same `.rail` geometry. The other two are still open.
