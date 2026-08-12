# Phase 1 — Foundation

Build the skeleton, the database, TMDB matching and the library scanner. Nothing in this phase
touches the network beyond TMDB, and nothing depends on the Pi's stack.

**Done when** `GET /api/movies` returns all 29 movies scanned off disk with metadata attached.

---

## Tasks

| Task | Owns | Depends on |
|---|---|---|
| [T1](tasks/T1-scaffold.md) scaffold | `cmd/curator/`, `internal/config/` | — |
| [T7](tasks/T7-test-fixture.md) fixture | `testdata/library/` | — *(done)* |
| [T2](tasks/T2-store.md) store | `internal/store/` | T1 |
| [T3](tasks/T3-library-scanner.md) scanner | `internal/library/` | T1, T7 |
| [T4](tasks/T4-tmdb-client.md) TMDB | `internal/tmdb/` | T1 |
| [T6](tasks/T6-absorb-cfprobe.md) absorb cfprobe | `internal/indexer/` | T1 |
| [T5](tasks/T5-api.md) API | `internal/api/` | T2, T3, T4 |

T1 first. Then T2, T3, T4 and T6 in parallel — they own separate packages and do not collide.
T5 integrates them.

---

## Schema

```sql
CREATE TABLE IF NOT EXISTS movies (
  id           INTEGER PRIMARY KEY,
  tmdb_id      INTEGER UNIQUE,              -- nullable: see decisions.md D6
  title        TEXT NOT NULL,
  year         INTEGER NOT NULL,
  media_type   TEXT NOT NULL DEFAULT 'movie',
  overview     TEXT,
  poster_path  TEXT,
  status       TEXT NOT NULL,               -- wanted | downloading | imported
  library_path TEXT UNIQUE,                 -- the scanner's identity key
  quality      TEXT,
  size_bytes   INTEGER,
  added_at     DATETIME NOT NULL,
  imported_at  DATETIME
);

CREATE TABLE IF NOT EXISTS downloads (
  id            INTEGER PRIMARY KEY,
  movie_id      INTEGER NOT NULL REFERENCES movies(id),
  torrent_hash  TEXT UNIQUE NOT NULL,
  indexer       TEXT NOT NULL,
  release_name  TEXT NOT NULL,
  magnet        TEXT NOT NULL,
  state         TEXT NOT NULL,              -- queued | downloading | completed | imported | failed
  progress      REAL NOT NULL DEFAULT 0,
  added_at      DATETIME NOT NULL,
  completed_at  DATETIME
);

CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
```

`downloads` and `settings` are created now although phase 1 only writes `movies`, so phase 3 needs no
migration.

**`library_path` is the identity key for scanning**, not `tmdb_id`. A rescan must be idempotent, and
a folder is identified by where it is, not by whether TMDB recognised it.

---

## API surface

| Endpoint | Behaviour |
|---|---|
| `GET /healthz` | `{"ok": true, "version": "..."}` — cheap, no I/O |
| `POST /api/scan` | Walk the library, upsert rows, match unmatched against TMDB. Returns `{scanned, added, matched, unmatched}` |
| `GET /api/movies` | All movies, newest first |
| `GET /api/movies/{id}` | One movie, 404 if absent |

Scanning is synchronous in this phase — 29 folders and a handful of TMDB calls. Making it a
background job is a later concern, and only if it turns out to be needed.

---

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `PORT` | `8090` | Listen port |
| `DB_PATH` | `./curator.db` | SQLite file |
| `LIBRARY_MOVIES` | `./testdata/library/movies` | Library root to scan |
| `TMDB_API_KEY` | — | Required for matching; scanning works without it |
| `LOG_LEVEL` | `info` | |

Defaulting `LIBRARY_MOVIES` to the fixture means `go run ./cmd/curator` does something useful
immediately, without a config file or an SSH mount.

---

## Verification

```bash
go build ./... && go test ./...
GOOS=linux GOARCH=arm64 go build ./...          # must pass — it is how this ships

go run ./cmd/curator &
curl -s localhost:8090/healthz | jq
curl -s -X POST localhost:8090/api/scan | jq
curl -s localhost:8090/api/movies | jq 'length'          # 29

# the colon-substituted titles matched real TMDB entries
curl -s localhost:8090/api/movies \
  | jq -r '.[] | select(.title | test(" - ")) | "\(.title) → tmdb=\(.tmdb_id)"'

# nothing silently unmatched
curl -s localhost:8090/api/movies | jq '[.[] | select(.tmdb_id == null) | .title]'

# rescan is idempotent — still 29, no duplicates
curl -s -X POST localhost:8090/api/scan | jq '.added'    # 0 on a second run

# and the fixture agrees with reality
ssh pi 'ls /media/storage/media/movies | wc -l'          # 29
```

---

## Out of scope for phase 1

Indexer wiring (T6 absorbs the code but leaves it unwired), qBittorrent, the import pipeline, the UI,
and anything that changes the Pi.
