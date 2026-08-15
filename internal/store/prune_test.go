package store

import (
	"context"
	"testing"
)

// MoviesOnDisk is the scan's list of rows it is allowed to consider removing, so
// what it LEAVES OUT is the interesting half: a wanted row has no folder to make a
// statement about, and a row being fetched has a folder that legitimately holds
// nothing yet.
func TestMoviesOnDiskExcludesWantedRows(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	imported, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Avengers - Infinity War (2018)"))
	if err != nil {
		t.Fatalf("seed imported: %v", err)
	}
	if _, err := s.UpsertWantedMovie(ctx, "Dune Part Three", 2026, nil); err != nil {
		t.Fatalf("seed wanted: %v", err)
	}

	on, err := s.MoviesOnDisk(ctx)
	if err != nil {
		t.Fatalf("MoviesOnDisk: %v", err)
	}
	if len(on) != 1 {
		t.Fatalf("candidates = %+v, want only the imported row", on)
	}
	if on[0].ID != imported.ID {
		t.Errorf("id = %d, want %d", on[0].ID, imported.ID)
	}
	if on[0].LibraryPath != "/movies/Avengers - Infinity War (2018)" {
		t.Errorf("library_path = %q", on[0].LibraryPath)
	}
	// Title and year ride along because the caller logs a removal by name, and
	// after the DELETE there is nothing left to look them up by.
	if on[0].Title != "Avengers - Infinity War" || on[0].Year != 2018 {
		t.Errorf("title/year = %q/%d, want the row's own", on[0].Title, on[0].Year)
	}
	if on[0].Downloading {
		t.Error("downloading = true with no downloads at all")
	}
}

// "In flight" is the same EXISTS LibraryByTMDBID uses for the badge on a poster —
// deliberately, so the screen and the pruner can never disagree about what curator
// is already getting. Only `imported` and `failed` are not in flight.
func TestMoviesOnDiskFlagsEveryUnfinishedDownload(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		state string
		want  bool
	}{
		{DownloadQueued, true},
		{DownloadDownloading, true},
		{DownloadStalled, true},
		{DownloadCompleted, true},
		{DownloadImported, false},
		{DownloadFailed, false},
	}

	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			s := newTestStore(t)
			movie, _, err := s.UpsertMovieByPath(ctx, scanned("/movies/Avengers - Infinity War (2018)"))
			if err != nil {
				t.Fatalf("seed: %v", err)
			}
			if _, err := s.InsertDownload(ctx, Download{
				MovieID:     movie.ID,
				TorrentHash: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
				Indexer:     "yts",
				ReleaseName: "Avengers.Infinity.War.2018.1080p",
				Magnet:      "magnet:?xt=urn:btih:a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
				State:       tc.state,
			}); err != nil {
				t.Fatalf("insert download: %v", err)
			}

			on, err := s.MoviesOnDisk(ctx)
			if err != nil {
				t.Fatalf("MoviesOnDisk: %v", err)
			}
			if len(on) != 1 {
				t.Fatalf("candidates = %+v, want 1 — the row is on disk either way", on)
			}
			if on[0].Downloading != tc.want {
				t.Errorf("state %q: downloading = %v, want %v", tc.state, on[0].Downloading, tc.want)
			}
		})
	}
}

func TestMoviesOnDiskIsEmptyRatherThanNil(t *testing.T) {
	on, err := newTestStore(t).MoviesOnDisk(context.Background())
	if err != nil {
		t.Fatalf("MoviesOnDisk: %v", err)
	}
	if on == nil {
		t.Error("nil slice; every list in this package is empty rather than null")
	}
	if len(on) != 0 {
		t.Errorf("candidates = %+v, want none", on)
	}
}
