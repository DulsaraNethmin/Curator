# T102 — twelve rails, and a design system under the CSS

**Owns** `web/app/globals.css`, the Discover screen and the rails behind it ·
**takes** [D20](../decisions.md#d20--the-film-comes-from-tmdb-the-search-box-only-finds-it)'s
TMDB-first browsing and [D48](../decisions.md#d48--television-is-additive-a-show-is-a-row-in-movies-and-the-second-library-root-is-opt-in)'s
opt-in television · **does not overturn** globals.css's own opening rule — no utility framework,
because that is one more build step that has to still install in two years for the binary to be
rebuildable

## What it owns

```
GET /api/tmdb/discover[?media=tv]     2 rails  →  12
```

| slot | films | shows |
|---|---|---|
| `trending` | Trending this week | Trending this week |
| `popular` | Popular | Popular |
| `top_rated` | Top rated | Top rated |
| `in_release` | **In cinemas now** | **On the air this week** |
| `genre_*` | Action · Comedy · Science fiction · Thriller · Horror · Animation · Drama · Documentary | Action & adventure · Comedy · Sci-fi & fantasy · Crime · Mystery · Animation · Drama · Documentary |

The ids are shared and the titles are not. An id names a **slot** — the UI keys on it and never
learns television exists — while the title says what the slot holds. `in_release` is the one that
diverges, because "in cinemas" and "on the air" are two different facts rather than one fact said
twice.

## MEASURED — do not re-derive

**All twelve rails answered live, both media types**, against the real TMDB on 2026-08-22:

```
FILMS                                          SHOWS
trending    20  Toy Story 5                    trending    20  Lanterns
popular     20  Spider-Man: Brand New Day      popular     20  Reacher
top_rated   20  Avatar Aang: The Last Air…     top_rated   20  Teach You a Lesson
in_release  20  Spider-Man: Brand New Day      in_release  20  Reacher
genre_28    20  Spider-Man: Brand New Day      genre_10759 20  Reacher
genre_35    20  Toy Story 5                    genre_35    20  Family Guy
genre_878   20  Spider-Man: Brand New Day      genre_10765 20  House of the Dragon
genre_53    20  Obsession                      genre_80    20  Reacher
genre_27    20  Colony                         genre_9648  20  The Mentalist
genre_16    20  Toy Story 5                    genre_16    20  Family Guy
genre_18    20  The Death of Robin Hood        genre_18    20  The Mentalist
genre_99    20  Jackass Number Two             genre_99    20  Mayday
```

Family Guy under both Comedy and Animation, House of the Dragon under Sci-fi & fantasy: the
television vocabulary is being used, not the film one.

**The full-bleed billboard overhangs by 8px** on a 1440px window with a classic scrollbar —
`scrollWidth 1433, clientWidth 1425`. `100vw` counts the scrollbar and `clientWidth` does not, so
`margin-inline: calc(50% - 50vw)` overshoots by half of it on each side. After `overflow-x: clip` on
**html and body**: `horizontalOverflow: 0`.

**Contrast, all 23 pairs, both themes.** The two that failed a first pass and were changed:

```
muted on bg-sunk (light)      4.34  → 4.74   --muted darkened to #64686f
border on panel (both)        1.69  → 3.59   split into --border / --border-control
```

The floors are 4.5:1 for text and 3:1 for a boundary that identifies a control (WCAG 1.4.11). Several
clear by under 0.3 — `muted on bg-sunk` at 4.74 and `border-control on bg-sunk` at 3.04 — so **do not
nudge one of these hexes without re-running the check**:

```
make contrast        # scripts/check-contrast.py
  all 46 pairs clear WCAG AA in both themes.
```

It **reads `globals.css`** rather than holding its own copy of the palette, for the reason
`check-lists.mjs` imports the modules it checks: a transliterated table passes while the shipped one
drifts. What it cannot derive is which foreground lands on which background — that is a fact about
the markup, so `PAIRS` is written by hand and is the part to extend when a new surface appears. **A
token no pair names is not checked.**

It is not in `make check`, deliberately: the gate needs Go and node, and python3 would be a third
language to install for a file that changes rarely.

**The check was verified by firing it.** Lightening `--muted` one step, to a value that still looks
fine on the page:

```
--muted: #64686f  →  #8d9199
  FAIL  2.93 / 4.5  muted on bg
  FAIL  3.16 / 4.5  muted on panel
  FAIL  2.68 / 4.5  muted on bg-sunk
3 pair(s) below the floor.          EXIT=1
```

**Gate.** `make check` green including the arm64 cross-compile, third run. The first two runs each
failed one *different* live test — `internal/remux TestTheCapRefusesTheNextOneAndFreesItsSlot` (a 10 s
wall-clock wait for a cancelled ffmpeg) and `internal/vpn TestLiveTheTunnelIsTornDownUnderADownload`
(222 s in isolation, four minutes of real NordVPN handshakes). Both packages have an **empty diff on
this branch** and both pass alone. They are load-sensitive under a full `-race` suite, not
regressions.

## TRAPS found the hard way

**`align-items: flex-end` makes a scrim shrink-wrap its text.** The billboard's gradient then starts a
third of the way down the image and draws a visible horizontal seam across the backdrop — it looks
like a broken image rather than a CSS mistake, which is why it survived the first screenshot. The
scrim now stretches and bottom-aligns its own content. If a seam ever reappears, this is it.

**`overflow-x: hidden` breaks the sticky topbar; `clip` does not.** `hidden` makes the element a
scroll container and `position: sticky` stops working. It has to be `clip`, and on **both** html and
body — the scrollbar is on the document element, so clipping only the body leaves the overhang
scrollable.

**A card that scales inside an overflow container is shaved off.** `.rail` carries vertical padding
purely to give the hover lift room. Removing it as dead space cuts the top and bottom off every
hovered poster.

**`/discover` does not reject a foreign genre id.** A film's `28` sent to `/discover/tv` returns a
plausible page of the wrong shows rather than an error, so a single shared genre table would be a bug
that looks exactly like a working screen. `movieGenres` and `showGenres` in `internal/api/browse.go`
are deliberately not factored together, and `TestEachDiscoverRailDrawsItsOwnSource` runs both media
types for this reason.

**Two lockstep slices were fine at two rails and a transposition waiting at twelve.** `handleDiscover`
held `rows` and `fetch` indexed together; nothing said `fetch[i]` belonged to `rows[i]`, so a rail
inserted in one list and appended to the other keeps both the right length, passes every assertion
about counts and failure envelopes, and draws top-rated films under "Trending this week". It is one
`[]discoverRail` now, and the test feeds every rail a card nothing else returns.

**A JSX comment cannot be the first child of `{cond && (…)}`.** `{/* … */}` there is a parse error,
not a comment — the build says "Parsing ecmascript source code failed" and points at the `<div>`
below it.

## The cache, and what it deliberately does not hold

`discoverCache` keeps `[]tmdb.Match` — the TMDB half — for **15 minutes**, and never the finished
response. A card carries `library: {…}`, which is the badge on a poster, and that changes the instant
somebody presses Download; caching the body would leave the poster unbadged for a quarter of an hour
and make the button look broken. `LibraryByTMDBID` is still re-read and re-merged every request, which
it already was.

**Failures are not cached.** Fifteen minutes of a remembered 502 is how a blip becomes a bug report.
The cost is that a hard TMDB outage is re-asked on every reload; the `discover: a rail failed` warn
line is what makes that visible.

Fifteen minutes against lists TMDB itself recomputes daily or weekly is not a staleness trade — it is
a floor on asking a question whose answer has not changed, and it makes Movies → Shows → Movies one
round of requests instead of three. The clock is injectable so the expiry test does not sleep.

## What it deliberately did not do

- **No Tailwind and no shadcn.** The skill this work was requested through is built on both; the
  ruling against them lives in globals.css's first three lines and was left standing. There is no
  D-number for it, which is worth knowing — the argument exists only in that comment.
- **No theme toggle.** Dark mode is still `prefers-color-scheme` alone.
- **No per-rail streaming.** Twelve rails are one response, so a cold cache waits for the slowest;
  the concurrent fan-out is what keeps that near one request rather than twelve.
  **Done in [T104](T104-discover-streams.md), and the diagnosis in this bullet was wrong** — the
  twelve rails answer within ~160ms of each other, so the wait was never the slowest rail. What was
  worth 800ms was telling the page its own shape before any rail answered.
- **No personalised ordering.** The genres are a fixed list. curator has no idea what anybody likes
  and inventing a ranking from nothing would be a lie told in a layout.
- **The film-side `top_rated` and `now_playing` fixtures reuse `popular.json`.** The envelope is
  identical; what is pinned there is the path. The two television fixtures are **written, not
  captured**, and say so — `on_the_air` is a rolling seven-day window, so a recording is stale the
  week after it is taken.
