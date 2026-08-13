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

---

## D14 — The importer is driven by the poller's torrent list, not by a completion event

**Status:** decided · **Rejects:** hooking the transition into `completed`, and a
"completed but not imported" store query

The importer runs inside the poller's existing tick, over the torrent list that tick already fetched.
Its trigger is a **state**, not an event: *this torrent reads `completed` and its row does not read
`imported`*.

**Hooking the transition is not crash safe.** The obvious design imports when a row moves from
`downloading` to `completed`, which is where phase 3 already stamps `completed_at`. Restart curator
between that write and a failed import and the transition never happens again — the torrent is
`completed` on both sides of every later tick, no edge is ever observed, and the row is stuck for
ever with no error and nothing to retry. Triggering on the state instead means **the recovery path
and the normal path are the same code**, which is the only version of a recovery path that is
actually exercised.

It also costs nothing. The tick already holds the torrent, `ContentPath` included, and has already
read the row to update its progress. The importer needs exactly those two things, so this is a
function call, not a second source of work.

**No second loop, and no `DownloadsAwaitingImport` query.** A store query for "completed but not
imported" would be a *second, divergent* answer to "what needs importing" — one derived from our
table, one from qBittorrent — and they disagree the moment a torrent is removed from qBittorrent by
hand. The torrent list is the work list, and the row is only ever consulted about torrents that are
in it. The `downloads` table therefore needs **no new column and no index**, which is why this repo
still has never run a migration: `downloads.state` already carries `imported`
([T2](tasks/T2-store.md) wrote the value into the schema comment in phase 1) and `movies` already
has `library_path` and `imported_at`.

The cost is accepted with open eyes: **a permanently failing import retries every poll interval for
ever.** That is the correct behaviour for the failures that actually happen — a torrent still being
moved, a full disk, a library root not yet mounted — all of which fix themselves. It is handled by
suppressing the repeat *log* per hash rather than by backoff, because backoff would add state and a
timer to solve a problem that is only noise.

---

## D15 — The Jellyfin refresh is best-effort, and its key is optional

**Status:** decided

`JELLYFIN_API_KEY` unset **disables the refresh and does not fail startup** — the posture already
established for an unset `TMDB_API_KEY` and an unset `QBIT_USER`. Jellyfin is 10.10.7 at
192.168.1.26:8096, and there is no API key yet, so this is the state curator ships in today.

**A refresh failure must never fail an import.** The file is hardlinked into the library and the row
says `imported`; whether a media server has noticed yet is a different, softer fact. Jellyfin also
rescans on its own schedule, so the worst case of a failed refresh is that the film appears later
rather than not at all. Letting a 500 from Jellyfin roll back a correct import would trade a real
outcome for a cosmetic one.

That guarantee is put in the **type**, not in a comment: the method the poller calls returns no error
at all, so there is nothing for a caller to mishandle. The client underneath it does return errors —
it has to, or the live test could not fail on a bad status — and the swallowing happens at exactly
one seam, where it is deliberate.

The refresh is fired **once per tick, not once per import**, and only when something was actually
imported. `POST /Library/Refresh` is a whole-library scan; asking for one per file in a batch of six
would queue six scans of the same library, and asking for one every ten seconds for ever would be
worse.

---

## D16 — The UI is embedded with `all:`, and a committed placeholder keeps `go build` honest

**Status:** decided · **Concedes:** "one command to ship", deliberately and in one place only

Phase 5 puts a Next.js static export inside the binary. Three things about that are load-bearing and
none of them is obvious.

**`//go:embed all:dist`, not `//go:embed dist`.** `go:embed` excludes files and directories whose
names begin with `.` or `_` unless the pattern carries the `all:` prefix. Next.js puts **every**
script and stylesheet under **`_next/`**. Without `all:`, the binary compiles, starts, serves
`index.html`, and every asset on the page 404s — a blank screen with no error anywhere in the Go. It
is the most expensive one-word omission available in this phase, so it is a test: the embedded
filesystem is walked and asserted to contain `_next/`.

**A placeholder `dist/index.html` is committed.** `//go:embed` is a *compile-time* directive: if the
directory is missing, the package does not build. Since the real export is generated and
gitignored, a fresh clone would otherwise fail `go build ./...` — and so would every commit under
`git bisect`, which this project verifies per commit precisely so that bisect works. The placeholder
is a real page that says the UI has not been built and gives the command, so the failure mode is a
legible page rather than a compile error or a white screen.

**The build output is not committed.** Minified bundles in `git log` make every UI change an
unreviewable diff and every merge a conflict. The cost is that shipping is now two commands rather
than one:

```bash
npm --prefix web run build && GOOS=linux GOARCH=arm64 go build ./...
```

That is a genuine regression against "deploying to the Pi is a single command"
([D4](#d4--pure-go-sqlite) bought that, and it still holds for the Go half). It is accepted because
the alternative is worse in a way that lasts longer, and it is confined to one documented line.
`go build ./...` on its own still succeeds, still passes every test, and produces a binary whose API
is complete — only the UI is the placeholder.

---

## D17 — Settings is read-only, and the `settings` table stays unused

**Status:** decided · **Resolves:** a table created in phase 1 and never touched

The Settings screen reports what is configured and what is reachable. It writes nothing, and the
`settings` table created by [T2](tasks/T2-store.md) remains unused — its first legitimate use is
still ahead of it, and inventing one for a screen that does not need it would be worse than leaving
it empty.

The reason is the threat model, not laziness. Authentication is
[out of scope](roadmap.md#deliberately-out-of-scope) — curator is LAN-only, the same posture as the
*arr stack it replaces. That is defensible for a page that *displays* a library. It stops being
defensible for a page that lets anyone on the network read back `QBIT_PASS`, `TMDB_API_KEY` and
`JELLYFIN_API_KEY`, or change where the importer writes.

So **no secret is ever sent to the browser**, not even masked. `GET /api/settings` answers
`configured: true|false` per integration and never the value, which is the only fact the UI actually
needs — every screen's real question is "can I press this button", not "what is the password".

Configuration stays in the environment, read once at startup
([CLAUDE.md](../CLAUDE.md#conventions)). One source of truth beats a database layer that shadows it,
disagrees with it after a restart, and has to be reconciled.

---

## D18 — The log tail is readable without authentication, so it is redacted at the source

**Status:** decided · **Extends:** [D17](#d17--settings-is-read-only-and-the-settings-table-stays-unused)

`GET /api/logs` serves the last `LOG_BUFFER_LINES` of curator's own log, and the Logs screen shows
it live. Until now a log line went to stderr on a machine you had to SSH into. Behind an endpoint it
goes to any browser on the LAN, and there is no authentication in front of it.

So **secrets are scrubbed on the way into the buffer**, not on the way out. `internal/tmdb` already
strips the API key out of transport errors at the source — `scrubURL` exists precisely because
`*url.Error` stringifies a URL carrying `api_key=` — but that is one careful call site, and this has
to hold for log calls nobody has written yet. The buffer is handed `TMDB_API_KEY`, `QBIT_PASS` and
`JELLYFIN_API_KEY` at startup and replaces any occurrence before storing. Values under six
characters are ignored: redacting an empty or two-character secret would mangle every line while
protecting nothing.

The tail is **in memory only**. It cannot read a file, a journal, or anything else on the host — it
holds what this process wrote, and it dies with the process. A log endpoint that could be pointed at
a path would be a file-read primitive with a friendly name.

Entries carry a monotonic cursor, and the API reports how many lines **fell off the ring** before the
caller asked. A log with a silent gap in it is worse than one that admits the gap, because the reader
draws conclusions from what is not there.

---

## D19 — Deleting a movie removes the file, and asks qBittorrent to remove its own

**Status:** decided · **Reverses:** [D8](#d8--import-by-hardlink)'s "source files are never deleted"

D8 said curator never deletes a source file and that cleanup stays qBittorrent's business.
`internal/qbit` was built with no delete method at all, so that guarantee was structural rather than
a promise. Removing a film from the library now has to actually free the disk, which reverses that.

**The reversal is narrowed to one rule: curator only ever deletes a file it created itself.** It
unlinks its own hardlink in `LIBRARY_MOVIES`, and for the downloaded copy it asks **qBittorrent** to
delete it, because qBittorrent created it and owns it. Curator never reaches into the download
directory and unlinks something there. That also avoids the state a naive delete produces — a
torrent whose files have vanished underneath it, which qBittorrent reports as `missingFiles` and the
poller then records as `failed`, for a download nobody failed to get.

Three guards make this safe while the *arr stack still shares that qBittorrent until phase 6:

1. **Only torrents in our category are deletable.** `DeleteTorrent` takes the category it requires,
   looks the torrent up first, and refuses if it does not match. A hash belonging to `radarr` cannot
   be removed by curator even if something hands it one — the check is in the client, at the lowest
   level, not only in the caller.
2. **Only paths inside `LIBRARY_MOVIES` are removable**, asserted after resolution, and never the
   root itself. The same containment check the importer makes before it writes.
3. **The order puts the recoverable failure last.** Torrent and its data, then our hardlink, then the
   database rows. A failure at any step leaves something a retry can finish, and the row survives
   longest — a row pointing at files that are already gone is repairable, while files with no row are
   silently re-adopted by the next scan.

[D13](#d13--downloads-are-scoped-by-a-qbittorrent-category-with-its-own-save-path) is what makes this
possible at all. The `curator` category has **its own save path**, so nothing in it was ever
hardlinked by radarr, and deleting it cannot pull a file out from under the stack we have not
replaced yet.

**There is no undo and no authentication.** Deleting is as reachable as reading, for anyone on the
LAN, which is the same posture as the rest of curator and as the *arr stack it replaces — but delete
is the first request that destroys something. The UI therefore says exactly what will be removed and
how much disk it frees before it does it.

---

## D20 — The film comes from TMDB; the search box only finds it

**Status:** decided, from a failure in the first real download · **Replaces:** typing a title and a
year into a search box

Curator used to learn what a film was called from whatever was typed. Searching `avengers` with no
year recorded `title='avengers', year=0`, and that one row produced three separate failures: the
import failed on every poll tick for ever, because `DestFolder` correctly refuses a year of 0; the
scan matched the yearless row against TMDB and got **Avengers: Doomsday (2026)**, which is the
confident-wrong-match [D9](#d9--query-tmdb-with-the-raw-folder-title) warns about in those exact
words; and the folder it would have written was `Avengers Endgame (2019)` rather than the film's
actual name.

So **the movie is the primary object.** You search TMDB, pick a film from a grid of posters, and the
releases hang off that film. Title, year and `tmdb_id` then come from TMDB and are authoritative:
`POST /api/downloads` has always accepted a `tmdb_id` and has never been sent one, and
`library.DestFolder` already turns the canonical `Avengers: Endgame` into the correct folder
`Avengers - Endgame (2019)` by D9's colon rule.

The measurement that settled it: **the first result TMDB returns for `avengers` is Avengers:
Doomsday.** The old code took position one. A human looking at posters does not.

### The canonical title is not the title the indexers answer

Measured against the live indexers, and the reason this is a decision rather than a refactor:

| query | 1337x | yts | tpb |
|---|---|---|---|
| `Avengers: Endgame` | **0** | 7 | 100 |
| `Avengers Endgame` | **20** | 7 | 100 |
| `Avengers: Infinity War` | **0** | 6 | 100 |
| `Avengers Infinity War` | **20** | 6 | 100 |
| `Spider-Man: No Way Home` | 20 | 6 | 55 |
| `Dune: Part Two` | 20 | 6 | 73 |

A colon silently loses 1337x on some films and not others, and it does it **without an error**:
`indexers[]` reports `ok: true, count: 0`, which is indistinguishable from a film nobody has
uploaded. Stripping the colon never lost a result in any case measured.

So there are now **two titles**, and conflating them is the bug this prevents:

- the **canonical** title — `Avengers: Endgame` — is what the API echoes, what dispatch stores, and
  what becomes the library folder;
- the **query** title — `Avengers Endgame` — is what the indexers are asked.

`NormaliseQuery` strips the colon and **nothing else**. Not the hyphen: `Spider-Man` and `X-Men`
contain real ones, and D9 exists because of that. Not the ampersand or the apostrophe — unmeasured,
and this function is a record of a measurement rather than a guess.

It is applied **once, in the aggregator, above the cache**. Putting it inside `x1337.searchQuery` —
the narrower option — would leave `indexer.Cache` holding two entries, `avengers: endgame` and
`avengers endgame`, for one identical minter fetch, so the TMDB path and the manual path would each
pay their own browser launch. Normalising above the cache means the key is the string actually
queried. This does **not** change `cacheNormaliseTitle`, which still refuses to fold punctuation on
its own, for the reason its comment gives: it must not merge queries that are genuinely different,
and after `NormaliseQuery` they are literally the same string.

### The key stays optional

An unset `TMDB_API_KEY` remains a supported state, as for `QBIT_USER` and `JELLYFIN_API_KEY`. With
no key, Search falls back to the release-name search that exists today and dispatch still demands a
year. Two paths, one of them already built — the cost of keeping "partially useful without every
integration" true.

---

## D21 — The movie page is `/movie/?id=…`, because the UI is a static export

**Status:** decided, forced by [D16](#d16--the-ui-is-embedded-with-all-and-a-committed-placeholder-keeps-go-build-honest)

`output: 'export'` cannot build a dynamic route like `/movie/[tmdbId]/` without
`generateStaticParams`, and TMDB ids cannot be enumerated at build time. So the id rides in a query
string on a static route.

It costs a slightly uglier URL and buys the single-binary embed, which is the whole point of the
project. `internal/web`'s `resolve()` needs **no change**: `r.URL.Path` is `/movie/`, the query is
not part of it, and the export writes `dist/movie/index.html`.

Teaching `internal/web` to rewrite `/movie/{id}/` was rejected. It would work in the binary and 404
under `next dev`, so the dev server and the shipped artifact would disagree about which URLs exist —
which is the class of surprise `web.go` already refuses a catch-all for.

`useSearchParams()` requires a `<Suspense>` boundary; without one the **build fails**, so this is
enforced by the toolchain rather than by remembering.

---

## D22 — The torrent engine moves inside the binary, and qBittorrent becomes the second backend

**Status:** decided, measured (T32) · **Supersedes:** [D13](#d13--downloads-are-scoped-by-a-qbittorrent-category-with-its-own-save-path)'s
category guard, which becomes structural · **Adds a rationale to:** [D4](#d4--pure-go-sqlite)

curator downloads through `anacrolix/torrent`, in its own process, as the default backend.
`internal/qbit` stays as a selectable second one.

The reason is not elegance, it is [D27](#d27--the-vpn-is-mandatory-and-curator-owns-the-socket): a
VPN curator can *guarantee* has to be a VPN curator controls, and that means the socket the peer
bytes leave through has to belong to this process. Everything else here is a consequence or a bonus.

**What it costs, measured rather than guessed** — the spike ran on darwin/arm64 against a 755 MB
payload and was thrown away, which is what a spike is here:

| | Before | After |
|---|---|---|
| arm64 binary, unstripped | 16.17 MB | **25.22 MB** (+9.05, against a +15 estimate) |
| arm64 binary, `-s -w` | 11.31 MB | **17.62 MB** |
| `go.mod` requires | 14 | **85** (estimate was 60–80) |
| `go list -m all` | 36 | **256** |
| `go.mod` requires | 14 | **87** as built |
| peak RSS, 755 MB download | — | **817.6 MB**, ~1:1 with the payload |
| peak **Go heap**, same download | — | **33.4 MB**, flat throughout |

The dependency jump is the real cost and it is paid once, with open eyes, in a repo that had three
direct dependencies.

The RSS figure looked like the cost that could lose the Pi and **it is not one**. Measured through
curator's own engine on the same payload: RSS climbs in lockstep with the percentage while the Go
heap sits at 33 MB and does not move. Those resident pages are the kernel's copy of a file being
written, which it reclaims under pressure; anonymous memory — the kind an OOM killer counts — is
~35 MB. Halving anacrolix's conn budget and its unverified-byte budget moved RSS by 2 %, which is
the confirmation: the lever was never there, because the memory was never the heap. The engine's
budget is asserted on heap for that reason, and Linux accounts `ru_maxrss` differently enough that
the Pi is worth re-measuring at phase 10.

**What it buys, beyond the VPN.** Path translation between two namespaces stops existing, because
there is one namespace. qBittorrent's ~110-line cookie session, its `200 Ok.`-means-nothing add, and
the confirm-by-hash dance that phase 3 needed all stop applying. And D13's guarantee — *the importer
can never touch a torrent somebody added by hand* — stops depending on a category string: the engine
only ever holds torrents curator added, so ownership is exclusive rather than filtered. The category
parameter survives because the interface is shared with a backend where it is still a real filter.

**Pure Go is what makes it possible, and it is now load-bearing twice.** D4 chose
`modernc.org/sqlite` so `GOARCH=arm64 go build` needs no cross-compiler; the same property is what
lets phase 9 ship `FROM scratch` multi-arch. anacrolix/torrent is pure Go **when cgo is off** —
with `CGO_ENABLED=1` it pulls `go-libutp`, `go-llsqlite/crawshaw` and `crawshaw/c`, which is a C uTP
*and a second SQLite*. Go disables cgo by itself when cross-compiling, so the repo's usual command
never sees this; the Dockerfile has to set it explicitly, and that is written down in
[phase-6.md](phase-6.md) because it is invisible until it is expensive.

**The alternatives, and why they lost.** Bundling `qbittorrent-nox` (~90 MB image) keeps every
verified line of phase 3 and 4 and was the fallback if the spike failed — it costs two processes, a
lifecycle to supervise and first-run config to generate, and it weakens the VPN story to "verify,
not guarantee" because the socket is still somebody else's. Transmission is smaller with a simpler
RPC but pays a full client rewrite *without* being the single-process end state, so the migration
gets paid twice. External-only qBittorrent — what curator does today — cannot promise the tunnel at
all.

**Keeping the second backend is deliberate, and so is the criterion for dropping it.** It is the
migration path for anyone already running the \*arr stack, and the fallback if the engine
disappoints on hardware nobody has tested it on. **Sunset:** if the embedded engine runs the Pi for
one full phase with no fallback needed, `internal/qbit` goes, and its session layer, its
`DOWNLOADS_PATH`/`QBIT_DOWNLOADS_PATH` translation and its state map go with it. Written down here
because a deletion with a trigger survives contact with a bad week, and a deletion done early gets
reverted under pressure.

---

## D27 — The VPN is mandatory, and curator owns the socket

**Status:** decided, measured (T33) · **Forces:** [D22](#d22--the-torrent-engine-moves-inside-the-binary-and-qbittorrent-becomes-the-second-backend)

A userspace WireGuard tunnel lives inside the binary, on a gVisor netstack device, and **only the
torrent engine's dialer is bound to it**. No `NET_ADMIN`, no privileged container, no sidecar, and
the credentials live in the app where the person configuring it can reach them.

**The kill switch is structural.** Built with `DisableTCP`, `DisableUTP` and `NoDHT`, the engine
opened **zero OS sockets** in the spike — it has no way to make one. Every byte has to go through a
dialer the tunnel handed it, so a dead tunnel is a failed dial. That is a stronger promise than any
checkbox, and it is destroyed by exactly one line of code falling back to `net.Dial` for trackers,
which is why the fallback is banned rather than discouraged.

**UDP was the risk and it carried.** uTP and DHT are UDP, and netstack is not a real interface:

| | Measured |
|---|---|
| DHT bootstrap over netstack | 51 nodes, 51 good, in 60 s |
| DHT announce of a real infohash | 518 peers in 10 s |
| a 755 MB torrent, entirely over netstack uTP | completed, 2.88 MB/s |
| throughput, tunnelled ÷ **like-for-like** direct | **0.69** (gate was ≥0.50) |
| point-to-point ceiling through userspace WireGuard | ~22 MB/s / 176 Mbps |

The like-for-like comparison is the honest one: against the *unconstrained* direct run the ratio
looks like 0.41, but that run also had TCP peers and a working HTTP tracker, neither of which the
tunnelled run could use. The 22 MB/s ceiling is above what a home connection delivers, so the
userspace stack is not the bottleneck on a Pi — the ISP is.

**What it does not cover, said plainly.** The web UI stays on the host's stack, deliberately: a bad
tunnel config must not be able to lock you out of the screen that fixes it. So do TMDB, the
indexers, minter and Jellyfin — **a 1337x search still leaves from the host address.** This phase
protects peer traffic, which is the traffic that is observed. Every one of those clients takes an
`*http.Client`, so routing them later is wiring rather than a rewrite; claiming today that they are
covered would be the more expensive mistake.

**Mandatory means the default refuses.** With the embedded backend and no tunnel configured,
dispatch reports itself unconfigured and names `VPN_CONFIG` — the same posture an unset `QBIT_USER`
has had since phase 3. `VPN_REQUIRED=false` is a deliberate, documented escape for a laptop. A
mandatory VPN that defaults to off is a slogan.

**What the external-qBittorrent path can and cannot promise.** That traffic is not curator's to
route, so the guarantee becomes a check — but a real one, not a shrug.
`GET /api/v2/sync/maindata` carries `server_state.last_external_address_v4`, the address libtorrent
last learned about itself from the swarm. Measured against the local qBittorrent 5.1.2 container:
`187.14.240.8`, **identical to curator's own exit IP**, and that container has no VPN. Equal
addresses therefore refuse a dispatch and say why; different ones pass; an empty one passes with a
warning, because a client that has never talked to a swarm has nothing to report and refusing there
would deadlock the first download behind a fact that only exists after it.

**The alternatives.** gluetun is what the Pi runs today and stays the escape hatch for anyone whose
provider is OpenVPN-only — it costs credentials living in a sidecar's environment rather than in the
app, and it puts curator's own UI behind the tunnel, so the port has to be published by gluetun and
the answer becomes a compose file rather than a `docker run`. A SOCKS5 proxy fits the same
per-dialer shape in a handful of lines but is weaker in a way that matters: the peer traffic is
proxied, not encrypted end to end, and DNS needs separate care. Both remain available; neither is
the default.
