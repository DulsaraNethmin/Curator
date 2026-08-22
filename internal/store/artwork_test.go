package store

import (
	"context"
	"testing"
)

// The bug this pins, in one sentence: a row curator writes for ITSELF has a TMDB
// id and no poster, and the pass that would give it one only looks at rows with
// no id.
//
// Measured on the Pi 2026-08-22, before the fix — five of five rows, every one
// of them written by a dispatch:
//
//	Deadpool (2016)          tmdb_id 293660    poster_path null
//	The Avengers (2012)      tmdb_id  24428    poster_path null
//	The Fast and the Furious tmdb_id   9799    poster_path null
//	Dracula Untold (2014)    tmdb_id  49017    poster_path null
//	Prison Break (2005)      tmdb_tv_id 2288   poster_path null
//
// UpsertWanted inserts (tmdb_id, title, year, media_type, status, added_at) and
// nothing else, and MoviesMissingMetadata selects `<tmdbcol> IS NULL`. The two
// lists are disjoint, so the row was never revisited by any scan, ever.
func TestADispatchedRowIsOnTheArtworkListAndNotTheMatchingOne(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// The row a Download creates: an id it was told, and no metadata.
	dispatched, err := s.UpsertWanted(ctx, Wanted{
		MediaType: MediaTypeMovie, Title: "Deadpool", Year: 2016, TMDBID: ptrInt64(293660),
	})
	if err != nil {
		t.Fatalf("UpsertWanted: %v", err)
	}
	if dispatched.PosterPath != nil {
		t.Fatalf("UpsertWanted wrote a poster %q — this test is asserting the wrong thing", *dispatched.PosterPath)
	}

	// A row off disk that TMDB has genuinely never matched, so neither list is
	// empty for the wrong reason.
	if _, _, err := s.UpsertMovieByPath(ctx, ScannedMovie{
		LibraryPath: "/movies/Some Film (2019)", Title: "Some Film", Year: 2019, MediaType: MediaTypeMovie,
	}); err != nil {
		t.Fatalf("seed the scanned film: %v", err)
	}

	artwork, err := s.MoviesMissingArtwork(ctx, MediaTypeMovie)
	if err != nil {
		t.Fatalf("MoviesMissingArtwork: %v", err)
	}
	if len(artwork) != 1 || artwork[0].Title != "Deadpool" {
		t.Fatalf("MoviesMissingArtwork = %v, want exactly the dispatched row", titlesOf(artwork))
	}

	// The other half, and the reason this is a second query rather than a wider
	// one: the matching pass must still mean "TMDB could not match this", which
	// is what the unmatched badge and the manual matcher read.
	matching, err := s.MoviesMissingMetadata(ctx, MediaTypeMovie)
	if err != nil {
		t.Fatalf("MoviesMissingMetadata: %v", err)
	}
	if len(matching) != 1 || matching[0].Title != "Some Film" {
		t.Fatalf("MoviesMissingMetadata = %v, want exactly the unmatched row — a matched row on "+
			"this list would be re-guessed from its folder name", titlesOf(matching))
	}
}

// Once the poster lands the row leaves the list, so a scan does not re-ask TMDB
// about it for ever. SetTMDBMetadata is the writer both passes use.
func TestAnArtworkedRowLeavesTheList(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	row, err := s.UpsertWanted(ctx, Wanted{
		MediaType: MediaTypeMovie, Title: "Deadpool", Year: 2016, TMDBID: ptrInt64(293660),
	})
	if err != nil {
		t.Fatalf("UpsertWanted: %v", err)
	}

	if err := s.SetTMDBArtwork(ctx, row.ID,
		ptrString("A wisecracking mercenary."),
		ptrString("/3E53WEZJqP6aM84D8CckXx4pIHw.jpg")); err != nil {
		t.Fatalf("SetTMDBArtwork: %v", err)
	}

	artwork, err := s.MoviesMissingArtwork(ctx, MediaTypeMovie)
	if err != nil {
		t.Fatalf("MoviesMissingArtwork: %v", err)
	}
	if len(artwork) != 0 {
		t.Fatalf("MoviesMissingArtwork = %v after the poster landed, want none", titlesOf(artwork))
	}
}

// SetTMDBArtwork can only ever ADD, and the case that proves it is reachable
// rather than theoretical: a row hand-matched through CorrectMatch can hold an
// overview and no poster, and that missing poster is exactly why it is on
// MoviesMissingArtwork's list. A TMDB record with an empty overview must fill
// the poster without blanking the sentence already there.
//
// This is the whole reason it is a second writer. SetTMDBMetadata sets
// `overview = ?` unconditionally, which is right for the matching pass — that
// caller resolved the row from nothing — and wrong here.
func TestSetTMDBArtworkOnlyEverAdds(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	row, err := s.UpsertWanted(ctx, Wanted{
		MediaType: MediaTypeMovie, Title: "Deadpool", Year: 2016, TMDBID: ptrInt64(293660),
	})
	if err != nil {
		t.Fatalf("UpsertWanted: %v", err)
	}
	if err := s.SetTMDBArtwork(ctx, row.ID, ptrString("An overview from earlier."), nil); err != nil {
		t.Fatalf("seed the overview: %v", err)
	}

	// TMDB knows the id and has a poster but no overview.
	if err := s.SetTMDBArtwork(ctx, row.ID, nil, ptrString("/p.jpg")); err != nil {
		t.Fatalf("SetTMDBArtwork: %v", err)
	}

	got, err := s.GetMovie(ctx, row.ID)
	if err != nil {
		t.Fatalf("GetMovie: %v", err)
	}
	if got.Overview == nil || *got.Overview != "An overview from earlier." {
		t.Errorf("overview = %v, want it left alone — this write may only add", got.Overview)
	}
	if got.PosterPath == nil || *got.PosterPath != "/p.jpg" {
		t.Errorf("poster_path = %v, want the new one", got.PosterPath)
	}

	// And it does not touch the id, so it can never trip either UNIQUE index.
	if got.TMDBID == nil || *got.TMDBID != 293660 {
		t.Errorf("tmdb_id = %v, want 293660 untouched", got.TMDBID)
	}
	if got.TMDBTVID != nil {
		t.Errorf("tmdb_tv_id = %v, want nil on a film", *got.TMDBTVID)
	}

	if err := s.SetTMDBArtwork(ctx, 9999, ptrString("x"), ptrString("/y.jpg")); err == nil {
		t.Error("SetTMDBArtwork on a row that does not exist was allowed")
	}
}

// The media type is required and the two id spaces stay apart. A show's row
// carries tmdb_tv_id, so asking the film list for it must return nothing — the
// caller picks /movie/{id} or /tv/{id} off this answer, and TMDB numbers the two
// independently (D48).
func TestTheArtworkListIsScopedToOneIDSpace(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Severance is tv id 95396, and some film holds movie id 95396.
	if _, err := s.UpsertWanted(ctx, Wanted{
		MediaType: MediaTypeTV, Title: "Severance", Year: 2022, TMDBID: ptrInt64(95396),
	}); err != nil {
		t.Fatalf("UpsertWanted(tv): %v", err)
	}

	films, err := s.MoviesMissingArtwork(ctx, MediaTypeMovie)
	if err != nil {
		t.Fatalf("MoviesMissingArtwork(movie): %v", err)
	}
	if len(films) != 0 {
		t.Fatalf("the film artwork pass was handed %v — it would ask /movie/95396 and "+
			"write an unrelated film's poster onto the show", titlesOf(films))
	}

	shows, err := s.MoviesMissingArtwork(ctx, MediaTypeTV)
	if err != nil {
		t.Fatalf("MoviesMissingArtwork(tv): %v", err)
	}
	if len(shows) != 1 || shows[0].Title != "Severance" {
		t.Fatalf("MoviesMissingArtwork(tv) = %v, want the show", titlesOf(shows))
	}

	for _, bad := range []string{"", "film", "series"} {
		if _, err := s.MoviesMissingArtwork(ctx, bad); err == nil {
			t.Errorf("MoviesMissingArtwork(%q) was allowed", bad)
		}
	}
}
