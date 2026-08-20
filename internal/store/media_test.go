package store

import (
	"context"
	"errors"
	"testing"
)

// T88's reason for existing, in one file.
//
// A show is a row in the same table as a film (D48), which buys the whole
// download pipeline unchanged and charges for it in every read that used to be
// able to assume one kind of thing. These are the reads, and each test here is
// one way the two would otherwise mix.
//
// The number 95396 recurs on purpose: it is Severance's TMDB **tv** id, and it
// stands in for a film's **movie** id throughout. TMDB's two id sequences
// overlap, and every test below would pass trivially if they did not.

// seedFilm and seedShow are the two rows every test here starts from — same
// title-shaped folder, different roots, and the same TMDB number in the column
// each one's media type owns.
func seedFilm(t *testing.T, s *Store, title string, year int, tmdbID int64) Movie {
	t.Helper()
	ctx := context.Background()
	m, _, err := s.UpsertMovieByPath(ctx, ScannedMovie{
		LibraryPath: "/movies/" + title + " (2022)",
		Title:       title,
		Year:        year,
		MediaType:   MediaTypeMovie,
	})
	if err != nil {
		t.Fatalf("seed film %q: %v", title, err)
	}
	if err := s.SetTMDBMetadata(ctx, m.ID, TMDBMatch{TMDBID: tmdbID}); err != nil {
		t.Fatalf("match film %q: %v", title, err)
	}
	got, err := s.GetMovie(ctx, m.ID)
	if err != nil {
		t.Fatalf("re-read film %q: %v", title, err)
	}
	return got
}

func seedShow(t *testing.T, s *Store, title string, year int, tmdbID int64) Movie {
	t.Helper()
	ctx := context.Background()
	m, _, err := s.UpsertMovieByPath(ctx, ScannedMovie{
		LibraryPath: "/tv/" + title + " (2022)",
		Title:       title,
		Year:        year,
		MediaType:   MediaTypeTV,
	})
	if err != nil {
		t.Fatalf("seed show %q: %v", title, err)
	}
	if err := s.SetTMDBMetadata(ctx, m.ID, TMDBMatch{TMDBID: tmdbID}); err != nil {
		t.Fatalf("match show %q: %v", title, err)
	}
	got, err := s.GetMovie(ctx, m.ID)
	if err != nil {
		t.Fatalf("re-read show %q: %v", title, err)
	}
	return got
}

// The write half: a TMDB id lands in the column the ROW's media type owns, and
// the caller never gets a say. tmdbIDWrite is a CASE over media_type precisely so
// that a caller cannot put a tv id in the movie column by passing the wrong
// argument — there is no argument to pass.
func TestATMDBIDLandsInTheColumnItsRowOwns(t *testing.T) {
	s := newTestStore(t)

	film := seedFilm(t, s, "Coincidence", 2011, 95396)
	if film.TMDBID == nil || *film.TMDBID != 95396 {
		t.Errorf("film tmdb_id = %v, want 95396", film.TMDBID)
	}
	if film.TMDBTVID != nil {
		t.Errorf("film tmdb_tv_id = %v, want NULL — a film has no tv id", *film.TMDBTVID)
	}

	show := seedShow(t, s, "Severance", 2022, 95396)
	if show.TMDBTVID == nil || *show.TMDBTVID != 95396 {
		t.Errorf("show tmdb_tv_id = %v, want 95396", show.TMDBTVID)
	}
	if show.TMDBID != nil {
		t.Errorf("show tmdb_id = %v, want NULL — putting it here is the corruption T88 exists to prevent", *show.TMDBID)
	}
}

// #1 on the contamination list, and the worst of them.
//
// A show's tmdb_id is NULL by construction, so an unscoped `WHERE tmdb_id IS
// NULL` puts every show on the matching pass's work list on EVERY scan. The
// caller feeds that list to TMDB's /search/movie and writes the answer back with
// SetTMDBMetadata, which overwrites unconditionally by design — and for Fargo,
// Watchmen, Hannibal, Westworld, Dune and Snowpiercer the lookup SUCCEEDS. The
// show would quietly acquire a film's id, overview and poster, with no error and
// no log, and it would happen again every scan.
func TestAMatchedShowIsNotOnTheFilmMatchingPassesWorkList(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// A show that has never been matched — tmdb_id NULL, tmdb_tv_id NULL. The
	// title is one TMDB answers for confidently as a film, which is the trap.
	if _, _, err := s.UpsertMovieByPath(ctx, ScannedMovie{
		LibraryPath: "/tv/Fargo (2014)", Title: "Fargo", Year: 2014, MediaType: MediaTypeTV,
	}); err != nil {
		t.Fatalf("seed the show: %v", err)
	}
	// And a film that genuinely does need matching, so the list is not empty for
	// the wrong reason.
	if _, _, err := s.UpsertMovieByPath(ctx, ScannedMovie{
		LibraryPath: "/movies/Some Film (2019)", Title: "Some Film", Year: 2019, MediaType: MediaTypeMovie,
	}); err != nil {
		t.Fatalf("seed the film: %v", err)
	}

	missing, err := s.MoviesMissingMetadata(ctx, MediaTypeMovie)
	if err != nil {
		t.Fatalf("MoviesMissingMetadata: %v", err)
	}
	for _, m := range missing {
		if m.MediaType == MediaTypeTV {
			t.Fatalf("the film matching pass was handed the show %q — "+
				"TMDB's /search/movie answers for that title and the show would take a film's poster", m.Title)
		}
	}
	if len(missing) != 1 || missing[0].Title != "Some Film" {
		t.Fatalf("missing = %d rows %v, want exactly the one film", len(missing), titlesOf(missing))
	}

	// And the mirror: the TV pass sees the show and not the film.
	missingTV, err := s.MoviesMissingMetadata(ctx, MediaTypeTV)
	if err != nil {
		t.Fatalf("MoviesMissingMetadata(tv): %v", err)
	}
	if len(missingTV) != 1 || missingTV[0].Title != "Fargo" {
		t.Fatalf("the TV pass got %v, want exactly the show", titlesOf(missingTV))
	}
}

// #5. The badge on a poster, and the dispatch guard behind it.
//
// One map keyed on a bare number would say "already in your library" over a film
// whose id a show happens to share — and the fourth caller of this is the
// dispatch refusal, so it would answer a TV grab with a sentence naming a film
// the user never asked about.
func TestTheLibraryIndexKeepsTheTwoIDSpacesApart(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	film := seedFilm(t, s, "Coincidence", 2011, 95396)
	show := seedShow(t, s, "Severance", 2022, 95396)

	films, err := s.LibraryByTMDBID(ctx, MediaTypeMovie)
	if err != nil {
		t.Fatalf("LibraryByTMDBID(movie): %v", err)
	}
	if got, ok := films[95396]; !ok || got.MovieID != film.ID {
		t.Fatalf("the film index gave %+v for 95396, want the film's row %d", got, film.ID)
	}

	shows, err := s.LibraryByTMDBID(ctx, MediaTypeTV)
	if err != nil {
		t.Fatalf("LibraryByTMDBID(tv): %v", err)
	}
	if got, ok := shows[95396]; !ok || got.MovieID != show.ID {
		t.Fatalf("the show index gave %+v for 95396, want the show's row %d", got, show.ID)
	}

	if len(films) != 1 || len(shows) != 1 {
		t.Fatalf("films=%d shows=%d, want one each — the two indexes are leaking into each other",
			len(films), len(shows))
	}
}

// #11. The film grid asks for films.
func TestListingOneMediaTypeExcludesTheOther(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	seedFilm(t, s, "Coincidence", 2011, 95396)
	seedShow(t, s, "Severance", 2022, 95396)

	films, err := s.ListMovies(ctx, MediaTypeMovie)
	if err != nil {
		t.Fatalf("ListMovies(movie): %v", err)
	}
	if len(films) != 1 || films[0].Title != "Coincidence" {
		t.Fatalf("films = %v, want just the film", titlesOf(films))
	}

	shows, err := s.ListMovies(ctx, MediaTypeTV)
	if err != nil {
		t.Fatalf("ListMovies(tv): %v", err)
	}
	if len(shows) != 1 || shows[0].Title != "Severance" {
		t.Fatalf("shows = %v, want just the show", titlesOf(shows))
	}
}

// #2's enabler. The pruner has to decide whether a row's folder is inside the
// root that owns it, and it cannot do that without knowing which root that is.
//
// Note what this asserts and what it does not: MoviesOnDisk returns BOTH kinds.
// Filtering here would make a row the prune cannot see, and the prune deletes on
// a positive finding — so a row it cannot see is a row it cannot keep either.
// D33's asymmetry needs every row present and the caller deciding.
func TestRowsOnDiskCarryTheirMediaType(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	seedFilm(t, s, "Coincidence", 2011, 95396)
	seedShow(t, s, "Severance", 2022, 95396)

	on, err := s.MoviesOnDisk(ctx)
	if err != nil {
		t.Fatalf("MoviesOnDisk: %v", err)
	}
	if len(on) != 2 {
		t.Fatalf("MoviesOnDisk returned %d rows, want both — a row it cannot see is a row it cannot keep", len(on))
	}
	byTitle := map[string]string{}
	for _, row := range on {
		byTitle[row.Title] = row.MediaType
	}
	if byTitle["Coincidence"] != MediaTypeMovie {
		t.Errorf("the film's media type came back %q", byTitle["Coincidence"])
	}
	if byTitle["Severance"] != MediaTypeTV {
		t.Errorf("the show's media type came back %q", byTitle["Severance"])
	}
}

// #12. A hand match is scoped to its own id space in both directions: the film
// holding 95396 must not block the show from taking 95396, and a second show
// must still be refused.
func TestAHandMatchIsScopedToItsOwnIDSpace(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	seedFilm(t, s, "Coincidence", 2011, 95396)

	unmatched, _, err := s.UpsertMovieByPath(ctx, ScannedMovie{
		LibraryPath: "/tv/Severance (2022)", Title: "Severance", Year: 2022, MediaType: MediaTypeTV,
	})
	if err != nil {
		t.Fatalf("seed the show: %v", err)
	}

	matched, err := s.MatchMovie(ctx, unmatched.ID, TMDBMatch{TMDBID: 95396})
	if err != nil {
		t.Fatalf("matching a show to 95396 was refused because a FILM holds 95396: %v", err)
	}
	if matched.TMDBTVID == nil || *matched.TMDBTVID != 95396 {
		t.Fatalf("tmdb_tv_id = %v, want 95396", matched.TMDBTVID)
	}

	// The refusal still works within one space.
	second, _, err := s.UpsertMovieByPath(ctx, ScannedMovie{
		LibraryPath: "/tv/Severance Again (2022)", Title: "Severance Again", Year: 2022, MediaType: MediaTypeTV,
	})
	if err != nil {
		t.Fatalf("seed the second show: %v", err)
	}
	if _, err := s.MatchMovie(ctx, second.ID, TMDBMatch{TMDBID: 95396}); !errors.Is(err, ErrTMDBIDTaken) {
		t.Fatalf("matching a second show to the same tv id gave %v, want ErrTMDBIDTaken", err)
	}
}

// Every media-scoped read refuses a media type that is not one of the two,
// rather than quietly answering about films.
//
// It matters more than an argument check usually would: tmdbColumn turns this
// value into a SQL identifier, so validMediaType is the gate between a caller's
// string and an interpolated query.
func TestAMediaScopedReadRefusesAMediaTypeItDoesNotKnow(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedFilm(t, s, "Coincidence", 2011, 95396)

	for _, bad := range []string{"", "movies", "TV", "film", "'; DROP TABLE movies; --"} {
		t.Run(bad, func(t *testing.T) {
			if _, err := s.ListMovies(ctx, bad); err == nil {
				t.Errorf("ListMovies(%q) was allowed", bad)
			}
			if _, err := s.MoviesMissingMetadata(ctx, bad); err == nil {
				t.Errorf("MoviesMissingMetadata(%q) was allowed", bad)
			}
			if _, err := s.LibraryByTMDBID(ctx, bad); err == nil {
				t.Errorf("LibraryByTMDBID(%q) was allowed", bad)
			}
		})
	}

	// And the library is still there, which is the half a returned error does not
	// prove on its own.
	films, err := s.ListMovies(ctx, MediaTypeMovie)
	if err != nil || len(films) != 1 {
		t.Fatalf("after the refusals: %d films, err %v — want the one film untouched", len(films), err)
	}
}

func titlesOf(movies []Movie) []string {
	out := make([]string, 0, len(movies))
	for _, m := range movies {
		out = append(out, m.Title)
	}
	return out
}
