package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestSettingsRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// A database nobody has configured answers an empty map, never nil: the
	// caller ranges over it.
	got, err := s.Settings(ctx)
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a fresh database has settings: %v", got)
	}

	if err := s.SetSettings(ctx, map[string]string{
		"tmdb_api_key":   "enc.v1.AAAA",
		"vpn_required":   "false",
		"search_timeout": "45s",
	}, nil); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}

	got, err = s.Settings(ctx)
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if len(got) != 3 || got["search_timeout"] != "45s" || got["vpn_required"] != "false" {
		t.Fatalf("Settings = %v", got)
	}

	// A second write of one key updates rather than colliding on the primary
	// key, and leaves its neighbours alone.
	if err := s.SetSettings(ctx, map[string]string{"search_timeout": "60s"}, nil); err != nil {
		t.Fatalf("SetSettings again: %v", err)
	}
	got, _ = s.Settings(ctx)
	if got["search_timeout"] != "60s" {
		t.Errorf("search_timeout = %q, want 60s", got["search_timeout"])
	}
	if got["vpn_required"] != "false" {
		t.Errorf("an untouched setting changed: %v", got)
	}
}

// Clearing removes the row, so "present in the table" and "configured" stay the
// same fact.
func TestSettingsClear(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.SetSettings(ctx, map[string]string{"qbit_user": "nethmin", "qbit_pass": "enc.v1.AAAA"}, nil); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	if err := s.SetSettings(ctx, map[string]string{"qbit_user": "someone"}, []string{"qbit_pass"}); err != nil {
		t.Fatalf("SetSettings with clear: %v", err)
	}

	got, _ := s.Settings(ctx)
	if _, ok := got["qbit_pass"]; ok {
		t.Error("a cleared setting is still present")
	}
	if got["qbit_user"] != "someone" {
		t.Errorf("qbit_user = %q", got["qbit_user"])
	}

	// Clearing something that was never there is not an error: the caller is a
	// form, and a form that submits an already-empty field is normal.
	if err := s.SetSettings(ctx, nil, []string{"jellyfin_api_key"}); err != nil {
		t.Errorf("clearing an absent setting: %v", err)
	}
}

// The all-or-nothing property, checked by making the write fail half way: a
// value that is not a string cannot be bound, and nothing before it may
// survive. A form with eight changed fields either applies all eight or none.
func TestSettingsWriteIsOneTransaction(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.SetSettings(ctx, map[string]string{"qbit_user": "nethmin"}, nil); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}

	// A cancelled context fails the transaction wherever it has got to.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.SetSettings(cancelled, map[string]string{
		"qbit_user":    "overwritten",
		"jellyfin_url": "http://example",
	}, []string{"qbit_user"}); err == nil {
		t.Fatal("a cancelled write reported success")
	}

	got, _ := s.Settings(ctx)
	if got["qbit_user"] != "nethmin" {
		t.Errorf("qbit_user = %q: a failed write left something behind", got["qbit_user"])
	}
	if _, ok := got["jellyfin_url"]; ok {
		t.Error("a failed write committed one of its statements")
	}
}

func TestSettingsEmptyWriteIsANoOp(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetSettings(context.Background(), nil, nil); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
}

// The first migration, from the direction that matters: a database created
// before the column existed. CREATE TABLE IF NOT EXISTS does nothing to a table
// that is already there, so without migrate.go a fresh clone would have the
// column and every existing database would not — invisible here, and only
// visible on somebody else's machine.
func TestMigrationAddsTheReasonColumnToAnOlderDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "phase-6.db")

	// The phase 6 schema, verbatim in the part that matters: downloads without
	// a reason column.
	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `
		CREATE TABLE downloads (
		  id            INTEGER PRIMARY KEY,
		  movie_id      INTEGER NOT NULL,
		  torrent_hash  TEXT UNIQUE NOT NULL,
		  indexer       TEXT NOT NULL,
		  release_name  TEXT NOT NULL,
		  magnet        TEXT NOT NULL,
		  state         TEXT NOT NULL,
		  progress      REAL NOT NULL DEFAULT 0,
		  added_at      DATETIME NOT NULL,
		  completed_at  DATETIME
		);
	`); err != nil {
		t.Fatalf("create the old table: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if !hasColumn(t, s, "downloads", "reason") {
		t.Fatal("downloads.reason is missing after opening an older database")
	}
	// The column existing is not the claim worth making. T55 renders what this
	// database serves, so the assertion is a reason written and read back on a
	// row that predates the column — including the rows already in the table,
	// which ALTER TABLE filled with NULL.
	servesAReason(t, s)

	// And again, because every start applies it: the second run must change
	// nothing rather than fail on a duplicate column.
	again, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open twice: %v", err)
	}
	defer again.Close()
	if !hasColumn(t, again, "downloads", "reason") {
		t.Fatal("downloads.reason vanished on the second open")
	}
	servesAReason(t, again)
}

// The other direction: a database created today gets the column from
// schema.sql, and the migration finds nothing to do.
func TestAFreshDatabaseHasTheReasonColumn(t *testing.T) {
	s := newTestStore(t)
	if !hasColumn(t, s, "downloads", "reason") {
		t.Fatal("downloads.reason is missing from a fresh database")
	}
	servesAReason(t, s)
}

// servesAReason is the end both paths have to reach: a download whose stall
// reason can be written and read back. It is what makes the two migration tests
// a claim about behaviour rather than about a pragma.
//
// The hash is per-call because one of the callers opens the same file twice and
// torrent_hash is UNIQUE — the second store would otherwise find the first
// one's row and pass without writing anything.
func servesAReason(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()

	m, err := s.UpsertWantedMovie(ctx, "Interstellar", 2014, nil)
	if err != nil {
		t.Fatalf("UpsertWantedMovie: %v", err)
	}
	hash := fmt.Sprintf("%040d", len(t.Name())+int(m.ID))
	if _, err := s.InsertDownload(ctx, Download{
		MovieID: m.ID, TorrentHash: hash, Indexer: "1337x",
		ReleaseName: "Interstellar.2014.1080p.BluRay.x264-SPARKS",
		Magnet:      "magnet:?xt=urn:btih:" + hash,
	}); err != nil {
		t.Fatalf("InsertDownload: %v", err)
	}

	const why = "no peers are connected — nobody appears to be seeding this release"
	if err := s.UpdateDownloadProgress(ctx, hash, DownloadStalled, 0, why, nil); err != nil {
		t.Fatalf("UpdateDownloadProgress: %v", err)
	}
	got, err := s.GetDownloadByHash(ctx, hash)
	if err != nil {
		t.Fatalf("GetDownloadByHash: %v", err)
	}
	if got.Reason != why {
		t.Errorf("Reason = %q, want %q — the column is there but the row does not serve it", got.Reason, why)
	}
}

// The same two directions for T68's column, because the mechanism's promise is
// per-column and a second passenger is where "each step asks the database about
// its own shape" either holds or does not.
func TestMigrationAddsTheTMDBYearColumnToAnOlderDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "phase-9.db")

	// The phase 9 movies table: everything T67 shipped against, without
	// tmdb_year. The columns scanMovie reads have to be here or the failure
	// would be a bad SELECT rather than a missing migration.
	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `
		CREATE TABLE movies (
		  id           INTEGER PRIMARY KEY,
		  tmdb_id      INTEGER UNIQUE,
		  title        TEXT NOT NULL,
		  year         INTEGER NOT NULL,
		  media_type   TEXT NOT NULL DEFAULT 'movie',
		  overview     TEXT,
		  poster_path  TEXT,
		  status       TEXT NOT NULL,
		  library_path TEXT UNIQUE,
		  quality      TEXT,
		  size_bytes   INTEGER,
		  added_at     DATETIME NOT NULL,
		  imported_at  DATETIME
		);
		INSERT INTO movies (title, year, status, library_path, added_at)
		VALUES ('Some Home Video', 2019, 'imported', '/movies/Some Home Video (2019)', '2026-08-16T00:00:00Z');
	`); err != nil {
		t.Fatalf("create the old table: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if !hasColumn(t, s, "movies", "tmdb_year") {
		t.Fatal("movies.tmdb_year is missing after opening an older database")
	}
	// The row that predates the column reads back, which is the half a pragma
	// cannot tell you: ALTER TABLE filled it with NULL and MatchYear has to
	// answer the folder's year for it rather than 0.
	servesATMDBYear(t, s)

	// Every start applies it, so the second must change nothing rather than fail
	// on a duplicate column.
	again, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open twice: %v", err)
	}
	defer again.Close()
	if !hasColumn(t, again, "movies", "tmdb_year") {
		t.Fatal("movies.tmdb_year vanished on the second open")
	}
}

func TestAFreshDatabaseHasTheTMDBYearColumn(t *testing.T) {
	s := newTestStore(t)
	if !hasColumn(t, s, "movies", "tmdb_year") {
		t.Fatal("movies.tmdb_year is missing from a fresh database")
	}
	servesATMDBYear(t, s)
}

// servesATMDBYear is the end both paths reach: a pre-existing row reads back
// with a NULL tmdb_year that MatchYear resolves to the folder's year, and a
// hand-match writes one that sticks.
func servesATMDBYear(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()

	const path = "/movies/Some Home Video (2019)"
	m, _, err := s.UpsertMovieByPath(ctx, ScannedMovie{
		LibraryPath: path, Title: "Some Home Video", Year: 2019,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if m.TMDBYear != nil {
		t.Errorf("tmdb_year = %v on a row that predates the column, want NULL", *m.TMDBYear)
	}
	if m.MatchYear() != 2019 {
		t.Errorf("MatchYear() = %d, want the folder's 2019", m.MatchYear())
	}

	matched, err := s.MatchMovie(ctx, m.ID, TMDBMatch{TMDBID: 1726, Year: ptrInt(2008)})
	if err != nil {
		t.Fatalf("MatchMovie: %v", err)
	}
	if matched.TMDBYear == nil || *matched.TMDBYear != 2008 {
		t.Fatalf("tmdb_year = %v, want 2008 — the column is there but the row does not serve it", matched.TMDBYear)
	}
	if matched.MatchYear() != 2008 {
		t.Errorf("MatchYear() = %d, want TMDB's 2008", matched.MatchYear())
	}
}

// T88's column, in both directions again — and its index, which is the first
// thing the mechanism has grown that is not a column.
func TestMigrationAddsTheTMDBTVIDColumnAndIndexToAnOlderDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "phase-10.db")

	// The phase 10 movies table: everything 0.3.0 shipped with, tmdb_year
	// included and tmdb_tv_id not. A film is in it, because the migration has to
	// leave one alone.
	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `
		CREATE TABLE movies (
		  id           INTEGER PRIMARY KEY,
		  tmdb_id      INTEGER UNIQUE,
		  title        TEXT NOT NULL,
		  year         INTEGER NOT NULL,
		  tmdb_year    INTEGER,
		  media_type   TEXT NOT NULL DEFAULT 'movie',
		  overview     TEXT,
		  poster_path  TEXT,
		  status       TEXT NOT NULL,
		  library_path TEXT UNIQUE,
		  quality      TEXT,
		  size_bytes   INTEGER,
		  added_at     DATETIME NOT NULL,
		  imported_at  DATETIME
		);
		INSERT INTO movies (tmdb_id, title, year, status, library_path, added_at)
		VALUES (95396, 'Some Film', 2011, 'imported', '/movies/Some Film (2011)', '2026-08-16T00:00:00Z');
	`); err != nil {
		t.Fatalf("create the old table: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if !hasColumn(t, s, "movies", "tmdb_tv_id") {
		t.Fatal("movies.tmdb_tv_id is missing after opening an older database")
	}
	if !hasIndex(t, s, "movies_tmdb_tv_id") {
		t.Fatal("the movies_tmdb_tv_id index is missing after opening an older database")
	}
	// The row that predates the column reads back through scanMovie, which now
	// selects one more column than it did — the half a pragma cannot tell you.
	film, err := s.GetMovie(ctx, 1)
	if err != nil {
		t.Fatalf("GetMovie on the pre-existing row: %v", err)
	}
	if film.TMDBTVID != nil {
		t.Errorf("tmdb_tv_id = %v on a film that predates the column, want NULL", *film.TMDBTVID)
	}
	if film.TMDBID == nil || *film.TMDBID != 95396 {
		t.Errorf("tmdb_id = %v, want the film's 95396 left exactly where it was", film.TMDBID)
	}

	// Every start applies it, so the second must change nothing rather than fail
	// on a duplicate column or a duplicate index.
	again, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open twice: %v", err)
	}
	defer again.Close()
	if !hasColumn(t, again, "movies", "tmdb_tv_id") {
		t.Fatal("movies.tmdb_tv_id vanished on the second open")
	}
}

func TestAFreshDatabaseHasTheTMDBTVIDColumnAndIndex(t *testing.T) {
	s := newTestStore(t)
	if !hasColumn(t, s, "movies", "tmdb_tv_id") {
		t.Fatal("movies.tmdb_tv_id is missing from a fresh database")
	}
	if !hasIndex(t, s, "movies_tmdb_tv_id") {
		t.Fatal("the movies_tmdb_tv_id index is missing from a fresh database")
	}
}

// The two halves of what a UNIQUE index over a NULLABLE column buys, and the
// reason a show's TMDB id could not simply go in tmdb_id.
//
// SQLite treats NULLs as distinct, so every film in the library — all of which
// have tmdb_tv_id NULL — coexists under a UNIQUE index without noticing it. Only
// two rows claiming the SAME show are refused. And a film and a show may hold the
// same NUMBER, which is the whole point: Severance is tv id 95396 and some film
// holds movie id 95396, and both have to be in one library.
func TestTheTVIDIndexSeparatesTheTwoIDSpaces(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	insert := func(t *testing.T, title, mediaType, path string, movieID, tvID *int64) error {
		t.Helper()
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO movies (tmdb_id, tmdb_tv_id, title, year, media_type, status, library_path, added_at)
			VALUES (?, ?, ?, 2022, ?, 'imported', ?, '2026-08-20T00:00:00Z')`,
			movieID, tvID, title, mediaType, path)
		return err
	}

	// Two films with no tv id at all. NULLs are distinct, so the index is silent.
	if err := insert(t, "One Film", MediaTypeMovie, "/movies/One Film (2022)", ptrInt64(11), nil); err != nil {
		t.Fatalf("first film: %v", err)
	}
	if err := insert(t, "Another Film", MediaTypeMovie, "/movies/Another Film (2022)", ptrInt64(12), nil); err != nil {
		t.Fatalf("a second film with a NULL tmdb_tv_id was refused: %v", err)
	}

	// The collision that made two columns necessary: the film holds movie id
	// 95396 and the show holds tv id 95396. One column could not have both.
	if err := insert(t, "Coincidence", MediaTypeMovie, "/movies/Coincidence (2022)", ptrInt64(95396), nil); err != nil {
		t.Fatalf("the film half of the collision: %v", err)
	}
	if err := insert(t, "Severance", MediaTypeTV, "/tv/Severance (2022)", nil, ptrInt64(95396)); err != nil {
		t.Fatalf("a show whose tv id equals a film's movie id was refused: %v — "+
			"this is exactly what tmdb_tv_id exists to allow", err)
	}

	// And the thing the index is actually for.
	if err := insert(t, "Severance Again", MediaTypeTV, "/tv/Severance Again (2022)", nil, ptrInt64(95396)); err == nil {
		t.Fatal("two rows claimed the same show and the index allowed it")
	}
}

func hasIndex(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var found int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name,
	).Scan(&found); err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	return found > 0
}

func hasColumn(t *testing.T, s *Store, table, column string) bool {
	t.Helper()
	var found int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, column,
	).Scan(&found); err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	return found > 0
}
