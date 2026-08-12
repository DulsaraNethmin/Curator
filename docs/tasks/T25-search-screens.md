# T25 — Search and Releases

**Owns:** `web/app/search/`, `web/app/releases/`
**Depends on:** T24

## Goal

The half of the flow that turns "I want this film" into a running torrent, over
`GET /api/search` and `POST /api/downloads`.

## Do

1. **Search**: title, optional year, optional quality filter. Submitting runs the search and renders
   the releases from the **same response** — it does not navigate to a page that fetches again.
2. **A first search takes up to thirteen seconds**, because a real browser is clearing a Cloudflare
   challenge behind minter; a repeat within the hour takes under one. The UI has to look like work in
   progress for those thirteen seconds. A spinner that could equally mean "hung" is the failure mode,
   so say what is happening and roughly how long it takes.
3. **Partial results are a success, not an error.** The response's `indexers` array reports per-source
   `ok`, `count` and `error`, and a search with minter down returns `200` with 57 releases and one
   failure. Render the releases *and* name the source that failed — an aggregator that hides a downed
   indexer is how somebody concludes a film does not exist.
4. Releases are already ranked by seeders
   ([D11](../decisions.md#d11--rank-by-seeders-quality-is-a-filter-not-a-score)). Do not re-sort by
   quality. Show seeders prominently: it is the only column that predicts whether the download
   finishes, which is the actual question.
5. `magnet` is `null` for 1337x rows until resolved, and that is normal — it costs a detail-page fetch
   and a browser, so it is deliberately lazy. **Do not resolve magnets to render a list.** Dispatch
   resolves server-side.
6. Dispatch posts `{release_id, title, year, tmdb_id?}`. `title` and `year` come from the search the
   user made, because a release id says nothing about which film it is.
7. **Handle `410` specifically.** An id is only valid while its search is cached, and an expired one
   is the single error in this system whose correct fix is a user action: say the search has aged out
   and offer to run it again. A generic "something went wrong" wastes the one place we can be
   genuinely helpful.
8. `503` means downloads are unconfigured — `QBIT_USER` is unset, which is **true right now**. Show
   the reason and which variable to set, and disable the button rather than letting it fail. Read
   that state from `GET /api/settings` (T23), not by trying and failing.

## Do not

- Re-implement ranking, dedup or quality filtering. All three are server-side and verified against
  live indexers; a second implementation in TypeScript is a second answer that will drift.
- Call `GET /api/releases/{id}/magnet` from the browser to show a magnet link. It is a server-side
  resolve step for dispatch, and pulling it forward means a browser launch per row.
- Poll the search endpoint, cache its results locally, or persist them. The server caches for an
  hour and the id's validity is tied to that.
- Add a watchlist, auto-grab or "download best" button
  ([D5](../decisions.md#d5--manual-search-not-automatic-grabbing)). A human choosing from a list is
  the design.

## Verify

In a browser, against a running curator:

- a real title returns ranked releases; the seeder order is descending and matches the API's
- the **first** search shows progress for its full duration and never looks hung
- with minter stopped, results still render and the failure is named
- blank title is refused client-side, and a `400` from the server still renders its message
- picking a release dispatches it and it appears in Activity
- an aged-out search produces the `410` wording, not a generic failure — force it by restarting
  curator between search and pick, which empties the cache
- with `QBIT_USER` unset the dispatch button is disabled and says why
- `?quality=2160p` narrows the list, and the count matches the API's
