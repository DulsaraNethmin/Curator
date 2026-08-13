package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Values the downloads.state column carries, mirroring schema.sql's comment.
//
// DownloadImported is phase 4's to set and the poller never writes it: a
// completed torrent is not an imported movie, and collapsing the two would leave
// the importer nothing to look for.
const (
	DownloadQueued      = "queued"
	DownloadDownloading = "downloading"
	DownloadCompleted   = "completed"
	DownloadImported    = "imported"
	DownloadFailed      = "failed"

	// DownloadStalled is phase 6's, and needs no migration: state is TEXT and
	// its values are a comment in schema.sql rather than a constraint. It is
	// not terminal — the next poll can move it back to downloading.
	DownloadStalled = "stalled"
)

// Download is one row of the downloads table.
//
// CompletedAt is a pointer for the same reason Movie.ImportedAt is: a download
// that has not finished has no completion time, and a zero-valued timestamp in
// the database is a lie that sorts and formats like a real one. It also marshals
// to JSON null, which is what GET /api/downloads should say.
//
// TorrentHash is always the upper-case 40-hex form indexer.InfoHash produces.
// qBittorrent reports lower-case, so every method here normalises before it
// queries — unnormalised the two never match and the reason is invisible.
type Download struct {
	ID          int64      `json:"id"`
	MovieID     int64      `json:"movie_id"`
	TorrentHash string     `json:"torrent_hash"`
	Indexer     string     `json:"indexer"`
	ReleaseName string     `json:"release_name"`
	Magnet      string     `json:"magnet"`
	State       string     `json:"state"`
	Progress    float64    `json:"progress"`
	AddedAt     time.Time  `json:"added_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

const selectDownload = `
SELECT id, movie_id, torrent_hash, indexer, release_name, magnet,
       state, progress, added_at, completed_at
FROM downloads`

// normaliseHash puts a torrent hash into the one case the table stores.
//
// Hashes are written upper-case to match indexer.InfoHash, which is where every
// hash curator holds comes from; qBittorrent's API answers in lower-case. A
// lookup that skipped this would simply find nothing, with no error to explain
// why, so it is applied on both the write and the read side.
func normaliseHash(hash string) string { return strings.ToUpper(hash) }

// UpsertWantedMovie records a film curator does not have yet, so a download has a
// movie_id to point at, and returns the existing row when it already knows the
// film. Re-downloading a film must not fork its identity into two rows.
//
// The new row has status 'wanted' and library_path NULL, because the film is not
// on disk — that is the entire reason it is being downloaded. This works because
// **SQLite permits any number of NULLs in a UNIQUE column**: library_path is
// UNIQUE, yet every wanted movie leaves it NULL and none of them collide. The
// same is true of tmdb_id, which is how D6's unmatched folders coexist. Do not
// "fix" the schema by making either column NOT NULL or by inventing a
// placeholder path — the placeholder is what would actually collide.
//
// Identity is matched on tmdb_id when the caller has one, because that is the
// film itself rather than how somebody spelled it, and on (title, year)
// otherwise. There is deliberately no fallback from one to the other: writing a
// tmdb_id onto a row matched by title would be TMDB matching done here, and
// SetTMDBMetadata is where that belongs, UNIQUE violation and all.
//
// An existing row is returned untouched. A film already imported off disk stays
// imported — asking to download it does not un-import the copy that is playing.
func (s *Store) UpsertWantedMovie(ctx context.Context, title string, year int, tmdbID *int64) (Movie, error) {
	if strings.TrimSpace(title) == "" {
		return Movie{}, errors.New("upsert wanted movie: title is empty")
	}

	fail := func(err error) (Movie, error) {
		return Movie{}, fmt.Errorf("upsert wanted movie %s (%d): %w", title, year, err)
	}

	// The lookup and the insert must not race a second dispatch of the same film;
	// _txlock=immediate means this holds the write lock from BeginTx.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	var id int64
	if tmdbID != nil {
		err = tx.QueryRowContext(ctx, `SELECT id FROM movies WHERE tmdb_id = ?`, *tmdbID).Scan(&id)
	} else {
		// (title, year) is not unique in the schema, so pick the oldest match and
		// stay deterministic rather than depending on scan order.
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM movies WHERE title = ? AND year = ? ORDER BY id LIMIT 1`, title, year).Scan(&id)
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// library_path is left out of the column list rather than passed as NULL,
		// so the intent reads as "this film has no path yet" and not as a value
		// somebody forgot to fill in.
		res, insErr := tx.ExecContext(ctx, `
			INSERT INTO movies (tmdb_id, title, year, media_type, status, added_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			tmdbID, title, year, MediaTypeMovie, StatusWanted, formatTime(s.now()))
		if insErr != nil {
			return fail(insErr)
		}
		if id, insErr = res.LastInsertId(); insErr != nil {
			return fail(insErr)
		}
	case err != nil:
		return fail(err)
	}

	row, err := scanMovie(tx.QueryRowContext(ctx, selectMovie+` WHERE id = ?`, id))
	if err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	return row, nil
}

// InsertDownload records a dispatched torrent and returns the stored row.
//
// torrent_hash is UNIQUE, and inserting one that is already there returns the
// existing row with no error rather than failing: adding a magnet qBittorrent
// already holds is idempotent on its side, so re-dispatching a release after a
// restart converges here too instead of turning a working torrent into a 500.
//
// The caller supplies MovieID, TorrentHash, Indexer, ReleaseName, Magnet and
// optionally State and Progress. ID, AddedAt and CompletedAt on the argument are
// ignored — the id is the database's, added_at is this store's clock, and a
// download being inserted has by definition not completed.
//
// movie_id is NOT NULL REFERENCES movies(id) and foreign keys are on, so a
// download for a movie that does not exist fails here. That is the point of the
// reference: UpsertWantedMovie runs first.
func (s *Store) InsertDownload(ctx context.Context, d Download) (Download, error) {
	hash := normaliseHash(d.TorrentHash)
	if hash == "" {
		return Download{}, errors.New("insert download: torrent_hash is empty")
	}
	state := d.State
	if state == "" {
		state = DownloadQueued
	}

	fail := func(err error) (Download, error) {
		return Download{}, fmt.Errorf("insert download %s: %w", hash, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM downloads WHERE torrent_hash = ?`, hash).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, insErr := tx.ExecContext(ctx, `
			INSERT INTO downloads (movie_id, torrent_hash, indexer, release_name, magnet, state, progress, added_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			d.MovieID, hash, d.Indexer, d.ReleaseName, d.Magnet, state, d.Progress, formatTime(s.now()))
		if insErr != nil {
			return fail(insErr)
		}
		if id, insErr = res.LastInsertId(); insErr != nil {
			return fail(insErr)
		}
	case err != nil:
		return fail(err)
	}

	row, err := scanDownload(tx.QueryRowContext(ctx, selectDownload+` WHERE id = ?`, id))
	if err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	return row, nil
}

// UpdateDownloadProgress writes what the poller last saw of a torrent, and
// returns an error wrapping ErrNotFound when no row holds that hash — a torrent
// in our category with no row is reported, never adopted.
//
// completedAt is the caller's decision, not this function's guesswork: it is not
// derived from state, because "which moment did this finish" is knowledge the
// poller has and a query does not. A nil completedAt **leaves the stored value
// alone** rather than clearing it, so a poller can pass nil on every tick after
// the first without erasing the moment the download finished, and without having
// to read the row back before each write.
func (s *Store) UpdateDownloadProgress(ctx context.Context, hash string, state string, progress float64, completedAt *time.Time) error {
	normalised := normaliseHash(hash)

	// Formatted here rather than handed to the driver as a time.Time, for the
	// reason timeLayout documents. any so that nil reaches SQLite as NULL, which
	// COALESCE then resolves to the column's current value.
	var completed any
	if completedAt != nil {
		completed = formatTime(*completedAt)
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE downloads
		SET state = ?, progress = ?, completed_at = COALESCE(?, completed_at)
		WHERE torrent_hash = ?`,
		state, progress, completed, normalised)
	if err != nil {
		return fmt.Errorf("update download %s: %w", normalised, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update download %s: %w", normalised, err)
	}
	if n == 0 {
		return fmt.Errorf("update download %s: %w", normalised, ErrNotFound)
	}
	return nil
}

// ListDownloads returns every download, newest first. id breaks ties on added_at
// because two dispatches can land inside one clock read.
func (s *Store) ListDownloads(ctx context.Context) ([]Download, error) {
	rows, err := s.db.QueryContext(ctx, selectDownload+` ORDER BY added_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list downloads: %w", err)
	}
	defer rows.Close()

	// Non-nil so an empty table marshals as [] rather than null.
	downloads := []Download{}
	for rows.Next() {
		d, err := scanDownload(rows)
		if err != nil {
			return nil, fmt.Errorf("list downloads: %w", err)
		}
		downloads = append(downloads, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list downloads: %w", err)
	}
	return downloads, nil
}

// GetDownloadByHash returns the download with that torrent hash in whatever case
// the caller has it, or an error wrapping ErrNotFound.
func (s *Store) GetDownloadByHash(ctx context.Context, hash string) (Download, error) {
	normalised := normaliseHash(hash)

	d, err := scanDownload(s.db.QueryRowContext(ctx, selectDownload+` WHERE torrent_hash = ?`, normalised))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Download{}, fmt.Errorf("get download %s: %w", normalised, ErrNotFound)
	case err != nil:
		return Download{}, fmt.Errorf("get download %s: %w", normalised, err)
	}
	return d, nil
}

func scanDownload(row rowScanner) (Download, error) {
	var (
		d Download
		// Both DATETIME columns land in `any` and go through asTime, which
		// documents why the driver's own representation cannot be assumed.
		addedAt     any
		completedAt any
	)
	if err := row.Scan(
		&d.ID, &d.MovieID, &d.TorrentHash, &d.Indexer, &d.ReleaseName, &d.Magnet,
		&d.State, &d.Progress, &addedAt, &completedAt,
	); err != nil {
		return Download{}, err
	}

	parsed, err := asTime(addedAt)
	if err != nil {
		return Download{}, fmt.Errorf("download %d added_at: %w", d.ID, err)
	}
	d.AddedAt = parsed

	if completedAt != nil {
		parsed, err := asTime(completedAt)
		if err != nil {
			return Download{}, fmt.Errorf("download %d completed_at: %w", d.ID, err)
		}
		d.CompletedAt = &parsed
	}
	return d, nil
}
