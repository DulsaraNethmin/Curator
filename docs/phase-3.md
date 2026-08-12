# Phase 3 — Downloads

Turn a picked release into a running torrent, and keep the database honest about what it is doing.

**Done when** `POST /api/downloads` puts a torrent into qBittorrent tagged as ours, `GET /api/downloads`
shows it progressing, progress survives a restart of curator, and a qBittorrent that is down fails the
dispatch loudly rather than recording a download that does not exist.

---

## Tasks

| Task | Owns | Depends on |
|---|---|---|
| [T13](tasks/T13-qbit-client.md) qBittorrent client | `internal/qbit/` | — |
| [T14](tasks/T14-downloads-store.md) downloads store | `internal/store/downloads.go` | — |
| [T15](tasks/T15-download-poller.md) poller | `internal/download/` | T13, T14 |
| [T16](tasks/T16-downloads-api.md) API | `internal/api/downloads.go`, config, wiring | T15 |

T13 and T14 are independent and own one directory each — run them in parallel. T15 joins them, T16
exposes them.

Unlike phase 2, ownership here is per **directory** again, except `internal/store/downloads.go` and
`internal/api/downloads.go`, which are new files in packages phase 1 owns. Do not edit
`internal/store/store.go`, `movies.go` or `schema.sql`.

---

## What is already there

The `downloads` table was created in [T2](tasks/T2-store.md) and has never been written to:

```sql
CREATE TABLE IF NOT EXISTS downloads (
  id            INTEGER PRIMARY KEY,
  movie_id      INTEGER NOT NULL REFERENCES movies(id),
  torrent_hash  TEXT UNIQUE NOT NULL,
  indexer       TEXT NOT NULL,
  release_name  TEXT NOT NULL,
  magnet        TEXT NOT NULL,
  state         TEXT NOT NULL,   -- queued | downloading | completed | imported | failed
  progress      REAL NOT NULL DEFAULT 0,
  added_at      DATETIME NOT NULL,
  completed_at  DATETIME
);
```

**No migration.** Phase 1 created it precisely so this phase would not need one.

`movie_id` is `NOT NULL`, which is a design statement, not an oversight: a download exists *for* a
movie. But the movie you are downloading is usually **not in the library yet** — that is why you are
downloading it — so it has no `library_path`. A `movies` row is created first with `status = 'wanted'`
and `library_path = NULL`. SQLite permits many NULLs in a `UNIQUE` column, so any number of wanted
movies coexist.

Phase 2 ends at a magnet: `GET /api/releases/{id}/magnet` resolves a picked release. Phase 3 begins
there.

---

## qBittorrent, as it actually is on the Pi

Measured 2026-08-12, not assumed:

| | |
|---|---|
| Version | **5.1.2** (`lscr.io/linuxserver/qbittorrent:5.1.2`), so Web API v2 |
| Reachable at | `http://gluetun:8080` in Docker — it shares gluetun's network namespace and has **no ports of its own** |
| From a laptop | `http://192.168.1.26:8080` |
| Auth | **Required.** Every endpoint returns `403 Forbidden` without a session. Username is `nethmin` |
| Downloads mount | host `/media/storage/media/downloads` → container `/downloads` |
| Existing categories | `radarr` → `/downloads/complete/movies`, `sonarr` → `/downloads/complete/tv` |

Two consequences worth stating before anyone is surprised by them.

**Authentication is a session, not a header.** `POST /api/v2/auth/login` with `username` and `password`
as form values returns an `SID` cookie, and every later call carries it. The cookie expires, and an
expired one looks exactly like bad credentials: `403`. So the client re-authenticates once on a `403`
and retries; a second `403` is a real failure. Do not log in before every call — that is a round trip
per request for a session that lasts an hour.

**The paths qBittorrent reports are in its own namespace.** It will say a torrent's content is at
`/downloads/complete/curator/Movie.2014.1080p/`, and that path does not exist on the machine curator
runs on unless curator mounts it identically. Phase 4 hardlinks from there, so phase 3 **stores what
qBittorrent said, verbatim, and translates nothing**. Inventing a translation here would bury an
assumption in the wrong phase.

---

## Category, not just a tag

Torrents are added with the category **`curator`**, whose save path is `/downloads/complete/curator`.

[`architecture.md`](architecture.md) says "add magnet, tag `curator`". A category is what that
sentence actually needs: a tag is only a label, while a category *also* sets the save path, and
`torrents/info?category=curator` scopes every later poll to our own torrents. Phase 4's promise that
the importer "never touches torrents added by hand" is that filter. The tag is applied as well —
`tags=curator` costs nothing on the same request and makes the ownership visible in the UI.

The save path is **not** `/downloads/complete/movies`, which is radarr's. Phase 6 runs both stacks
side by side on purpose, and two importers writing the same directory is how a duplicate download
quietly overwrites a good file. A separate directory is on the same filesystem, so
[D8](decisions.md#d8--import-by-hardlink)'s hardlinks still work.

See [D13](decisions.md#d13--downloads-are-scoped-by-a-qbittorrent-category-with-its-own-save-path).

---

## Dispatch, and what must not happen

`POST /api/downloads` takes a release id from a live search, resolves its magnet through phase 2's
aggregator, and adds it.

The order matters, and it is the opposite of the obvious one:

1. Resolve the magnet — a `410` here is the search having expired, and nothing has been written.
2. Add to qBittorrent, and **confirm the torrent is actually there** before writing a row.
3. Only then insert the `downloads` row.

Writing the row first and dispatching second would leave a database claiming a download that no
client has ever heard of, every time qBittorrent is down — and the poller would then report it as
missing forever. **A dispatch that cannot reach qBittorrent is a `502`, not a recorded download.**

`torrents/add` is the trap: it answers **`200 Ok.`** for a magnet it has accepted *and* for one it has
silently ignored, and it does not return the hash. The hash comes from the magnet itself
(`indexer.InfoHash`, already ported), and the add is confirmed by looking the torrent up by hash
afterwards. A `200` from this endpoint means "request received", not "torrent added" — the same class
of lie as minter's 200-carrying-a-failure.

Adding a magnet already in qBittorrent is **not** an error: it is idempotent, and re-dispatching a
release after a restart should converge rather than fail.

---

## Polling

A poller reconciles qBittorrent into the `downloads` table on an interval.

- One request per tick: `torrents/info?category=curator` returns every torrent we own, so the cost is
  one round trip regardless of how many downloads are in flight.
- It updates `state`, `progress` and `completed_at`. It does **not** create rows: a torrent in the
  category with no row is reported, not adopted — it is either a leftover from a wiped database or
  someone else using our category, and guessing which is worse than saying so.
- It never deletes from qBittorrent. Seeding and cleanup stay qBittorrent's business under its own
  rules, exactly as [D8](decisions.md#d8--import-by-hardlink) says for files.
- It is owned by the process that starts it and stops on shutdown. A poller goroutine that outlives
  its owner is the leak [T10](tasks/T10-search-cache.md) refused to create for the cache.

`imported` is set by phase 4, never by the poller — a completed torrent is not an imported movie, and
overwriting that distinction here would leave phase 4 nothing to do.

### State mapping

qBittorrent has nineteen states and the `downloads` table has five. The mapping is explicit, and
anything unrecognised becomes `downloading` rather than `failed`, because a new qBittorrent state is
far more likely than a genuine error:

| qBittorrent | ours |
|---|---|
| `queuedDL`, `stalledDL`, `metaDL`, `allocating`, `checkingDL`, `checkingResumeData`, `pausedDL`, `stoppedDL` | `queued` |
| `downloading`, `forcedDL`, `moving` | `downloading` |
| `uploading`, `stalledUP`, `queuedUP`, `forcedUP`, `checkingUP`, `pausedUP`, `stoppedUP` | `completed` |
| `error`, `missingFiles` | `failed` |

`pausedUP` is **completed**, not paused-and-therefore-stuck: a torrent that has finished downloading
and been paused has the file we wanted. `pausedDL` is a partial download and stays `queued`.

**`stoppedUP` and `stoppedDL` are the spellings that actually arrive.** qBittorrent 5.0 renamed
pause/resume to stop/start and the Web API states followed, so the Pi's 5.1.2 sends `stopped*` where
4.x sent `paused*`. This was missed when the table was first written, and it is not cosmetic: without
`stoppedUP`, a finished-and-stopped torrent falls through to the default and reads `downloading`
for ever, so phase 4 would never see a completed download to import. Both spellings are mapped —
the `paused*` pair costs nothing and keeps a 4.x instance working.

---

## API surface

| Endpoint | Behaviour |
|---|---|
| `POST /api/downloads` | Body `{"release_id": "...", "title": "...", "year": 2014, "tmdb_id": 157336}`. Dispatches and records. `201` with the row |
| `GET /api/downloads` | Every download with its current state and progress |

`title` and `year` identify the movie the release is for, because a release id says nothing about
which film it is — the client picked it from a search it made, and it knows. `tmdb_id` is optional;
without it the movie row is recorded unmatched, exactly as [D6](decisions.md#d6--tmdb_id-is-nullable)
allows for a folder that TMDB could not match.

Status codes carry the same honesty as phase 2's:

- an expired or unknown `release_id` → **`410`**, forwarding phase 2's sentinel
- qBittorrent unreachable, or the add unconfirmed → **`502`**, and **no row is written**
- a blank `title`, or a `year` that is not a number → `400`
- re-dispatching a release already downloading → `200` with the existing row, not a duplicate

---

## Configuration

Added to phase 2's table:

| Variable | Default | Purpose |
|---|---|---|
| `QBIT_URL` | `http://127.0.0.1:8080` | qBittorrent Web API. `http://gluetun:8080` in Docker |
| `QBIT_USER` | — | Web UI username. Empty disables downloads rather than failing startup |
| `QBIT_PASS` | — | Web UI password |
| `QBIT_CATEGORY` | `curator` | Category and tag applied to everything we add |
| `DOWNLOAD_POLL_INTERVAL` | `10s` | How often qBittorrent is reconciled into the table |

An unset `QBIT_USER` is **not a startup error**, matching how an unset `TMDB_API_KEY` is handled: the
library still scans and search still works, and only dispatch reports that it is unconfigured. A
service that refuses to start because one of its five integrations is unconfigured is worse at being
partially useful.

---

## Verification

```bash
go build ./... && go vet ./... && go test -race ./...
GOOS=linux GOARCH=arm64 go build ./...

QBIT_URL=http://192.168.1.26:8080 QBIT_USER=... QBIT_PASS=... go run ./cmd/curator &

# search, pick, dispatch
id=$(curl -s 'localhost:8090/api/search?title=Interstellar&year=2014' | jq -r '.releases[0].id')
curl -s -X POST localhost:8090/api/downloads -d "{\"release_id\":\"$id\",\"title\":\"Interstellar\",\"year\":2014}" | jq

# it is really in qBittorrent, under our category and nobody else's
curl -s "$QBIT_URL/api/v2/torrents/info?category=curator" -b /tmp/qbit.cookie | jq '.[].name'

# progress moves, and survives a restart of curator
curl -s localhost:8090/api/downloads | jq '.[] | {release_name, state, progress}'

# qBittorrent down is a 502 and writes nothing
QBIT_URL=http://127.0.0.1:9 go run ./cmd/curator &
curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8090/api/downloads -d '{...}'   # 502
curl -s localhost:8090/api/downloads | jq 'length'                                          # unchanged
```

**Do not verify against the Pi's radarr category, and do not delete anything from qBittorrent.** The
*arr stack keeps serving until phase 6. Everything this phase adds is scoped to `curator`, which is
what makes it safe to run alongside.

---

## Out of scope

The import itself — hardlinking, renaming, Jellyfin — is phase 4, and `imported` is its state to set.
No automatic grabbing, no watchlist, no retry-on-failure ([D5](decisions.md#d5--manual-search-not-automatic-grabbing));
a failed download is reported and a human picks another release. No torrent removal, no seeding
policy, no bandwidth management: qBittorrent already does those and does them better.
