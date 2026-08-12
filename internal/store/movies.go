package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Values the status column carries. Phase 1 only ever writes StatusImported; the
// other two arrive with the download pipeline in phase 3.
const (
	StatusWanted      = "wanted"
	StatusDownloading = "downloading"
	StatusImported    = "imported"
)

// MediaTypeMovie is the default media_type. It exists as a column from the start
// so TV is additive later (decisions.md D6).
const MediaTypeMovie = "movie"

// ErrNotFound reports that no row matched. GetMovie wraps it, so internal/api can
// turn a missing id into a 404 with errors.Is.
var ErrNotFound = errors.New("movie not found")

// Movie is one row of the movies table.
//
// The nullable columns are pointers rather than sql.NullInt64 / sql.NullString
// for two reasons. "Unmatched" (tmdb_id NULL) and "matched to TMDB id 0" must
// stay distinguishable (decisions.md D6), and a *int64 marshals to JSON null
// where a sql.NullInt64 marshals to {"Int64":0,"Valid":false} — phase 1's
// verification greps the API response for `.tmdb_id == null`.
//
// LibraryPath is nullable too: a movie can be wanted before it is on disk.
type Movie struct {
	ID          int64      `json:"id"`
	TMDBID      *int64     `json:"tmdb_id"`
	Title       string     `json:"title"`
	Year        int        `json:"year"`
	MediaType   string     `json:"media_type"`
	Overview    *string    `json:"overview"`
	PosterPath  *string    `json:"poster_path"`
	Status      string     `json:"status"`
	LibraryPath *string    `json:"library_path"`
	Quality     *string    `json:"quality"`
	SizeBytes   *int64     `json:"size_bytes"`
	AddedAt     time.Time  `json:"added_at"`
	ImportedAt  *time.Time `json:"imported_at"`
}

// ScannedMovie is everything a library scan knows about a folder.
//
// It carries no TMDB fields on purpose: a rescan re-reads title, year and size
// off disk but knows nothing about tmdb_id, overview or poster_path, and must not
// overwrite a match an earlier run established.
type ScannedMovie struct {
	// LibraryPath is the identity key for the upsert. A folder is identified by
	// where it is, not by whether TMDB recognised it, so rescans are idempotent.
	LibraryPath string
	Title       string
	Year        int
	MediaType   string // empty defaults to MediaTypeMovie
	Status      string // empty defaults to StatusImported
	Quality     *string
	SizeBytes   *int64
}

// TMDBMatch is what a TMDB lookup established about a movie.
//
// Title is optional: nil keeps the title parsed off the folder name, so a caller
// that does not trust a match enough to rename the row is not forced to.
type TMDBMatch struct {
	TMDBID     int64
	Title      *string
	Overview   *string
	PosterPath *string
}

const selectMovie = `
SELECT id, tmdb_id, title, year, media_type, overview, poster_path,
       status, library_path, quality, size_bytes, added_at, imported_at
FROM movies`

// UpsertMovieByPath inserts the scanned folder, or updates whichever row already
// claims its library_path, and reports which of the two happened — internal/api
// counts the inserts as a scan's `added`.
//
// The division of authority is: the scan owns title, year, media_type, status,
// quality and size_bytes, and those are written exactly as supplied, NULL
// included. TMDB owns tmdb_id, overview and poster_path, and an upsert never
// touches them. added_at and imported_at are likewise left alone — added_at is
// when curator first saw the folder, not when it last looked at it.
func (s *Store) UpsertMovieByPath(ctx context.Context, m ScannedMovie) (Movie, bool, error) {
	if m.LibraryPath == "" {
		return Movie{}, false, errors.New("upsert: library_path is empty")
	}

	mediaType := m.MediaType
	if mediaType == "" {
		mediaType = MediaTypeMovie
	}
	status := m.Status
	if status == "" {
		status = StatusImported
	}

	fail := func(err error) (Movie, bool, error) {
		return Movie{}, false, fmt.Errorf("upsert %s: %w", m.LibraryPath, err)
	}

	// The lookup and the write must not race a concurrent scan, hence the
	// transaction; _txlock=immediate means it holds the write lock throughout.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM movies WHERE library_path = ?`, m.LibraryPath).Scan(&id)
	inserted := errors.Is(err, sql.ErrNoRows)
	switch {
	case inserted:
		res, insErr := tx.ExecContext(ctx, `
			INSERT INTO movies (title, year, media_type, status, library_path, quality, size_bytes, added_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			m.Title, m.Year, mediaType, status, m.LibraryPath, m.Quality, m.SizeBytes, formatTime(s.now()))
		if insErr != nil {
			return fail(insErr)
		}
		if id, insErr = res.LastInsertId(); insErr != nil {
			return fail(insErr)
		}
	case err != nil:
		return fail(err)
	default:
		if _, updErr := tx.ExecContext(ctx, `
			UPDATE movies
			SET title = ?, year = ?, media_type = ?, status = ?, quality = ?, size_bytes = ?
			WHERE id = ?`,
			m.Title, m.Year, mediaType, status, m.Quality, m.SizeBytes, id); updErr != nil {
			return fail(updErr)
		}
	}

	row, err := scanMovie(tx.QueryRowContext(ctx, selectMovie+` WHERE id = ?`, id))
	if err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	return row, inserted, nil
}

// SetTMDBMetadata records a match against a movie row. It is the write half of
// MoviesMissingMetadata's read: without it a match found during a scan could not
// be persisted, and phase 1 is not done until GET /api/movies carries metadata.
//
// tmdb_id is UNIQUE, so matching two folders to the same TMDB id fails here
// rather than silently collapsing them into one.
func (s *Store) SetTMDBMetadata(ctx context.Context, id int64, match TMDBMatch) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE movies
		SET tmdb_id = ?, title = COALESCE(?, title), overview = ?, poster_path = ?
		WHERE id = ?`,
		match.TMDBID, match.Title, match.Overview, match.PosterPath, id)
	if err != nil {
		return fmt.Errorf("set tmdb metadata %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set tmdb metadata %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("set tmdb metadata %d: %w", id, ErrNotFound)
	}
	return nil
}

// ListMovies returns every movie, newest first. id breaks ties on added_at
// because a scan stamps a whole batch of inserts from one clock read.
func (s *Store) ListMovies(ctx context.Context) ([]Movie, error) {
	movies, err := s.queryMovies(ctx, selectMovie+` ORDER BY added_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list movies: %w", err)
	}
	return movies, nil
}

// GetMovie returns one movie by id, or an error wrapping ErrNotFound if there is
// no such row.
func (s *Store) GetMovie(ctx context.Context, id int64) (Movie, error) {
	m, err := scanMovie(s.db.QueryRowContext(ctx, selectMovie+` WHERE id = ?`, id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Movie{}, fmt.Errorf("get movie %d: %w", id, ErrNotFound)
	case err != nil:
		return Movie{}, fmt.Errorf("get movie %d: %w", id, err)
	}
	return m, nil
}

// MoviesMissingMetadata returns the rows TMDB has not matched — tmdb_id IS NULL.
// It is the work list for the matching pass, and what "record NULL and surface
// it, never guess" (decisions.md D9) means in practice.
func (s *Store) MoviesMissingMetadata(ctx context.Context) ([]Movie, error) {
	movies, err := s.queryMovies(ctx, selectMovie+` WHERE tmdb_id IS NULL ORDER BY added_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list movies missing metadata: %w", err)
	}
	return movies, nil
}

func (s *Store) queryMovies(ctx context.Context, query string, args ...any) ([]Movie, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Non-nil so the caller marshals an empty library as [] rather than null.
	movies := []Movie{}
	for rows.Next() {
		m, err := scanMovie(rows)
		if err != nil {
			return nil, err
		}
		movies = append(movies, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return movies, nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanMovie(row rowScanner) (Movie, error) {
	var (
		m Movie
		// The two DATETIME columns land in `any` and go through asTime, which
		// documents why the driver's own representation cannot be assumed.
		addedAt    any
		importedAt any
	)
	if err := row.Scan(
		&m.ID, &m.TMDBID, &m.Title, &m.Year, &m.MediaType, &m.Overview, &m.PosterPath,
		&m.Status, &m.LibraryPath, &m.Quality, &m.SizeBytes, &addedAt, &importedAt,
	); err != nil {
		return Movie{}, err
	}

	parsed, err := asTime(addedAt)
	if err != nil {
		return Movie{}, fmt.Errorf("movie %d added_at: %w", m.ID, err)
	}
	m.AddedAt = parsed

	if importedAt != nil {
		parsed, err := asTime(importedAt)
		if err != nil {
			return Movie{}, fmt.Errorf("movie %d imported_at: %w", m.ID, err)
		}
		m.ImportedAt = &parsed
	}
	return m, nil
}
