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
