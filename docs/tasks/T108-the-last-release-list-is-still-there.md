# T108 — the last release list is still there when you come back

**Owns** `web/lib/recent.ts`, `web/lib/duration.ts`, `web/scripts/check-lib.mjs` and the `make units`
gate target · **takes** `intervals.search_cache_ttl`, which `GET /api/settings` has published since
phase 7 · **bound by** [D10](../decisions.md#d10--releases-are-identified-by-an-opaque-id-not-a-url),
whose opaque ids are what expires · **does not disturb**
[D20](../decisions.md#d20--the-film-comes-from-tmdb-the-search-box-only-finds-it) or
[D52](../decisions.md#d52--the-quality-filter-narrows-the-rendered-list-and-its-chips-are-ordered-by-resolution) —
the filter still narrows a list that is already in hand, whether it arrived from the network or from
memory.

## What was wrong

Releases are behind a button because a release search is expensive: **7.08 s for a film and up to
13 s for television** (T100's measurement), and the server cache wraps only 1337x, so YTS, TPB and
EZTV are re-hit live every single time. Having paid that, the film screen threw the answer away on
the way out — `web/app/movie/page.tsx`'s effect on `id` did `setReleases(null)` unconditionally, and
there was no client-side cache anywhere in the UI to fall back on. Clicking the wrong nav item cost
the whole search again.

## What it owns

**`web/lib/recent.ts` — one entry, in memory.** One, because the case this exists for is routing
away by accident and coming back, which is one title; a second entry buys the "two tabs" case and
pays for it with a question nobody has an answer to. In memory rather than `sessionStorage` because
every link in this UI is a `next/link`, so App Router keeps the module alive across every in-app
navigation — and a hard reload is precisely when a stale list is least wanted.

**`<Releases>` writes; the pages read.** It is the only component in the tree that has already
fetched `/api/settings` (for the Download button's 503 sentence), so it is the only one holding the
TTL, and making it the writer costs no extra request anywhere. It stamps an **absolute** deadline
once, so `recall` needs no notion of how long anything lasts.

**`web/lib/duration.ts`** — `parseGoDuration`, lifted out of `web/app/activity/page.tsx` now that a
second screen needs it, plus `ago` for the notice. The `Math.max(ms, 1000)` floor **did not come
with it**: that is a polling rule, not a fact about durations, and the new reader is a cache TTL a
one-second floor would be nonsense for. It stayed at Activity's call site as `pollInterval`.

**`make units`** — a sibling of `make lists`, not a section inside it. That target is "the
ranked-list rules against captured answers" and every assertion in it reads `testdata/search/`;
these have no fixtures and take an injected clock. Its name is quoted in four records, so giving it
two modes would make all four half true.

## The design, in two claims

**The TTL is an optimisation. The 410 is the guarantee.** `Aggregator.issue`
(`internal/indexer/aggregate.go:295-297`) stamps every release id `now + SEARCH_CACHE_TTL` in a map
that lives in curator's process — so a **restart expires every id whatever any clock says**, and no
arithmetic on the client can know whether these ids still resolve. What makes a stale list safe is
the refusal path: `POST /api/downloads` answers 410, `<Releases>` catches it, calls `forget()`, and
the existing banner offers *Search again* through `onRetry`. The expiry only keeps the common case
from showing an obviously old list.

`forget()` is called inside `<Releases>` rather than through a callback the pages pass down, because
that is the one implementation of dispatch — a page that forgot to wire the prop would serve the
dead list back on the next visit.

**Two rules keep the cache invisible to `useEffect(…, [result])`.** Only the `id` effect reads the
cache; `findReleases` never does — so *Search again* and every season button always reach the
network and always produce a new object, which is what keeps that reset firing. And `recall` returns
the **same object** each time, so a re-render does not clear the "queued" marks.

## MEASURED — do not re-derive

Headless Chrome 151, against a throwaway curator at `:8097`. **Every hop below is a click on a link
the app itself rendered** — a Library card, or the nav's own Library entry — and a marker planted on
`window` before each one proves the router handled it rather than the browser reloading the page.

That check is not ceremony, and two earlier attempts at this measurement were **thrown away because
of it**: navigating by injecting a plain `<a>` is a full page load, which drops the module the cache
lives in, and `history.back()` after one can be served from Chrome's bfcache — which restores the
whole JS heap and makes a dead cache look alive. A run that does not verify client-side routing is
measuring the bfcache.

```
library cards               /movie/?id=49017  /movie/?id=9799  /movie/?id=24428  /movie/?id=293660

Deadpool search              27 rows · 1 request · 1.01 s
Avengers on arrival           0 rows            (never searched)
Avengers search              89 rows · 1 request · 1.01 s      <- evicts Deadpool

back to Deadpool              0 rows · 0 requests               <- one entry, and Avengers holds it
back to Avengers             89 rows · 0 requests               <- THE CLAIM
notice                       "These are the releases from your last search for this film,
                              moments ago. Press Search again for fresh ones."

hard reload of Avengers      marker gone (page load) · 0 rows   <- in memory, not sessionStorage
```

An earlier run of the same shape, navigating away to `/library/` and back with the nav link, showed
**95 rows restored and 0 requests**, with the notice rendered.

**The 1.01 s is not 7.08 s, and the difference is not this task.** `cfg.IndexerX1337` was off in that
instance, so the search was YTS + TPB with EZTV declining — `{yts ok 5, tpb ok 88, eztv failed 0}`
for Interstellar. 1337x is the ~9 s leg and the only one behind the server-side cache, so a search
that includes it is where the seven seconds live. The cache saves whatever the search costs; this
run simply did not pay the expensive part.

## TRAPS found the hard way

- **A synthetic `<a>` click is a page load, and bfcache will hide that from you.** Any browser
  measurement of this cache has to prove the navigation was client-side, or it is measuring
  Chrome's heap restore rather than `recent.ts`. The marker in the driver is the guard.
- **`recall` must return the same object.** `<Releases>` keys its dispatch reset on `[result]`
  identity, so a fresh object per recall clears every "queued" mark on every render.
  `check-lib.mjs` asserts `recall(k) === recall(k)`.
- **A client TTL longer than the server's id TTL** would hold a list whose every Download button
  answers 410. It is read from `intervals.search_cache_ttl` rather than written down twice.

## Still outstanding

- **The 410 path is not measured.** It is wired (`ApiError.expiredSearch` → `forget()`) and the unit
  rules are pinned, but proving it end to end means restarting curator between the search and the
  Download press, and this run had no torrent backend to dispatch against.
- **Television is wired but unmeasured.** `/show/` recalls only the whole-series list — its arrival
  resets season and episode to 0, and a season list is keyed on its own number, so a season-2 answer
  correctly misses a season-0 question. Nothing here exercised a real show search.
- **A flake was found and not fixed.** `TestTheCapRefusesTheNextOneAndFreesItsSlot`
  (`internal/remux`) failed once during this task's gate at a commit touching only `web/` and the
  `Makefile`, taking **10.94 s against its usual ~1.8 s**. It passed 3/3 in isolation and the full
  gate passed on a re-run at the same commit. It waits on two fake ffmpegs through `waitFor`, so it
  is load-sensitive rather than wrong — the machine was running a background curator and the gate at
  once. Recorded rather than repaired: it belongs to `internal/remux`, not to this task.

## Verification

```bash
make units          # the module rules, against an injected clock
make check          # includes it

# the behaviour, which no unit test can reach — see the trap above about
# navigating with real links and proving the hop was client-side
node --experimental-strip-types web/scripts/check-lib.mjs
```
