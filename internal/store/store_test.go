package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Every test runs against a real temp-file database rather than ":memory:",
// because the file is what production uses and WAL does not exist in memory.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "curator.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func ptrString(s string) *string { return &s }
func ptrInt64(n int64) *int64    { return &n }

// scanned is a plausible library folder, so tests only spell out what they care
// about. The title carries the ' - ' colon substitution from CLAUDE.md.
func scanned(path string) ScannedMovie {
	return ScannedMovie{
		LibraryPath: path,
		Title:       "Avengers - Infinity War",
		Year:        2018,
		Quality:     ptrString("1080p"),
		SizeBytes:   ptrInt64(8_500_000_000),
	}
}

func TestOpenAppliesSchemaTwice(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "curator.db")

	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, err := first.UpsertMovieByPath(ctx, scanned("/movies/Avengers - Infinity War (2018)")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The second Open re-runs the whole schema over a populated database. It must
	// neither error nor drop the row.
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer second.Close()

	movies, err := second.ListMovies(ctx)
	if err != nil {
		t.Fatalf("ListMovies: %v", err)
	}
	if len(movies) != 1 {
		t.Fatalf("after re-applying the schema: got %d movies, want 1", len(movies))
	}
}

func TestOpenCreatesAllThreeTables(t *testing.T) {
	s := newTestStore(t)

	// Phase 1 writes only movies; downloads and settings exist so phase 3 needs
	// no migration.
	for _, table := range []string{"movies", "downloads", "settings"} {
		var name string
		err := s.db.QueryRowContext(context.Background(),
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
}

// foreign_keys is off by default in SQLite and is a *per-connection* setting, so
// this also guards against the pragma being applied to one pooled connection and
// not the next.
func TestPragmasApplyToEveryConnection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	s.db.SetMaxIdleConns(0) // force a fresh connection for each statement below

	for i := 0; i < 3; i++ {
		var foreignKeys int
		if err := s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
			t.Fatalf("PRAGMA foreign_keys: %v", err)
		}
		if foreignKeys != 1 {
			t.Errorf("connection %d: foreign_keys = %d, want 1", i, foreignKeys)
		}

		var journalMode string
		if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
			t.Fatalf("PRAGMA journal_mode: %v", err)
		}
		if journalMode != "wal" {
			t.Errorf("connection %d: journal_mode = %q, want \"wal\"", i, journalMode)
		}
	}

	// With the pragma on, the downloads → movies reference is enforced rather
	// than decorative.
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO downloads (movie_id, torrent_hash, indexer, release_name, magnet, state, added_at)
		VALUES (404, 'deadbeef', '1337x', 'Some.Release.1080p', 'magnet:?xt=x', 'queued', '2026-08-12T00:00:00.000000000Z')`)
	if err == nil {
		t.Error("insert referencing a nonexistent movie succeeded; foreign keys are not enforced")
	}
}

func TestUpsertMovieByPathIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const path = "/movies/Spider-Man - No Way Home (2021)"

	first := ScannedMovie{
		LibraryPath: path,
		Title:       "Spider-Man - No Way Home",
		Year:        2021,
		Quality:     ptrString("720p"),
		SizeBytes:   ptrInt64(4_000_000_000),
	}
	inserted, wasInsert, err := s.UpsertMovieByPath(ctx, first)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if !wasInsert {
		t.Error("first upsert reported an update, want an insert")
	}

	// A rescan of the same folder, now holding a bigger file.
	second := first
	second.Quality = ptrString("2160p")
	second.SizeBytes = ptrInt64(21_000_000_000)
	second.Title = "Spider-Man - No Way Home Extended"

	updated, wasInsert, err := s.UpsertMovieByPath(ctx, second)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if wasInsert {
		t.Error("second upsert reported an insert, want an update — `added` would over-count on a rescan")
	}
	if updated.ID != inserted.ID {
		t.Errorf("second upsert produced id %d, want %d", updated.ID, inserted.ID)
	}

	movies, err := s.ListMovies(ctx)
	if err != nil {
		t.Fatalf("ListMovies: %v", err)
	}
	if len(movies) != 1 {
		t.Fatalf("got %d rows for one library_path, want 1", len(movies))
	}

	got := movies[0]
	if got.Title != second.Title {
		t.Errorf("title = %q, want the second call's %q", got.Title, second.Title)
	}
	if got.Quality == nil || *got.Quality != "2160p" {
		t.Errorf("quality = %v, want 2160p", got.Quality)
	}
	if got.SizeBytes == nil || *got.SizeBytes != 21_000_000_000 {
		t.Errorf("size_bytes = %v, want 21000000000", got.SizeBytes)
	}
	if got.LibraryPath == nil || *got.LibraryPath != path {
		t.Errorf("library_path = %v, want %q", got.LibraryPath, path)
	}
	if got.AddedAt != inserted.AddedAt {
		t.Errorf("added_at = %v, want the insert's %v — it is when curator first saw the folder",
			got.AddedAt, inserted.AddedAt)
	}
}

// The regression this guards: a rescan re-supplies title, year and size from
// disk and knows nothing about TMDB, so it must not blank a match.
func TestUpsertDoesNotClobberTMDBMetadata(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const path = "/movies/X-Men Origins - Wolverine (2009)"

	m, _, err := s.UpsertMovieByPath(ctx, scanned(path))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	match := TMDBMatch{
		TMDBID:     2080,
		Title:      ptrString("X-Men Origins: Wolverine"),
		Overview:   ptrString("After the murder of his girlfriend..."),
		PosterPath: ptrString("/xTNsSbi1gDHAiJRJ9zLQlmzXhH1.jpg"),
	}
	if err := s.SetTMDBMetadata(ctx, m.ID, match); err != nil {
		t.Fatalf("SetTMDBMetadata: %v", err)
	}

	// Rescan: same folder, same disk facts, no TMDB knowledge at all.
	rescan := scanned(path)
	rescan.Title = "X-Men Origins - Wolverine"
	if _, _, err := s.UpsertMovieByPath(ctx, rescan); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	got, err := s.GetMovie(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMovie: %v", err)
	}
	if got.TMDBID == nil || *got.TMDBID != 2080 {
		t.Errorf("tmdb_id = %v after rescan, want 2080", got.TMDBID)
	}
	if got.Overview == nil || *got.Overview != *match.Overview {
		t.Errorf("overview = %v after rescan, want it preserved", got.Overview)
	}
	if got.PosterPath == nil || *got.PosterPath != *match.PosterPath {
		t.Errorf("poster_path = %v after rescan, want it preserved", got.PosterPath)
	}
	// The scan owns the title, so that one does move.
	if got.Title != rescan.Title {
		t.Errorf("title = %q, want the rescan's %q", got.Title, rescan.Title)
	}
}

// "Unmatched" and "matched to TMDB id 0" must stay distinguishable (D6).
func TestNullTMDBIDRoundTripsAsNull(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	unmatched, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Unmatched Film (2026)"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if unmatched.TMDBID != nil {
		t.Fatalf("tmdb_id from the upsert = %v, want nil", *unmatched.TMDBID)
	}

	got, err := s.GetMovie(ctx, unmatched.ID)
	if err != nil {
		t.Fatalf("GetMovie: %v", err)
	}
	if got.TMDBID != nil {
		t.Errorf("tmdb_id round-tripped as %v, want nil — a zero here would read as a TMDB match", *got.TMDBID)
	}
	if got.Overview != nil {
		t.Errorf("overview = %v, want nil", *got.Overview)
	}
	if got.PosterPath != nil {
		t.Errorf("poster_path = %v, want nil", *got.PosterPath)
	}
	if got.ImportedAt != nil {
		t.Errorf("imported_at = %v, want nil", *got.ImportedAt)
	}

	// The other half of the distinction: an actual match to id 0 is not NULL.
	zero, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Matched To Zero (2026)"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetTMDBMetadata(ctx, zero.ID, TMDBMatch{TMDBID: 0}); err != nil {
		t.Fatalf("SetTMDBMetadata: %v", err)
	}
	got, err = s.GetMovie(ctx, zero.ID)
	if err != nil {
		t.Fatalf("GetMovie: %v", err)
	}
	if got.TMDBID == nil {
		t.Fatal("tmdb_id = nil for a movie matched to id 0, want a non-nil 0")
	}
	if *got.TMDBID != 0 {
		t.Errorf("tmdb_id = %d, want 0", *got.TMDBID)
	}
}

func TestNullableTimeRoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	m, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Imported Film (2019)"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// imported_at belongs to the phase-3 import pipeline, so no method writes it
	// yet; set it directly to exercise the nullable-DATETIME read path.
	want := time.Date(2026, 8, 12, 9, 30, 0, 123456789, time.UTC)
	if _, err := s.db.ExecContext(ctx,
		`UPDATE movies SET imported_at = ? WHERE id = ?`, formatTime(want), m.ID); err != nil {
		t.Fatalf("set imported_at: %v", err)
	}

	got, err := s.GetMovie(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMovie: %v", err)
	}
	if got.ImportedAt == nil {
		t.Fatal("imported_at = nil, want a time")
	}
	if !got.ImportedAt.Equal(want) {
		t.Errorf("imported_at = %v, want %v", *got.ImportedAt, want)
	}
	if !got.AddedAt.Equal(m.AddedAt) {
		t.Errorf("added_at = %v, want %v", got.AddedAt, m.AddedAt)
	}
}

func TestMoviesMissingMetadataReturnsOnlyUnmatched(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	matched, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Matched (2018)"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	unmatchedA, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Unmatched A (2026)"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	unmatchedB, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Unmatched B (2026)"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetTMDBMetadata(ctx, matched.ID, TMDBMatch{TMDBID: 299536}); err != nil {
		t.Fatalf("SetTMDBMetadata: %v", err)
	}

	missing, err := s.MoviesMissingMetadata(ctx)
	if err != nil {
		t.Fatalf("MoviesMissingMetadata: %v", err)
	}
	if len(missing) != 2 {
		t.Fatalf("got %d unmatched rows, want 2", len(missing))
	}
	ids := map[int64]bool{}
	for _, m := range missing {
		if m.TMDBID != nil {
			t.Errorf("movie %d has tmdb_id %d and should not be in the work list", m.ID, *m.TMDBID)
		}
		ids[m.ID] = true
	}
	if !ids[unmatchedA.ID] || !ids[unmatchedB.ID] {
		t.Errorf("missing = %v, want ids %d and %d", ids, unmatchedA.ID, unmatchedB.ID)
	}
	if ids[matched.ID] {
		t.Errorf("matched movie %d is in the work list", matched.ID)
	}
}

func TestListMoviesNewestFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Freeze the clock and step it, so the ordering assertion is about added_at
	// and not about how fast the test machine is.
	//
	// The offsets are deliberately sub-second: added_at is TEXT and ORDER BY
	// compares it as text, so a variable-width fraction (RFC3339Nano trims
	// trailing zeros) would sort "…00.5Z" before "…00Z" and get this backwards.
	base := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	offsets := []time.Duration{0, 500 * time.Millisecond, time.Second}
	var tick int
	s.now = func() time.Time { return base.Add(offsets[tick]) }

	var want []int64
	for _, title := range []string{"Oldest", "Middle", "Newest"} {
		m := scanned("/movies/" + title + " (2020)")
		m.Title = title
		row, _, err := s.UpsertMovieByPath(ctx, m)
		if err != nil {
			t.Fatalf("upsert %s: %v", title, err)
		}
		want = append([]int64{row.ID}, want...) // newest first
		tick++
	}

	movies, err := s.ListMovies(ctx)
	if err != nil {
		t.Fatalf("ListMovies: %v", err)
	}
	if len(movies) != len(want) {
		t.Fatalf("got %d movies, want %d", len(movies), len(want))
	}
	for i, m := range movies {
		if m.ID != want[i] {
			t.Errorf("position %d: id %d (%q), want id %d", i, m.ID, m.Title, want[i])
		}
	}
}

// A whole scan stamps one clock read, so ties must still order deterministically.
func TestListMoviesBreaksAddedAtTiesByID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	frozen := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return frozen }

	var ids []int64
	for _, title := range []string{"A", "B", "C"} {
		row, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/"+title+" (2020)"))
		if err != nil {
			t.Fatalf("upsert %s: %v", title, err)
		}
		ids = append(ids, row.ID)
	}

	movies, err := s.ListMovies(ctx)
	if err != nil {
		t.Fatalf("ListMovies: %v", err)
	}
	for i, m := range movies {
		want := ids[len(ids)-1-i]
		if m.ID != want {
			t.Errorf("position %d: id %d, want %d", i, m.ID, want)
		}
	}
}

func TestGetMovieNotFound(t *testing.T) {
	s := newTestStore(t)

	_, err := s.GetMovie(context.Background(), 12345)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetMovie on a missing id: err = %v, want one wrapping ErrNotFound", err)
	}
}

func TestListMoviesEmptyIsNotNil(t *testing.T) {
	s := newTestStore(t)

	movies, err := s.ListMovies(context.Background())
	if err != nil {
		t.Fatalf("ListMovies: %v", err)
	}
	if movies == nil {
		t.Error("ListMovies returned nil on an empty library; it would marshal as JSON null, not []")
	}
}

func TestUpsertRejectsEmptyLibraryPath(t *testing.T) {
	s := newTestStore(t)

	m := scanned("")
	if _, _, err := s.UpsertMovieByPath(context.Background(), m); err == nil {
		t.Error("upsert with an empty library_path succeeded; every such row would collide on the identity key")
	}
}

func TestUpsertDefaultsMediaTypeAndStatus(t *testing.T) {
	s := newTestStore(t)

	m, _, err := s.UpsertMovieByPath(context.Background(), scanned("/movies/Defaults (2020)"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if m.MediaType != MediaTypeMovie {
		t.Errorf("media_type = %q, want %q", m.MediaType, MediaTypeMovie)
	}
	if m.Status != StatusImported {
		t.Errorf("status = %q, want %q", m.Status, StatusImported)
	}
}

// t.TempDir() names can carry the test name, and DB_PATH is user-supplied, so the
// DSN must survive a path the file: URI syntax would otherwise choke on.
func TestOpenPathWithSpecialCharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "media library (new)")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s, err := Open(context.Background(), filepath.Join(dir, "curator db?.sqlite"))
	if err != nil {
		t.Fatalf("Open with a spaced path: %v", err)
	}
	defer s.Close()

	if _, _, err := s.UpsertMovieByPath(context.Background(), scanned("/movies/Any (2020)")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
}
