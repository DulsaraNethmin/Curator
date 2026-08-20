package store

import (
	"context"
	"errors"
	"testing"
)

// The property the whole of T67 rests on: a match a human chose is not undone by
// the next scan.
//
// TestUpsertDoesNotClobberTMDBMetadata already proves it for the scanner's own
// write. This asserts it for MatchMovie's, because the two write the same columns
// through different code and a future change to one of them would otherwise be
// caught for the scan and missed for the human.
func TestAManualMatchSurvivesARescan(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const path = "/movies/Some Home Video (2019)"

	m, _, err := s.UpsertMovieByPath(ctx, scanned(path))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if m.TMDBID != nil {
		t.Fatalf("a freshly scanned folder should be unmatched, got tmdb_id %v", *m.TMDBID)
	}

	matched, err := s.MatchMovie(ctx, m.ID, TMDBMatch{
		TMDBID:     299536,
		Overview:   ptrString("As the Avengers and their allies have continued..."),
		PosterPath: ptrString("/7WsyChQLEftFiDOVTGkv3hFpyyt.jpg"),
	})
	if err != nil {
		t.Fatalf("MatchMovie: %v", err)
	}
	if matched.TMDBID == nil || *matched.TMDBID != 299536 {
		t.Fatalf("returned row's tmdb_id = %v, want 299536", matched.TMDBID)
	}
	// The title is the folder's, not TMDB's: Title was nil, so COALESCE keeps it.
	if matched.Title != scanned(path).Title {
		t.Errorf("title = %q, want the folder's %q", matched.Title, scanned(path).Title)
	}
	// The year is the folder's and stays the folder's, even now that TMDBMatch
	// has a Year: it writes tmdb_year. The scan rewrites `year` on every pass and
	// the importer builds the folder name back out of it, so TMDB's year needed a
	// column of its own rather than this one (D37).
	if matched.Year != scanned(path).Year {
		t.Errorf("year = %d, want the folder's %d", matched.Year, scanned(path).Year)
	}

	// Rescan: the same folder, no TMDB knowledge at all, exactly as a scan runs.
	if _, _, err := s.UpsertMovieByPath(ctx, scanned(path)); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	got, err := s.GetMovie(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMovie: %v", err)
	}
	if got.TMDBID == nil || *got.TMDBID != 299536 {
		t.Errorf("tmdb_id = %v after rescan, want 299536 — a manual match must survive", got.TMDBID)
	}
	if got.Overview == nil || *got.Overview != "As the Avengers and their allies have continued..." {
		t.Errorf("overview = %v after rescan, want it preserved", got.Overview)
	}

	// And the row is no longer work for the matching pass, which is the other half
	// of why it survives: the scan never looks at it again.
	missing, err := s.MoviesMissingMetadata(ctx, MediaTypeMovie)
	if err != nil {
		t.Fatalf("MoviesMissingMetadata: %v", err)
	}
	for _, row := range missing {
		if row.ID == m.ID {
			t.Errorf("movie %d is still in the match work list after being matched", m.ID)
		}
	}
}

// A row that already names a film is not the row to correct, and adoptTwin
// decided this for the same column: a match already established is not worth
// overwriting with one a client sent.
func TestMatchMovieRefusesAnAlreadyMatchedRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	m, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Avengers - Infinity War (2018)"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := s.MatchMovie(ctx, m.ID, TMDBMatch{TMDBID: 299536}); err != nil {
		t.Fatalf("first MatchMovie: %v", err)
	}

	_, err = s.MatchMovie(ctx, m.ID, TMDBMatch{TMDBID: 24428})
	if !errors.Is(err, ErrAlreadyMatched) {
		t.Fatalf("second MatchMovie error = %v, want ErrAlreadyMatched", err)
	}

	got, err := s.GetMovie(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMovie: %v", err)
	}
	if got.TMDBID == nil || *got.TMDBID != 299536 {
		t.Errorf("tmdb_id = %v, want the original 299536 left alone", got.TMDBID)
	}
}

// tmdb_id is UNIQUE. The refusal is a named error rather than the driver's
// constraint message, so internal/api can answer 409 without parsing SQLite.
func TestMatchMovieRefusesAnIDAnotherRowHolds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Avengers - Infinity War (2018)"))
	if err != nil {
		t.Fatalf("upsert first: %v", err)
	}
	second, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Infinity War (2018)"))
	if err != nil {
		t.Fatalf("upsert second: %v", err)
	}
	if _, err := s.MatchMovie(ctx, first.ID, TMDBMatch{TMDBID: 299536}); err != nil {
		t.Fatalf("MatchMovie first: %v", err)
	}

	_, err = s.MatchMovie(ctx, second.ID, TMDBMatch{TMDBID: 299536})
	if !errors.Is(err, ErrTMDBIDTaken) {
		t.Fatalf("MatchMovie second error = %v, want ErrTMDBIDTaken", err)
	}

	// Neither row moved.
	gotFirst, err := s.GetMovie(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetMovie first: %v", err)
	}
	if gotFirst.TMDBID == nil || *gotFirst.TMDBID != 299536 {
		t.Errorf("first row's tmdb_id = %v, want 299536", gotFirst.TMDBID)
	}
	gotSecond, err := s.GetMovie(ctx, second.ID)
	if err != nil {
		t.Fatalf("GetMovie second: %v", err)
	}
	if gotSecond.TMDBID != nil {
		t.Errorf("second row's tmdb_id = %v, want it still NULL", *gotSecond.TMDBID)
	}
}

func TestMatchMovieOnAMissingRowIsErrNotFound(t *testing.T) {
	s := newTestStore(t)

	_, err := s.MatchMovie(context.Background(), 4242, TMDBMatch{TMDBID: 299536})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("MatchMovie error = %v, want ErrNotFound", err)
	}
}

// SetTMDBMetadata keeps overwriting unconditionally. It is the scan's write, its
// caller selects on tmdb_id IS NULL already, and narrowing it here would change
// phase 1's contract for a phase 9 feature — so this asserts the two writes stay
// different on purpose.
func TestSetTMDBMetadataStillOverwrites(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	m, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Avengers - Infinity War (2018)"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetTMDBMetadata(ctx, m.ID, TMDBMatch{TMDBID: 299536}); err != nil {
		t.Fatalf("first SetTMDBMetadata: %v", err)
	}
	if err := s.SetTMDBMetadata(ctx, m.ID, TMDBMatch{TMDBID: 24428}); err != nil {
		t.Fatalf("second SetTMDBMetadata: %v", err)
	}

	got, err := s.GetMovie(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMovie: %v", err)
	}
	if got.TMDBID == nil || *got.TMDBID != 24428 {
		t.Errorf("tmdb_id = %v, want 24428 — SetTMDBMetadata must still overwrite", got.TMDBID)
	}
}

// T68's whole claim, and the one T67 could not make: TMDB's year is recorded,
// it is recorded somewhere the scan does not own, and it is still there after a
// rescan.
//
// T67 wrote TMDB's year onto `year` itself and measured it revert on the next
// pass — a 2019 folder matched to a 2008 film answered 2008 and a deep link,
// then 2019 and a search. This is that measurement turned into an assertion,
// against the column that survives it.
func TestAManualMatchsTMDBYearSurvivesARescan(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const path = "/movies/Some Home Video (2019)"

	m, _, err := s.UpsertMovieByPath(ctx, scanned(path))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// The folder says 2018 (scanned()); Iron Man came out in 2008. A row whose
	// two years agree could not tell these apart.
	if _, err := s.MatchMovie(ctx, m.ID, TMDBMatch{TMDBID: 1726, Year: ptrInt(2008)}); err != nil {
		t.Fatalf("MatchMovie: %v", err)
	}

	for _, pass := range []string{"straight after the match", "after one rescan", "after two"} {
		got, err := s.GetMovie(ctx, m.ID)
		if err != nil {
			t.Fatalf("GetMovie %s: %v", pass, err)
		}
		if got.Year != 2018 {
			t.Errorf("%s: year = %d, want the folder's 2018 — the scan owns this column", pass, got.Year)
		}
		if got.TMDBYear == nil || *got.TMDBYear != 2008 {
			t.Errorf("%s: tmdb_year = %v, want 2008 — this is the column T67 could not keep", pass, got.TMDBYear)
		}
		if got.MatchYear() != 2008 {
			t.Errorf("%s: MatchYear() = %d, want TMDB's 2008", pass, got.MatchYear())
		}
		if _, _, err := s.UpsertMovieByPath(ctx, scanned(path)); err != nil {
			t.Fatalf("rescan %s: %v", pass, err)
		}
	}
}

// A row nobody hand-matched has no tmdb_year, and MatchYear answers the folder's
// year for it. That NULL is the load-bearing half of the design: every row the
// scan matched already agrees with TMDB, because SearchMovie rejects a candidate
// whose year disagrees, so the column needs no backfill to be correct.
func TestMatchYearFallsBackToTheFoldersYear(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	m, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Avengers - Infinity War (2018)"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if m.TMDBYear != nil {
		t.Errorf("tmdb_year = %v on a freshly scanned row, want NULL", *m.TMDBYear)
	}
	if m.MatchYear() != 2018 {
		t.Errorf("MatchYear() = %d, want the folder's 2018", m.MatchYear())
	}

	// The scan's own match does not write it either, for the same reason.
	if err := s.SetTMDBMetadata(ctx, m.ID, TMDBMatch{TMDBID: 299536}); err != nil {
		t.Fatalf("SetTMDBMetadata: %v", err)
	}
	got, err := s.GetMovie(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMovie: %v", err)
	}
	if got.TMDBYear != nil {
		t.Errorf("tmdb_year = %v after a scan match, want NULL — the two agree by construction", *got.TMDBYear)
	}
	if got.MatchYear() != 2018 {
		t.Errorf("MatchYear() = %d, want 2018", got.MatchYear())
	}
}

// TMDB has no release date for some films, and 0 is not a year. NULL keeps
// MatchYear on the folder's year rather than narrowing a Jellyfin lookup to a
// year nothing was released in.
func TestAMatchWithNoTMDBYearLeavesTheColumnNull(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	m, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Avengers - Infinity War (2018)"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	matched, err := s.MatchMovie(ctx, m.ID, TMDBMatch{TMDBID: 299536})
	if err != nil {
		t.Fatalf("MatchMovie: %v", err)
	}
	if matched.TMDBYear != nil {
		t.Errorf("tmdb_year = %v, want NULL for a film with no release date", *matched.TMDBYear)
	}
	if matched.MatchYear() != 2018 {
		t.Errorf("MatchYear() = %d, want the folder's 2018", matched.MatchYear())
	}
}

// The happy path of a correction: every column TMDB owns is replaced, and every
// column the folder owns is left exactly where it was.
func TestCorrectMatchRepointsTheRowAndRefreshesEveryTMDBColumn(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const path = "/movies/Avengers - Infinity War (2018)"

	m, _, err := s.UpsertMovieByPath(ctx, scanned(path))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Matched to the wrong film, which is the state this method exists for.
	if _, err := s.MatchMovie(ctx, m.ID, TMDBMatch{
		TMDBID:     1726,
		Overview:   ptrString("Tony Stark builds an armored suit."),
		PosterPath: ptrString("/iron.jpg"),
		Year:       ptrInt(2008),
	}); err != nil {
		t.Fatalf("MatchMovie: %v", err)
	}

	corrected, err := s.CorrectMatch(ctx, m.ID, TMDBMatch{
		TMDBID:     299536,
		Overview:   ptrString("As the Avengers and their allies have continued..."),
		PosterPath: ptrString("/infinity.jpg"),
		Year:       ptrInt(2018),
	})
	if err != nil {
		t.Fatalf("CorrectMatch: %v", err)
	}

	if corrected.TMDBID == nil || *corrected.TMDBID != 299536 {
		t.Errorf("tmdb_id = %v, want 299536", corrected.TMDBID)
	}
	// All four TMDB columns move together. A correction that changed the id and
	// left the previous film's overview and poster behind would be worse than no
	// correction at all — the row would name one film and describe another.
	if corrected.Overview == nil || *corrected.Overview != "As the Avengers and their allies have continued..." {
		t.Errorf("overview = %v, want the new film's", corrected.Overview)
	}
	if corrected.PosterPath == nil || *corrected.PosterPath != "/infinity.jpg" {
		t.Errorf("poster_path = %v, want the new film's", corrected.PosterPath)
	}
	if corrected.TMDBYear == nil || *corrected.TMDBYear != 2018 {
		t.Errorf("tmdb_year = %v, want the new film's 2018", corrected.TMDBYear)
	}

	// And the folder's half is untouched, exactly as MatchMovie leaves it (D37).
	if corrected.Title != scanned(path).Title {
		t.Errorf("title = %q, want the folder's %q", corrected.Title, scanned(path).Title)
	}
	if corrected.Year != scanned(path).Year {
		t.Errorf("year = %d, want the folder's %d", corrected.Year, scanned(path).Year)
	}
	if corrected.LibraryPath == nil || *corrected.LibraryPath != path {
		t.Errorf("library_path = %v, want %q", corrected.LibraryPath, path)
	}
}

// The inverse of MatchMovie's refusal, and it is a refusal rather than a silent
// fallback to MatchMovie: replacing a match and establishing one are different
// intents, and a caller that got the method wrong is told which one it wanted.
func TestCorrectMatchRefusesARowWithNoMatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	m, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Avengers - Infinity War (2018)"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	_, err = s.CorrectMatch(ctx, m.ID, TMDBMatch{TMDBID: 299536})
	if !errors.Is(err, ErrNotMatched) {
		t.Fatalf("CorrectMatch error = %v, want ErrNotMatched", err)
	}

	got, err := s.GetMovie(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMovie: %v", err)
	}
	if got.TMDBID != nil {
		t.Errorf("tmdb_id = %v, want it still NULL — a refused correction writes nothing", *got.TMDBID)
	}
}

// tmdb_id is UNIQUE, and correcting onto a film another folder already holds is
// the same collision MatchMovie answers, reached from the other direction.
func TestCorrectMatchRefusesAnIDAnotherRowHolds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Avengers - Infinity War (2018)"))
	if err != nil {
		t.Fatalf("upsert first: %v", err)
	}
	second, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Infinity War (2018)"))
	if err != nil {
		t.Fatalf("upsert second: %v", err)
	}
	if _, err := s.MatchMovie(ctx, first.ID, TMDBMatch{TMDBID: 299536}); err != nil {
		t.Fatalf("MatchMovie first: %v", err)
	}
	if _, err := s.MatchMovie(ctx, second.ID, TMDBMatch{TMDBID: 1726}); err != nil {
		t.Fatalf("MatchMovie second: %v", err)
	}

	_, err = s.CorrectMatch(ctx, second.ID, TMDBMatch{TMDBID: 299536})
	if !errors.Is(err, ErrTMDBIDTaken) {
		t.Fatalf("CorrectMatch error = %v, want ErrTMDBIDTaken", err)
	}

	// Neither row moved: the refusal is decided before the write, in the same
	// transaction, so there is nothing half-applied to undo.
	gotFirst, err := s.GetMovie(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetMovie first: %v", err)
	}
	if gotFirst.TMDBID == nil || *gotFirst.TMDBID != 299536 {
		t.Errorf("first row's tmdb_id = %v, want 299536", gotFirst.TMDBID)
	}
	gotSecond, err := s.GetMovie(ctx, second.ID)
	if err != nil {
		t.Fatalf("GetMovie second: %v", err)
	}
	if gotSecond.TMDBID == nil || *gotSecond.TMDBID != 1726 {
		t.Errorf("second row's tmdb_id = %v, want the original 1726", gotSecond.TMDBID)
	}
}

// The one refusal that is deliberately NOT inverted. MatchMovie's taken-probe asks
// whether ANY row holds the id; this one excludes the row being corrected, so
// re-picking the film already stored is allowed and costs a fresh overview, poster
// and tmdb_year. Without the exclusion a correction would collide with itself, and
// the sentence the user got back would name a conflict with their own film.
func TestCorrectMatchToTheFilmAlreadyStoredIsARefreshNotACollision(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	m, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Avengers - Infinity War (2018)"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := s.MatchMovie(ctx, m.ID, TMDBMatch{
		TMDBID:   299536,
		Overview: ptrString("stale"),
	}); err != nil {
		t.Fatalf("MatchMovie: %v", err)
	}

	corrected, err := s.CorrectMatch(ctx, m.ID, TMDBMatch{
		TMDBID:   299536,
		Overview: ptrString("fresh"),
		Year:     ptrInt(2018),
	})
	if err != nil {
		t.Fatalf("CorrectMatch onto the same id = %v, want it allowed", err)
	}
	if corrected.Overview == nil || *corrected.Overview != "fresh" {
		t.Errorf("overview = %v, want the re-read 'fresh'", corrected.Overview)
	}
	if corrected.TMDBYear == nil || *corrected.TMDBYear != 2018 {
		t.Errorf("tmdb_year = %v, want 2018", corrected.TMDBYear)
	}
}

// A correction survives a rescan for the same three reasons a match does, and the
// assertion is made rather than argued because it is the whole point of writing
// the correction into the database instead of the UI.
func TestACorrectionSurvivesARescan(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const path = "/movies/Avengers - Infinity War (2018)"

	m, _, err := s.UpsertMovieByPath(ctx, scanned(path))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := s.MatchMovie(ctx, m.ID, TMDBMatch{TMDBID: 1726, Year: ptrInt(2008)}); err != nil {
		t.Fatalf("MatchMovie: %v", err)
	}
	if _, err := s.CorrectMatch(ctx, m.ID, TMDBMatch{TMDBID: 299536, Year: ptrInt(2018)}); err != nil {
		t.Fatalf("CorrectMatch: %v", err)
	}

	for _, pass := range []string{"after one rescan", "after two", "after three"} {
		if _, _, err := s.UpsertMovieByPath(ctx, scanned(path)); err != nil {
			t.Fatalf("%s: rescan: %v", pass, err)
		}
		got, err := s.GetMovie(ctx, m.ID)
		if err != nil {
			t.Fatalf("%s: GetMovie: %v", pass, err)
		}
		if got.TMDBID == nil || *got.TMDBID != 299536 {
			t.Errorf("%s: tmdb_id = %v, want the corrected 299536", pass, got.TMDBID)
		}
		if got.TMDBYear == nil || *got.TMDBYear != 2018 {
			t.Errorf("%s: tmdb_year = %v, want the corrected 2018", pass, got.TMDBYear)
		}
		if got.Year != scanned(path).Year {
			t.Errorf("%s: year = %d, want the folder's %d", pass, got.Year, scanned(path).Year)
		}
	}
}

// **This is the measurement behind CorrectMatch being one statement, made
// executable so the design cannot be simplified back into the bug.**
//
// The obvious implementation of "correct a wrong match" is to clear the row and
// reuse MatchMovie. This test does the clearing half with raw SQL — deliberately,
// because no exported method does it and none should — and then shows what the
// row becomes: work for the scan's matching pass, which matches from the folder
// name. The folder name is what produced the wrong match in the first place, so a
// scan landing in that window restores exactly the film being corrected away from.
//
// The second half asserts the real path never enters that state.
func TestClearingAMatchWouldHandTheRowBackToTheScan(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const path = "/movies/Avengers - Infinity War (2018)"

	m, _, err := s.UpsertMovieByPath(ctx, scanned(path))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := s.MatchMovie(ctx, m.ID, TMDBMatch{TMDBID: 1726, Year: ptrInt(2008)}); err != nil {
		t.Fatalf("MatchMovie: %v", err)
	}

	// The clear a two-step correction would perform between its two requests.
	if _, err := s.db.ExecContext(ctx, `
		UPDATE movies SET tmdb_id = NULL, overview = NULL, poster_path = NULL, tmdb_year = NULL
		WHERE id = ?`, m.ID); err != nil {
		t.Fatalf("clear: %v", err)
	}

	missing, err := s.MoviesMissingMetadata(ctx, MediaTypeMovie)
	if err != nil {
		t.Fatalf("MoviesMissingMetadata: %v", err)
	}
	var exposed bool
	for _, row := range missing {
		if row.ID == m.ID {
			exposed = true
		}
	}
	if !exposed {
		t.Fatal("a cleared row is not on the scan's work list — if this ever becomes true, " +
			"CorrectMatch's single-statement design has lost its reason and this test should say so")
	}

	// The real path, on a row in the same starting state, never exposes it.
	again, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Infinity War (2018)"))
	if err != nil {
		t.Fatalf("upsert again: %v", err)
	}
	if _, err := s.MatchMovie(ctx, again.ID, TMDBMatch{TMDBID: 1726, Year: ptrInt(2008)}); err != nil {
		t.Fatalf("MatchMovie again: %v", err)
	}
	if _, err := s.CorrectMatch(ctx, again.ID, TMDBMatch{TMDBID: 299536, Year: ptrInt(2018)}); err != nil {
		t.Fatalf("CorrectMatch: %v", err)
	}
	missing, err = s.MoviesMissingMetadata(ctx, MediaTypeMovie)
	if err != nil {
		t.Fatalf("MoviesMissingMetadata after correcting: %v", err)
	}
	for _, row := range missing {
		if row.ID == again.ID {
			t.Errorf("movie %d is on the scan's work list after a correction — "+
				"CorrectMatch must never leave tmdb_id NULL", again.ID)
		}
	}
}
