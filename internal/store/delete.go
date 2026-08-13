package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Deleted is what DeleteMovie removed: the movie row, and every download that
// pointed at it.
//
// The downloads are returned rather than discarded because the caller has work
// left to do with them — each one names a torrent that has to be removed from
// qBittorrent, and once the row is gone there is nothing left to look it up by.
type Deleted struct {
	Movie     Movie
	Downloads []Download
}

// DeleteMovie removes a movie and its downloads in one transaction, and reports
// what was removed.
//
// It deletes ROWS ONLY. The hardlink in the library and the file qBittorrent
// downloaded are the caller's to deal with, in that order, because this package
// knows about rows and has never touched a disk (docs/decisions.md D19).
//
// The downloads go first because they must: downloads.movie_id is NOT NULL
// REFERENCES movies(id) with foreign keys on, so deleting the movie while a
// download still points at it is refused by SQLite. That is the same ordering
// adoptTwin needs, and for the same reason.
func (s *Store) DeleteMovie(ctx context.Context, id int64) (Deleted, error) {
	fail := func(err error) (Deleted, error) {
		return Deleted{}, fmt.Errorf("delete movie %d: %w", id, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	movie, err := scanMovie(tx.QueryRowContext(ctx, selectMovie+` WHERE id = ?`, id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fail(ErrNotFound)
	case err != nil:
		return fail(err)
	}

	// Read before deleting: after the DELETE there is no way to learn which
	// torrents belonged to this film, and the caller needs every one of them.
	rows, err := tx.QueryContext(ctx, selectDownload+` WHERE movie_id = ? ORDER BY id`, id)
	if err != nil {
		return fail(err)
	}
	downloads := []Download{}
	for rows.Next() {
		d, scanErr := scanDownload(rows)
		if scanErr != nil {
			rows.Close()
			return fail(scanErr)
		}
		downloads = append(downloads, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fail(err)
	}
	rows.Close()

	if _, err := tx.ExecContext(ctx, `DELETE FROM downloads WHERE movie_id = ?`, id); err != nil {
		return fail(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM movies WHERE id = ?`, id); err != nil {
		return fail(err)
	}

	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	return Deleted{Movie: movie, Downloads: downloads}, nil
}

// DownloadsForMovie lists every download recorded against a movie.
//
// It exists for the delete path, which has to know the torrents BEFORE the rows
// are removed: D19 deletes the torrent and its files first and the rows last, so
// that a failure leaves something a retry can finish, and after the DELETE there
// is nothing left to look them up by.
func (s *Store) DownloadsForMovie(ctx context.Context, movieID int64) ([]Download, error) {
	rows, err := s.db.QueryContext(ctx, selectDownload+` WHERE movie_id = ? ORDER BY id`, movieID)
	if err != nil {
		return nil, fmt.Errorf("downloads for movie %d: %w", movieID, err)
	}
	defer rows.Close()

	downloads := []Download{}
	for rows.Next() {
		d, err := scanDownload(rows)
		if err != nil {
			return nil, fmt.Errorf("downloads for movie %d: %w", movieID, err)
		}
		downloads = append(downloads, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("downloads for movie %d: %w", movieID, err)
	}
	return downloads, nil
}
