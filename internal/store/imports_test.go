package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

const (
	importPath = "/movies/Avengers - Infinity War (2018)"
	importHash = "AAAABBBBCCCCDDDDEEEEFFFF0000111122223333"
	importSize = int64(8_500_000_000)
)

var importedAt = time.Date(2026, 8, 12, 19, 30, 0, 0, time.UTC)

// The plain path: a film that was wanted, downloaded and is now on disk.
func TestMarkImportedRecordsTheMovieAndTheDownload(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	movie, err := s.UpsertWanted(ctx, Wanted{MediaType: MediaTypeMovie, Title: "Avengers - Infinity War", Year: 2018, TMDBID: nil})
	if err != nil {
		t.Fatalf("UpsertWanted: %v", err)
	}
	dispatch(t, s, movie.ID, importHash)

	got, err := s.MarkImported(ctx, importHash, importPath, importSize, importedAt)
	if err != nil {
		t.Fatalf("MarkImported: %v", err)
	}

	if got.ID != movie.ID {
		t.Errorf("movie id = %d, want the wanted row's %d", got.ID, movie.ID)
	}
	if got.Status != StatusImported {
		t.Errorf("status = %q, want %q", got.Status, StatusImported)
	}
	if got.LibraryPath == nil || *got.LibraryPath != importPath {
		t.Errorf("library_path = %v, want %q", got.LibraryPath, importPath)
	}
	if got.SizeBytes == nil || *got.SizeBytes != importSize {
		t.Errorf("size_bytes = %v, want %d", got.SizeBytes, importSize)
	}
	if got.ImportedAt == nil || !got.ImportedAt.Equal(importedAt) {
		t.Errorf("imported_at = %v, want %v", got.ImportedAt, importedAt)
	}

	d, err := s.GetDownloadByHash(ctx, importHash)
	if err != nil {
		t.Fatalf("GetDownloadByHash: %v", err)
	}
	if d.State != DownloadImported {
		t.Errorf("download state = %q, want %q", d.State, DownloadImported)
	}
}

// The sibling of TestWantedMovieDoesNotDisturbTheScanUpsert, which pins the
// state this one resolves. That test proves a wanted row and a scanned row for
// the same film coexist as two rows; this proves the import folds them back into
// one, and that the survivor is the SCANNED row.
//
// It is the whole reason MarkImported is a transaction: every partial version of
// this leaves a library showing one film twice, or a download pointing at a row
// that no longer exists.
func TestWantedThenScanThenMarkImported(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	wanted, err := s.UpsertWanted(ctx, Wanted{MediaType: MediaTypeMovie, Title: "Avengers - Infinity War", Year: 2018, TMDBID: nil})
	if err != nil {
		t.Fatalf("UpsertWanted: %v", err)
	}
	dispatch(t, s, wanted.ID, importHash)

	// A scan lands between the hardlink and the record, which is exactly the
	// race this handles: it claims the folder first.
	twin, inserted, err := s.UpsertMovieByPath(ctx, scanned(importPath))
	if err != nil {
		t.Fatalf("UpsertMovieByPath: %v", err)
	}
	if !inserted || twin.ID == wanted.ID {
		t.Fatalf("the scan did not create a separate twin (inserted=%v, id=%d, wanted=%d)", inserted, twin.ID, wanted.ID)
	}

	got, err := s.MarkImported(ctx, importHash, importPath, importSize, importedAt)
	if err != nil {
		t.Fatalf("MarkImported: %v", err)
	}

	if got.ID != twin.ID {
		t.Errorf("survivor id = %d, want the twin's %d — the twin is what every future scan finds", got.ID, twin.ID)
	}
	if got.Status != StatusImported {
		t.Errorf("status = %q, want %q", got.Status, StatusImported)
	}
	// The twin's title came off the folder name, which is what the library is
	// actually called; the wanted row's came from a client.
	if got.Title != "Avengers - Infinity War" {
		t.Errorf("title = %q, want the folder's", got.Title)
	}

	movies, err := s.ListMovies(ctx, MediaTypeMovie)
	if err != nil {
		t.Fatalf("ListMovies: %v", err)
	}
	if len(movies) != 1 {
		t.Fatalf("got %d movie rows, want 1 — the wanted row must have been folded into the twin", len(movies))
	}
	if _, err := s.GetMovie(ctx, wanted.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the wanted row is still there: %v", err)
	}

	d, err := s.GetDownloadByHash(ctx, importHash)
	if err != nil {
		t.Fatalf("GetDownloadByHash: %v", err)
	}
	if d.MovieID != twin.ID {
		t.Errorf("download movie_id = %d, want the twin's %d", d.MovieID, twin.ID)
	}
	if d.State != DownloadImported {
		t.Errorf("download state = %q, want %q", d.State, DownloadImported)
	}

	// And the whole point: a later scan finds the survivor rather than inserting
	// a third row.
	again, inserted, err := s.UpsertMovieByPath(ctx, scanned(importPath))
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if inserted || again.ID != twin.ID {
		t.Errorf("rescan inserted=%v id=%d; want it to find the survivor %d", inserted, again.ID, twin.ID)
	}
}

// A film the client identified but TMDB could not match off the folder title
// keeps its id through the fold.
func TestMarkImportedCarriesTMDBIDOntoAnUnmatchedTwin(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	wanted, err := s.UpsertWanted(ctx, Wanted{MediaType: MediaTypeMovie, Title: "Avengers - Infinity War", Year: 2018, TMDBID: ptrInt64(299536)})
	if err != nil {
		t.Fatalf("UpsertWanted: %v", err)
	}
	dispatch(t, s, wanted.ID, importHash)
	if _, _, err := s.UpsertMovieByPath(ctx, scanned(importPath)); err != nil {
		t.Fatalf("UpsertMovieByPath: %v", err)
	}

	got, err := s.MarkImported(ctx, importHash, importPath, importSize, importedAt)
	if err != nil {
		t.Fatalf("MarkImported: %v", err)
	}
	if got.TMDBID == nil || *got.TMDBID != 299536 {
		t.Errorf("tmdb_id = %v, want 299536 carried forward from the wanted row", got.TMDBID)
	}
}

func TestMarkImportedLeavesAMatchedTwinsTMDBIDAlone(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	wanted, err := s.UpsertWanted(ctx, Wanted{MediaType: MediaTypeMovie, Title: "Avengers - Infinity War", Year: 2018, TMDBID: ptrInt64(111)})
	if err != nil {
		t.Fatalf("UpsertWanted: %v", err)
	}
	dispatch(t, s, wanted.ID, importHash)

	twin, _, err := s.UpsertMovieByPath(ctx, scanned(importPath))
	if err != nil {
		t.Fatalf("UpsertMovieByPath: %v", err)
	}
	if err := s.SetTMDBMetadata(ctx, twin.ID, TMDBMatch{TMDBID: 299536}); err != nil {
		t.Fatalf("SetTMDBMetadata: %v", err)
	}

	got, err := s.MarkImported(ctx, importHash, importPath, importSize, importedAt)
	if err != nil {
		t.Fatalf("MarkImported: %v", err)
	}
	if got.TMDBID == nil || *got.TMDBID != 299536 {
		t.Errorf("tmdb_id = %v, want the twin's own 299536 kept", got.TMDBID)
	}
}

// The third-row guard in adoptTwin, and why it cannot be exercised end to end.
//
// The guard exists so a carry-forward can never turn a successful import — one
// whose hardlink is already on disk — into a UNIQUE violation. It is currently
// UNREACHABLE, and that is worth stating rather than faking: tmdb_id is UNIQUE,
// so if the wanted row holds an id then by construction no other row can, and
// there is no legal state in which a third row contests it. UpsertWanted
// matches on tmdb_id and returns the existing row instead of inserting a rival,
// and SetTMDBMetadata fails outright on a conflict.
//
// So what is asserted here is the constraint that makes it unreachable. The day
// tmdb_id stops being UNIQUE — a plausible change, since D6 already treats it as
// soft data — this test fails, and the guard stops being defence in depth and
// becomes load-bearing.
func TestATMDBIDCannotBeContested(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	wanted, err := s.UpsertWanted(ctx, Wanted{MediaType: MediaTypeMovie, Title: "Avengers - Infinity War", Year: 2018, TMDBID: ptrInt64(299536)})
	if err != nil {
		t.Fatalf("UpsertWanted: %v", err)
	}

	other, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Some Other Folder (2018)"))
	if err != nil {
		t.Fatalf("third row: %v", err)
	}
	if err := s.SetTMDBMetadata(ctx, other.ID, TMDBMatch{TMDBID: 299536}); err == nil {
		t.Fatal("two movie rows took tmdb_id 299536; adoptTwin's third-row check is now load-bearing and needs a real test")
	}

	// And the other direction: asking to download a film already matched returns
	// that row rather than forking its identity.
	again, err := s.UpsertWanted(ctx, Wanted{MediaType: MediaTypeMovie, Title: "Avengers Infinity War", Year: 2018, TMDBID: ptrInt64(299536)})
	if err != nil {
		t.Fatalf("UpsertWanted: %v", err)
	}
	if again.ID != wanted.ID {
		t.Errorf("a second dispatch produced row %d, want the existing %d", again.ID, wanted.ID)
	}
}

// A film can have two attempts against it. Leaving the second pointing at a row
// that is about to be deleted is what the foreign key exists to refuse.
func TestMarkImportedRepointsEveryDownloadAtTheTwin(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	const secondHash = "9999888877776666555544443333222211110000"

	wanted, err := s.UpsertWanted(ctx, Wanted{MediaType: MediaTypeMovie, Title: "Avengers - Infinity War", Year: 2018, TMDBID: nil})
	if err != nil {
		t.Fatalf("UpsertWanted: %v", err)
	}
	dispatch(t, s, wanted.ID, importHash)
	dispatch(t, s, wanted.ID, secondHash)

	twin, _, err := s.UpsertMovieByPath(ctx, scanned(importPath))
	if err != nil {
		t.Fatalf("UpsertMovieByPath: %v", err)
	}

	if _, err := s.MarkImported(ctx, importHash, importPath, importSize, importedAt); err != nil {
		t.Fatalf("MarkImported: %v", err)
	}

	for _, hash := range []string{importHash, secondHash} {
		d, err := s.GetDownloadByHash(ctx, hash)
		if err != nil {
			t.Fatalf("GetDownloadByHash %s: %v", hash, err)
		}
		if d.MovieID != twin.ID {
			t.Errorf("download %s movie_id = %d, want the twin's %d", hash, d.MovieID, twin.ID)
		}
	}

	// Only the imported one changes state; the other is still whatever it was.
	other, err := s.GetDownloadByHash(ctx, secondHash)
	if err != nil {
		t.Fatalf("GetDownloadByHash: %v", err)
	}
	if other.State == DownloadImported {
		t.Error("the second download was marked imported; only the hash passed in should move")
	}
}

// A re-added torrent must not reset the moment the film entered the library.
func TestMarkImportedKeepsTheFirstImportedAt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	movie, err := s.UpsertWanted(ctx, Wanted{MediaType: MediaTypeMovie, Title: "Avengers - Infinity War", Year: 2018, TMDBID: nil})
	if err != nil {
		t.Fatalf("UpsertWanted: %v", err)
	}
	dispatch(t, s, movie.ID, importHash)

	first, err := s.MarkImported(ctx, importHash, importPath, importSize, importedAt)
	if err != nil {
		t.Fatalf("first MarkImported: %v", err)
	}

	later := importedAt.Add(72 * time.Hour)
	second, err := s.MarkImported(ctx, importHash, importPath, importSize+1, later)
	if err != nil {
		t.Fatalf("second MarkImported: %v", err)
	}

	if second.ID != first.ID {
		t.Errorf("a re-import produced a different row: %d then %d", first.ID, second.ID)
	}
	if second.ImportedAt == nil || !second.ImportedAt.Equal(importedAt) {
		t.Errorf("imported_at = %v after a re-import, want the first moment %v", second.ImportedAt, importedAt)
	}
	// The size is not COALESCEd: a re-import legitimately re-measures the file.
	if second.SizeBytes == nil || *second.SizeBytes != importSize+1 {
		t.Errorf("size_bytes = %v, want the re-measured %d", second.SizeBytes, importSize+1)
	}
}

func TestMarkImportedUnknownHashIsNotFoundAndWritesNothing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	movie, _, err := s.UpsertMovieByPath(ctx, scanned(importPath))
	if err != nil {
		t.Fatalf("UpsertMovieByPath: %v", err)
	}

	if _, err := s.MarkImported(ctx, importHash, importPath, importSize, importedAt); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	after, err := s.GetMovie(ctx, movie.ID)
	if err != nil {
		t.Fatalf("GetMovie: %v", err)
	}
	if after.ImportedAt != nil || after.Status != StatusImported {
		// scanned() rows are already 'imported' by definition, so the assertion
		// that matters is that nothing stamped a time on it.
		if after.ImportedAt != nil {
			t.Errorf("imported_at = %v after a failed call, want nil", after.ImportedAt)
		}
	}

	for _, arg := range []struct{ hash, path string }{{"", importPath}, {importHash, ""}, {importHash, "  "}} {
		if _, err := s.MarkImported(ctx, arg.hash, arg.path, importSize, importedAt); err == nil {
			t.Errorf("MarkImported(%q, %q) succeeded, want an error", arg.hash, arg.path)
		}
	}
}

// The rollback, proven rather than assumed. A trigger makes the LAST statement
// fail, after the twin has already been adopted and the wanted row deleted, and
// the database must come back exactly as it was.
func TestMarkImportedRollsBackTheWholeFoldWhenTheLastStepFails(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	wanted, err := s.UpsertWanted(ctx, Wanted{MediaType: MediaTypeMovie, Title: "Avengers - Infinity War", Year: 2018, TMDBID: ptrInt64(299536)})
	if err != nil {
		t.Fatalf("UpsertWanted: %v", err)
	}
	dispatch(t, s, wanted.ID, importHash)
	twin, _, err := s.UpsertMovieByPath(ctx, scanned(importPath))
	if err != nil {
		t.Fatalf("UpsertMovieByPath: %v", err)
	}

	if _, err := s.db.ExecContext(ctx, `
		CREATE TRIGGER refuse_import BEFORE UPDATE OF state ON downloads
		WHEN NEW.state = 'imported'
		BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if _, err := s.MarkImported(ctx, importHash, importPath, importSize, importedAt); err == nil {
		t.Fatal("MarkImported succeeded with a trigger refusing its last statement")
	}

	// Everything adoptTwin did must be gone.
	if _, err := s.GetMovie(ctx, wanted.ID); err != nil {
		t.Errorf("the wanted row was deleted despite the failure: %v", err)
	}
	surviving, err := s.GetMovie(ctx, twin.ID)
	if err != nil {
		t.Fatalf("GetMovie(twin): %v", err)
	}
	if surviving.ImportedAt != nil {
		t.Errorf("the twin was stamped imported_at = %v despite the failure", surviving.ImportedAt)
	}
	if surviving.TMDBID != nil {
		t.Errorf("the twin took tmdb_id %v despite the failure", surviving.TMDBID)
	}
	d, err := s.GetDownloadByHash(ctx, importHash)
	if err != nil {
		t.Fatalf("GetDownloadByHash: %v", err)
	}
	if d.MovieID != wanted.ID {
		t.Errorf("download movie_id = %d, want it still pointing at the wanted row %d", d.MovieID, wanted.ID)
	}
	if d.State == DownloadImported {
		t.Error("the download reads imported despite the failure")
	}
}

// The reason adoptTwin repoints before it deletes. If this ever stops failing,
// foreign keys are off and the ordering has quietly become decorative.
func TestForeignKeysRefuseADeleteBeforeTheRepoint(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	movie, err := s.UpsertWanted(ctx, Wanted{MediaType: MediaTypeMovie, Title: "Avengers - Infinity War", Year: 2018, TMDBID: nil})
	if err != nil {
		t.Fatalf("UpsertWanted: %v", err)
	}
	dispatch(t, s, movie.ID, importHash)

	if _, err := s.db.ExecContext(ctx, `DELETE FROM movies WHERE id = ?`, movie.ID); err == nil {
		t.Fatal("deleting a movie with a download pointing at it succeeded; _foreign_keys=1 is not in effect")
	}
}

// dispatch records a download against a movie, the way Service.Dispatch does
// once qBittorrent has confirmed the add.
func dispatch(t *testing.T, s *Store, movieID int64, hash string) Download {
	t.Helper()
	d, err := s.InsertDownload(context.Background(), Download{
		MovieID:     movieID,
		TorrentHash: hash,
		Indexer:     "yts",
		ReleaseName: "Avengers Infinity War 2018 1080p BluRay x264",
		Magnet:      "magnet:?xt=urn:btih:" + hash,
		State:       DownloadCompleted,
	})
	if err != nil {
		t.Fatalf("InsertDownload: %v", err)
	}
	return d
}

// --- DeleteMovie ------------------------------------------------------------

func TestDeleteMovieRemovesTheRowAndReportsItsDownloads(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const secondHash = "9999888877776666555544443333222211110000"

	movie, err := s.UpsertWanted(ctx, Wanted{MediaType: MediaTypeMovie, Title: "Avengers - Infinity War", Year: 2018, TMDBID: nil})
	if err != nil {
		t.Fatalf("UpsertWanted: %v", err)
	}
	dispatch(t, s, movie.ID, importHash)
	dispatch(t, s, movie.ID, secondHash)
	if _, err := s.MarkImported(ctx, importHash, importPath, importSize, importedAt); err != nil {
		t.Fatalf("MarkImported: %v", err)
	}

	deleted, err := s.DeleteMovie(ctx, movie.ID)
	if err != nil {
		t.Fatalf("DeleteMovie: %v", err)
	}

	// The caller needs every torrent: after the DELETE there is nothing left to
	// look them up by, and each one has to be removed from qBittorrent.
	if len(deleted.Downloads) != 2 {
		t.Fatalf("reported %d downloads, want 2", len(deleted.Downloads))
	}
	if deleted.Movie.LibraryPath == nil || *deleted.Movie.LibraryPath != importPath {
		t.Errorf("library_path = %v, want %q — the caller removes that folder", deleted.Movie.LibraryPath, importPath)
	}

	if _, err := s.GetMovie(ctx, movie.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the movie row survived: %v", err)
	}
	for _, hash := range []string{importHash, secondHash} {
		if _, err := s.GetDownloadByHash(ctx, hash); !errors.Is(err, ErrNotFound) {
			t.Errorf("download %s survived: %v", hash, err)
		}
	}
	movies, err := s.ListMovies(ctx, MediaTypeMovie)
	if err != nil {
		t.Fatalf("ListMovies: %v", err)
	}
	if len(movies) != 0 {
		t.Errorf("got %d movies, want 0", len(movies))
	}
}

// A film scanned off disk has no downloads at all, and deleting it must not
// depend on there being any.
func TestDeleteMovieWithNoDownloads(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	movie, _, err := s.UpsertMovieByPath(ctx, scanned(importPath))
	if err != nil {
		t.Fatalf("UpsertMovieByPath: %v", err)
	}

	deleted, err := s.DeleteMovie(ctx, movie.ID)
	if err != nil {
		t.Fatalf("DeleteMovie: %v", err)
	}
	if len(deleted.Downloads) != 0 {
		t.Errorf("reported %d downloads, want 0", len(deleted.Downloads))
	}
	if _, err := s.GetMovie(ctx, movie.ID); !errors.Is(err, ErrNotFound) {
		t.Error("the movie row survived")
	}
}

func TestDeleteMovieUnknownIDIsNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.DeleteMovie(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// Deleting the movie while a download still points at it is refused by SQLite,
// which is why the downloads go first. If this ordering were reversed the
// foreign key would fail the whole transaction.
func TestDeleteMovieLeavesNoOrphanedDownloads(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	movie, err := s.UpsertWanted(ctx, Wanted{MediaType: MediaTypeMovie, Title: "Avengers - Infinity War", Year: 2018, TMDBID: nil})
	if err != nil {
		t.Fatalf("UpsertWanted: %v", err)
	}
	dispatch(t, s, movie.ID, importHash)

	if _, err := s.DeleteMovie(ctx, movie.ID); err != nil {
		t.Fatalf("DeleteMovie: %v", err)
	}
	downloads, err := s.ListDownloads(ctx)
	if err != nil {
		t.Fatalf("ListDownloads: %v", err)
	}
	if len(downloads) != 0 {
		t.Errorf("got %d downloads, want 0 — an orphan would violate the foreign key", len(downloads))
	}
}

// --- LibraryByTMDBID --------------------------------------------------------

func TestLibraryByTMDBIDIndexesOnlyMatchedFilms(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	matched, _, err := s.UpsertMovieByPath(ctx, scanned(importPath))
	if err != nil {
		t.Fatalf("UpsertMovieByPath: %v", err)
	}
	if err := s.SetTMDBMetadata(ctx, matched.ID, TMDBMatch{TMDBID: 299536}); err != nil {
		t.Fatalf("SetTMDBMetadata: %v", err)
	}
	// A folder TMDB could not match. No card can ever match it either, so it must
	// not be in the index.
	if _, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Some Unmatched Folder (2019)")); err != nil {
		t.Fatalf("second folder: %v", err)
	}

	byID, err := s.LibraryByTMDBID(ctx, MediaTypeMovie)
	if err != nil {
		t.Fatalf("LibraryByTMDBID: %v", err)
	}
	if len(byID) != 1 {
		t.Fatalf("indexed %d films, want 1 — a NULL tmdb_id is not indexable", len(byID))
	}

	state, ok := byID[299536]
	if !ok {
		t.Fatal("the matched film is missing from the index")
	}
	if state.MovieID != matched.ID {
		t.Errorf("movie_id = %d, want %d", state.MovieID, matched.ID)
	}
	if state.Status != StatusImported {
		t.Errorf("status = %q, want %q", state.Status, StatusImported)
	}
	if state.LibraryPath == nil || *state.LibraryPath != importPath {
		t.Errorf("library_path = %v, want %q", state.LibraryPath, importPath)
	}
	if state.Downloading {
		t.Error("a film with no downloads reports Downloading")
	}
}

// The reason Downloading is an EXISTS over downloads and not movies.status.
//
// store.StatusDownloading is declared and never written — UpsertWanted
// inserts 'wanted' and the importer writes 'imported' — so a card reading
// movies.status would label a film whose torrent is at 60% as "wanted".
func TestLibraryByTMDBIDReportsDownloadingFromTheDownloadNotTheStatus(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	movie, err := s.UpsertWanted(ctx, Wanted{MediaType: MediaTypeMovie, Title: "Avengers - Infinity War", Year: 2018, TMDBID: ptrInt64(299536)})
	if err != nil {
		t.Fatalf("UpsertWanted: %v", err)
	}
	dispatch(t, s, movie.ID, importHash)

	// The download is in flight. movies.status still says 'wanted', which is the
	// trap.
	byID, err := s.LibraryByTMDBID(ctx, MediaTypeMovie)
	if err != nil {
		t.Fatalf("LibraryByTMDBID: %v", err)
	}
	if got := byID[299536]; !got.Downloading {
		t.Errorf("Downloading = false while a download is in flight; status was %q", got.Status)
	}

	// And once it is imported it stops being in flight, without movies.status
	// ever having said 'downloading' at any point.
	if _, err := s.MarkImported(ctx, importHash, importPath, importSize, importedAt); err != nil {
		t.Fatalf("MarkImported: %v", err)
	}
	byID, err = s.LibraryByTMDBID(ctx, MediaTypeMovie)
	if err != nil {
		t.Fatalf("LibraryByTMDBID: %v", err)
	}
	got := byID[299536]
	if got.Downloading {
		t.Error("Downloading is still true after the import")
	}
	if got.Status != StatusImported {
		t.Errorf("status = %q, want %q", got.Status, StatusImported)
	}
}

// A failed download is not in flight; the film is not coming.
func TestLibraryByTMDBIDIgnoresAFailedDownload(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	movie, err := s.UpsertWanted(ctx, Wanted{MediaType: MediaTypeMovie, Title: "Avengers - Infinity War", Year: 2018, TMDBID: ptrInt64(299536)})
	if err != nil {
		t.Fatalf("UpsertWanted: %v", err)
	}
	dispatch(t, s, movie.ID, importHash)
	if err := s.UpdateDownloadProgress(ctx, importHash, DownloadFailed, 0.3, "", nil); err != nil {
		t.Fatalf("UpdateDownloadProgress: %v", err)
	}

	byID, err := s.LibraryByTMDBID(ctx, MediaTypeMovie)
	if err != nil {
		t.Fatalf("LibraryByTMDBID: %v", err)
	}
	if byID[299536].Downloading {
		t.Error("a failed download counts as in flight")
	}
}

func TestLibraryByTMDBIDEmptyIsAnEmptyMap(t *testing.T) {
	byID, err := newTestStore(t).LibraryByTMDBID(context.Background(), MediaTypeMovie)
	if err != nil {
		t.Fatalf("LibraryByTMDBID: %v", err)
	}
	if byID == nil {
		t.Error("an empty library returned a nil map")
	}
	if len(byID) != 0 {
		t.Errorf("got %d entries", len(byID))
	}
}
