# Phase 5 — Interface

The screens that make the previous four phases usable by someone who is not holding a terminal.

**Done when** the whole flow — search a title, pick a release, watch it download, see it land in the
library — is drivable from a browser with **no hand-written API calls**, the UI ships inside the
binary, and `GOOS=linux GOARCH=arm64 go build ./...` still passes.

---

## Tasks

| Task | Owns | Depends on |
|---|---|---|
| [T22](tasks/T22-embed-ui.md) embed and serve | `internal/web/`, the mount in `cmd/curator` | — |
| [T23](tasks/T23-settings-api.md) settings endpoint | `internal/api/settings.go` | — |
| [T24](tasks/T24-ui-shell.md) app shell and build wiring | `web/` scaffold, API client, nav | T22 |
| [T25](tasks/T25-search-screens.md) Search → Releases | `web/app/(search\|releases)` | T24 |
| [T26](tasks/T26-library-screens.md) Library, Activity, Settings | `web/app/(library\|activity\|settings)` | T24, T23 |

T22 and T23 are independent Go tasks and can run in parallel. T24 is the only one that may create
`web/`; T25 and T26 add screens to it and own disjoint route directories.

---

## What must not change

- **No handler in `internal/api` changes its shape.** Phases 1–4 verified those responses against
  live services, and a UI is a consumer, not a reason to rewrite a contract. T23 adds a new endpoint;
  it edits none.
- **No new state in Go.** The UI holds its own view state. Nothing here adds a session, a cookie, or
  a server-side notion of "the current search".
- **The `settings` table stays unused** ([D17](decisions.md#d17--settings-is-read-only-and-the-settings-table-stays-unused)).
- **No authentication**, and therefore **no secret leaves the process** — same decision, other half.
- **`go build ./...` alone still works** on a fresh clone, produces a complete API, and serves a
  placeholder page rather than failing to compile
  ([D16](decisions.md#d16--the-ui-is-embedded-with-all-and-a-committed-placeholder-keeps-go-build-honest)).

---

## The traps, named before anyone hits them

**`//go:embed all:dist`.** `go:embed` silently excludes anything beginning with `_`, and Next.js puts
every script and stylesheet under `_next/`. Omit `all:` and you get a compiling binary, a serving
`index.html`, and a blank page whose assets all 404 with nothing wrong in the Go. T22 asserts
`_next/` is present in the embedded filesystem.

**The export directory must live inside the embedding package.** `go:embed` patterns cannot reach
outside their own directory (`..` is rejected), so Next writes to **`internal/web/dist/`** rather
than the conventional `web/out/`. `.gitignore` already carried `/web/out/` from phase 1; it now
needs the real path instead.

**`output: 'export'` disables half of Next.** No server components doing I/O, no `next/image`
optimisation, no middleware, no route handlers, no ISR, and — the one that bites during development —
**no `rewrites`**, so the usual "proxy /api to the backend in dev" trick is unavailable. The API base
is therefore an explicit build-time value: empty in the embedded build, which makes every call
same-origin, and `http://localhost:8090` under `next dev`.

**`trailingSlash: true`.** It makes the export write `dist/search/index.html` rather than
`dist/search.html`, which is the layout `http.FileServer` resolves without special cases. Without it
every route needs a rewrite rule in Go, which is the sort of thing that works for four routes and
breaks on the fifth.

**A folder does not imply a file, and 15 of 29 are empty.** The Library screen renders real data:
`size_bytes` is 0 for those, and `tmdb_id`, `overview` and `poster_path` are all nullable
([D6](decisions.md#d6--tmdb_id-is-nullable)). A UI that assumes a poster exists renders 15 broken
images on the first screen anyone sees.

---

## The screens

Five, matching the four phases plus a status page. Every one of them is a view over an endpoint that
already exists, except Settings.

| Screen | Reads | Writes |
|---|---|---|
| **Search** | `GET /api/search?title=&year=&quality=` | — |
| **Releases** | the same response, ranked | `POST /api/downloads` |
| **Library** | `GET /api/movies` | `POST /api/scan` |
| **Activity** | `GET /api/downloads` | `POST /api/downloads/{hash}/import` |
| **Settings** | `GET /api/settings` *(new)* | — |

### Search and Releases are one flow, not two pages of data

A release id is only valid for as long as the search that issued it is cached — an hour
([D10](decisions.md#d10--releases-are-identified-by-an-opaque-id-not-a-url)) — and resolving an
expired one is a `410`. So the Releases view is rendered from the search response already in hand,
and a `410` on dispatch is surfaced as "this search has aged out, search again" rather than as a
generic failure. It is the one error in the system whose correct fix is a user action.

A search takes **up to 13 seconds** the first time, because a real browser is clearing a Cloudflare
challenge behind minter, and **under a second** on a repeat within the hour. The UI must show that it
is working for thirteen seconds without looking broken, and must render partial results: the
`indexers` array reports per-source `ok` and `error`, and a search with 1337x down is a **success**
carrying 57 releases and one failure, not an error page.

### Activity is a poll, not a push

The download poller already reconciles qBittorrent into the database every `DOWNLOAD_POLL_INTERVAL`.
The screen re-fetches `GET /api/downloads` on a timer of its own; there is no websocket, no SSE, and
no server-side change feed, because the data is already only as fresh as a ten-second poll and a
second transport would add a moving part to make it no fresher.

The manual import button exists for the case [phase 4](phase-4.md) built it for: a row that reads
`completed` and has not become `imported`, where waiting a poll interval to see whether a fix worked
is worse than asking. It is shown only for rows in that state.

### Settings is read-only

It answers "is this thing configured, and can curator reach it" for TMDB, qBittorrent, minter and
Jellyfin, plus the resolved paths and intervals. **It never carries a secret**, not even masked —
see [D17](decisions.md#d17--settings-is-read-only-and-the-settings-table-stays-unused). Every screen's
real question is "can I press this button", and `configured: true|false` answers it.

---

## The build

```bash
npm --prefix web install          # once
npm --prefix web run build        # writes internal/web/dist/
go build ./...                    # embeds it
```

Shipping to the Pi is now **two commands**, which is a real regression against phase 1's one and is
accepted in exactly one place — [D16](decisions.md#d16--the-ui-is-embedded-with-all-and-a-committed-placeholder-keeps-go-build-honest)
says why, and why committing the bundle would be worse.

For development, the two halves run separately and the UI is told where the API is:

```bash
go run ./cmd/curator &                                  # :8090
NEXT_PUBLIC_API_BASE=http://localhost:8090 npm --prefix web run dev   # :3000
```

---

## Verification

```bash
go build ./... && go vet ./... && go test -race ./...
GOOS=linux GOARCH=arm64 go build ./...

# the placeholder path: a clone that has never run npm still builds and serves
git stash -u && go build ./... && go run ./cmd/curator   # placeholder page, full API

npm --prefix web install && npm --prefix web run build
go run ./cmd/curator                                     # http://localhost:8090
```

Driven in a browser, with the *arr stack untouched:

- a search for a real title returns ranked releases, and the slow first search **looks** like work in
  progress rather than a hang
- an indexer being down shows the other two's results **and says which one failed**
- picking a release dispatches it, and it appears in Activity
- Activity's rows move on their own, without a reload
- the Library shows 29 folders with 15 of them size-zero and posterless, and **nothing is broken**
- Settings reports qBittorrent as **not configured**, which is the truth today
- **every screen is reachable by typing its URL directly**, not only by clicking — that is what
  `trailingSlash` and the embed's directory resolution are for
- `_next/` assets return **200**, which is the whole of the `all:` trap

---

## Out of scope

- **Authentication.** LAN-only, as the roadmap says, and D17 depends on it staying that way.
- **Editing settings**, and therefore any use of the `settings` table.
- **Websockets or SSE.** The data is a ten-second poll at source.
- **TV**, still. `media_type` carries it; no screen offers it.
- **Mobile-native anything.** This is a LAN web UI embedded in a Go binary.
- **The cutover.** Phase 6, and the *arr config backup comes first.

---

# The TMDB-first redesign — T27–T31

Added 2026-08-13, after the first real download exposed the design fault the original Search screen
had: it learned what a film was called from whatever was typed.

**Done when** you search a title, pick a film from a grid of posters, land on a page with its
backdrop and details, press **Find releases**, and dispatch — with the title, year and `tmdb_id` all
coming from TMDB rather than from a text box.

## Why

Searching `avengers` with no year recorded `title='avengers', year=0` and produced three failures
from one row: an import that failed on every tick for ever, a poster showing **Avengers: Doomsday
(2026)** because a yearless TMDB query guesses, and a folder named after the search box. Requiring a
year was a stopgap; making the movie the primary object is the fix.

See [D20](decisions.md#d20--the-film-comes-from-tmdb-the-search-box-only-finds-it) — including the
measured table showing that a canonical title with a colon **silently loses 1337x** — and
[D21](decisions.md#d21--the-movie-page-is-movieid--because-the-ui-is-a-static-export) for why the
movie page is a query-string route.

## Tasks

| Task | Owns | Depends on |
|---|---|---|
| [T27](tasks/T27-tmdb-browse.md) TMDB browsing | `internal/tmdb/` — `get`, `Details`, four methods | — |
| [T28](tasks/T28-query-title.md) query normalisation | `internal/indexer/query.go`, one line of `aggregate.go` | — |
| [T29](tasks/T29-library-by-tmdb.md) library index | `internal/store/movies.go` | — |
| [T30](tasks/T30-browse-api.md) browse endpoints | `internal/api/browse.go`, wiring | T27, T29 |
| [T31](tasks/T31-browse-ui.md) the screens | `web/` — Discover, cards, movie page | T30 |

T27, T28 and T29 are independent and can land in any order.

## The shape

| Route | Answers |
|---|---|
| `GET /api/tmdb/discover` | `{rows:[{id,title,ok,error,results:[card]}]}` — trending and popular |
| `GET /api/tmdb/search?query=&year=` | `{query,year,results:[card]}` |
| `GET /api/tmdb/movies/{id}` | a card plus tagline, runtime, genres, status, studios, languages |

Everything TMDB-backed lives under `/api/tmdb/`, and the prefix is the rule: **if it is under
`/api/tmdb/`, it goes dark without a key**. `/api/movies` stays the library and cannot be confused
with it. `GET /api/search` is untouched — it is the fallback.

Every card carries `library: {movie_id, state, library_path} | null`, where `state` is one of
`imported`, `downloading`, `wanted`. That is the green check on a poster.

Screens: `/` becomes Discover (trending and popular rails), `/search/?q=` returns movie cards with a
toggle back to release-name search, and `/movie/?id=` is the detail page. Releases load **only when
asked**, so browsing Discover costs no indexer traffic and launches no browser.

## The traps

- **A colon loses 1337x.** `NormaliseQuery` strips it, in the aggregator, above the cache. The
  canonical title with its colon is still what gets stored and what becomes the folder — the two must
  not be conflated (D20).
- **`store.StatusDownloading` is declared and never written.** `UpsertWantedMovie` inserts `wanted`
  and the importer writes `imported`, so a film at 60% would be labelled "wanted" on a card. The
  library index derives `downloading` from an `EXISTS` over `downloads`, not from `movies.status`.
- **`scrubURL` is not enforced by anything.** Four new TMDB endpoints are four new chances to leak
  `api_key=` into a log line. Every one goes through a single `get`, and the leak test is
  table-driven over every exported method so a fifth cannot skip it.
- **`useSearchParams()` without `<Suspense>` fails the build**, which is the good outcome (D21).
- **A film with no release date has year 0** and cannot become a folder. Download is disabled with
  that as the reason — a rare true sentence rather than "you forgot to type a year".
