# T31 — Discover, cards, and the movie screen

**Owns:** `web/app/page.tsx`, `web/app/search/page.tsx`, `web/app/movie/page.tsx`,
`web/components/{releases,movie-card}.tsx`, `web/components/nav.tsx`, `web/lib/api.ts`,
`web/app/globals.css`
**Depends on:** T30

## Goal

Search finds films, not release names. A film has a page. Releases hang off it.

## Do

1. **Extract `<Releases>` first**, as its own commit and with no behaviour change: the release table,
   the per-indexer block, the dispatch button, the 410 "search has aged out" wording and the
   `GET /api/settings` qBittorrent check, lifted verbatim out of `search/page.tsx`. This is what stops
   the detail page and the fallback search growing two implementations of dispatch — and the 410 path
   is the one error whose wording this repo cares about most.
2. `/movie/?id=299534` — a **static** route reading `useSearchParams()` inside a `<Suspense>`
   boundary (D21). Backdrop hero, poster, title, year, runtime, genres, tagline, overview, facts
   panel, library badge — then a **`Find releases`** button. Nothing touches an indexer until it is
   pressed, so browsing Discover costs no requests and launches no browser.
3. Metadata and releases are **two fetches, not `Promise.all`**. Metadata paints in ~150 ms; a cold
   release search takes up to thirteen seconds. Awaiting both would hold a poster hostage to a
   browser launch.
4. Dispatch sends `{release_id, title: details.title, year: details.year, tmdb_id: details.tmdb_id}`
   — canonical, straight into `DestFolder`, which spells the colon `" - "`. **Delete the "Add a year"
   banner from this path**; keep it in the fallback, where its reasoning is still true.
5. `/search/?q=` renders a grid of `<MovieCard>` with a toggle to release-name search. When
   `settings` reports tmdb unconfigured the screen **starts** in release mode and says why — read
   from `/api/settings`, exactly as the qBittorrent check already works. No new mechanism.
6. `/` becomes Discover: the existing counters compacted into one strip, then Trending and Popular
   rails. A failing rail renders inline, naming itself; it does not blank the page.

## Do not

- Re-rank, re-sort, re-filter or dedupe releases in TypeScript. D11 is server-side and verified
  against live indexers; a second implementation is a second answer that will drift.
- Strip the colon client-side. That is D20's server-side job, and a client that did it would send the
  stripped title to dispatch and write the wrong folder name.
- Make `year` required anywhere on the TMDB path. That is the bug being fixed.
- Delete the release-name search. It is the escape hatch for a film TMDB does not have, and the
  fallback the unconfigured state depends on.
- Add a rewrite to `internal/web` so `/movie/{id}/` works. It would 404 under `next dev` (D21).

## Verify

`npm --prefix web run build` — `ignoreBuildErrors: false` makes a type error a build failure — then
in a browser:

- search `avengers` → a grid with **Doomsday among the cards rather than silently chosen**
- a card click reaches `/movie/?id=…`, and that URL is **reloadable in a fresh tab** — the static
  export check
- the detail page paints immediately and makes **no indexer request** until Find releases is pressed
  (confirm on `/logs`)
- pressing it shows the counting loader for the full cold search, then 1337x reporting **20, not 0**
- dispatching a colon title writes the folder `Avengers - Endgame (2019)`, single-spaced, with
  `movies.tmdb_id` set
- a film already in the library shows its badge on the card and the page
- `?id=` missing or non-numeric → an empty state, not a request for `/api/tmdb/movies/NaN`
- with no key → Search opens in release mode and says why, Discover says "Not configured" and the
  counters still work
