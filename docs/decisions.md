# Decisions

A record of what was decided and why. Several of these **reversed an earlier plan** — the reasoning
was expensive to establish, so read before overturning.

---

## D1 — Keep 1337x, build our own Cloudflare solver

**Status:** decided, implemented · **Reverses:** the original build plan

The first plan excluded 1337x. It has no API and sits behind Cloudflare, and dropping it was what
removed `flaresolverr` *and* `byparr` from the stack — two containers for one indexer.

That was reversed on request, so instead we built [`minter`](https://github.com/DulsaraNethmin/Minter),
which replaces both with one service we control. Net container math is **13 → 6**, not 13 → 5.

A teardown of [Byparr](https://github.com/ThePhaseless/Byparr) established the constraint that made
this tractable: its anti-detection is **not its own code**. Byparr is ~500 lines of FastAPI glue over
`invisible-playwright`, which ships a Firefox patched at the C++ level. So writing our own equivalent
costs about 500 lines, not a browser engine.

**Do not** re-drop 1337x to "simplify" without recognising that minter already exists and works.

---

## D2 — Fetch pages through the browser; do not reuse cookies

**Status:** decided, measured · **Reverses:** minter's original design

minter was designed as a *credential issuer*: mint a `cf_clearance` cookie once, then replay it
cheaply from an ordinary HTTP client. Measurement killed it. Cloudflare binds the cookie to exit IP,
User-Agent **and** TLS fingerprint, and no off-the-shelf uTLS profile reproduces the patched
Firefox 151:

| | patched Firefox 151 | uTLS `HelloFirefox_Auto` (= FF 120) |
|---|---|---|
| JA3 | `6447ab086255d194909d4013b1a89e87` | `b5001237acdf006056b409cc433726b0` |
| JA4 | `t13d1617h2_86a278354501_…` | `t13d1715h2_5b57614c22b0_…` |
| HTTP/2 | `1:65536;2:0;4:131072;5:16384` | `2:0;4:4194304;5:16384;6:10485760` |

The browser also offers TLS extensions `18` and `27` and curve `4588` (X25519MLKEM768) that
Firefox 120 predates. All three fingerprints differ; a reused cookie gets a 403 indistinguishable
from having no cookie.

`POST /fetch` returns rendered HTML. `/mint` still exists for a client that *can* match the
fingerprint, but nothing we have does.

---

## D3 — Only non-interactive Cloudflare challenges are solvable

**Status:** measured

Cloudflare states the challenge type in `cType` on the interstitial. `non-interactive` clears itself
once a real browser runs the JS and works reliably (~3–8 s). `interactive` mounts a Turnstile widget
and does not — tested against `ext.to`, where the widget never even renders, so there is nothing to
click.

**Before adding an indexer, grep its challenge page for `cType`.** `non-interactive` → fine.
`interactive` → it will not work, and no amount of effort on our side changes that without a paid
CAPTCHA-solving API.

---

## D4 — Pure-Go SQLite

**Status:** decided

`modernc.org/sqlite`, not `mattn/go-sqlite3`. The cgo driver makes `GOARCH=arm64 go build` require a
cross-compilation toolchain; the pure-Go driver makes shipping to the Pi one command. Postgres for a
29-row table would be over-engineering.

---

## D5 — Manual search, not automatic grabbing

**Status:** decided

You search, see ranked releases, and pick one. No watchlist, no background monitoring, no quality
scoring. For a single user this is often better than automation — no wrong-release surprises — and
release ranking plus retry-on-failure is the bulk of what Radarr's complexity actually buys.

---

## D6 — `tmdb_id` is nullable

**Status:** decided · **Deviates from:** the build plan artifact

The artifact specified `tmdb_id INTEGER UNIQUE NOT NULL`. A folder exists on disk whether or not TMDB
can match it, and `NOT NULL` would drop exactly the rows that need human attention. Unmatched movies
are recorded with `tmdb_id = NULL` and surfaced.

A `media_type` column defaulting to `'movie'` is included from the start so TV is additive later.

---

## D7 — Do not adopt the Knaben aggregator

**Status:** decided, recorded for later

`en.yts.lu/?api=torrents` returns 102 hits with magnets in 0.9 s, aggregating many trackers including
1337x and TPB — faster and broader than anything else we found, and it needs no browser.

Not adopted because it is a **clone site** carrying third-party ad and tracker domains. Making it the
primary dependency of the whole system means a middleman that can change shape or vanish without
notice. Worth revisiting if our own indexer coverage proves insufficient.

The general lesson it taught is worth keeping: **before assuming a site needs a browser, check whether
its front-end talks to a JSON API.** We found this one by reading the HTML minter returned — the page
was a JavaScript SPA and shipped its whole client, endpoints included.

---

## D8 — Import by hardlink

**Status:** decided, verified

Downloads and library are on the same ext4 filesystem — both report device `2049`. A move breaks
seeding; a copy doubles disk usage on a drive already filled once. A hardlink is instant, costs no
space, and leaves both names independently valid. Falls back to copy on `EXDEV`.

Source files are never deleted; cleanup stays qBittorrent's business under its own seeding rules.

---

## D9 — Query TMDB with the raw folder title

**Status:** decided, from real data

Colons are illegal in filenames and were replaced with ` - ` (8 of 29 titles). But `Spider-Man` and
`X-Men` contain real hyphens, so a `-` → `:` replacement corrupts them — and
`Spider-Man - No Way Home` contains one of each.

Query TMDB with the raw folder title and disambiguate by year; its search is fuzzy enough. Only on an
empty result, retry with ` - ` collapsed to a space. Record `tmdb_id = NULL` rather than guess — seven
titles are 2026 releases where a confident-but-wrong match is plausible.

**Verified against the live API (2026-08-12):** all 8 hyphenated titles match on the raw query,
including `Tom Clancy's Jack Ryan - Ghost War` → 1380291, whose canonical TMDB title is
`Tom Clancy's Jack Ryan: Ghost War` — a colon exactly where the folder has ` - `. The collapse
fallback never fires on this library; it stays as a safety net for folders that drift.

---

## D10 — Releases are identified by an opaque id, not a URL

**Status:** decided · **Resolves:** the gap left open when cfprobe was absorbed

1337x puts magnets on detail pages, so a search knows a release's *path* but not its magnet, and that
path has to survive from search to pick. `Release` in [`architecture.md`](architecture.md#indexers)
has no field for it — noted during [T6](tasks/T6-absorb-cfprobe.md), where it became an unexported
`detailPath`.

The obvious fix, exporting it so the client hands it back, is the wrong one. It would mean the API
accepts a URL from the caller and passes it to minter to fetch — server-side request forgery through
a service whose entire job is fetching arbitrary pages convincingly.

So the path stays unexported and server-side, and every release carries a deterministic opaque id:
`sha256(indexer + "\x00" + title + "\x00" + detailPath|magnet)`, first 8 bytes, hex. Stable across
searches, and it discloses nothing about the source. `GET /api/releases/{id}/magnet` resolves it out
of the search cache — a detail-page fetch for 1337x, free for YTS and TPB, which already carry
theirs.

A resolve after the cache has expired is a `410 Gone`, not a silent re-search: the caller asked for a
specific release, and quietly returning a different one is worse than saying no. An hour is far
longer than a manual pick takes, so this is rare.

---

## D11 — Rank by seeders; quality is a filter, not a score

**Status:** decided · **Narrows:** the roadmap's "ranked by seeders and quality preference"

Releases sort by seeders descending, ties broken by quality then name. Quality is offered as a
filter (`?quality=1080p`), reusing the `FilterQuality` already ported from cfprobe.

Ranking by quality first puts a 1-seeder 2160p above a 500-seeder 1080p, which is the wrong answer to
the only question a manual picker is actually asking — whether this will finish downloading. Seeders
predict that; resolution does not.

Weighing the two into a single score is where Radarr's custom formats begin, and
[D5](#d5--manual-search-not-automatic-grabbing) declines to rebuild that. With a human choosing from
a list, a filter plus an honest sort beats a scoring heuristic that has to be tuned and second-guessed.

---

## D12 — YTS is reached at `movies-api.accel.li`, not `yts.mx`

**Status:** decided, measured · **Overtakes:** the base URL written into
[T8](tasks/T8-yts-indexer.md) and [phase-2.md](phase-2.md#the-three-indexers)

`yts.mx`, the host both documents name, **is NXDOMAIN** — no A record on 1.1.1.1, on 8.8.8.8, or from
the Pi (2026-08-12). It is not an ISP block; the domain is simply gone, so there was no "keep the
spec" option.

The live API was found by following the historical domains: `yts.lt` and `yts.am` both 301 to
`https://yts.gg/api/v2/…`, which serves the genuine response and says so in its own payload:

```
"status_message": "Query was successful. NOTICE: Base URL moving to https://movies-api.accel.li/api/v2/"
```

So the base is `https://movies-api.accel.li/api/v2` — the migration target the API itself names,
reached through a redirect chain from the official domains rather than trusted on appearance. Its
parsed `data` was diffed against `yts.gg`'s for the same query and is **equal**; it also answers
without a User-Agent, which `yts.gg` needs. `yts.gg` is the fallback if accel.li goes the way of
`yts.mx`, and both are one overridable constant.

**Do not "fix" this by switching to `yts.rs` or `yts.hn`.** They resolve and look plausible, but
return `{"message":"Cannot read property 'moviesPerPage' of undefined"}` — a different, re-implemented
API behind a clone site, which is exactly what [D7](#d7--do-not-adopt-the-knaben-aggregator) declined
to depend on.

Two further measurements are recorded in `yts.go` because they are cheap to lose and expensive to
rediscover: the **API host is not behind Cloudflare** while the `yts.gg` *site* is, so YTS needs no
minter; and the API **clamps `seeds` at 100**, so YTS's contribution to a seeder-ranked list is a
plateau rather than a spread.

---

## D13 — Downloads are scoped by a qBittorrent category with its own save path

**Status:** decided, from the Pi's live config · **Sharpens:** `architecture.md`'s "tag `curator`"

Everything curator adds goes in the category **`curator`**, saving to
`/downloads/complete/curator`. The tag `curator` is applied too, on the same request, because it
costs nothing and makes ownership visible in the Web UI.

A tag alone would not do. A category *also* sets the save path, and `torrents/info?category=curator`
is the filter that makes every later poll — and phase 4's importer — incapable of touching a torrent
somebody added by hand. "The importer never touches torrents added by hand" has to be enforced by
something, and this is the something.

The save path is deliberately **not** radarr's `/downloads/complete/movies` (measured on the Pi
2026-08-12, alongside `sonarr` → `/downloads/complete/tv`). Phase 6 runs both stacks side by side on
purpose, and two importers writing one directory is how a duplicate download quietly overwrites a
good file. A separate directory is still on the same ext4 filesystem, so
[D8](#d8--import-by-hardlink)'s hardlinks are unaffected.

One consequence to carry into phase 4: qBittorrent reports content paths in **its own namespace** —
`/downloads/complete/curator/...`, because its mount is host `/media/storage/media/downloads` →
container `/downloads`. Phase 3 stores what it is told, verbatim, and translates nothing. The
translation belongs where the hardlink is made, not buried in a client that only reads.
