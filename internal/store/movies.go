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

// The values media_type carries. The column has existed since phase 1 so that TV
// would be additive later (decisions.md D6), and phase 11 is where that is spent.
//
// These two strings are the whole enum, and validMediaType is the only gate on
// it. That matters more than it looks: tmdbColumn below turns one of them into a
// SQL identifier, so anything that widens this set widens what can be
// interpolated into a query.
const (
	MediaTypeMovie = "movie"
	MediaTypeTV    = "tv"
)

// validMediaType reports whether m is one of the two values the schema means.
func validMediaType(m string) bool {
	return m == MediaTypeMovie || m == MediaTypeTV
}

// tmdbColumn is the column holding this media type's TMDB id.
//
// **They are two columns because TMDB's movie and tv id sequences overlap**, and
// `tmdb_id` is UNIQUE at table level: Severance is tv id 95396 and a film holds
// movie id 95396 too. Storing both in one column is not a rare loud collision, it
// is routine silent corruption — UpsertWanted's `WHERE tmdb_id = ?` probe would
// return the film's row and attach a season pack to it, with no error anywhere.
// Relaxing the constraint means rebuilding the table that holds the library, and
// migrate.go cannot do that; a second nullable column is additive and it can.
//
// The return value is one of two compile-time literals, chosen by a value
// validMediaType has already accepted, and it is interpolated because SQLite
// cannot bind an identifier — the same rule addColumn states. It must never
// become anything a caller supplies.
func tmdbColumn(mediaType string) string {
	if mediaType == MediaTypeTV {
		return "tmdb_tv_id"
	}
	return "tmdb_id"
}

// ErrNotFound reports that no row matched. GetMovie wraps it, so internal/api can
// turn a missing id into a 404 with errors.Is.
var ErrNotFound = errors.New("movie not found")

// ErrAlreadyMatched, ErrNotMatched and ErrTMDBIDTaken are the refusals MatchMovie
// and CorrectMatch answer with, and they are separate errors because they are
// different mistakes with different remedies: the first means this row already
// names a film and the correction route is the one to use, the second means there
// is no match here to correct yet, and the third means this film is already in the
// library under another folder.
//
// ErrAlreadyMatched and ErrNotMatched are exact inverses, one per method. Neither
// method infers which the caller meant: MatchMovie establishes a match and
// CorrectMatch replaces one, and a request that names the wrong one is answered
// rather than quietly redirected.
//
// All three are 409s in internal/api — the request is well-formed and the refusal
// is deliberate.
var (
	ErrAlreadyMatched = errors.New("movie is already matched to a tmdb id")
	ErrNotMatched     = errors.New("movie has no tmdb id to correct")
	ErrTMDBIDTaken    = errors.New("another movie already holds that tmdb id")
)

// Movie is one row of the movies table.
//
// The nullable columns are pointers rather than sql.NullInt64 / sql.NullString
// for two reasons. "Unmatched" (tmdb_id NULL) and "matched to TMDB id 0" must
// stay distinguishable (decisions.md D6), and a *int64 marshals to JSON null
// where a sql.NullInt64 marshals to {"Int64":0,"Valid":false} — phase 1's
// verification greps the API response for `.tmdb_id == null`.
//
// LibraryPath is nullable too: a movie can be wanted before it is on disk.
//
// Year and TMDBYear are two different facts and the column names say so. Year is
// the *folder's* year: it is parsed out of `Title (Year)`, it is written back out
// by library.DestFolder when the importer creates that folder, and it round-trips
// (link_test.go). TMDBYear is what TMDB says the film came out, recorded only
// when a human matched the row by hand and the two can therefore disagree. NULL
// is not missing data — it is the statement that the folder's year *is* TMDB's,
// which is true by construction for every row the scan matched, because
// SearchMovie rejects a candidate whose year disagrees. See MatchYear.
type Movie struct {
	ID     int64  `json:"id"`
	TMDBID *int64 `json:"tmdb_id"`
	// TMDBTVID is the same fact for a show, in its own column because the two id
	// sequences overlap — see tmdbColumn. Exactly one of the pair is ever
	// non-NULL, and which one is decided by media_type rather than by a caller.
	TMDBTVID    *int64     `json:"tmdb_tv_id"`
	Title       string     `json:"title"`
	Year        int        `json:"year"`
	TMDBYear    *int       `json:"tmdb_year"`
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

// MatchYear is the year to identify this film to anything outside curator by,
// which is TMDB's when it is known to differ from the folder's and the folder's
// otherwise.
//
// It exists because one column was doing two jobs. `year` names a directory on
// disk and it identifies a film to Jellyfin, and for every row the scan matched
// those two are the same number, so nothing distinguished them until a row could
// be matched by hand (T67). Jellyfin wants this one: D32 narrows its lookup with
// `years=` on the premise that "both sides take the year from TMDB", and a
// hand-matched row is the only thing that breaks it. The importer wants `year`,
// because that is the folder it already wrote.
func (m Movie) MatchYear() int {
	if m.TMDBYear != nil {
		return *m.TMDBYear
	}
	return m.Year
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
	// MediaType is required, and it did not used to be. Empty defaulted to
	// MediaTypeMovie, which was correct while there was one media type and became
	// a loaded gun the moment there were two: UpsertMovieByPath REWRITES
	// media_type from this field on every pass, so one construction site that
	// forgot it would silently relabel a show as a film — and the very next
	// prune would then delete it for sitting outside LIBRARY_MOVIES.
	//
	// Status keeps its default because it has no such second reader.
	MediaType string
	Status    string // empty defaults to StatusImported
	Quality   *string
	SizeBytes *int64
}

// TMDBMatch is what a TMDB lookup established about a movie.
//
// Title is optional: nil keeps the title parsed off the folder name, so a caller
// that does not trust a match enough to rename the row is not forced to.
//
// **Year writes `tmdb_year`, and emphatically not `year` — that was tried
// first and it reverted.** T67 wrote TMDB's year onto `year` itself, because
// that is what makes D32's `years=` Jellyfin narrowing find a hand-matched
// film. Measured on a real library: matched to a 2008 film, the row answered
// 2008 and a deep link, and after one rescan answered 2019 and a search again,
// because UpsertMovieByPath's SET list includes `year` and rewrites it from the
// folder name every pass.
//
// Freezing `year` for matched rows is the obvious repair and it is the wrong
// one, which is [D37]. `year` is not metadata the scan happens to own: it is
// half the directory name, and importer.go:105 builds the destination folder
// from the *row's* title and year. Move it to TMDB's and the next release
// imported for that film lands in a second folder beside the first. So `year`
// stays the folder's, TMDB's goes in a column of its own, and Movie.MatchYear
// is which one anything outside curator gets asked with.
//
// [D37]: ../../docs/decisions.md
type TMDBMatch struct {
	TMDBID     int64
	Title      *string
	Overview   *string
	PosterPath *string
	// Year is TMDB's release year, nil when TMDB has no release date for the
	// film. It is only ever written by MatchMovie: the scan's own matches agree
	// with the folder by construction, so recording it there would store a
	// number equal to the one beside it.
	Year *int
}

const selectMovie = `
SELECT id, tmdb_id, tmdb_tv_id, title, year, tmdb_year, media_type, overview, poster_path,
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

	if !validMediaType(m.MediaType) {
		return Movie{}, false, fmt.Errorf("upsert %s: %w", m.LibraryPath, badMediaType(m.MediaType))
	}
	mediaType := m.MediaType
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

// tmdbIDWrite is the SET clause that lands a TMDB id in the column this row's
// own media_type owns, leaving the other alone.
//
// **The row decides, not the caller.** A media type passed in beside the id
// would be a second source of truth about something the row already states, and
// getting it wrong puts a tv id in the movie column — which is the exact
// corruption tmdbColumn exists to prevent, arrived at from the other direction.
// The id is bound twice because only one branch of each CASE ever fires.
const tmdbIDWrite = `
	    tmdb_id    = CASE media_type WHEN 'tv' THEN tmdb_id ELSE ? END,
	    tmdb_tv_id = CASE media_type WHEN 'tv' THEN ? ELSE tmdb_tv_id END`

// currentMatch reads a row's media type and the TMDB id it currently holds,
// taking that id from whichever column the media type owns.
//
// It reads both columns and chooses in Go rather than interpolating tmdbColumn
// into the SELECT, because the media type is the thing being looked up: a query
// that needed the answer in order to name its own column could not run.
func currentMatch(ctx context.Context, tx *sql.Tx, id int64) (string, *int64, error) {
	var (
		mediaType     string
		movieID, tvID *int64
	)
	if err := tx.QueryRowContext(ctx,
		`SELECT media_type, tmdb_id, tmdb_tv_id FROM movies WHERE id = ?`, id,
	).Scan(&mediaType, &movieID, &tvID); err != nil {
		return "", nil, err
	}
	if mediaType == MediaTypeTV {
		return mediaType, tvID, nil
	}
	return mediaType, movieID, nil
}

// SetTMDBMetadata records a match against a movie row. It is the write half of
// MoviesMissingMetadata's read: without it a match found during a scan could not
// be persisted, and phase 1 is not done until GET /api/movies carries metadata.
//
// Both TMDB id columns are UNIQUE, so matching two folders to the same TMDB id
// fails here rather than silently collapsing them into one.
func (s *Store) SetTMDBMetadata(ctx context.Context, id int64, match TMDBMatch) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE movies
		SET`+tmdbIDWrite+`, title = COALESCE(?, title), overview = ?, poster_path = ?
		WHERE id = ?`,
		match.TMDBID, match.TMDBID, match.Title, match.Overview, match.PosterPath, id)
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

// MatchMovie records a match a human chose, against a row that has none.
// CorrectMatch is its counterpart for a row that already has one.
//
// It is deliberately not SetTMDBMetadata. That one serves the scan, where the row
// was selected by `WHERE tmdb_id IS NULL` and overwriting unconditionally is
// therefore correct; widening its contract for this caller would change phase 1's
// write for a phase 9 feature. This one is reached from a request, where every
// argument is somebody's typing, so it refuses instead of overwriting.
//
// The two refusals are decided by SELECT inside the transaction rather than by
// reading the UNIQUE violation back off the driver. adoptTwin does the same for
// the same column (imports.go), and a substring match on a modernc.org/sqlite
// message is a guard that stops working on a driver upgrade without failing a
// test — the row would silently 500 instead of 409.
//
// _txlock=immediate means the transaction holds the write lock throughout, so a
// concurrent scan cannot match this row between the check and the write.
func (s *Store) MatchMovie(ctx context.Context, id int64, match TMDBMatch) (Movie, error) {
	fail := func(err error) (Movie, error) {
		return Movie{}, fmt.Errorf("match movie %d to tmdb %d: %w", id, match.TMDBID, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	mediaType, existing, err := currentMatch(ctx, tx, id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fail(ErrNotFound)
	case err != nil:
		return fail(err)
	case existing != nil:
		// Still not "overwrite it", even now that CorrectMatch exists: replacing a
		// match is a different intent from establishing one, and a caller that
		// meant it says so with a different call. This one stays the write that
		// cannot destroy an answer somebody already gave.
		return fail(ErrAlreadyMatched)
	}

	// Scoped to this row's own id space. A film and a show may hold the same
	// number, so probing both columns would refuse a legitimate match with a
	// sentence about a film the user has never heard of.
	var other int64
	switch err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT id FROM movies WHERE %s = ?`, tmdbColumn(mediaType)),
		match.TMDBID).Scan(&other); {
	case errors.Is(err, sql.ErrNoRows):
		// The only outcome that proceeds.
	case err != nil:
		return fail(err)
	default:
		return fail(ErrTMDBIDTaken)
	}

	if err := writeMatch(ctx, tx, id, match); err != nil {
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

// CorrectMatch repoints a row that is already matched at a different film.
//
// **It overwrites in one statement and never lets the row pass through NULL, and
// that is the whole design rather than an implementation detail.** The obvious
// shape — clear tmdb_id, overview, poster_path and tmdb_year, then reuse
// MatchMovie — is broken twice over, because a row with a NULL tmdb_id is not an
// inert row waiting for a human:
//
//   - MoviesMissingMetadata selects exactly `WHERE tmdb_id IS NULL`, and the scan
//     feeds every row it returns to SetTMDBMetadata, which overwrites
//     unconditionally. A scan landing between the clear and the match re-matches
//     the row **from its folder name** — which is the thing that produced the
//     wrong match in the first place, so it would restore precisely the film
//     being corrected away from.
//   - adoptTwin writes a tmdb_id onto a row whose own is NULL (imports.go), so an
//     import completing in that window could repoint it too.
//
// Neither needs a slow human to go wrong: the clear and the match are two HTTP
// requests, and a scan is one button. Overwriting inside a single transaction
// removes the window rather than narrowing it.
//
// The refusals are MatchMovie's, inverted and with one relaxation. A row with no
// tmdb_id is ErrNotMatched — there is nothing here to correct and POST is the
// route for it. Another row holding the target id is still ErrTMDBIDTaken, but the
// probe ignores this row, so re-picking the film already stored is allowed: it
// costs a fresh overview, poster and tmdb_year, which is a refresh and not a
// collision.
func (s *Store) CorrectMatch(ctx context.Context, id int64, match TMDBMatch) (Movie, error) {
	fail := func(err error) (Movie, error) {
		return Movie{}, fmt.Errorf("correct movie %d to tmdb %d: %w", id, match.TMDBID, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	mediaType, existing, err := currentMatch(ctx, tx, id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fail(ErrNotFound)
	case err != nil:
		return fail(err)
	case existing == nil:
		return fail(ErrNotMatched)
	}

	// `AND id != ?` is the one difference from MatchMovie's probe. Without it a
	// correction that lands on the film already stored would collide with itself.
	// The column is this row's own, for the reason MatchMovie's probe states.
	var other int64
	switch err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT id FROM movies WHERE %s = ? AND id != ?`, tmdbColumn(mediaType)),
		match.TMDBID, id).Scan(&other); {
	case errors.Is(err, sql.ErrNoRows):
		// The only outcome that proceeds.
	case err != nil:
		return fail(err)
	default:
		return fail(ErrTMDBIDTaken)
	}

	if err := writeMatch(ctx, tx, id, match); err != nil {
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

// writeMatch is the UPDATE MatchMovie and CorrectMatch share, and it is one
// function so the two cannot drift: a row corrected by hand and a row matched by
// hand have to be the same shape afterwards, or a second correction would behave
// differently from the first.
//
// The columns are the ones SetTMDBMetadata writes, plus `tmdb_year` — and still
// not `year`, which stays the folder's. The scan rewrites `year` from the folder
// on every pass and the importer builds the folder back out of it, so this is the
// one TMDB fact that needs somewhere else to live. See TMDBMatch.
//
// It takes the transaction rather than the Store because both callers have
// already decided, under the write lock, that the row is theirs to write.
func writeMatch(ctx context.Context, tx *sql.Tx, id int64, match TMDBMatch) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE movies
		SET`+tmdbIDWrite+`, title = COALESCE(?, title), overview = ?, poster_path = ?,
		    tmdb_year = ?
		WHERE id = ?`,
		match.TMDBID, match.TMDBID, match.Title, match.Overview, match.PosterPath,
		match.Year, id)
	return err
}

// ListMovies returns every row of one media type, newest first. id breaks ties
// on added_at because a scan stamps a whole batch of inserts from one clock read.
//
// **mediaType is required rather than optional, and there is no value meaning
// "both".** Every caller of this and of the three reads below wants one kind of
// thing, and a default that reads fine at the call site is how a show ends up
// rendered as a film on the library screen. Making it a parameter turned every
// call site into a decision somebody had to make on purpose.
func (s *Store) ListMovies(ctx context.Context, mediaType string) ([]Movie, error) {
	if !validMediaType(mediaType) {
		return nil, fmt.Errorf("list movies: %w", badMediaType(mediaType))
	}
	movies, err := s.queryMovies(ctx,
		selectMovie+` WHERE media_type = ? ORDER BY added_at DESC, id DESC`, mediaType)
	if err != nil {
		return nil, fmt.Errorf("list movies: %w", err)
	}
	return movies, nil
}

// badMediaType is the refusal every media-scoped read shares. It is an error
// rather than a silent fallback to "movie" because a caller that got this wrong
// asked the wrong question, and answering the other one is how the two get mixed.
func badMediaType(mediaType string) error {
	return fmt.Errorf("%q is not a media type (%q or %q)", mediaType, MediaTypeMovie, MediaTypeTV)
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

// MoviesMissingMetadata returns the rows of one media type TMDB has not matched.
// It is the work list for the matching pass, and what "record NULL and surface
// it, never guess" (decisions.md D9) means in practice.
//
// **The media_type predicate is the load-bearing half, not the column choice.**
// Without it every show is on this list on every scan — a show's `tmdb_id` is
// NULL by construction — and the caller feeds the list to TMDB's /search/movie
// and writes the answer back with SetTMDBMetadata, which overwrites
// unconditionally by design. For Fargo, Watchmen, Hannibal, Westworld, Dune and
// Snowpiercer that lookup SUCCEEDS, and the show quietly acquires a film's id,
// overview and poster. No error, no log, and it re-fires every scan.
func (s *Store) MoviesMissingMetadata(ctx context.Context, mediaType string) ([]Movie, error) {
	if !validMediaType(mediaType) {
		return nil, fmt.Errorf("list movies missing metadata: %w", badMediaType(mediaType))
	}
	movies, err := s.queryMovies(ctx, fmt.Sprintf(
		selectMovie+` WHERE media_type = ? AND %s IS NULL ORDER BY added_at DESC, id DESC`,
		tmdbColumn(mediaType)), mediaType)
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
		&m.ID, &m.TMDBID, &m.TMDBTVID, &m.Title, &m.Year, &m.TMDBYear, &m.MediaType, &m.Overview, &m.PosterPath,
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

// LibraryState is what a TMDB card needs to know about a film curator already
// has.
//
// Deliberately not a Movie. A card needs four facts, and reading thirteen
// columns for every row in the library to annotate forty posters is a query
// nobody should make twice.
type LibraryState struct {
	MovieID     int64
	Status      string  // movies.status, as stored
	LibraryPath *string // NULL until the film is actually on disk
	Downloading bool
}

// LibraryByTMDBID indexes the library by tmdb_id — the annotation behind
// "already in your library" on a poster.
//
// It returns the WHOLE set rather than taking a list of ids. The library is 29
// films and will be hundreds; one query with no placeholders beats building an
// IN clause per request, and it can never meet SQLite's variable limit. Rows
// with tmdb_id NULL are excluded, because those are exactly the ones no TMDB
// card could ever match.
//
// Downloading is an EXISTS over downloads rather than movies.status, and that is
// not a detail. **store.StatusDownloading is declared and never written**:
// UpsertWanted inserts 'wanted' and the importer writes 'imported', so
// nothing ever sets it. A card reading movies.status would label a film whose
// torrent is at 60% as "wanted". The state that is true lives in
// downloads.state, where 'imported' and 'failed' are the two that do not count
// as in flight.
//
// **One media type per call, and the reason is that the two id spaces overlap.**
// A single map keyed on a bare number would badge a film's poster "already in
// your library" because a show happens to hold that number as its tv id — and
// worse, the fourth caller is a dispatch guard, so it would refuse the grab with
// a sentence naming a film the user never asked about.
func (s *Store) LibraryByTMDBID(ctx context.Context, mediaType string) (map[int64]LibraryState, error) {
	if !validMediaType(mediaType) {
		return nil, fmt.Errorf("library by tmdb id: %w", badMediaType(mediaType))
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT m.%[1]s, m.id, m.status, m.library_path,
		       EXISTS (
		           SELECT 1 FROM downloads d
		            WHERE d.movie_id = m.id
		              AND d.state NOT IN (?, ?)
		       ) AS downloading
		  FROM movies m
		 WHERE m.media_type = ? AND m.%[1]s IS NOT NULL`, tmdbColumn(mediaType)),
		DownloadImported, DownloadFailed, mediaType)
	if err != nil {
		return nil, fmt.Errorf("library by tmdb_id: %w", err)
	}
	defer rows.Close()

	// Non-nil so a caller can index it without checking, and so an empty library
	// is an empty map rather than a nil one.
	byID := map[int64]LibraryState{}
	for rows.Next() {
		var (
			tmdbID int64
			state  LibraryState
		)
		if err := rows.Scan(&tmdbID, &state.MovieID, &state.Status, &state.LibraryPath, &state.Downloading); err != nil {
			return nil, fmt.Errorf("library by tmdb_id: %w", err)
		}
		byID[tmdbID] = state
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("library by tmdb_id: %w", err)
	}
	return byID, nil
}

// OnDisk is one row that claims a folder, and whether anything is still fetching
// it. Title and Year ride along because the caller logs a removal by name, and
// after the DELETE there is nothing left to look them up by.
type OnDisk struct {
	ID    int64
	Title string
	Year  int
	// MediaType is here rather than in the query's WHERE clause, and that is
	// deliberate: this list is what a prune is allowed to CONSIDER, and a row
	// filtered out of it is a row the prune cannot see. Since a film and a show
	// live under different roots, the caller needs the media type in order to ask
	// "is this path inside the root that owns it" — but it must be asked about
	// every row, not only the ones this scan happened to walk. A row whose root
	// was not scanned falls through to "kept", which is D33's asymmetry doing
	// exactly its job.
	MediaType   string
	LibraryPath string
	Downloading bool
}

// MoviesOnDisk lists every row a scan is allowed to consider removing.
//
// Two exclusions, and neither is a branch the caller should have to remember.
//
// A WANTED film has library_path NULL — it was never on disk, so "there is no film
// in its folder" is not a statement about it, and it has no folder to join on
// either. And a film with a download IN FLIGHT is mid-import: the importer creates
// the destination folder and only then hardlinks into it, so there is a window in
// which the folder legitimately holds nothing at all, and pruning then would take
// the downloads rows with it (docs/decisions.md D33).
//
// "In flight" is EXISTS over downloads with state NOT IN (imported, failed) — the
// same definition LibraryByTMDBID uses for the badge on a poster, deliberately, so
// the screen and the pruner can never disagree about what curator is already
// getting.
func (s *Store) MoviesOnDisk(ctx context.Context) ([]OnDisk, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.title, m.year, m.media_type, m.library_path,
		       EXISTS (
		           SELECT 1 FROM downloads d
		            WHERE d.movie_id = m.id
		              AND d.state NOT IN (?, ?)
		       ) AS downloading
		  FROM movies m
		 WHERE m.library_path IS NOT NULL AND TRIM(m.library_path) <> ''
		 ORDER BY m.id`,
		DownloadImported, DownloadFailed)
	if err != nil {
		return nil, fmt.Errorf("movies on disk: %w", err)
	}
	defer rows.Close()

	on := []OnDisk{}
	for rows.Next() {
		var row OnDisk
		if err := rows.Scan(&row.ID, &row.Title, &row.Year, &row.MediaType, &row.LibraryPath, &row.Downloading); err != nil {
			return nil, fmt.Errorf("movies on disk: %w", err)
		}
		on = append(on, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("movies on disk: %w", err)
	}
	return on, nil
}
