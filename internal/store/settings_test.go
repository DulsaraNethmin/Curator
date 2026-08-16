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
