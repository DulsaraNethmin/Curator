-- The schema of docs/phase-1.md, verbatim. Every statement is IF NOT EXISTS so
-- applying it on every startup is a no-op after the first.
--
-- downloads and settings are created now although phase 1 only writes movies,
-- so phase 3 needs no migration.

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
  state         TEXT NOT NULL,              -- queued | downloading | stalled | completed | imported | failed
  progress      REAL NOT NULL DEFAULT 0,
  added_at      DATETIME NOT NULL,
  completed_at  DATETIME
);

CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
