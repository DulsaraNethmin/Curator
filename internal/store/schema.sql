-- The schema of docs/phase-1.md, verbatim. Every statement is IF NOT EXISTS so
-- applying it on every startup is a no-op after the first.
--
-- downloads and settings are created now although phase 1 only writes movies,
-- so phase 3 needs no migration.

CREATE TABLE IF NOT EXISTS movies (
  id           INTEGER PRIMARY KEY,
  tmdb_id      INTEGER UNIQUE,              -- nullable: see decisions.md D6
  title        TEXT NOT NULL,
  year         INTEGER NOT NULL,             -- the FOLDER's year: see store.Movie
  tmdb_year    INTEGER,                      -- TMDB's, only when it differs (T68)
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
  state         TEXT NOT NULL,              -- queued | downloading | stalled | completed | imported | failed
  progress      REAL NOT NULL DEFAULT 0,
  added_at      DATETIME NOT NULL,
  completed_at  DATETIME,
  -- Why a torrent is stalled, from the backend that knows (T55). Nullable and
  -- unindexed: it is a sentence about the current state, read with the row.
  -- A database created before phase 7 gets it from migrate.go instead, because
  -- CREATE TABLE IF NOT EXISTS does nothing to a table that already exists.
  reason        TEXT
);

CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
