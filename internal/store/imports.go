package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MarkImported records that a completed download is now a file in the library,
// and returns the movie row that survived.
//
// It is one method and one transaction because every half-finished version of
// it is a library that shows one film twice. libraryPath is the movie's
// FOLDER, not the video file inside it: Scan sets LibraryPath to
// filepath.Join(root, folderName) and library_path is its identity key, so a
// row holding the .mkv path is a row no future scan will ever match.
//
// The complication it exists for is the twin. A movies row with status
// 'wanted' and library_path NULL was created when the download was dispatched.
// If a scan ran between the hardlink and this call, UpsertMovieByPath has
// already inserted a SECOND row for the same folder, keyed on the path that is
// now on disk. Both rows are the same film and only one may survive.
//
// The twin is the one that survives, always. It is what every future
// UpsertMovieByPath finds, because library_path is what that upsert matches on;
// keeping the wanted row instead would have the next scan insert the twin all
// over again, and the problem would repeat for ever.
func (s *Store) MarkImported(ctx context.Context, hash, libraryPath string, sizeBytes int64, at time.Time) (Movie, error) {
	normalised := normaliseHash(hash)
	if normalised == "" {
		return Movie{}, errors.New("mark imported: torrent_hash is empty")
	}
	if strings.TrimSpace(libraryPath) == "" {
		return Movie{}, errors.New("mark imported: library_path is empty")
	}

	fail := func(err error) (Movie, error) {
		return Movie{}, fmt.Errorf("mark imported %s: %w", normalised, err)
	}

	// _txlock=immediate takes the write lock at BeginTx, so the two lookups
	// below cannot race a concurrent scan inserting the very twin they are
	// looking for.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	var movieID int64
	err = tx.QueryRowContext(ctx, `SELECT movie_id FROM downloads WHERE torrent_hash = ?`, normalised).Scan(&movieID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fail(ErrNotFound)
	case err != nil:
		return fail(err)
	}

	var twinID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM movies WHERE library_path = ?`, libraryPath).Scan(&twinID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		twinID = 0 // nothing has claimed this folder; the download's own row takes it
	case err != nil:
		return fail(err)
	}

	surviving := movieID
	if twinID != 0 && twinID != movieID {
		surviving = twinID
		if err := adoptTwin(ctx, tx, movieID, twinID); err != nil {
			return fail(err)
		}
	}

	// library_path is written even when the survivor is the twin, where it is a
	// no-op: one statement that is always correct beats two that have to agree.
	//
	// imported_at is COALESCEd so a re-import keeps the FIRST moment. The film
	// entered the library once, whatever a re-added torrent does later.
	if _, err := tx.ExecContext(ctx, `
		UPDATE movies
		SET status = ?, library_path = ?, size_bytes = ?, imported_at = COALESCE(imported_at, ?)
		WHERE id = ?`,
		StatusImported, libraryPath, sizeBytes, formatTime(at), surviving); err != nil {
		return fail(err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE downloads SET state = ? WHERE torrent_hash = ?`, DownloadImported, normalised); err != nil {
		return fail(err)
	}

	row, err := scanMovie(tx.QueryRowContext(ctx, selectMovie+` WHERE id = ?`, surviving))
	if err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	return row, nil
}

// adoptTwin folds the wanted row into the twin a scan already created.
//
// The order of these four steps is forced, not stylistic:
//
//  1. The repoint must precede the delete. downloads.movie_id is NOT NULL
//     REFERENCES movies(id) and foreign keys are genuinely on (the DSN sets
//     _foreign_keys=1), so deleting first is refused by SQLite.
//  2. The delete must precede the tmdb_id write. tmdb_id is UNIQUE, and the row
//     about to be removed is itself the row the new value would collide with.
//
// EVERY download pointing at the wanted row is repointed, not just the one being
// imported: a film can have two attempts against it, and leaving the second
// pointing at a row that is about to disappear is exactly what the foreign key
// exists to refuse.
func adoptTwin(ctx context.Context, tx *sql.Tx, wantedID, twinID int64) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE downloads SET movie_id = ? WHERE movie_id = ?`, twinID, wantedID); err != nil {
		return err
	}

	// Read through currentMatch so each row's id comes from the column its own
	// media_type owns. A show's lives in tmdb_tv_id and a film's in tmdb_id, and
	// reading the wrong one here would carry NULL and silently leave the twin
	// unmatched — for a show, permanently, because MoviesMissingMetadata's TV
	// pass is the only thing that would look at it again.
	_, wantedTMDB, err := currentMatch(ctx, tx, wantedID)
	if err != nil {
		return err
	}
	twinMedia, twinTMDB, err := currentMatch(ctx, tx, twinID)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM movies WHERE id = ?`, wantedID); err != nil {
		return err
	}

	// Nothing to carry, or the twin is already matched. A match the scanner
	// established from the folder title is not worth overwriting with one a
	// client sent.
	if wantedTMDB == nil || twinTMDB != nil {
		return nil
	}

	// tmdb_id is UNIQUE. The wanted row is gone by now, so the only conflict left
	// would be a THIRD movie already holding this id.
	//
	// That state is currently unreachable — UNIQUE means the wanted row holding
	// the id is itself proof no other row does — so this check is defence in
	// depth, and TestATMDBIDCannotBeContested asserts the constraint that makes
	// it so. It is kept because the failure it prevents is the expensive kind:
	// an import whose hardlink is already on disk failing on a constraint, and
	// then failing identically on every retry for ever. Leaving the twin
	// unmatched instead costs nothing — MoviesMissingMetadata surfaces it and a
	// human can look, which is exactly what D6 says a NULL tmdb_id is for.
	//
	// The probe and the write are both scoped to the twin's own id space, because
	// a film and a show may legitimately hold the same number.
	var third int64
	err = tx.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT id FROM movies WHERE %s = ? AND id <> ?`, tmdbColumn(twinMedia)),
		*wantedTMDB, twinID).Scan(&third)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx,
			`UPDATE movies SET`+tmdbIDWrite+` WHERE id = ?`, *wantedTMDB, *wantedTMDB, twinID)
		return err
	case err != nil:
		return err
	}
	return nil
}
