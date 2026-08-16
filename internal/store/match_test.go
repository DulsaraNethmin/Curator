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
	// The year is the folder's and stays the folder's — TMDBMatch has no Year
	// field, because the scan rewrites that column on every pass and a value
	// written here would revert. TMDBMatch's own comment carries the measurement.
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
	missing, err := s.MoviesMissingMetadata(ctx)
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
