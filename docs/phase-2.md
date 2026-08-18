# Phase 2 — Indexers

Find releases. Three sources behind one interface, searched concurrently, merged and ranked, with
the expensive one cached so it is paid for once.

**Done when** `/api/search?title=Interstellar&year=2014` returns ranked releases with working
magnets, a second search inside the hour launches no browser, and stopping minter degrades search to
the other indexers rather than erroring.

---

## Tasks

| Task | Owns | Depends on |
|---|---|---|
| [T8](tasks/T8-yts-indexer.md) YTS | `internal/indexer/yts.go` | — |
| [T9](tasks/T9-tpb-indexer.md) TPB | `internal/indexer/tpb.go` | — |
| [T10](tasks/T10-search-cache.md) cache | `internal/indexer/cache.go` | — |
| [T11](tasks/T11-aggregator.md) aggregator | `internal/indexer/aggregate.go` | T8, T9, T10 |
| [T12](tasks/T12-search-api.md) API | `internal/api/search.go`, `internal/config/`, `cmd/curator/` | T11 |

T8, T9 and T10 are independent and own one file each — run them in parallel. T11 merges them, T12
exposes them.

**They share a package.** Unlike phase 1, ownership here is per *file*, not per directory. Nothing
outside your listed files, and in particular `indexer.go`, `x1337.go`, `minter.go` and `quality.go`
are phase 1's ported code — read them, do not edit them.

---

## The three indexers

`Release`, `Indexer` and `MagnetResolver` already exist in `internal/indexer/indexer.go`, along with
`parseQuality`, `FilterQuality`, `InfoHash` and the minter client, absorbed from cfprobe in
[T6](tasks/T6-absorb-cfprobe.md). 1337x is already implemented and tested. It is not wired.

| Source | Endpoint | Cost | Magnet |
|---|---|---|---|
| YTS | `yts.mx/api/v2/list_movies.json` — **now `movies-api.accel.li/api/v2`, see below** | fast | build from `hash` in the response |
| The Pirate Bay | `apibay.org/q.php` | fast | build from `info_hash` in the response |
| 1337x | HTML via minter | ~9 s | **lazy** — detail page, only for the release picked |

> **Corrected after this phase shipped.** `yts.mx` went NXDOMAIN and YTS is reached at
> `https://movies-api.accel.li/api/v2`
> ([D12](decisions.md#d12--yts-is-reached-at-movies-apiaccelli-not-ytsmx)). The row above is left as
> written because this document is a record; the host in it does not resolve. `yts.rs` and `yts.hn`
> do resolve and look plausible while being clone sites running a re-implemented API.

YTS and TPB are plain JSON and need no browser. That asymmetry is the whole reason for the cache and
for lazy magnet resolution: 1337x costs roughly 9 seconds and a browser per fetch, the other two cost
under a second and an HTTP round trip.

---

## Release identity, and why the API never returns a URL

1337x puts magnets on detail pages, so a search knows a release's *path* but not its magnet. That
path has to survive from search to pick. It must not do so by travelling through the client:
`POST {"url": ...}` to minter with a client-supplied path is a request forgery waiting to happen, and
`Release` in [`architecture.md`](architecture.md#indexers) deliberately has no field for it.

So the path stays server-side and unexported, and every release carries a stable, opaque `id`:

```
id = first 8 bytes of sha256(indexer + "\x00" + title + "\x00" + detailPath|magnet), hex
```

Deterministic, so the same release gets the same id on every search; opaque, so it says nothing
about how the indexer works. `GET /api/releases/{id}/magnet` resolves it — a cache lookup for
1337x, and a no-op for YTS and TPB, which already have theirs. See
[D10](decisions.md#d10--releases-are-identified-by-an-opaque-id-not-a-url).

A resolve after the cache has expired is a `410 Gone` telling the caller to search again, not a
silent re-fetch. An hour is far longer than the seconds a manual pick takes ([D5](decisions.md#d5--manual-search-not-automatic-grabbing)),
so this is rare and worth being loud about.

---

## Ranking, and dedup

Sort by **seeders descending**, ties broken by quality rank then name.

Not by quality first: a 1-seeder 2160p above a 500-seeder 1080p is a worse answer to the only
question a manual picker is really asking, which is whether this will finish downloading. Quality is
offered as a **filter** (`?quality=1080p,2160p`, via the already-ported `FilterQuality`), not as a
score. Radarr's custom-format scoring is exactly the complexity [D5](decisions.md#d5--manual-search-not-automatic-grabbing)
declines to rebuild.

Three sources overlap heavily, so **dedup on info hash**, keeping the copy with the most seeders and
recording which indexers carried it. 1337x releases have no hash until resolved and so cannot be
deduplicated against — a near-duplicate row is the honest outcome, not a bug to paper over.

---

## Caching

A decorator around `Indexer`, keyed on `(indexer name, normalised title, year)`, holding the parsed
releases with their unexported detail paths.

**Wrap 1337x only.** The requirement is that a repeat search launches no browser, and caching YTS and
TPB would buy about a second while making results an hour stale for no reason.

In memory, not the `settings` table. A restart losing the cache is correct: the entries hold detail
paths that only matter for a pick that is seconds away, and persisting them would mean serving a
magnet resolved from a page fetched days ago.

---

## API surface

| Endpoint | Behaviour |
|---|---|
| `GET /api/search?title=&year=&quality=` | Ranked releases from every indexer. `year` and `quality` optional |
| `GET /api/releases/{id}/magnet` | `{"magnet": "...", "info_hash": "..."}`. `410` once the search has expired |

```json
{
  "title": "Interstellar",
  "year": 2014,
  "releases": [
    { "id": "3f2a9c1b7d4e5a60", "title": "Interstellar.2014.1080p.BluRay.x265",
      "year": 2014, "quality": "1080p", "size_bytes": 2469606195, "seeders": 512,
      "indexers": ["yts", "tpb"], "magnet": "magnet:?xt=urn:btih:..." },
    { "id": "8b1d0e6f2c7a4593", "title": "Interstellar.2014.PROPER.IMAX.1080p.UHD.BluRay",
      "year": 2014, "quality": "1080p", "size_bytes": 4187593114, "seeders": 87,
      "indexers": ["1337x"], "magnet": null }
  ],
  "indexers": [
    { "name": "yts",   "ok": true,  "count": 12 },
    { "name": "tpb",   "ok": true,  "count": 30 },
    { "name": "1337x", "ok": false, "error": "calling minter: connection refused" }
  ]
}
```

**The per-indexer block is not decoration.** "A failing indexer is omitted, never fatal" must not
become "a failing indexer is invisible" — that is minter's 200-carrying-a-failure bug wearing a
different hat. A search where 1337x is down still returns `200`, and says so.

A `magnet` of `null` means "not resolved yet", never "no magnet exists".

---

## Configuration

Added to the [phase 1 table](phase-1.md#configuration):

| Variable | Default | Purpose |
|---|---|---|
| `MINTER_URL` | `http://127.0.0.1:8191` | 1337x fetches |
| `SEARCH_TIMEOUT` | `30s` | Whole-search deadline; a straggler is omitted, not waited for |
| `SEARCH_CACHE_TTL` | `1h` | How long 1337x results are reused |

`127.0.0.1`, not `localhost`: **minter binds IPv4 only**, so `localhost` resolves to `::1` first and
the connection fails. Inside Docker the name `minter` resolves correctly and the point is moot —
this default is for running on a laptop, which is where it will be typed most.

---

## Verification

```bash
go build ./... && go test ./...
GOOS=linux GOARCH=arm64 go build ./...          # must pass — it is how this ships

go run ./cmd/curator &

# every indexer reports in, and the failures are visible
curl -s 'localhost:8090/api/search?title=Interstellar&year=2014' | jq '.indexers'

# ranked by seeders, best first
curl -s 'localhost:8090/api/search?title=Interstellar&year=2014' \
  | jq -r '.releases[] | "\(.seeders)\t\(.quality)\t\(.indexers|join(","))\t\(.title)"' | head

# the second search inside the hour launches no browser: seconds the first time, instant the second
time curl -s -o /dev/null 'localhost:8090/api/search?title=Interstellar&year=2014'
time curl -s -o /dev/null 'localhost:8090/api/search?title=Interstellar&year=2014'

# lazy resolution: a 1337x release has no magnet until asked for
id=$(curl -s 'localhost:8090/api/search?title=Interstellar&year=2014' \
     | jq -r '[.releases[] | select(.indexers|index("1337x")) | select(.magnet==null)][0].id')
curl -s "localhost:8090/api/releases/$id/magnet" | jq

# stopping minter degrades, never errors — still 200, still results, 1337x marked failed
docker stop minter
curl -s -o /dev/null -w '%{http_code}\n' 'localhost:8090/api/search?title=Dune&year=2021'   # 200
curl -s 'localhost:8090/api/search?title=Dune&year=2021' | jq '.indexers[] | select(.ok==false)'
```

---

## Out of scope for phase 2

qBittorrent and magnet dispatch (phase 3), the import pipeline (phase 4), the UI (phase 5), TV, and
anything that changes the Pi. No watchlist, no background monitoring, no automatic grabbing —
[D5](decisions.md#d5--manual-search-not-automatic-grabbing) is the design, not a stepping stone.

Do not add a fourth indexer here. If one is ever considered, grep its challenge page for `cType`
first ([D3](decisions.md#d3--only-non-interactive-cloudflare-challenges-are-solvable)), and do not
adopt the Knaben aggregator ([D7](decisions.md#d7--do-not-adopt-the-knaben-aggregator)).
