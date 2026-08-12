# T6 — Absorb cfprobe

**Owns:** `internal/indexer/`
**Depends on:** T1

## Goal

Move the working 1337x code out of the `cfprobe` prototype and into this repo, with tests. It is
**not wired to anything** in phase 1 — that is phase 2. This task exists so proven code is not lost
or rewritten from memory later.

## Source

`~/Projects/BuildwNethmin/cfprobe/main.go` — a standalone prototype that already searches 1337x
through minter, parses the result table, resolves magnets lazily and filters by quality.

## Do

1. Create `internal/indexer/` with the `Release` struct and `Indexer` interface from
   [`../architecture.md`](../architecture.md#indexers).
2. Port these, keeping the behaviour and the comments explaining *why*:
   - `parseQuality` / `qualityRank` → `quality.go`
   - `filterQuality`
   - `parseSearch`, `parseMagnet`, `infoHash` → `x1337.go`
   - the minter `POST /fetch` client → `minter.go`
3. Preserve the ordering guard in `parseQuality`. Explicit resolution tokens (`2160p`, `1080p`, …)
   are checked **before** `4K`/`UHD`, because names like
   `Interstellar.2014.PROPER.IMAX.1080p.UHD.BluRay` contain `UHD` while actually being 1080p.
   Matching `UHD` first files them as 4K and they show up in a 4K filter as a 4 GB "4K" that is not.
4. Preserve **lazy magnet resolution**. 1337x puts magnets on detail pages, so a 20-result search
   would otherwise mean 21 protected requests. Only the release a user picks costs a second fetch.
5. Dependencies come with it: `github.com/PuerkitoBio/goquery`.
6. Delete `~/Projects/BuildwNethmin/cfprobe` once the tests pass here. Its job was proving the path
   works, and it has.

## Do not

- Wire this into `internal/api` or call it from anywhere. Phase 2.
- Port the `cmd/fp` fingerprint tool or the uTLS transport. Cookie reuse was measured and does not
  work ([`../decisions.md`](../decisions.md) D2); that code is a dead end, and the measurements that
  killed it are already recorded.
- Change the parsing while moving it. Port first, improve later, so a regression is attributable.

## Verify

`go test ./internal/indexer` — table-driven, no network:

- `parseQuality` on real release names, including the `1080p.UHD` case asserting **1080p**
- `4K` and `UHD` still resolve to 2160p when no explicit token is present
- `filterQuality` accepts bare numbers (`720`) and `4k`
- `parseSearch` against a saved 1337x results page fixture yields 20 rows with names, seeders, sizes
- `infoHash` extracts a 40-character hash from a real magnet

One live check, run by hand with minter running, that a search still returns results.
