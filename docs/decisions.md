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

## D24 — Playback remuxes, and never transcodes

**Status:** decided while specifying phase 8 · **Implemented in:**
[T44](tasks/T44-remux.md) · **Does not touch:** [D4](#d4--pure-go-sqlite)

Direct play first. When the browser refuses the file, curator rewrites the **container** and copies
the streams — `ffmpeg -c copy`, a few percent of one core, lossless. It never re-encodes the video,
not behind a setting and not for one codec.

**The container is what actually breaks, so this buys nearly everything a transcode would.** YTS
ships `.mp4` that plays in every browser; the `.mkv` releases are where Play fails, and an
H.264 + AAC stream inside an MKV is playable the moment it is inside a fragmented MP4 instead. The
remaining cases — HEVC a browser will not decode, DTS or TrueHD audio, VC-1 — are a minority of a
library, and their answer is the VLC link rather than a bigger ffmpeg.

**What a transcode would cost, which is the arithmetic behind this.** A Pi 5 does not re-encode 1080p
in software in real time, so the honest version of "transcode when needed" on the target hardware is
"stutter when needed". It also throws away quality the user chose deliberately half an hour earlier
at a specific release size, and it turns a playback feature into a queue, a job, a progress bar and a
cache with an eviction policy. Jellyfin does all of that well and is one click away; curator's claim
is the film it just downloaded, on the page you are already on.

**ffmpeg is an external binary, not a linked library**, so `CGO_ENABLED=0` and D4 are untouched — the
same property that makes the arm64 cross-compile one command makes a `FROM scratch` image possible,
and a cgo libav binding would end both. It is **optional**: absent ffmpeg means direct play only,
reported in the UI and never a start-up failure, which is
[D15](#d15--the-jellyfin-refresh-is-best-effort-and-its-key-is-optional)'s posture applied to a
second optional dependency.

**The alternatives, and why they lost.** *Transcoding on demand* is above. *Shipping the 78 MB
general-purpose static ffmpeg* is four times the binary curator ships today, for encoders this
decision says will never run. *Pre-remuxing every import to MP4* doubles the disk for a file most
players already handle and makes the library a derived artifact rather than the films themselves.
*Running Jellyfin for playback and linking to it* is what the Open in Jellyfin link already does —
this is the case where you do not want to leave the page.

**The stated size budget is 20 MB, and past 40 MB the answer is to ship direct play alone.** That
fallback is real, not a formality: T43 and T45 stand without T44, and the `error` event's fallback
chain simply has one fewer step before the VLC card.

---

## D25 — Authentication is optional, and off by default

**Status:** decided, implemented in [T41](tasks/T41-auth.md) · **Rewrites:**
[roadmap.md](roadmap.md#deliberately-out-of-scope)'s "Authentication — LAN-only, same posture as the
stack it replaces" · **Cited by:** [D17](#d17--settings-is-read-only-and-the-settings-table-stays-unused),
[D18](#d18--the-log-tail-is-readable-without-authentication-so-it-is-redacted-at-the-source),
[D19](#d19--deleting-a-movie-removes-the-file-and-asks-qbittorrent-to-remove-its-own)

One password, no usernames, no roles, in front of `/api/*`. It is **off unless somebody turns it
on**, and nothing changes for anyone who does not.

"LAN-only, so no authentication" was written when curator displayed a library. Three things have
since moved to the other side of that endpoint: `DELETE /api/movies/{id}` removes files from disk
([D19](#d19--deleting-a-movie-removes-the-file-and-asks-qbittorrent-to-remove-its-own)),
`GET /api/logs` serves the process log to any browser on the network
([D18](#d18--the-log-tail-is-readable-without-authentication-so-it-is-redacted-at-the-source)), and
from phase 7 `PUT /api/settings` accepts a WireGuard private key. Each of those was decided
defensibly on its own; the bullet they all cite is what stopped being true.

**Off by default is not timidity, it is the only correct default.** Every install that exists is
LAN-only and works, every `curl` in `docs/` is unauthenticated, and a default that flips on upgrade
locks people out of their own library to protect them from their own household. The switch is one
field on a screen; the decision to need it belongs to whoever runs it.

**What it protects against, said exactly.** A browser on the network — a housemate, a guest, a
device that should not be deleting films. **There is no TLS**, so the password crosses the LAN in
clear on every Basic-auth request, and this is not protection against something reading the network.
The UI says that where the password is set, not only here, because the person typing it is the one
forming the belief.

**The lockout escape hatch is not a feature, it is the precedence rule.**
[D28](#d28--settings-are-writable-secrets-are-encrypted-at-rest-and-write-only-across-the-api) makes
the environment beat the store for everything, so `AUTH_ENABLED=false` beats a stored `true` and
`AUTH_PASSWORD` beats a stored hash. There is no rescue mode to build and no database to edit by
hand: the recovery is one `-e`, and it is written next to the switch.

**The alternatives.** Basic auth alone is three lines and no cookie, but the browser dialog cannot be
logged out of and cannot be changed without clearing a cache — so Basic stays *available*, for
`curl`, and the browser gets a signed cookie. Server-side sessions need a map to evict and lose
everybody on restart, which [D29](#d29--a-written-setting-applies-at-the-next-start-the-password-applies-at-once)
makes a routine event; an HMAC over an expiry, keyed partly by the password hash, needs no state and
ends every session when the password changes. A reverse proxy with authentication in front is the
right answer for anyone who already runs one and remains entirely available — it is not an answer
for a product whose promise is one `docker run`.

**What this is not.** Not users, not roles, not registration, not OAuth, not TLS, not a second
credential for scripts. One household, one library, one password.

## D26 — Television keeps its stack; the cutover removes only what curator replaces for movies

**Status:** decided, measured · **Reserved for phase 10** since
[phase-9.md](phase-9.md):103 and written at the front of it · **Settles** what
[T53](tasks/T53-run-alongside.md) and [T54](tasks/T54-remove-what-is-replaced.md) do · **Overtakes**
`roadmap.md`'s "remove seven containers"

The cutover removes **radarr, seerr and recyclarr**, and nothing else. `gluetun`, `qbittorrent`,
`prowlarr` and `flaresolverr` stay up, because **sonarr needs all four and curator replaces none of
sonarr.**

### The dependency, measured 2026-08-18

Read-only from both APIs on the running Pi:

- radarr and sonarr have **the same download client** — one `QBittorrent`, enabled, in each.
- Prowlarr's Applications list is exactly **`Radarr` and `Sonarr`**, so removing it strips sonarr's
  indexers too; sonarr's are 1337x, EZTV and The Pirate Bay, which also need flaresolverr.
- **recyclarr is the exception that proves the rule.** It looks shared and is not:
  `configs/recyclarr/configs/` holds `radarr.yml` and no sonarr file, and its one instance is
  `base_url: http://radarr:7878`. It goes with radarr.

So the containers curator makes redundant are **not** the containers that can be removed, and every
count in this project before phase 10 was taken against what curator replaces rather than against
what still depends on it. Thirteen become **ten**, not six.

### Why television is not retired instead

Retiring sonarr is the only branch where the count collapses, and it was rejected on use rather than
on principle: sonarr holds **3 monitored series, 9 episode files of 18 monitored episodes, 40.0 GB**,
and it imported an episode on **2026-08-17**, the day before this was decided. That is a live
service, not an abandoned one, and the cutover is not the place to end it.

### What it costs, stated up front so nobody discovers it

**Two tunnels and two torrent clients on one Pi.** curator brings up its own WireGuard tunnel and
runs its own engine ([D22](#d22--the-torrent-engine-moves-inside-the-binary-and-qbittorrent-becomes-the-second-backend),
[D27](#d27--the-vpn-is-mandatory-and-curator-owns-the-socket)), while gluetun keeps its own for
qbittorrent. That is two NordVPN device slots and two exit addresses, and a download problem now has
two places to look.

It is affordable, and that is measured too: the Pi 5 has **7.9 GB total with 6.2 GB available**, 4
cores, and swap untouched at 1.7 GB used. curator's own ceiling is the 400 MB heap budget phase 6
fixed (`heapBudget`, `internal/engine/live_test.go`). Memory is not what makes this a trade-off;
having two of everything is.

### The consequence for the documents

`roadmap.md`'s "Cutover — run alongside, confirm parity, remove seven containers" and every
descendant of "thirteen containers become six" are now wrong in a way that has a number attached.
[T51](tasks/T51-documents.md) already owns those strings and this is what they become: **13 → 10, and
television keeps its stack.**


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
`187.14.240.8`, **identical to curator's own exit address**. Equal addresses refuse a dispatch and
say why; different ones pass; an empty one passes with a warning, because a client that has never
talked to a swarm has nothing to report and refusing there would deadlock the first download behind
a fact that only exists after it.

**Be exact about what equality proves, because the obvious reading is wrong.** It does not prove the
client has no VPN. It proves the client's traffic leaves **by the same route curator's does**, and
therefore that curator adds nothing and can vouch for nothing: whatever protects this machine
protects that client by accident rather than by design, and whatever does not, does not. The case
that makes the distinction concrete is the one on the desk — this laptop is itself connected to
NordVPN, so the container the check refused *is* behind a VPN, and the refusal is still correct,
because it is not behind one curator chose, configured or can see fail.

That is also why **`VPN_REQUIRED=false` silences it into a warning**. Somebody whose whole machine
is behind a tunnel has made exactly the arrangement this check cannot distinguish from having none,
and the escape hatch that already exists for the embedded engine has to mean the same thing here:
you have told curator you accept an arrangement it cannot verify. It still says so, once, every time
it dispatches.

**The alternatives.** gluetun is what the Pi runs today and stays the escape hatch for anyone whose
provider is OpenVPN-only — it costs credentials living in a sidecar's environment rather than in the
app, and it puts curator's own UI behind the tunnel, so the port has to be published by gluetun and
the answer becomes a compose file rather than a `docker run`. A SOCKS5 proxy fits the same
per-dialer shape in a handful of lines but is weaker in a way that matters: the peer traffic is
proxied, not encrypted end to end, and DNS needs separate care. Both remain available; neither is
the default.

---

## D28 — Settings are writable, secrets are encrypted at rest, and write-only across the API

**Status:** decided · **Amends:** [D17](#d17--settings-is-read-only-and-the-settings-table-stays-unused) —
its threat model survives untouched, its "configuration comes from the environment only" does not

The `settings` table [T2](tasks/T2-store.md) created and nothing ever read becomes curator's second
source of configuration. `GET /api/settings` still never returns a secret; `PUT /api/settings`
accepts one.

**What forced it.** [D27](#d27--the-vpn-is-mandatory-and-curator-owns-the-socket) put a WireGuard
private key inside the binary — not a key curator checks, a key it *uses* on every boot. D17's
argument was that configuration lives in the environment, so there is nothing to write and a screen
that cannot write cannot leak. That holds for a laptop with a `.env`. It does not survive phase 9,
whose entire promise is that a stranger runs one `docker run` and configures the rest in a browser,
and the first thing they must configure is the tunnel.

**D17's real point is untouched: no secret travels outward.** Not masked, not truncated, not its
length, not a prefix — a masked secret still confirms an existence and a length to anyone on the
LAN. Reads answer `configured: true` exactly as they always have. Only the *write* direction opens,
which is the direction that was never the risk.

**The environment wins, the store fills the rest, defaults fill what is left.** Every existing
deployment keeps working and `docker run -e` stays a promise rather than a suggestion. The property
that decided the order, though, is recovery: a bad stored value is always beatable by one `-e`, so
there is no rescue mode, no offline editor, and no support answer that begins "open the database".
[D25](#d25--authentication-is-optional-and-off-by-default)'s lockout escape is this rule and nothing
else. Its one sharp edge — a stored value the environment silently shadows — is paid for by every
setting reporting its **source**, and by the screen refusing to let you type into a field it would
ignore.

**Every stored setting has exactly one environment variable, and the key is its lower-case form.**
`TMDB_API_KEY` ↔ `tmdb_api_key`, asserted in a test rather than remembered. Two ways to configure one
service is already one more than ideal; two *vocabularies* would be the version of this that rots.

**Encrypted, and honest about how much that buys.** AES-256-GCM, a fresh nonce per value, the
setting's key as additional authenticated data so a ciphertext cannot be moved between rows, keyed
by a `0600` file beside the database or `SECRET_KEY` inline. That file is in the same volume as the
database, so **anything that copies the volume copies both**. What it defends against is narrower
and entirely real: a `curator.db` pasted into an issue, a backup that globs `*.db`, a future handler
that returns a settings row by accident, and anyone who can read the file without owning the
process. It is not protection against someone who has the machine — they can read the key, and the
plaintext is in the process's memory regardless.

**Encrypt what must be used; hash what is only ever compared.** curator has to decrypt a VPN key
because it has to bring up a tunnel with it. It never has to read a password back, so it must not be
able to: `auth_password` is bcrypt, one-way, and the asymmetry is the design rather than an
implementation detail.

**A restored database without its key is the failure worth designing for.** GCM's tag makes a wrong
key a detectable failure rather than plausible garbage, so those fields report themselves
*unreadable* and ask to be re-entered, the process starts, and the integrations behave as
unconfigured. A key is generated **only** when there is no ciphertext to fail against — silently
re-keying over unreadable secrets turns a recoverable mistake into an invisible one.

**What is deliberately not settable:** `DB_PATH`, `PORT`, `LOG_LEVEL`, `LOG_BUFFER_LINES` and the
secret key itself. The rule is one sentence — **anything needed in order to reach the settings screen
is not settable from the settings screen** — and it is also why the key that decrypts the store
cannot live in the store.

**The alternatives.** Staying environment-only is what exists and it cannot survive `docker run`
with no `-e`. Plaintext in the database is what most self-hosted software does and would be one
fewer moving part; it loses the four cases above, and "the database is the thing people copy" is
exactly the failure mode. A key derived from the login password breaks when authentication is off,
which is the default, and would re-encrypt everything on a password change. An external secret
manager is out of proportion to one household — and `SECRET_KEY` inline is the seam that makes one a
one-line integration if that ever changes.

---

## D29 — A written setting applies at the next start; the password applies at once

**Status:** decided · **Follows from:**
[D28](#d28--settings-are-writable-secrets-are-encrypted-at-rest-and-write-only-across-the-api)

Saving writes to the database. It does not rebuild the running process. The screen says so, per
field, and shows what is pending next to what is live.

**Live reconfiguration is not a bigger feature, it is a different phase.** The two things a settings
screen most obviously wants to change are the tunnel and the backend, and those are precisely the
two phase 6 learned are order-sensitive to take down: `Engine.Close` must be cancel → `wg.Wait` →
`client.Close` or it deadlocks, and the engine must close strictly before the tunnel or the uTP read
loop reads from a device that is gone. Swapping a backend at runtime means rebuilding the
dispatcher, the poller and the importer's client underneath live requests, and every one of those
failure modes would arrive through a form. Phase 7 would stop being a settings phase and become a
lifecycle rewrite wearing one.

**Restart-to-apply is only acceptable because it is visible.** A saved value that differs from the
running one is reported as `pending` by the API and shown by the screen beside the live value, with
a banner naming what is waiting. An honest "restart curator" beats a half-applied configuration
nobody can diff — and it is one command in the deployment this is being built for.

**The password is the exception, and it is the reason there is an exception at all.** A password
that takes effect at the next restart leaves you unprotected until then, which inverts the point of
setting one. It is read per request through an atomic holder that the write path updates, so
enabling authentication applies to the next request and changing it ends every existing session
([D25](#d25--authentication-is-optional-and-off-by-default) signs the cookie with the password hash,
so that last part costs nothing).

**The alternative, and where it belongs.** A `POST /api/restart` that exits cleanly is trivial and
correct *if something restarts the process* — true of `docker run --restart`, false of
`go run ./cmd/curator`, which is how this is developed. It is worth reconsidering in phase 9, where
the container has a supervisor by construction and the answer stops depending on how curator was
launched.

---

## D30 — A tunnel announces only in the families it has, and never hands the library an error it panics on

**Status:** decided, measured (T56) · **Follows from:**
[D27](#d27--the-vpn-is-mandatory-and-curator-owns-the-socket)

A `udp://` tracker in a resumed magnet took the whole process down at boot, and the two halves of
why are two different decisions.

**A v4-only tunnel refuses a udp6 announce; it does not fake a listener for one, and it is not a
configuration error.** anacrolix turns one `udp://` tracker into two announcers, `udp4://` and
`udp6://` (`torrent.go:2180`), and starts the v6 one unless `DisableIPv6` says otherwise. A NordLynx
`.conf` has a single IPv4 `Address` line, so there is no v6 address to bind — and that is not a
broken config, it is *every* config this product will meet. So curator probes the network for the
families it can actually listen on and tells the client the truth. The same fact is what anacrolix's
peer filters want (`client.go:475-478`): a tunnel with no v6 address cannot reach a v6 peer either,
so offering it one is only a connection attempt that has to fail.

**The alternatives, and why they lost.** *Refusing at config time* turns a crash into a product that
will not start for the config everybody has. *Handing back a listener that fails politely* — as the
only answer — opens a socket that cannot work for every udp tracker in every magnet, forever, and
buries an answerable question under a retry loop. It is kept, but as the backstop below rather than
the fix. What is not on the table is a fallback to `net.ListenPacket`: that is the kill switch D27
measured as zero OS sockets, and "just for trackers" is exactly how it would be lost.

**The wider rule, which is the part that outlives this bug: no curator error may reach a library's
`panicif` on a path that cannot return it.** `TrackerListenPacket`'s error is passed straight to
`panicif.Err` on a function that returns nothing (`client-tracker-announcer.go:861`), so an error
there is not one failed announce, it is the process — and at boot, where resume re-adds every
unfinished row, it is a crash loop. That hook therefore **never returns an error**; a network that
will not open the socket yields one that fails on every operation instead.

An audit of the other five things curator hands the client — `HTTPDialContext`,
`TrackerDialContext`, the added dialer, the added listener, the DHT server — found all five land in
ordinary error paths. One of six. The number is in the code, with its line reference, because a
wrapper that looks like ceremony is a wrapper somebody deletes.

**The lie stops at the engine.** `internal/vpn` keeps returning a real error from `ListenPacket`,
because its own callers want one — including `New`, which must refuse to start a tunnel that cannot
listen at all. It is the engine that knows the library is hostile, so it is the engine that absorbs
it.

**What this cost to find, and the lesson in it.** Phase 6 verified the entire tunnel against
`live_test.go`'s Debian magnet, which carries exactly one **`http://`** tracker — the one tracker
scheme that never asks for a packet socket. A fixture that avoided the normal case made the normal
case the untested one for two phases.

---

## D31 — The stream is behind the same password, and a ticket is how a player carries it

**Status:** decided while specifying phase 8 · **Implemented in:**
[T43](tasks/T43-stream.md) · **Follows from:**
[D25](#d25--authentication-is-optional-and-off-by-default)

`GET /api/movies/{id}/stream` is protected exactly like every other route under `/api/`, with no
exemption. A player that cannot carry a cookie carries a **ticket** instead: a signed, expiring,
single-path bearer credential minted only when somebody asks for one.

**Exempting the route was the cheap option and it is indefensible.** With authentication on,
`GET /api/movies` is protected — so an install that hides the *titles* of its films while serving the
*films* to anyone on the network is not a posture, it is an oversight. The browser needs nothing new
for this: a `<video src>` is a same-origin subresource and sends the session cookie by itself.

**The ticket is the cookie's own machinery pointed at a URL**, which is why it adds a signature and
no state: `<expiry>.<HMAC>` over `"ticket\n" + path + "\n" + expiry`, keyed by the session key mixed
with the current credential. Three properties fall out rather than being built — it is valid for one
path, so a ticket for one film is not a ticket for the library; it expires in 12 hours, comfortably
longer than a film and far shorter than a cookie's 30 days; and **changing the password invalidates
every outstanding one**, with nothing to evict, exactly as it already ends every session.

**Its cost, stated rather than implied: a ticket is a bearer credential in a URL.** It lands in shell
history, in VLC's recent-files list, and in any proxy's access log. So it is minted on request and
never used by the page's own player, and the playback response keeps the two apart — a relative
`stream_url` for the browser, an absolute `external_url` for the clipboard.

**The alternative that lost: putting the password in the URL.**
`http://x:hunter2@curator:8090/api/movies/7/stream` works in VLC today with no code at all, and it
will keep working, because it is a property of Basic auth and not something curator can or should
block. It is not what a *button* should hand out: that URL is the whole install rather than one film,
it never expires, it cannot be revoked short of changing the password, and it goes into the same
recent-files list. A per-film credential that dies on its own is strictly better for the same click.

**Session tokens in a query string were rejected for the browser path** for the ordinary reason —
they leak through `Referer`, through history, and through anything that logs a URL — which is
precisely why the ticket is confined to the one case that has no other option.

**With authentication off, which is the default, none of this exists.**
`POST /api/movies/{id}/playback` answers with plain URLs and mints nothing, so the UI has one code
path and every LAN install gets a Play button and never meets a ticket.

---

## D32 — The Jellyfin link is keyed on the TMDB id, not on the path

**Status:** decided by measurement while building [T45](tasks/T45-player.md) · **Amends:**
[D15](#d15--the-jellyfin-refresh-is-best-effort-and-its-key-is-optional)

"Open in Jellyfin" needs Jellyfin's item id for a film curator has on disk. The obvious key is the
path — curator knows where the file is, and Jellyfin reports a `Path` for every item. **That key
does not work, and the reasons are structural rather than a version's bug.**

**Measured against the real 10.10.7 at 192.168.1.26:8096, read-only, before any client was written.**
`GET /Items?Recursive=true&IncludeItemTypes=Movie&Fields=Path` needs only `X-Emby-Token` and answers
the whole library. Adding `Path=<the exact path Jellyfin itself reported>` answers the whole library
too, and so does `Path=/nowhere/at/all.mkv`: **the parameter is dropped, not applied.** The same is
true of `AnyProviderIdEquals=tmdb.210577` in either casing, with and without a `userId`. This is not
an endpoint that filters badly — `years=2014` narrows 18 films to 1 and `searchTerm=Pulp` narrows
them to 1 — it is two parameters that do not exist.

**Even filtering client-side, the path is the wrong key.** Jellyfin's movies library location is
`/movies`, its own bind mount; curator stores `/media/storage/media/movies/Title (Year)`. And
Jellyfin's `Path` is the *file* while `library_path` is the *folder*, on purpose and for a reason
that is not negotiable — the folder is the scanner's identity key. So the two strings disagree on
the prefix and on what they point at, and they will disagree on every deployment where the two
services see the disk through different mounts, which is the normal one rather than the exotic one.

**So the key is the TMDB id, which both sides already have.** Every Jellyfin item carries
`ProviderIds.Tmdb`, and the movie page is addressed by the TMDB id in the first place
([D21](#d21--the-movie-page-is-movieid-because-the-ui-is-a-static-export)). It is exact, it survives
a file being renamed or re-imported at a different quality, it is indifferent to how either side
mounts the disk, and it steps around the ` - ` → `:` trap that a title match walks straight into —
Jellyfin says `Spider-Man: No Way Home` and the folder on disk says `Spider-Man - No Way Home`.

**`years=` is what keeps it one small request.** The whole library with `Fields=Path,ProviderIds` is
21,386 bytes in 74 ms for 18 films, and that grows with the library; narrowed to the film's year it
is 542 bytes in 5.5 ms and stays there. The narrowing is safe because both sides take the year from
TMDB: all 18 of Jellyfin's `ProductionYear` values equal TMDB's `release_date` year, checked film by
film against the live API rather than assumed.

**Two honest edges.** A TMDB id is not unique in Jellyfin — `Iron Man` is in this library twice under
`1726`, once under `/movies/` and once under `/media/downloads/complete/` — so the first match wins,
because either lands on that film and a tie-break would be inventing a preference nobody expressed.
And a film Jellyfin has not matched to TMDB has no `ProviderIds.Tmdb` at all, which is a miss; the
fallback for every miss is the same, a link to a Jellyfin **search** for the title, which always
lands somewhere useful and never 404s.

**The narrowness rule survives, amended by exactly one method.** `internal/jellyfin` gains one
read-only lookup and still cannot write, control playback, or read a user or a session — the
household is watching that Jellyfin throughout this phase, and a method that does not exist cannot
be called by mistake.

---

## D33 — A folder with no film in it is not a movie: the row goes, the folder stays

**Status:** decided, implemented in [T57](tasks/T57-library-way-in.md) · **Reverses:**
[T17](tasks/T17-library-link.md)'s "do not touch `scan.go`" rule and
[`docs/phase-4.md`](phase-4.md)'s "`internal/library/scan.go` is not modified"

`internal/library/scan.go` recorded **every** directory under `LIBRARY_MOVIES` as an imported movie,
because `largestVideo` answered 0 for an empty folder and the scanner wrote the row anyway. 15 of the
29 folders on the Pi are empty, so over half the library was a row for a film that is not there — and
`status: imported` is exactly what the movie page reads to decide whether to draw Play. The Play
button appeared, and the stream 404'd.

Two halves, and neither works alone. **The scanner stops creating those rows**, and **every scan
removes the ones already recorded**. Pruning alone is not stable: the next `POST /api/scan` puts
every one straight back.

### What was reversed, and why it was right before

T17 said, in a *Do not* list: *"Touch `scan.go`. Not `largestVideo`, not `videoExtensions`, not
`Scan`… the flat, non-recursive, no-floor `largestVideo` is what the scanner needs and it stays as it
is."* `docs/phase-4.md` repeated it. **That was correct while a disagreement between the two pickers
produced a wrong `size_bytes`.** It stops being correct now that the same disagreement decides
whether a row **exists**: a scanner that missed a film the stream endpoint can serve would delete its
row. Two answers to "which file is the film" went from a smell to data loss, so there is now exactly
one — `library.FindFeature`, shared by the importer, the stream endpoint and the scanner, called with
`FeatureOpts{}` in all three.

The consequence worth stating positively: `size_bytes > 0` now means **playable, by construction**.
No "playable" column, no migration, and no disk read on the list endpoint.

### What removes a row, and what emphatically does not

Classification is a pure join over what the scan just walked — no second look at the disk, so there
is no window in which the two disagree.

| the row's `library_path` | the scan said | verdict |
|---|---|---|
| a download is in flight for it | *(checked first)* | **keep, always** |
| outside `LIBRARY_MOVIES` | *(never visited)* | **remove** — `AssertInside` means it can never be served |
| a folder that holds a film | recorded | keep |
| a folder read successfully with no film in it | `NoMedia` | **remove** |
| absent, unreadable, or a name that no longer parses | anything else | keep, and say so in the log |

**Only two branches delete, and both are positive findings.** Everything else keeps. That asymmetry
is the whole safety argument: an unmounted library reads as an empty directory, every row falls
through to "could not account for it", and **nothing is removed**. If the root itself cannot be read,
`Scan` returns an error, the request is a 500, and the prune never runs at all. `Skipped` therefore
carries a `NoMedia` **bool** rather than a sentence — a caller deciding a row's fate on a substring
match is one reworded message away from deleting the wrong thing.

A row with a download in flight is never pruned even when its folder is empty, because the importer
creates the destination folder and only then hardlinks into it: there is a real window where the
folder legitimately holds nothing. "In flight" is the EXISTS over `downloads` that `LibraryByTMDBID`
already uses for the badge on a poster, so the screen and the pruner cannot disagree.

### Rows only. The directory is never touched

The prune calls `store.DeleteMovie` — rows, `downloads` then `movies` for the foreign key, no disk
and no torrent client. Emphatically **not** `download.Service.DeleteMovie`, which removes files and
talks to qBittorrent; that stays on the one request that destroys things on purpose
([D19](#d19--deleting-a-movie-removes-the-file-and-asks-qbittorrent-to-remove-its-own)).

Deleting the empty directories is the first improvement the next reader will propose, and it is
refused: until the phase 10 cutover those directories belong to Radarr, and curator removing them is
curator fighting the \*arr stack for the library.

### Nothing vanishes silently

That is [D6](#d6--tmdb_id-is-nullable)'s principle — surface what needs attention rather than
dropping it — applied to a different fact. `POST /api/scan` reports `empty`, `removed` and `missing`
beside `scanned`, and the log carries one line per removed row and one per kept-but-unaccounted-for
row. `scanned` narrowed its meaning to "folders that hold a film", which is why `empty` ships in the
same response rather than later: a library of 29 folders reporting 2 films has to explain itself.

### Three costs, accepted rather than worked around

1. **A film under 50 MiB stops being a movie** and its row is pruned. It was already unstreamable —
   `featureFile` applies the same floor — so the library was claiming a film it could not play.
2. **A symlinked feature *file* inside a library folder stops counting.** `largestVideo` used an
   lstat and recorded the link's own few bytes; the shared picker skips every non-regular entry, on a
   security argument written for torrent folders. Folder-level symlinks still work, and a test names
   the file-level behaviour so it is a decision rather than a surprise.
3. **The committed fixture scans as ZERO films at the production floor.** Its videos are 4096, 2048
   and 512 bytes, and a 50 MiB blob does not belong in git — `docs/phase-4.md` and T17 both say
   nothing may be added to `testdata/library/movies/`, and that rule survives. So `ScanWith` takes
   the picker's options, tests over the fixture lower the floor, and the real 50 MiB floor is proven
   over **sparse** files in `t.TempDir()` — the shape `internal/api/stream_test.go` already uses.

---

## D34 — curator provisions a Jellyfin it brought up, and never rewrites one somebody is already watching

**Status:** decided, measured against a throwaway 10.10.7 (2026-08-15) · **Amends:**
[D15](#d15--the-jellyfin-refresh-is-best-effort-and-its-key-is-optional) and the narrowness rule in
`internal/jellyfin`'s package doc · **Cited by:** [`phase-9.md`](phase-9.md),
[T64](tasks/T64-jellyfin-provisioner.md), [T66](tasks/T66-adopt-jellyfin.md)

`internal/jellyfin` gains the ability to **write** — complete a startup wizard, create a library,
mint an API key. Those methods live on a `Provisioner` that **only the setup flow constructs**, they
refuse any server reporting `StartupWizardCompleted: true`, and the importer and the poller keep the
narrow read-only `Client` they have always had.

**What forced it.** Phase 9's promise is that a stranger gets from `docker compose up -d` to watching
on an Apple TV. curator's own player is the right answer in a browser and will never be the answer on
a television, so Jellyfin is the other half — and the onboarding for that today is install a second
server, click through its wizard, find its API-keys page, paste a key back, and get two sets of
Docker mounts to agree. **The last step is the one people fail silently**: the paths differ, the
library scans, nothing appears, and no error is produced anywhere.

**The rule this reverses was right, and it stays right for `Client`.** The package doc says: *"no
user or session endpoints … nothing that writes, for the same reason `internal/qbit` cannot delete or
pause a torrent: a method that does not exist cannot be called by mistake against a media server the
household is watching."* That argument is untouched by this decision. What changes is that the
guarantee moves from *the package has no such method* to *the type that has it cannot be reached from
the poller* — which is weaker, and is therefore paid for with a guard rather than asserted.

**The guard has teeth because Jellyfin has none.** Measured: on a server reporting
`StartupWizardCompleted: true`, `POST /Startup/User {"Name":"attacker","Password":"x"}` carrying a
valid API key answered **`204`** and renamed the admin account and changed its password. Same user
`Id`; the original credentials answered `401` afterwards. Unauthenticated, the same call is `401`. So
a configured Jellyfin does **not** close its setup endpoints to an authenticated caller, and the only
thing between a household and being locked out of their own media server is curator's own restraint.
`Provisioner` re-reads `/System/Info/Public` before every write and does not cache the answer.

**Two operations are permitted against a configured server, and both are additive**: adding a library
and minting a key. Both are opt-in per use and both are things the user explicitly confirms. Nothing
under `/Startup/` ever runs against one. That is the adopt branch — a NAS install, or the Pi at the
phase 10 cutover.

**No Docker socket, and this is where that is recorded.** The alternative that lost was curator
downloading and running the Jellyfin container itself, which was the first proposal and is the one
everybody proposes. It requires `-v /var/run/docker.sock:/var/run/docker.sock`, which is **root on
the host**, handed to a service that ships with authentication **off by default**
([D25](#d25--authentication-is-optional-and-off-by-default)) — an unauthenticated LAN-wide root
shell, in a product whose entire security posture is "one password, optional, no TLS, LAN only". It
is not a trade that can be made safe by being careful with it.

**The cost of refusing it is one pasted command**, accepted deliberately:
`docker compose --profile jellyfin up -d`. Jellyfin sits behind a compose profile and **never runs
unbidden** — `docker compose up -d` brings up curator alone, which is the shape Nethmin asked for
outright: *"when the main app starts, does it start jellyfin as well even if user has not select
jellyfin? if yes it's not what i expect."* Everything after that line is curator's. The same
mechanism carries minter, which is why [T49](tasks/T49-minter-on-demand.md) no longer needs a socket
either, and why D23 — reserved to record that socket's cost — stays unwritten.

**What this buys, measured rather than claimed.** A library created entirely through the API, with
`LibraryOptions` carrying only `PathInfos`, scanned and produced
`ProviderIds: {"Tmdb":"1083381"}` with `ProductionYear: 2026`. That is exactly the field
[D32](#d32--the-jellyfin-link-is-keyed-on-the-tmdb-id-not-on-the-path) keys Open in Jellyfin on, and
exactly the year `years=` narrows by. **The deep link works on a bundle nobody configured by hand** —
which is the property being sold, on the key that was already chosen for other reasons.

**Pinned, and degrading to instructions.** The startup endpoints are not a documented contract; they
are what the setup wizard happens to call in 10.10.7, which is what the Pi runs and what the compose
file pins. When the API answers something unexpected, the flow **shows the manual steps with the
exact path to paste** rather than failing. A provisioning flow that breaks on somebody else's release
schedule and has no fallback is a support burden this project cannot carry.

**The alternatives.** Shipping Jellyfin always-on inside the bundle is simpler and was rejected
above. Asking the user to do all of it by hand is what exists today and is the thing that fails
silently on paths. Writing a Jellyfin plugin puts curator's code inside the process it is trying to
stay narrow against. And configuring Jellyfin by writing its `system.xml` and library folders
directly on the shared volume would skip the API entirely — it works, it is undocumented, it breaks
on any schema change, and it requires curator to hold a second, private theory of how Jellyfin stores
its configuration.

---

## D35 — A library row with no TMDB id is addressed by curator's own id, at `/library/film/?id=…`

**Status:** decided, implemented in [T61](tasks/T61-unmatched-film-has-no-way-in.md) ·
**Extends:** [D21](#d21--the-movie-page-is-movieid-because-the-ui-is-a-static-export), which it
deliberately does not amend · **Follows from:** [D6](#d6--tmdb_id-is-nullable)

[D21](#d21--the-movie-page-is-movieid-because-the-ui-is-a-static-export) addresses the film page by
TMDB's id, and [D6](#d6--tmdb_id-is-nullable) keeps that id NULL for a folder TMDB could not match.
Together they leave a film curator can play with no URL to play it from:
[T60](tasks/T60-library-way-in-web.md) made every matched Library card a link and left the unmatched
one a `<div>`, which is the gap this closes.

**D21 is not amended. It is still true, and it is about a different page.** `/movie/` is a page
*about a catalogue entry* — every fetch it makes is a TMDB call, and its identity is
[D20](#d20--the-film-comes-from-tmdb-the-search-box-only-finds-it)'s "the film comes from TMDB".
A row with no catalogue entry does not get a worse version of that page; it gets a page about the
**row**, at `/library/film/?id=<movies.id>`, where title, year, size, `library_path`, the Jellyfin
link and the player all come from `GET /api/movies/{id}` and nothing needs a catalogue at all.

So there are two addressing modes, and **which one a card uses is decided by the data rather than by
preference**: a row with a `tmdb_id` opens `/movie/?id=<tmdb_id>`, a row without one opens
`/library/film/?id=<movies.id>`. Adding `?curator_id=` to `/movie/` was the cheaper shape and was
rejected — it would make every TMDB fetch on that page conditional, which is D20's identity turned
into a branch.

### The count that was supposed to stop this, and why it did not

[T61](tasks/T61-unmatched-film-has-no-way-in.md) gated itself on a measurement: count the unmatched
films on the Pi, and if it is zero, say so and stop. **It is zero.** Measured 2026-08-16 against the
real library, read-only: 16 of the 29 folders hold a file over the 50 MiB feature floor, the other 13
are empty and are no longer rows at all ([D33](#d33--a-folder-with-no-film-in-it-is-not-a-movie-the-row-goes-the-folder-stays)),
and all 16 folder names match TMDB on the raw query — `{"scanned":16,"added":16,"matched":16,"unmatched":0}`.
Not one is unresolvable, including the eight with ` - ` in them that [D9](#d9--query-tmdb-with-the-raw-folder-title)
exists for.

**The gate was written for a project that no longer exists.** curator stopped being a Raspberry Pi's
\*arr replacement in phase 6 and is now a tool anybody runs on any server. One library measuring zero
is a fact about that library, not about the population: a home video, an anime fansub, a documentary
and a film TMDB has never indexed are all ordinary, and for each of them the card is a dead end for
ever. The gate is therefore satisfied and overruled in the same breath, and this record is where that
is said out loud so the zero is not rediscovered and misread as an argument to revert.

### The population that is not zero, and heals

There is a second way a row gets a NULL id, and it is guaranteed rather than incidental: **a scan run
before a TMDB key is in force marks every film unmatched.** Measured on a clean instance over the
same 16 folders, `{"matched":0,"unmatched":16}`.

It heals, and the shape of the healing is the part worth recording. `PUT /api/settings` stores the
key and answers `"source":"stored","pending_change":true,"restart_required":true` — and a rescan
immediately afterwards still matches **nothing**, because the TMDB client was built at startup. Only
after a restart does the same scan return `{"matched":16,"unmatched":0}`. So "every scan retries the
rows that still have no match" is true and incomplete; the restart is load-bearing, and
[T50](tasks/T50-first-run.md)'s wizard already says so in the one place it matters.

That population is why `/library/film/` explains *which* of the two reasons applies rather than
asserting the match failed. A keyless install has not asked TMDB anything, and telling somebody their
folder name was rejected when no question was ever put is the class of small lie this codebase
refuses elsewhere.

### Jellyfin needed one change, and it was a fallback that already existed

`jellyfin_url` lived only on the TMDB detail body, so an unmatched film had no link *path*, not
merely no link. `GET /api/movies/{id}` now carries it, through a struct that **embeds**
`store.Movie` — so the route keeps the exact JSON it has always answered and gains one optional key.
`GET /api/movies` deliberately does not: the lookup costs a request per film against a service that
may be switched off, and a library screen asks for every row at once.

For a row with no id there is nothing to look up, so nothing is looked up — it goes straight to
[D32](#d32--the-jellyfin-link-is-keyed-on-the-tmdb-id-not-on-the-path)'s search link. That is not a
new fallback: D32 already sends *"a film Jellyfin has not matched to TMDB"* there, and a film
**curator** has not matched is the same situation seen from the other end.

### What this deliberately does not do

**Manual matching** — a "search TMDB for this folder" action that writes the `tmdb_id` — was T61's
third option and is the only one that would also fix the poster, the overview and the deep link. It
is not rejected, it is *not this*: it cannot help the keyless population at all (there is no key to
search with, which is why the rows are unmatched), it is a feature rather than a way in, and the way
in has to exist first. When it is built it takes a number of its own and cites this one.

---

## D36 — A row TMDB could not match is matched by hand, and the scan never takes it back

**Status:** decided, implemented in [T67](tasks/T67-manual-match.md) · **Follows from:**
[D35](#d35--a-library-row-with-no-tmdb-id-is-addressed-by-curators-own-id-at-libraryfilmid), which
reserved this number in its closing line · **Narrows:**
[D9](#d9--query-tmdb-with-the-raw-folder-title)'s "never guess" to "never guess, and let a human say"

[D35](#d35--a-library-row-with-no-tmdb-id-is-addressed-by-curators-own-id-at-libraryfilmid) gave a
row with no `tmdb_id` a page, and closed by naming what it deliberately did not do: *"Manual
matching … is not rejected, it is not this … When it is built it takes a number of its own and cites
this one."* This is that number.

`POST /api/movies/{id}/match` takes curator's own `movies.id` and a TMDB id, and writes `tmdb_id`,
`overview` and `poster_path`. The picker lives on `/library/film/`, seeded from the title and year
the folder already carries — the strings the scan searched with and failed on — rather than a blank
box somebody retypes.

**This does not contradict [D9](#d9--query-tmdb-with-the-raw-folder-title), it completes it.** D9
refuses to *guess*, because an unconstrained query for a bare title returns a confident wrong answer.
A human choosing from a grid of posters is not a guess, and
[D20](#d20--the-film-comes-from-tmdb-the-search-box-only-finds-it) already settled that the person
looking at the posters is the one who can tell Avengers: Endgame from Avengers: Doomsday. What was
missing was somewhere to say so.

### It refuses rather than overwrites, and the precedent is already in the codebase

A row that already has a `tmdb_id` is refused with a 409, not corrected.
[`adoptTwin`](../internal/store/imports.go) decided this for the same column: *"the twin is already
matched. A match the scanner established from the folder title is not worth overwriting with one a
client sent."* Correcting a **wrong** match is a different feature and needs its own way in — a
matched card routes to `/movie/` and never reaches this page — so building the server half now would
ship a path with no caller.

`tmdb_id` is UNIQUE, so a second refusal sits beside it: another row already holding that id is also
a 409. Both are decided by `SELECT` inside the transaction rather than by reading the driver's
constraint message back, because a substring match on a `modernc.org/sqlite` string is a guard that
stops working on a driver upgrade without failing a test.

### The population is smaller than it sounds, and saying so is the point

Manual matching cannot help the keyless install at all — there is no key to search with, which is
why those rows are unmatched, and D35 already measured that they heal on a **restart**. It cannot
help a film TMDB has never indexed either. What is left is one population: a key is in force and the
folder name did not resolve. That is small, and it is permanent — no rescan will ever change it,
because the match pass only ever reads `WHERE tmdb_id IS NULL` and a folder name that will never
resolve is retried for ever.

So the picker is drawn only where a key is **in force**, and `integrations[].configured` is what
answers that rather than the settings row. Measured on a running instance: after `PUT /api/settings`
stores a key, `settings[].configured` is `true` while `integrations[].configured` is still `false`
and the endpoint still answers `503` — the TMDB client is built at startup, which is D35's
load-bearing restart seen from a third angle. Gating on the settings row would draw a button that
fails on every click, and would tell somebody their folder name was rejected when no question has
been put yet.

### What survives a rescan, and the one thing that does not

A manual match is permanent, by three constructions rather than by a promise:
`UpsertMovieByPath`'s `SET` list does not include `tmdb_id`, `overview` or `poster_path`;
`store.ScannedMovie` has no field for them; and the scan's match pass reads only
`WHERE tmdb_id IS NULL`. The only way to lose one is
[D33](#d33--a-folder-with-no-film-in-it-is-not-a-movie-the-row-goes-the-folder-stays) removing the
row when the film leaves the disk.

**`year` is the exception, and it was built the other way first.** A hand-matched row is the only way
to produce a row whose year disagrees with TMDB's, and that disagreement costs the Jellyfin deep
link, because [D32](#d32--the-jellyfin-link-is-keyed-on-the-tmdb-id-not-on-the-path) narrows its
lookup with `years=` on the stated premise that *"both sides take the year from TMDB"*. Writing
TMDB's year fixes the link; it also reverts, because `year` **is** in the scan's `SET` list. Measured
end to end: a 2019 folder matched to a 2008 film answered `2008` and a deep link, then `2019` and a
search one rescan later, while `overview` and `poster_path` held. A value that reverts on the next
scan is worse than one never written, so the year stays the folder's and the link takes D32's search
fallback, which always lands somewhere useful and never 404s.

Making it stick means moving `year` out of the scan's authority for matched rows. That is a change to
the division of authority `UpsertMovieByPath` documents — the scan owns title, year, media_type,
status, quality and size_bytes — and it is a decision of its own rather than a rider on this one.

---

## D37 — `year` is the folder's; TMDB's year gets a column of its own

**Status:** decided, implemented in [T68](tasks/T68-tmdb-year.md) · **Follows from:**
[D36](#d36--a-row-tmdb-could-not-match-is-matched-by-hand-and-the-scan-never-takes-it-back), which
closes by naming this as a decision of its own · **Restores the premise of:**
[D32](#d32--the-jellyfin-link-is-keyed-on-the-tmdb-id-not-on-the-path)

`movies.year` was doing two jobs, and nothing noticed until a row could be matched by hand. It is
**the folder's year** — parsed out of `Title (Year)`, written back out by `library.DestFolder`, and
round-tripped by a test. It is also **the film's year**, which is what a Jellyfin lookup is narrowed
by. For every row the scan matched those are the same number, because `SearchMovie` rejects a
candidate whose year disagrees, so the two meanings were never separable. [T67](tasks/T67-manual-match.md)
made them separable and the Jellyfin deep link is where it showed: a hand-matched row was looked up
under the folder's year, `years=` narrowed to a year the film is not in, and a perfectly good match
silently degraded to a search link.

### The obvious repair is the wrong one, and it costs a second folder

[D36](#d36--a-row-tmdb-could-not-match-is-matched-by-hand-and-the-scan-never-takes-it-back) proposed
moving `year` out of the scan's authority for matched rows. **That was measured and refused.**
`year` is not metadata the scan happens to own — it is half a directory name, and
`internal/importer/importer.go` builds the destination folder from **the row's** title and year:

```go
folder, err := library.DestFolder(movie.Title, movie.Year)
```

A hand-matched row keeps a `/movie/` catalogue page, and a release dispatched from it resolves
through `UpsertWantedMovie`, which matches on `tmdb_id` and returns the existing row untouched. So a
matched row whose `year` had moved to TMDB's would import its next release into `Title (TMDBYear)/`
while the film already sat in `Title (FolderYear)/` — one film, two folders, and the second one
scanned back in as a second row. Freeing the column fixes a link by breaking an import.

### So the row carries both, and says which is which

`movies.tmdb_year`, nullable, written only by `store.MatchMovie`. `year` keeps its meaning and the
scan keeps its authority intact — phase 1's division is unchanged, which is what T67 asked for.

**NULL is not missing data; it is a statement.** It says the folder's year *is* TMDB's, which is
true by construction for every row the scan matched. So the column needs no backfill to be correct
on an existing database, and the scan's own match (`SetTMDBMetadata`) deliberately does not write it
— it would only ever store a number equal to the one beside it.

`Movie.MatchYear()` is the accessor, and the rule lives there rather than at three call sites: TMDB's
year when it is known to differ, the folder's otherwise. Everything that identifies the film to
something outside curator asks it — `GET /api/movies/{id}`, the match response, and `checkFilm`,
which proves a Jellyfin key against a library row and would otherwise report a good key as broken.
The importer is the one caller that still wants `year`, because that is the folder it already wrote.

### Measured, against the real TMDB and the real 10.10.7

A folder named `Zzz Nonexistent Home Movie Xyq (2011)` matched by hand to Iron Man (`tmdb_id` 1726,
released 2008) — T67's failing case exactly:

| | `year` | `tmdb_year` | `jellyfin_url` |
|---|---|---|---|
| after the match | 2011 | 2008 | `#/details?id=0a938432…&serverId=004aee63…` |
| after 1, 2 and 3 rescans | 2011 | 2008 | the same deep link |

T67 measured the same shape reverting on the first rescan. Opening a real 29-row database written
before the column existed added it, left all 29 rows intact, and left every one of them NULL.

**Two honest edges.** TMDB can revise a release date after a match, and neither `year` nor
`tmdb_year` would learn about it — the same staleness `tmdb_id` already has, and not worth a refresh
pass. And nothing on screen reads `tmdb_year`; it is in the API response because the row carries it,
not because a screen wants it.

---

## D38 — A wrong match is corrected by overwriting it, never by clearing it first

**Status:** decided, implemented in [T69](tasks/T69-correct-a-wrong-match.md) · **Closes:**
[D36](#d36--a-row-tmdb-could-not-match-is-matched-by-hand-and-the-scan-never-takes-it-back), which
named this case and declined it for want of a way in · **Builds on:**
[D35](#d35--a-library-row-with-no-tmdb-id-is-addressed-by-curators-own-id-at-libraryfilmid), whose
page already accepted a matched row on purpose

[D36](#d36--a-row-tmdb-could-not-match-is-matched-by-hand-and-the-scan-never-takes-it-back) let a
human match a row TMDB could not, and refused the row TMDB matched **wrongly** — *"a different
feature and needs its own way in — a matched card routes to `/movie/` and never reaches this page —
so building the server half now would ship a path with no caller."* This is that way in, and the
write that follows it.

### The way in is a link, because the page was already open to it

A matched card still routes to `/movie/?id=<tmdb_id>` and an unmatched one still routes to
`/library/film/?id=<movies.id>`. Neither changes. What was missing was an edge from the first back to
the second, and it turned out to cost nothing: `/movie/` already holds `library.movie_id` — it is
what the player is handed — and `/library/film/` already declined to refuse a matched row, its own
comment calling that *"an invented rule"*. Two seams left open by earlier tasks met in the middle.

`/movie/` is also where the doubt arises. Somebody clicks a card in their Library and gets a page
about a film that is not the one in that folder; the poster, the title and *"curator already has this
film"* all assert the match is right, so the one control that doubts it belongs beside them rather
than a screen away.

### The obvious implementation restores the very film being corrected away from

Clear `tmdb_id`, `overview`, `poster_path` and `tmdb_year`, then reuse `MatchMovie`. It reuses code,
it needs no new store method, and **it is broken**, because a NULL `tmdb_id` is not an inert state:

```
MoviesMissingMetadata  →  WHERE tmdb_id IS NULL
```

That is the scan's work list, and every row on it goes to `SetTMDBMetadata`, which overwrites
unconditionally from a search on **the folder name** — the thing that produced the wrong match to
begin with. A scan landing between the clear and the match therefore restores exactly the film being
corrected away from, and reports success while doing it. `adoptTwin` is a second writer through the
same hole: it sets `tmdb_id` on a row whose own is NULL, so a completing import can repoint a cleared
row with no request at all.

This needs no unlucky timing to hit. The clear and the match are two HTTP requests with a human
between them, and a scan is one button on the Library screen.

So `store.CorrectMatch` writes the four columns in one statement inside one transaction, and the row
goes from the wrong id to the right one without ever being NULL. The cost is a second store method
rather than a reuse; the window not existing is what is bought.

### POST establishes, PUT replaces

Two methods on one path, not two paths. The resource is the same — the row's match — and what
differs is whether one is being established or replaced, which is precisely what an HTTP method
carries. Go 1.22 routing dispatches on it, so the pair costs one line.

A body flag (`{"replace": true}`) was the alternative and it loses on one property: a client that
forgets the flag gets the destructive behaviour, whereas a client that sends the wrong method gets a
409 that names the right one. Each method keeps a refusal that is the other's inverse —
`ErrAlreadyMatched` and `ErrNotMatched` — so neither can quietly do the other's job.

One refusal is deliberately **not** inverted. `CorrectMatch`'s uniqueness probe carries `AND id != ?`,
so correcting a row onto the film it already holds is allowed and refreshes the overview, poster and
`tmdb_year`. Without that clause a correction collides with itself, and the sentence handed back
names a conflict with the user's own row.

### What did not change, which is most of it

`year` is still the folder's and `tmdb_year` still the film's
([D37](#d37--year-is-the-folders-tmdbs-year-gets-a-column-of-its-own)) — a correction moves the
second only. The title is still the folder's ([D9](#d9--query-tmdb-with-the-raw-folder-title)). The
Library grid, `GET /api/movies` and both routing rules are untouched. `MatchMovie` still refuses a
matched row, and `SetTMDBMetadata` is still the scan's unconditional write, because widening either
for this feature is what T67 refused one layer down and the refusal still holds.

**There is deliberately no unmatch.** Nothing in the product wants a row naming no film, and the one
situation that produces one — the film leaving the disk — is
[D33](#d33--a-folder-with-no-film-in-it-is-not-a-movie-the-row-goes-the-folder-stays)'s and already
handled. Refusing to build it is also what keeps the paragraph above true.

**One honest edge.** `states.tsx` titles every 409 *"Not finished yet"*, and 409 now covers five
unrelated refusals. It stays latent because the picker renders the server's sentence inline rather
than through `<Failure>` — but a screen that ever routes a 409 the other way surfaces it, and fixing
the title is a task of its own.

---

## D39 — A failure title must be true of every situation its status covers, or there is no title

**Status:** decided, implemented in [T70](tasks/T70-a-title-that-is-true.md) · **Corrects:**
[D38](#d38--a-wrong-match-is-corrected-by-overwriting-it-never-by-clearing-it-first), whose closing
paragraph called this latent and counted it at five · **Applies to** `web/components/states.tsx`
alone; no Go file changes

D38 ended by naming one honest edge: *"`states.tsx` titles every 409 'Not finished yet', and 409 now
covers five unrelated refusals. It stays latent because the picker renders the server's sentence
inline rather than through `<Failure>`."* Both halves are wrong. It is ten refusals across eight
routes, and it was never latent: the picker is one caller among several, and the Library screen has
been titling a refused delete *"Not finished yet"* since `ccd78f1`, which added the delete and its
`ErrWrongCategory` 409 together. The same paragraph was carried into
`api.ts` and into the last two handoffs unchecked, which is its own lesson — a count nobody can
verify cheaply is a count that goes stale silently.

### The status was doing a job it cannot do

`title()` maps an HTTP status onto a sentence about what happened. That works only while the mapping
is a function, and for two statuses it is not:

```
409  →  ten sentences, eight routes
422  →  four sentences, two of which reach the same banner
```

A delete refused because the torrent belongs to the *arr stack, a dispatch refused because the film
is already in the library, and an import refused because the torrent is still going are three
different events, and the browser had one word for all of them — the third one's. `ErrNoVideo` and
`ErrBadTitle` leave `failImport` as the same 422 and arrive at the same banner, so 422 was wrong
without even leaving the screen it was written for.

**The count is the argument, not a documentation defect.** It went from five to ten while every
build passed, because a status→words table in TypeScript is a second vocabulary that has to be kept
in step with the Go one by hand — precisely what `Download.reason`'s comment refuses two files away,
in the same words, for the same reason. Nothing makes the eleventh refusal cheaper to notice than the
tenth was.

### The rule that survives is about categories, not counts

A title is a category and a category may cover many situations, so "one status, one meaning" is the
wrong test and would have deleted most of the list. The test is narrower: **a title may only say
something true of every situation its status covers.** 503 keeps *Not configured* because every 503
is an unconfigured integration; 502 keeps *A dependency is down* for the same reason; 404 and 410
keep theirs. 409 and 422 have no such sentence, so they get none.

The null is returned explicitly and the cases are not deleted. A deleted case falls through to
`default` and renders `Failed (409)`, which is the status number dressed as a diagnosis.

**Both ways of getting this wrong compile, and that was measured rather than assumed.** The other is
drawing the `<strong>` unconditionally: `ReactNode` includes `null`, so `<strong>{heading}</strong>`
typechecks clean — `npx tsc --noEmit` exits 0 — and renders an empty bold block whose only symptom is
a stray gap. `web/` has no test runner and `next build`'s type check refuses neither, so this
decision is enforced by two explicit lines of code and the comments on them, and by nothing else. A
reader who deletes either as redundant has reintroduced the bug silently, which is why they say so in
place.

### The caller cannot supply what the status could not

The obvious alternative is a `title` prop, so the screen that knows what the user was doing names it.
It loses on a fact about this UI: **no error state in it holds a single status.** The Library
screen's one `error` is set by `load()`, `rescan()` and `remove()` alike, so a fixed word above it is
wrong for two of the three, and `importError` takes a 500 or a 502 as readily as the 422. A prop
would be wrong wherever it was used and would leave the guess standing wherever it was not.

So the title comes from neither, and the sentence the handler already wrote carries the banner alone.
That is not a new pattern: `match-picker.tsx` has rendered its own two 409s as a bare message with no
title since T67, and its comment gives this decision's reason a task early — *"a generic banner would
throw away the only part that says which"*.

### What this deliberately did not do

**No `code` field on the error envelope.** It is the durable fix and it was refused on scope, not on
merit: `s.fail` is the chokepoint for six of the eleven and the other five write their status through
three different envelopes, so it is a seven-site server change buying a title that the server's
sentence already carries. If a screen ever has to *branch* on which 409 it received — rather than
print it — that is the moment to build it, and `jellyfinFailureBody.Adopt` is the precedent for how.

**One honest edge.** Three of the ten sentences leak a Go error chain at a human — `import <hash>: …`
and `delete movie 7: removing torrent …: the torrent client: qbit torrents/delete: …` among them —
and the title was the paper over that. Removing the paper makes them more visible, not less, which is
the correct direction and is why this was not deferred until after they are rewritten. Writing those
three sentences is a server task with its own measurements.

---

## D40 — A refusal's sentence is written at the boundary that answers it

**Status:** decided, implemented in [T71](tasks/T71-a-sentence-written-for-a-human.md) · **Completes:**
[D39](#d39--a-failure-title-must-be-true-of-every-situation-its-status-covers-or-there-is-no-title),
whose closing paragraph deferred this and counted it at three · **Applies to** `internal/api`'s five
classifiers and the three typed errors they read; no envelope changed and no web file changed

D39 removed the browser's false title on the argument that *"the sentence the handler already wrote
carries the banner"*. For five of the thirteen 409 and 422 answers there was no such sentence. There
was a `fmt.Errorf` chain, and `internal/api/api.go:508` — `map[string]string{"error": err.Error()}` —
put the whole flattened thing on the wire.

### It was five, not three, and the two extra were at 422

D39 named three, all at 409. Counting every distinct sentence and reconstructing each from its format
strings gives 10 at 409 and 3 at 422, of which **8 were written for a human and 5 were chains**. The
two nobody had counted are both in `provisionFailure`, and one of them is a single character:
`body.Error = "…" + body.Error` appended the raw chain to a sentence that had already said it better,
while `adoptFailure` did the same job for the same sentinel with `=` a few hundred lines away.

**That is the third time a count in this area has been wrong** — five in `api.ts`, ten in D39, thirteen
here — and the reason is the same each time: nothing derives it, so it is re-counted by hand or not at
all.

### The design question D39 left had a third answer, and both its horns were false

D39 framed the choice as a field on the envelope versus handlers that stop wrapping and cost the log
its detail. Measured:

- **Nothing logs these chains.** `internal/api/api.go:505` gates the only log on `status >= 500`, so no
  409 or 422 error chain is ever written to a log. There was no log line to protect — the response was
  the verbose channel and the log was the silent one.
  **Precision added by [T72](tasks/T72-the-chain-belongs-in-the-log.md):** as first written this said
  *"every 409 and every 422 curator answers is written to the client and never written to a log at
  all"*, and that sweeping form is false. Two 4xx paths log independently, before reaching `fail` —
  `internal/api/downloads.go:142` logs the already-have-this-film 409 at Info, and
  `internal/api/stream.go:215` logs a 404 at Warn. Neither logs a chain, so the argument stands; the
  sentence did not.
- **The one chain that is logged already carries its prefix as a field.**
  `internal/importer/importer.go:302` logs `"hash", hash` as an attribute, so `importer.go:95`'s
  `import %s: ` duplicated it.

So neither horn was paid for. The answer is the one `failMatch` has used since T67 and
`internal/api/imports.go:47-49` uses once for `store.ErrNotFound`: **the handler that identified the
refusal writes the sentence.** It already knows which refusal it is — that is what its `errors.Is`
switch is — and it is the last place that knows both the refusal and the reader.

**No package stopped wrapping.** `CLAUDE.md`'s `fmt.Errorf("scan %s: %w", path, err)` convention is
untouched and every chain is intact. What changed is that `err.Error()` is no longer what the browser
reads.

### Where a fact would be lost, it travels in a typed error

Three sentences needed a value that only exists deep in the chain, so three types now carry one each —
`torrent.WrongCategory`, `download.NotCompleted`, `library.BadTitle` — each `Unwrap`ing to the
sentinel that already existed, so every `errors.Is` in the repo and in the tests keeps answering. That
is `stepError`'s shape (a field recovered with `errors.As`) with `unreachable`'s discipline (the
sentinel is reached through `Unwrap` and its text never prints).

`torrent.WrongCategory` also closed a defect nobody had written down: `internal/qbit` wrapped the
sentinel with `qbit torrents/delete: ` and `internal/engine` with `engine: `, so **which words a user
was shown depended on `TORRENT_BACKEND`**. Both construct the type now and the sentence is one
sentence.

**The sentence never depends on the typed error being present.** Each handler writes something true
from the sentinel alone and the type only adds a clause. This is not caution for its own sake: seven
existing tests construct their cases as bare `fmt.Errorf("import x: %w", …)`, so the fallback is the
branch most of the suite exercises.

### What this deliberately did not do

**No `code`, `kind` or `slug` on the envelope**, for the reason D39 gave and which still holds:
nothing branches on which 409 it got, and `jellyfinFailureBody.Adopt` remains the precedent for the
day something does.

**The 4xx are still not logged.** Noticing that nothing logs them makes logging them tempting, and it
is a separate argument with a real cost on the other side — `failFields`
(`internal/api/settings.go:511-512`) deliberately never logs because its messages can quote a rejected
value. It was left where it was found.

**The guard is a test that asserts absences.** `make check` proves nothing about prose, and the wrong
implementation here is invisible rather than broken — passing `err` through compiles, answers the
right status, and carries a non-empty `{"error": …}`, which is all the pre-T71 tests checked. So the
tests pin the specific substrings that must NOT appear: `qbit`, `torrents/delete`, `delete movie`, the
info hash, `find feature in`, `destination folder`, `AuthenticateByName`. Verified by reverting the
handler and watching them fail.

---

## D41 — A dependency's failure has two readers, and the chain belongs to the second one

**Status:** decided, implemented in [T72](tasks/T72-the-chain-belongs-in-the-log.md) · **Extends:**
[D40](#d40--a-refusals-sentence-is-written-at-the-boundary-that-answers-it) from the refusals to the
dependency failures, where its central measurement does not hold · **Applies to** `internal/api`'s
twelve 502/503 sites and one 200, plus `failCause`/`logCause` and `download.Unprotected`; no envelope
changed and no web file changed

D40 stopped five 409 and 422 answers rendering a `fmt.Errorf` chain at a person. It deliberately left
the 5xx alone. There were twelve of those, not the two that had been measured, and one more at 200.

### D40's argument does not transfer, and inverts

D40 turned on a measurement: `internal/api/api.go:505` gates the only log on `status >= 500`, so a
refusal's chain was written to the client and to nobody else. **Losing it cost nothing because it was
already lost.** At 502 and 503 that gate is open, and the same investigation produces the opposite
result in two different ways:

- **The `fail` sites already log the chain**, so writing a sentence through `fail` would have put the
  sentence in both channels and *destroyed* the diagnostic — the precise inverse of D40's case.
- **The seven Jellyfin sites log nothing**, because `provisionFailure` and `adoptFailure` answer
  through `respond` rather than `fail`. They were 5xx written to **nobody**: a chain sitting in a body
  where no human could read it, and in no log where an operator could.

So the answer is not "move the chain" but **split the readers**. `failCause(w, status, sentence,
cause)` writes the sentence and logs the cause. `fail` is untouched — D40's *Do not* refused a fourth
parameter on it because at 409 the second channel was dead, and this is a second function for the
case where both channels are live.

`internal/api/stream.go:299-301` has done this since T44 and `stream_test.go:945-966` pins both
halves. It is the precedent, and the rule is a generalisation of it rather than a new idea.

### The count was wrong for the fourth time, and the thirteenth site was found by accident

Ten 502 sites, fifteen 503, of which **twelve leaked**. Six of the ten and nine of the fifteen are
outside `jellyfin.go`, so the two measured strings — both from `POST /api/jellyfin/provision` — were
samples from one route in one package.

**`internal/api/browse.go:177` was the thirteenth, and it answers 200.** A failed discover rail set
`rows[i].Error = errs[i].Error()`, under a comment arguing the chain was *"exactly what the operator
needs"* — which is true, and is exactly why it now goes to the log. It was found by hitting a live
instance with a key TMDB refuses, not by any grep: every audit of this surface has keyed on a status
constant, and this site has none. **A count derived from statuses cannot find a leak that is not at a
status**, which is the fourth distinct reason a count in this area has been wrong.

### What the sentence may not contain

`snippet` (`internal/jellyfin/client.go:175-184`) quotes **up to 256 bytes of a third party's HTTP
response body**, and ten templates splice it into a chain. Every one of them reached the JSON `error`
field that `web/components/states.tsx:48` renders verbatim. Jellyfin's `"Error processing request."`
and TMDB's `"Invalid API key: You must be granted a valid key."` are not curator's prose and were
being shown as though they were.

**Which backend answered is also not the reader's business.** `internal/qbit` prefixes `qbit <path>: `
and `internal/engine` prefixes `engine: `, so which words a user read on a 502 depended on
`TORRENT_BACKEND`. `torrent.WrongCategory` closed that for the 409 in T71; this closes it for the 502
one line below, which was the other half of the same defect and had not been written down either.

### Where a fact would be lost, it travels in a typed error

One sentence needed a value only the chain had, and `download.Unprotected{Reason}` carries it in D40's
shape — `Error()` is the reason alone, `ErrUnprotected` reached through `Unwrap` and never printed.
`internal/vpn`'s guard sentences were always good; the defect was `dispatch <releaseID>: ` and a
second sentinel in front of them, so the reason survives whole and the prefixes go to the log.

Its `Unwrap` returns **both** errors, in `unreachable`'s multi-error form, because the wrap it
replaces was `%w: %w`: a guard that timed out must keep answering
`errors.Is(err, context.DeadlineExceeded)`.

### What this deliberately did not do

**The 500s still put a chain on the wire.** There the chain is arguably the right answer — it is
curator's own failure, and there is no other sentence to write — and settling it as a side effect of
this task would be settling it without an argument.

**The 4xx are still not logged**, for D40's unchanged reason. `logCause`'s gate at 500 is what let the
Jellyfin handlers — which answer 409 and 422 as well as 5xx — gain logging without reopening it, and
a test asserts a 409 still reaches no log.

**The guard is a test that asserts an absence and a presence.** Every one of these sites passed the
whole suite before the rewrite, because passing `err` through compiles and answers the right status.
The tests pin substrings out of the body — `jellyfin provision`, `jellyfin find movie`,
`torrents/delete`, `dispatch <id>`, `Invalid API key`, `Error processing request`, both upstream paths
— **and into the log**, which is the half D40 never needed. Verified by reverting each handler and
watching both halves fail.

---

## D42 — A dead base URL fails the build, but only once a control name proves the machine is online

**Status:** decided, measured, implemented in [T77](tasks/T77-a-dead-host-fails-loudly.md) ·
**Completes:** [D12](#d12--yts-is-reached-at-movies-apiaccelli-not-ytsmx), which has been assumed to
be guarded since it was written and was not · **Applies to** `internal/indexer/live_test.go` and the
two live indexer tests; no production code changed

D12 was paid for when `yts.mx` went NXDOMAIN and the base URL in the code kept pointing at it. The
guard that grew out of it — the live indexer tests — **could not have caught it happening again.**
A dead name produces `*net.DNSError`, a `*net.DNSError` is a `net.Error`, and
`classifyLiveFailure`'s transport rule reads any `net.Error` as *"this machine has no network"* and
skips. Measured 2026-08-18: a real live search against `yts.mx` **skipped**, and the suite was green.

### The obvious fix is right on Linux and wrong on darwin

`IsNotFound` is the NXDOMAIN discriminator, and it is a real one: an unreachable resolver gives
`IsNotFound=false, IsTemporary=true`, and a connection refused is not a `*net.DNSError` at all. On a
Linux runner, failing on `IsNotFound` would be exactly correct.

It is not correct on the machine this repo is written on. Go maps both `EAI_NONAME` and `EAI_NODATA`
to `errNoSuchHost` (`net/cgo_unix.go:189`), and macOS's `getaddrinfo` answers that way with no
network at all — so **a laptop on a plane and a host that has been deleted produce the same error
value.** The live tests exist under the promise that an offline machine does not fail the build, and
a rule that cannot tell those apart cannot keep it.

So the discriminator is not the error. It is **whether anything else resolves**: the verdict on a
name failure is `fail` if a control name resolves and `skip` if it does not.

### The control is the other indexer, and that is what makes this cheap

The alternative designs both cost something this one does not:

- **A third-party control host** (`one.one.one.one`, `cloudflare-dns.com`) puts a new external
  dependency inside a test, which is the thing [D7](#d7--do-not-adopt-the-knaben-aggregator) declined
  for the indexer list and is no more attractive here.
- **CI and a laptop answering differently** (`os.Getenv("CI")`) is cheap and targets the guard where
  "offline" is not a plausible explanation. It lost because the laptop gate would stop proving what
  the CI gate proves, and this repo has one definition of the gate on purpose —
  `.github/workflows/check.yml`'s header refuses a second one for the same reason.

`TestTPBLive` asks `movies-api.accel.li`; `TestYTSLiveSearchInterstellar` asks `apibay.org`. Both are
hosts the suite already contacts, so the dependency count is unchanged. Two measured properties make
it work:

- **A refused host still resolves.** apibay answers 403 to a GitHub address range (T73) while its
  name resolves from that same runner, so the control is intact in the one case CI actually hits.
- **Each host is guarded by the test that is not it.** If apibay.org dies, `TestTPBLive` asks
  accel.li, is told DNS works, and fails loudly. Only both dying in one run hides either — which is
  indistinguishable from an offline machine regardless.

### What this does not change

**"Skip on any non-200" is still the wrong fix**, unchanged since T73: 403/401/429 are a decision
about the caller, and every other status stays loud. **`IsTemporary` stays a skip** — that is the
offline machine, and failing it is precisely the outcome this decision was shaped to avoid. And the
control lookup happens on **one branch only**, because a skip that quietly resolves a name is a
second network call on the path that exists to avoid the first one.

---

## D43 — The Pi is a clean slate; television is retired and curator is the only downloader

**Status:** decided and **executed** 2026-08-18 ·
**Reverses** [D26](#d26--television-keeps-its-stack-the-cutover-removes-only-what-curator-replaces-for-movies),
two days after it was written · **Voids** T53's parity target · **Rewrites**
[T53](tasks/T53-run-alongside.md) and [T54](tasks/T54-remove-what-is-replaced.md)

The Pi's media disk was emptied. **363 GB deleted, 2.2 MB left, 870 GB free**, and the three
directories kept so the paths still exist:

| | freed |
|---|---|
| `downloads/complete/` | 201 GB — a **second copy**, measured `links=1`, never hardlinked to the library |
| `movies/` | 117 GB — 29 folders, 16 with a file |
| `tv/` | 46 GB — 4 series |

### Why D26 no longer holds

D26 kept television because television was in use: 3 monitored series, 40 GB, an episode imported
**2026-08-17**, the day before. That was the whole argument and it was a good one. **The premise is
gone by choice** — the series are deleted and television is retired deliberately, not because the
dependency analysis changed. D26's *reading* stands: radarr and sonarr did share one qBittorrent and
one Prowlarr. What changed is that there is no longer a sonarr to protect.

### The removal set goes from three to nine

`radarr`, `sonarr`, `seerr`, `recyclarr`, `prowlarr`, `flaresolverr`, `qbittorrent`, `gluetun` and
`byparr`. What remains is `jellyfin`, `portainer`, `watchtower`, `homepage` — **plus curator**.

**Thirteen services become five.** The original README promised "thirteen containers become six" and
D26 corrected it to ten; this overshoots the original promise rather than missing it, and
[T51](tasks/T51-documents.md) now has a third number to write instead of a second.

The two-tunnel cost D26 accepted **disappears with it.** curator's own WireGuard is the only tunnel
on the box and its engine the only torrent client, which is what
[D22](#d22--the-torrent-engine-moves-inside-the-binary-and-qbittorrent-becomes-the-second-backend)
and [D27](#d27--the-vpn-is-mandatory-and-curator-owns-the-socket) were built for.

It also retires a fragility rather than inheriting it: `qbittorrent` had been dead since
2026-08-18T01:38:35Z because `network_mode: "service:gluetun"` cannot be honoured by Docker's
restart-on-boot — `depends_on` binds `docker compose up` and not the daemon's own restart policy, so
a reboot raced qbittorrent ahead of gluetun and nothing retried. curator has no such seam because
the tunnel is in-process.

### What was kept, and what was actually lost

**The film list survives in the repository.** `testdata/library/movies/` holds 29 fixture
directories, and they were verified **set-identical** to the 29 folder names deleted from the Pi. The
titles, the years and the ` - ` colon substitutions that
[CLAUDE.md's title-parsing trap](../CLAUDE.md) is built on are all still here. A sized inventory was
captured beside the T52 backup before the delete.

**The T52 backup is still worth having**, though not for the reason written here first. This
paragraph originally read "its `.env` carries `NORDVPN_USERNAME` and `NORDVPN_PASSWORD`, which
curator needs for its own tunnel," and **that sentence is wrong**, corrected while T53 was being
prepared. Nothing in this repository reads either variable: `internal/vpn/config.go` parses a
wg-quick `.conf` and `validate()` demands `PrivateKey`, a peer `PublicKey` and an `Endpoint`.
gluetun consumed those credentials and *derived* a NordLynx config from them; curator has no such
step, which is the cost of owning the tunnel in-process rather than delegating it.

The backup is worth having because it is the \*arr rollback, which is what T52 was always for. The
tunnel is a separate problem and [T53](tasks/T53-run-alongside.md) states it.

What is genuinely gone is **163 GB of media**, which is re-downloadable, and Jellyfin's watch state
for it. Jellyfin itself is untouched and stays.

### What this costs the plan

**T53 loses its parity target.** "curator agrees with radarr about 29 and 16" was the only
independent check that curator reads a real library correctly, and there is now no radarr library to
agree with. T53 becomes *"curator works on the Pi from nothing"* — stand it up, search, download,
import, play — which is a weaker assertion honestly labelled rather than a missing one.

---

## D44 — curator reads the version; something else installs it

**Status:** decided and built · **Settles** [T80](tasks/T80-update-from-the-app.md) · **Asked for**
after [T53](tasks/T53-run-alongside.md) put curator on the Pi and [T79](tasks/T79-the-download-button.md)
produced a fix with no way to reach it

A container cannot replace itself. curator's image is `FROM scratch` and its process is uid 1000
with no shell ([T47](tasks/T47-image.md)), so an in-app Update button has to end somewhere that can
talk to the Docker daemon. There were three places it could end and the choice is the decision.

### What was rejected, and why it was tempting

**Mounting `/var/run/docker.sock` into curator.** One container, one button, no dependency, works on
every install. It was refused because that socket is root on the host: it can start a privileged
container, bind-mount `/`, and read or rewrite anything. curator answers on the LAN, parses
untrusted HTML from three indexers and renders release names from strangers. Handing that process
the ability to rewrite every container on the box is a trade nobody would make deliberately, and it
would undo the exact property T47 built the scratch image to have.

**Watchtower on a timer**, which is what the Pi was already doing to every other container:
`--cleanup --interval 86400`, no `--label-enable`. T53 turned it off for curator on arrival, because
an unattended updater restarts the container at an hour nobody chose — possibly mid-download — and
`--cleanup` then deletes the image it was running. Automatic updating is a different product
decision from *being able* to update, and only the second was asked for.

### What was chosen

**curator reads a version number over HTTPS and asks an updater to install it.** The privilege lives
in the updater, which is watchtower under compose's `updater` profile, holding the socket in eight
readable lines. curator holds nothing.

The bundle's updater runs `--http-api-update --http-api-periodic-polls=false --scope curator
--cleanup`. The second flag is the one that matters: without it this is the timer that was just
rejected. The third keeps it to containers carrying `com.centurylinklabs.watchtower.scope=curator`,
so an updater that *does* hold the socket still cannot reach a household's other containers.
`UPDATER_TOKEN` is mandatory — `${UPDATER_TOKEN:?…}` makes compose refuse to start rather than leave
a restart endpoint answering to anyone on the network.

### Three states, and the screen says which

A newer version with an updater configured is a button. A newer version with no updater is **the
command to paste, and that is not an error** — it is the ordinary state of anyone running curator
without watchtower, and it must work identically there. No newer version, or checking switched off,
are also different sentences: "up to date" and "never looked" must never be printed for each other.

### What it costs, stated rather than discovered

**curator cannot report whether the update worked.** Triggering one restarts the container serving
the request, so the last thing the process does is send it. The screen says "restarting", the
connection dies, and the answer is a page that loads again on a new version. The UI treats that
dropped connection as success, because it is.

**A version check is a request to GitHub.** It sends nothing about the install and reads a public
endpoint, but it is still a request a media server makes on its own, so `UPDATE_CHECK` is a switch.
It defaults **on**: knowing a security fix exists is worth more than the one request, and the
failure this defaults against is an update nobody hears about.

### The flaw this design walked into, and the rule that came out

The first implementation probed the updater before offering the button. **watchtower's `/v1/update`
is a GET, so the probe performed the update** — opening the Settings screen against a tokenless
updater would pull an image and restart curator. It was caught by running it and watching the
request counter, not by reading it.

There is no safe read-only endpoint to substitute, so the probe is gone: the button is offered
whenever a URL is configured, and a wrong token is reported when it is pressed. **An action endpoint
is touched at the moment the action is wanted and at no other time**, and
`TestProbingIsRefusedBecauseTheUpdateEndpointIsAGet` pins it.

### One thing that had to be fixed for any of it to work

There were **no GitHub Releases at all** — `release.yml` pushed tags and images and never created a
release object, so `releases/latest` answered 404 forever. The pipeline now publishes one with
`--generate-notes`, and the checker reports a 404 as "no releases have been published yet" rather
than as a fault.

---

## D45 — A mandatory value belongs to the service that needs it, not to the file that describes it

**Status:** decided and built · **Amends** [D44](#d44--curator-reads-the-version-something-else-installs-it),
whose property is unchanged · **Found by** [T51](tasks/T51-documents.md) running the quickstart from
an empty directory, which is the verification T51 was written around

D44 made `UPDATER_TOKEN` mandatory and said so in compose: `${UPDATER_TOKEN:?set UPDATER_TOKEN in
.env to enable the updater}`. The reasoning was right — an updater that restarts containers must not
answer to anyone on the network — and the mechanism was in the wrong layer.

**Compose interpolates the entire file before it filters profiles.** So a `:?` inside an opt-in
service is not opt-in at all. The measured result, from a clean directory with the published
`compose.yaml` and nothing else:

```
$ docker compose up -d
error while interpolating services.updater.environment.WATCHTOWER_HTTP_API_TOKEN:
required variable UPDATER_TOKEN is missing a value: set UPDATER_TOKEN in .env to enable the updater
```

No curator. **The quickstart — the one command phase 9 exists to deliver — did not work for anybody
who had not already opted into the updater**, and the failure names a variable for a feature they
never asked for. `docker compose config --services` proves the updater is not in the default set;
compose fails the file anyway.

### The fix is to let the service refuse

`WATCHTOWER_HTTP_API_TOKEN: ${UPDATER_TOKEN:-}`, and watchtower does the refusing. Measured against
`containrrr/watchtower:1.7.1` with an empty token and the socket mounted:

```
level=info msg="The HTTP API is enabled at :8080."
level=fatal msg="api token is empty or has not been set. exiting"     exit 1
```

It exits **before the API listens**, so there is no window in which a tokenless restart endpoint
answers. D44's property survives intact; only its enforcement point moved to the one place that can
express "required *when this service actually runs*", which compose's interpolation cannot.

**The general rule:** a `:?` in a compose file is a requirement on *every* invocation of that file,
including the ones that do not want the service. Put a profiled service's mandatory values behind
the service's own validation, and reserve `:?` for what the default run genuinely cannot start
without.

### What it costs, stated rather than discovered

The refusal is no longer a message from `docker compose up -d`; it is a container that exits 1 and,
under `restart: unless-stopped`, retries. Measured with the `updater` profile and no token:

```
SERVICE   STATUS
curator   Up 12 seconds (healthy)
updater   Restarting (1) 1 second ago
```

So somebody who opts in and forgets the token gets a restart loop rather than a refusal to start,
and has to read a log to find the one fatal line. That is worse **for them** than what D44 had, and
it is the correct trade anyway: it is paid only by the person who asked for the updater, in exchange
for the product installing at all for everyone who did not. The old behaviour charged the second
group for the first group's mistake.

### What this cost, and why it went unnoticed

T80 verified the updater by configuring one. Every path that exercised this file had a token in the
environment already, so the one case that mattered — a stranger with no `.env` at all — was the one
nobody ran. It is the same shape as [T79](tasks/T79-the-download-button.md)'s Download button, which
was broken in a browser on every install while every API-level test passed: **a default that is only
ever exercised by someone who is not you.**

---

## D46 — A probe budget belongs to the service being probed

**Status:** decided and built · **Found by** [T81](tasks/T81-a-probe-that-outlasts-the-browser.md)
reading minter's own HEALTHCHECK log on the Pi, after the defect had been carried across three
handoffs as prose

`probeTimeout` was one constant, 5 s, shared by the settings integrations table, the Jellyfin probe,
and minter's. The number came from a sentence in `Minter.Probe`:

> a question /health answers in milliseconds

Nobody had measured it. Docker had, every fifteen minutes, and kept the answer:

```
23:40:54 -> 23:41:02   8.61 s   {"ok":true,…}
23:56:02 -> 23:56:09   6.73 s   {"ok":true,…}
```

**A healthy minter never once answered inside curator's budget.** minter serves `/health` from the
process that drives its browser and waits on the same lock, so the endpoint's latency is a property
of what the browser is doing — not of the network, and not of whether minter is up. minter's own
HEALTHCHECK budgets `1m30s` for the identical call. curator budgeted 5 s and called the difference
"unreachable".

### The rule

**A reachability budget is a claim about the service, so it belongs beside that service and it has to
come from a measurement.** One shared constant is one claim applied to things that have nothing in
common: TMDB answers in milliseconds and a headless Firefox does not. `probeTimeout` stays 5 s for
the integrations table, which asks its question of services that answer immediately;
`minterProbeTimeout` is 20 s, a little over twice the slowest healthy answer observed, and still far
inside minter's own.

### The half that matters more

A bigger number would not have fixed the Pi. When T81 looked, minter was `Up 2 hours (unhealthy)`
with a browser that never finished, and `/health` had stopped answering at all — 30 s, three times,
zero bytes, on a port that still accepted the connection:

```
mint queued 854456ms behind an in-flight browser
```

curator reported that as **"minter is not running"**, above `docker compose --profile 1337x up -d`.
The container was running; the command was a no-op; the screen was confidently wrong.

The cause is that `indexer.unreachable{}` carries `ErrUnreachable` **alongside** the transport error
so that both `errors.Is` checks answer truthfully — a deliberate design, documented as such — and
`handleMinterProbe` tested `ErrUnreachable` first. A `switch` over a value that matches two sentinels
is order-dependent, and nothing about it looks order-dependent.

**A deadline is not evidence that nothing is listening.** It is evidence that nothing answered *yet*.
The two states take opposite instructions — start the container, versus wait for the one you have —
so a probe that collapses them is worse than one that reports neither. The deadline is now tested
first and reported as `unhealthy`, which the screen already renders as "not ready … until it
settles".

### What it costs

The screen can now take 20 s to move off "checking", against 5 s before, on an install where minter
is genuinely absent — a dial to a dead port fails immediately, so this is paid only where something
accepts the connection and then does not answer. In exchange, the two states that were one are two.

The interval was 5 s and the budget is now 20 s, so the browser polls **one probe at a time**. Left
alone, a busy minter would have collected a fresh request every tick while the previous sat queued
behind its browser: raising a timeout without a re-entrancy guard turns one late answer into a queue
of them, aimed at the service that was already too busy to answer.

---

D23 remains reserved for phase 9's socket cost and is still unwritten. D26 was written at the
front of phase 10 and reversed by [D43](#d43--the-pi-is-a-clean-slate-television-is-retired-and-curator-is-the-only-downloader) two days later.
D44's `${UPDATER_TOKEN:?…}` was moved into watchtower by
[D45](#d45--a-mandatory-value-belongs-to-the-service-that-needs-it-not-to-the-file-that-describes-it)
the day after it shipped; D44's paragraph still shows the compose form it was written with, which is
what a record does.

---

## D47 — Every torrent network operation is tunnel-bound or disabled

**Status:** decided, built and **measured on the Pi** ([T85](tasks/T85-the-capture-that-settles-it.md)) ·
**Amends** [D27](#d27--the-vpn-is-mandatory-and-curator-owns-the-socket), whose claim was true of the
paths it measured and silent about four others, and
[D29](#d29--a-written-setting-applies-at-the-next-start-the-password-applies-at-once), which gains a
second immediate setting · **Found by** T82 auditing the byte paths against
`anacrolix/torrent@v1.61.0` rather than against D27's own comment

The invariant, written once and quoted onto the VPN screen:

> **Every network operation of the torrent subsystem — payload, peers, uTP, DHT, trackers, webseeds,
> WebRTC, DNS and local discovery — is tunnel-bound or disabled. There is no third option.**

Two scope limits are part of the claim rather than footnotes to it. It covers the **embedded**
backend only: with `TORRENT_BACKEND=qbittorrent` curator does not own the socket and compares exit
addresses instead, which D27 already says and which the page now repeats where somebody might
generalise from a green badge. And it covers the **torrent subsystem** only: searches, TMDB,
artwork, minter, Jellyfin and the update check are on the host stack by design.

### What was actually leaking

D27 said the kill switch is structural, and T33 measured it: with `DisableTCP`, `DisableUTP` and
`NoDHT` the client opened zero OS sockets. **That measurement was true and is still true.** It was
about peer, tracker and DHT sockets. It was never a statement about the four paths below, every one
of which leaves the process without asking any dialer curator installed.

| Path | Carries | Why it was open |
|---|---|---|
| **WebTorrent / WebRTC** | **payload** | `DisableWebtorrent` appeared **nowhere** in the repository. `torrent.go:2203` gates it; past that gate `startWebsocketAnnouncer` builds a `websocket.Dialer` of its own (`webtorrent/tracker-client.go:35`) and WebRTC data channels move pieces. A `ws://` or `wss://` tracker in a magnet is the whole trigger. 1337x, YTS and TPB hand out `udp://`, so it has probably never fired — and "probably never" is not the promise D27 makes. |
| **DHT bootstrap DNS** | metadata | `bindConfig` never set `DhtStartingNodes`, so production kept anacrolix's default (`config.go:261`) → `dht.GlobalBootstrapAddrs` → `ResolveHostPorts` → a package-global `dnscache.Resolver` on the **host** (`dht/v2@v2.23.0/dns.go:26-30`). Eight lookups saying this machine runs a BitTorrent client, on the resolver the tunnel exists to bypass, refreshed every five minutes for the life of the process. |
| **UPnP** | nothing, but it announces | `NoDefaultPortForwarding` was set **only under `cfg.hermetic`**. Production ran `go cl.forwardPort()` (`client.go:414`) → `upnp.Discover` → SSDP multicast out of every real interface, for a mapping that would forward a router port to a listener inside a netstack no LAN packet can reach. |
| **A host-capable engine with the kill switch "on"** | **payload** | `startEngine` left `network` nil when `VPN_REQUIRED` was true with no tunnel and called `engine.New` anyway. `bindConfig` runs only when there IS a Network, so `DisableTCP`, `DisableUTP` and `NoDHT` were all **false**: host sockets and a **DHT node on the household connection**, announcing itself, on every boot, with no magnet dispatched and the settings screen reporting the switch as on. |

| **A `udp://` tracker's NAME** | metadata | Found by the capture, and by nothing else — see below. `tracker/udp/conn-client.go:82` calls `net.ResolveUDPAddr` on the stdlib resolver to produce the destination it then writes to curator's tunnel socket. The announce is encrypted; the question that produced its address is not. |

**One cause under the first four, and it is the sentence worth keeping: `cfg.hermetic` hardened the
TEST configuration in ways the production one never got.** `internal/engine/engine.go` even documents the
DHT behaviour and fixes it for tests only. No existing test could have caught any of this, because
every test was already behind the fix. That is the shape of the bug, not a detail of it.

### The fifth one, and why a capture was not optional

The first four came from reading `anacrolix.ClientConfig` against what each field does. The fifth
could not, because it is not a field: it is a stdlib call inside a dependency, on a path whose
*socket* was correctly bound the whole time. `LookupTrackerIp` is the hook anacrolix provides for it
and in v1.61.0 it is declared and called nowhere — so the classification test saw it, T82 marked it
`inert`, and that was true of the field while the capability it was meant to replace kept running.

**That is the limit of the guard below, stated where somebody will read it: it proves every field is
accounted for. It does not prove every egress path has a field.** The only thing that catches the
rest is looking at the wire, which is why [T85](tasks/T85-the-capture-that-settles-it.md) is a task
and not a paragraph. It found this within an hour of being run, on a path four separate readings of
the source had walked past.

[T86](tasks/T86-a-tracker-name-is-a-leak.md) closes it by resolving `udp://` tracker names through
the tunnel before anacrolix sees the URL — the second caller of the `LookupHost` this decision added
for the DHT.

### The guard that outlives this decision

`TestEveryEgressFieldOfTheClientConfigIsClassified` reflects over `anacrolix.ClientConfig` and
requires every field to be classified `bound`, `disabled`, `nil` or `inert`. Bump the dependency and
any new way out of the process fails the build until a person has read what it does. It earned its
keep immediately: `LookupTrackerIp` is declared and called nowhere in v1.61.0, and its own comment is
a TODO to wire it back in.

`nil` is the third category and the one that is not obvious. `WebTransport` and
`MetainfoSourcesClient` must stay at their zero values, because `client.go:284-296` uses them
**instead of** the transport carrying `HTTPDialContext` — setting either voids the webseed and
metainfo binding without touching `HTTPDialContext` at all.

### Bound, not disabled, where that costs nothing

The DHT bootstrap resolves **through the tunnel** rather than being switched off, because returning
nothing would cost cold-start peer discovery for every download. `engine.Network` gains `LookupHost`
for it. The alternative — resolving the eight names in `cmd/curator` and handing the addresses in —
keeps that interface at "dial and listen" but loses laziness, and laziness is the requirement: a
tunnel has not handshaken at start-up, and the DHT asks at bootstrap, which is later. Resolving is a
network operation, and the interface says so.

A config with **no `DNS =` line** resolves nothing and the bootstrap returns nothing. There is no
fallback to the host resolver, ever. A hostname tracker already fails that way, so this adds no new
failure mode; it declines to add the one that would matter, which is answering correctly by leaking
the question.

### Proving it rather than asserting it

`Engine.Binding()` reports the address the engine's one socket actually holds — read off the socket,
because the engine does not keep the Network it was given, so there is nothing here that could report
what it was configured with instead of what it got. `Tunnel.Owns(addr)` answers whether that address
is the tunnel's. **Two independent reads that have to agree**, and neither is a sentence written next
to the wiring. The page's headline is an AND of that, a fresh handshake, a differing exit address and
nothing being held, so it cannot go green on one of them.

### The watchdog, which reverses an earlier call

**This supersedes the earlier decision not to have one.** That was taken when the goal was a monitor,
and a monitor that runs only while somebody has the page open is a reasonable monitor. The goal is an
enforced kill switch on a box in a cupboard, and the same design cannot deliver it.

A `Sentinel` re-proves the tunnel on two cadences. The cheap one is a device read in this process
every 15 s — no third party, no network, so it still works when the tunnel is what is broken. The
expensive one is `CheckExit`, and it is **deliberately not unconditional**: running it forever is 288
requests a day to somebody else's service from a box doing nothing. It runs while something is
downloading, which is when the failure it catches can cost anything, and while the last verdict is
bad, which is not symmetry — a held download does not report itself as downloading, so without that
clause a bad verdict would switch off the only thing that could ever clear it.

`Hold` stops every torrent and drops its peers rather than dropping the torrents. A kill switch that
costs a half-finished download every time a tunnel blips is more expensive than what it protects
against, and a safety feature that expensive gets turned off. It is belt-and-braces over the
structural guarantee and honest about being redundant most of the time: with the tunnel down the
dials already fail. It exists for the state the structure does not cover — a tunnel up and
handshaking and no longer changing where traffic leaves from, where the dials succeed and the bytes
are the problem.

`Resume` asks the guard, and that is not symmetry with `Dispatch`. Dispatch is somebody choosing to
start one download; Resume is the whole library restarting itself on a box that has just rebooted,
which is exactly when a tunnel is most likely to be down and least likely to be watched.

### `VPN_REQUIRED` applies at once, and what that cannot do

It becomes the third `Immediate` setting, for D29's own argument about the password: a switch that
takes effect at the next restart leaves you unprotected until then, which inverts the point of
turning it on.

**It applies to the check and cannot conjure an engine.** A process that booted with no tunnel and
enforcement on has no torrent client at all, so turning enforcement off there still needs a restart.
`GET /api/vpn` reports that as `engine_started` and the screen says so.

**The trap it arrived with, recorded because the fix is not where it looks.** `settingsStates`
reported an Immediate setting's live value as the *stored* text. That is right for the two
authentication settings and wrong for this one, and the difference was never "immediate" — it is
whether there is a `*config.Config` field to read the value back from. With nothing stored the
resolution is `""`, and `VPN_REQUIRED` defaults to **true**, so a fresh install would have drawn the
kill switch **off** while curator was enforcing it. The same defect one layer down is resolving
enforcement through the stored string instead of `config.Load`: `ParseBool("")` is false, and
clearing the setting is how somebody returns to the default.

### Alternatives

**Disabling the DHT outright** rather than binding its bootstrap. Simpler, and it costs peer
discovery on every magnet that carries no working tracker, which is what the DHT is for.

**Refusing to start with no tunnel** rather than starting with no engine. It makes the failure
loud, and it locks somebody out of the settings screen that configures the tunnel — the argument
D27 makes for the UI staying off the tunnel applies here unchanged.

**Leaving the watchdog out and relying on the structure.** It is genuinely nearly enough: bytes stop
on their own when a tunnel dies. What it cannot do is notice, so the rows keep saying nobody is
seeding a release while the real answer is that curator's tunnel went away — and it cannot see the
one state where the dials succeed and the bytes still leave from the wrong address.

---

## D48 — Television is additive: a show is a row in `movies`, and the second library root is opt-in

**Status:** decided and **built** ([T88](tasks/T88-two-media-types.md)) · **Spends the hook**
[D6](#d6--tmdb_id-is-nullable) left in the schema in phase 1 · **Reopens** what
`roadmap.md` listed under *Deliberately out of scope* · **Extends**
[D5](#d5--manual-search-not-automatic-grabbing) to a second media type rather than reversing it ·
**Does not disturb** [D26](#d26--television-keeps-its-stack-the-cutover-removes-only-what-curator-replaces-for-movies)
or [D43](#d43--the-pi-is-a-clean-slate-television-is-retired-and-curator-is-the-only-downloader)

Curator does television: search a show on TMDB, pick a release, download it through the tunnel,
hardlink the episodes into a TV library, tell Jellyfin. **One deliberate grab at a time, exactly
like a film.** No monitoring, no scheduler, no RSS, no quality profiles, no unattended grabbing.

### Why this does not overturn D43

D43 retired television on the Pi, and the sentence that matters is its own: *"the series are deleted
and television is retired deliberately, not because the dependency analysis changed… there is no
longer a sonarr to protect."* **It retired a stack, not a capability.** Nothing in it argues curator
must not do television; it argues that sonarr no longer needed protecting, which is why nine
containers could go instead of three. D26 and D43 are records and are left exactly as written.

What *is* overturned is `roadmap.md`'s bullet — *"**TV.** Retired by choice in D43, not deferred"* —
and the README's *"No television. Movies only."* Both were true when written and both were always
qualified by the same escape hatch, which D6 put there on purpose: *"A `media_type` column
defaulting to `'movie'` is included from the start so TV is additive later."* This is later.

### A show is a row in `movies`, and that is forced rather than chosen

The obvious alternative is a `shows` table with an `episodes` table beside it. It does not survive
contact with one column:

```sql
downloads.movie_id INTEGER NOT NULL REFERENCES movies(id)
```

Foreign keys are genuinely on (`_foreign_keys=1` in the DSN), and `movie_id` is `NOT NULL`. A
`shows` table plus a nullable `downloads.show_id` — which `addColumn` could add today — **still
cannot insert a downloads row without a movies row**, so every show would need a shadow row anyway:
the single-table design plus a redundant second table. The only real alternative is a second
`show_downloads` table, and that duplicates far more than dispatch: the poller's `GetDownloadByHash`
sweep and its "torrent in our category has no download row" warning, `Service.Resume`,
`GET /api/downloads`, and `store.DeleteMovie`'s FK-ordered transaction.

**The stronger argument is that the accumulation semantics a season pack needs are already
written.** `MarkImported` → `adoptTwin` folds the dispatch-time `wanted` row into whichever row
already claims that `library_path`, repointing every download at it. That gives **one row per show,
N downloads pointing at it, converging on the folder as identity** — which is exactly what "season 2
arrives six months after season 1" needs, and it only works because downloads point at `movies.id`.

What carries over free, stated so nobody "fixes" it: `year INTEGER NOT NULL` as the *folder's* year
works unchanged, because `Show (FirstAirYear)` is Jellyfin's own series convention and `DestFolder`'s
four-digit check and `ParseFolder` round-trip it; `library_path UNIQUE` is fine, since a show folder
and a film folder are different paths; and the poller, `Resume`, `DeleteMovie`, `InsertDownload` and
`UpdateDownloadProgress` are media-agnostic already.

### `tmdb_id` could not be reused, and this is the expensive half

**TMDB's movie and tv id sequences overlap.** Severance is tv id 95396, and a film holds movie id
95396. `tmdb_id` is `UNIQUE` at table level, and `migrate.go` cannot drop it — its whole mechanism is
`addColumn` via `pragma_table_info`, chosen in phase 7 so that every step is idempotent by
construction, and relaxing a column constraint means rebuilding the one table that holds somebody's
library.

Putting a tv id in `tmdb_id` is not a rare loud collision. It is routine silent corruption:

| | what happens |
|---|---|
| `UpsertWanted` | probes `WHERE tmdb_id = ?`, so dispatching Severance returns **the film's row** and attaches the season pack to it. No error. |
| `MatchMovie` / `CorrectMatch` | answer `ErrTMDBIDTaken`, rendered as *"curator already has that film in the library under another folder"* — a sentence the user cannot act on because it is false |
| `adoptTwin` | its third-row probe finds the film and **silently skips the tmdb_id carry**, leaving the show unmatched for ever |

So `tmdb_tv_id`, nullable, NULL for every film — additive, and it backfills to the right answer with
no backfill. `store.tmdbColumn` is the single place a media type becomes a column name.

**Its index is the first thing the migration mechanism has grown that is not a column**, and it could
not have gone anywhere else. A column added by `ALTER TABLE` cannot carry `UNIQUE` in its
declaration, and `schema.sql` is execd *before* `migrate` runs — so an index declared there would be
created against a column that does not exist yet and fail with `no such column` on exactly the
databases the mechanism exists to serve. `addIndex` asks the database nothing, because
`CREATE UNIQUE INDEX IF NOT EXISTS` is already idempotent by construction. A UNIQUE index over a
NULLABLE column is the right instrument: SQLite treats NULLs as distinct, so every film coexists
under it without noticing, and only two rows claiming the same show are refused.

### The charge for one table: eighteen reads and writes that could assume one kind of thing

Reusing `movies` buys the download pipeline unchanged and charges for it in every query. The three
that would have shipped as silent damage:

1. **`MoviesMissingMetadata`.** A show's `tmdb_id` is NULL by construction, so `WHERE tmdb_id IS
   NULL` puts **every show on the matching pass's work list on every scan**. `handleScan` feeds that
   list to TMDB's `/search/movie` and writes the answer back with `SetTMDBMetadata`, which
   overwrites unconditionally by design — and for **Fargo, Watchmen, Hannibal, Westworld, Dune and
   Snowpiercer the lookup succeeds.** The show acquires a film's id, overview and poster, with no
   error and no log, and it re-fires every scan.
2. **`prune`.** `case outside` sits *before* `case recorded[key]`, so a TV row is not merely unfound
   — it is affirmatively deleted with the reason *"outside LIBRARY_MOVIES, so it can never be
   served"*, cascading its downloads. The first movie scan after the first TV import would empty the
   TV library.
3. **`ScannedMovie.MediaType` defaulting to `movie`.** Correct with one media type; a loaded gun with
   two, because `UpsertMovieByPath` **rewrites** `media_type` from that field on every pass. One
   construction site that forgot it would relabel a show as a film, and then (2) deletes it.

The media type is therefore a **required argument with no value meaning "both"** on every scoped
read, and a **required field** on every write. Making it required turned all four `LibraryByTMDBID`
call sites and nine construction sites into compile errors and loud test failures somebody had to
answer on purpose — which is the whole argument for a refusal over a default. The writes go the
other way: `tmdbIDWrite` is a `CASE` over the row's own `media_type`, so a caller cannot put a tv id
in the movie column by passing the wrong argument, because there is no argument to pass.

`MoviesOnDisk` is the deliberate exception: it grows `MediaType` on the **row** rather than a filter
on the query. That list is what a prune may *consider*, and a prune deletes on a positive finding —
so a row filtered out of it is a row that cannot be kept either. Every row present, the caller
deciding which root owns it, is what [D33](#d33--a-folder-with-no-film-in-it-is-not-a-movie-the-row-goes-the-folder-stays)'s
asymmetry needs.

### `LIBRARY_TV` has no default, and that asymmetry is the opt-in

`LIBRARY_MOVIES` points at the fixture so a fresh clone does something useful. `LIBRARY_TV` points at
nothing, and **empty means television is off** — no Shows tab, no TV rails, and the TV routes
answering 503 naming the variable. A default would point curator at a directory nobody asked it to
write to, and would turn television on for every existing install on the next image. It is the same
posture `QBIT_USER` and `JELLYFIN_URL` already have: unconfigured is a supported state, not a broken
one, and `config.TVConfigured()` is the one place to ask.

### Alternatives

**Separate `shows` and `episodes` tables.** Cleaner on paper and it removes the contamination sweep
entirely. It loses to `downloads.movie_id NOT NULL`, which forces either a shadow movies row or a
duplicate downloads pipeline — and to `adoptTwin`, which already implements the accumulation
semantics that would then have to be rewritten.

**Rebuilding `movies` to relax `tmdb_id UNIQUE`.** SQLite's twelve-step table rebuild, with the
foreign key from `downloads` to work around. It is possible and it is surgery on the one table that
holds the user's library, to avoid one nullable column.

**An `episode_count` column.** Only a scan could write it, and there is no scheduler by design — so
it would be stale from the moment an import landed until somebody pressed Scan. Given that
season-by-season tracking is deliberately out of scope, **a count that is silently wrong half the
time is worse than no count.** Episodes are files on disk; the scan is the source of truth.

**Making `media_type` optional on the reads, defaulting to `movie`.** It reads fine at every call
site and is wrong at one — the dispatch guard, where a colliding id refuses a TV grab with a sentence
about a film the user never asked about.

---

## D49 — A season narrows after the fetch, and a pack that contains the episode is kept below it

**Status:** decided and **built** · **Completes** [D48](#d48--television-is-additive-a-show-is-a-row-in-movies-and-the-second-library-root-is-opt-in),
which shipped the season control without the narrowing behind it ·
**Extends** [D11](#d11--rank-by-seeders-quality-is-a-filter-not-a-score)'s seeders-first order
rather than reversing it · **Same posture as** `tpbNameAllowsYear`, where narrowing happens only
where "ambiguous" can mean "keep"

A television search takes a season and an episode. Both narrow the merged result **after** the
indexers answer, not by being folded into what is asked.

### The season was never actually narrowing anything

Since T90 a season went into 1337x's keyword string and nowhere else. Nothing filtered a result, so
TPB — whose query is the bare title on purpose — answered "season 2" with all four seasons, and the
two backends answered different questions from one `Query`. That was invisible because the screen
only ever showed one merged list.

### Why the narrowing cannot move into TPB's query

Measured against the live apibay on 2026-08-20, and it is the same measurement `tpbCategories`
already rests on:

```
q=severance                 100 rows, packs and episodes
q=severance s02               8 rows — and the 727-seeder
                              "Severance - Season 2 - Mp4 x264 AC3 1080p" is NOT among them
q=severance season 2          4 rows, a different four
```

"S02" and "Season 2" are two spellings of one thing and apibay matches letters, so a season in the
query costs the best season pack the show has. The query stays bare and the rows are narrowed in the
aggregator, which is the only place both backends' answers are in hand at once.

### Four tiers, and only one is dropped

**Exact** is the episode asked for, or any release of the season asked for when no episode was
named. **Pack** is a season pack offered against a single-episode query. **Unstated** is a name that
claims no season at all. **Wrong** states a season or episode that is not the one asked for, and is
the only tier dropped — on the name's own evidence, never on its silence.

### Keeping the pack, and putting the tier above seeders

The clean alternative is to show only exact matches. It answers **"no releases found"** for an
episode that has a 727-seeder source sitting right there, because that pack outseeds every single
Severance episode apibay carries by roughly two to one. A pack is a real way to get the episode; it
is only the wrong thing to offer *first*.

Those same numbers are why the tier sorts above seeders — 727 against 381 — since seeders-first
buries the episode somebody explicitly asked for underneath the pack they did not. This does not
reverse D11: below the tier the order is untouched, and every query naming no season is `TierExact`
for everything, so a film search sorts exactly as it always did.

**Do not** move the tier below seeders to "rank by popularity". It looks reasonable in a diff and it
is wrong for the only question a manual picker is asking.
`TestRankPutsTheAskedForEpisodeAboveABetterSeededPack` fails when it is moved.

### An episode with no season is refused, not honoured

No release names itself `E05` — the convention is `S02E05` — so a bare episode is a query that
reliably finds nothing while reporting `ok:true, count:0`, which is the silent-empty failure
[D20](#d20--the-film-comes-from-tmdb-the-search-box-only-finds-it) and `NormaliseQuery` already
exist to prevent. `internal/api` answers 400 at the edge, which is the last place a caller can be
told; `indexer.Query` ignores the field below that, because a half-applied filter is worse than
neither.

### Deliberately not decided here

**The cache key.** It carries the episode because 1337x sends it, so walking ten episodes is ten
identical apibay fetches under ten keys. That is the pre-existing shape of the season key, it costs
one call to a source answering in milliseconds, and the fix — a key derived from the string each
indexer actually queries — is a refactor rather than a line.
