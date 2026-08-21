# T97 — EZTV, and an indexer that can decline

**Owns** the fourth indexer and the path that carries an IMDb id to it ·
**decides** [D50](../decisions.md#d50--an-indexer-may-decline-a-query-it-cannot-answer-and-that-is-not-a-failure) ·
**feeds** [D49](../decisions.md#d49--a-season-narrows-after-the-fetch-and-a-pack-that-contains-the-episode-is-kept-below-it),
whose tiers are only as good as the season each release is believed to be ·
**needed by** [T98](T98-season-and-episode-pickers.md), which draws the season list this fetches

## Why now, when phase 11 deliberately skipped it

`architecture.md` carried a row reading *"EZTV — structured season/episode, and still not used"*
from phase 2, and [phase-11.md](../phase-11.md) listed a fourth indexer as out of scope because
TPB's TV categories and 1337x's keyword search were enough to build television on.

D49 is what changed the arithmetic. Its tiers decide whether a release is the episode asked for, a
pack containing it, or a name that claims no season — and TPB and 1337x publish a **name** and
nothing else, so `parseSeasonEpisode` has to recover the season from scene convention and is careful
to the point of refusing anything ambiguous. EZTV answers with the fields. Measured live: **270 of
270** Silo rows state a season.

It also answers with more of them. apibay caps a response at 100 rows; EZTV returned 270.

## What it owns

`internal/indexer/eztv.go` — plain JSON over the shared client, modelled on `yts.go`. No Cloudflare,
so **no minter**. `MediaCapable` for television-only, `QueryCapable` for the id.

`internal/indexer/indexer.go` — `QueryCapable`, `answersQuery`, and `Query.IMDBID`.
`aggregate.go` asks both capabilities in the one pre-fan-out skip; `cache.go` forwards `Answers`.

The id's path: `internal/tmdb` (`append_to_response=external_ids`) → `internal/api/browse.go`
(`imdb_id` on the show body) → `internal/api/search.go` (`?imdb_id=`) → `web/lib/api.ts` →
`web/app/show/page.tsx`. **The `tt` prefix is stripped at EZTV's boundary and nowhere earlier**:
`internal/tmdb` reports what TMDB says, and one source's URL format is not its business.

`INDEXER_EZTV`, defaulting **true** — with YTS and TPB rather than with 1337x, because it needs no
browser, no companion container and no credential.

## Measured 2026-08-21, live — do not re-derive

| | |
|---|---|
| `eztvx.to/api/get-torrents?imdb_id=14688458` | **270 torrents** for Silo, one page 87 KB in ~2.5 s |
| `limit=300` | answers `"limit":30` with **thirty** rows. Above the cap the value is *discarded*, not clamped — asking for more returns fewer |
| `&season=3&episode=8` | **ignored**; the identical page 1 comes back. Narrowing stays in the aggregator where D49 put it |
| ordering | **newest first**. Silo page 1 is seasons 2 and 3 only; season 1 first appears on page 2 |
| no `imdb_id` | HTTP 200, `torrents_count: 1075625` — the newest uploads across every show on the site. See D50 |
| unknown id | HTTP 200, `torrents_count: 0`, and **no `torrents` key at all** — the same trap `ytsData` documents for `movie_count` |
| catalogue sizes | Game of Thrones 146 · Stranger Things 196 · Silo 270 · The Simpsons **2,425** |
| `eztv.re` | **301**. `eztvx.to` is the live front door, and `architecture.md` named the wrong one from phase 2 until now |
| end to end, S03E08 | with an id `yts NA · tpb 100 · eztv 270` → 5 exact · without one `eztv NA` → 2 |
| end to end, season 1 | eztv contributed **93 of 102** releases, every one off page 2 or 3 |

## The page budget, and what it costs

Three pages, 300 rows. One page is ~2.5 s, so this is ~7.5 s of a 30 s `SEARCH_TIMEOUT` now shared
by four sources; twenty-five pages would cover The Simpsons and eat the whole budget.

It is not **one** page, which the 100-row apibay cap would have suggested: rows are newest-first, so
a one-page budget answers a season-1 search with a hundred rows `narrow()` then drops — `ok:true`
contributing nothing. It is not ten either. The truncation is real and lives in `eztvMaxPages`' own
comment, where the next person to raise it will be standing.

A **later** page failing keeps what the earlier ones returned; page one failing is a failed search.
That is the same trade the cap already makes, and discarding a hundred good rows over one hiccup is
the worse answer.

## Traps

- **`size_bytes`, `season` and `episode` are JSON strings** beside numeric `seeds` and `peers`, in
  the same object. `eztvNumber` decodes either, so EZTV changing its mind costs one field rather
  than every television search.
- **A stated season with `episode: "0"` is a season pack**, not a missing value — measured, `Silo
  S02 1080p x265-ELiTE`. The name fallback fires on a season of 0 alone; re-reading a pack's name
  would at best agree and at worst lose the season.
- **Do not give it a private live-test rule.** `classifyLiveFailure` is shared because
  [T76](T76-a-skip-that-covers-the-call.md) existed to remove that divergence and `CLAUDE.md`
  forbids a third. With three live tests the control arrangement is a **cycle** rather than a pair —
  TPB←YTS←EZTV←TPB — and `TestEachIndexerIsTheOthersControl` is now a table that pins it. A fourth
  live indexer joins that table.
- **A fourth live test is a fourth chance of CI reddening on an outage**, not only on a break. YTS
  returned an nginx 500 during T96 and failed loudly, blocking the gate until it recovered.
  Knowingly accepted: [D42](../decisions.md#d42--a-dead-base-url-fails-the-build-but-only-once-a-control-name-proves-the-machine-is-online)
  is what it buys.

## Deliberately not in scope

- **A fifth indexer.** `torrentgalaxy.to` does not resolve; `nyaa.si` is up but anime-specific.
- **DHT as a search.** BEP 5 maps `infohash → peers` and has no keyword index, so "Silo S03E08" is
  not a question it can be asked. curator already runs a DHT node on the tunnel socket
  (`internal/engine/network.go`) doing the only job it can do. A *searchable* index means crawling
  it — a 24/7 harvester plus a database that takes months to be useful, on the same tunnel curator
  downloads through.
- **Quality sections.** See [T98](T98-season-and-episode-pickers.md).
