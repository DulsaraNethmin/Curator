package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// A real 40-hex info hash, in the upper case indexer.InfoHash produces.
const testInfoHash = "89599BF4DC369A3A8ECA26411C5CCF922D78B486"

// dispatchedDownload is a plausible freshly-added torrent, so tests only spell
// out what they care about. Named for downloads so it cannot collide with
// store_test.go's scanned().
func dispatchedDownload(movieID int64, hash string) Download {
	return Download{
		MovieID:     movieID,
		TorrentHash: hash,
		Indexer:     "1337x",
		ReleaseName: "Interstellar.2014.1080p.BluRay.x264-SPARKS",
		Magnet:      "magnet:?xt=urn:btih:" + hash,
	}
}

// seedWantedMovie is the movie a download hangs off, since movie_id is NOT NULL.
func seedWantedMovie(t *testing.T, s *Store) Movie {
	t.Helper()
	m, err := s.UpsertWantedMovie(context.Background(), "Interstellar", 2014, nil)
	if err != nil {
		t.Fatalf("UpsertWantedMovie: %v", err)
	}
	return m
}

func TestUpsertWantedMovieCreatesWantedRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	m, err := s.UpsertWantedMovie(ctx, "Interstellar", 2014, nil)
	if err != nil {
		t.Fatalf("UpsertWantedMovie: %v", err)
	}
	if m.Status != StatusWanted {
		t.Errorf("status = %q, want %q", m.Status, StatusWanted)
	}
	if m.LibraryPath != nil {
		t.Errorf("library_path = %q, want nil — the film is not on disk, that is why it is being downloaded", *m.LibraryPath)
	}
	if m.TMDBID != nil {
		t.Errorf("tmdb_id = %d, want nil for a request that carried none", *m.TMDBID)
	}
	if m.MediaType != MediaTypeMovie {
		t.Errorf("media_type = %q, want %q", m.MediaType, MediaTypeMovie)
	}
	if m.Title != "Interstellar" || m.Year != 2014 {
		t.Errorf("got %q (%d), want Interstellar (2014)", m.Title, m.Year)
	}
	if m.AddedAt.IsZero() {
		t.Error("added_at is zero, want the clock's reading")
	}
	if m.ImportedAt != nil {
		t.Errorf("imported_at = %v, want nil", *m.ImportedAt)
	}

	// It really is readable back as a row, not just as a struct we built.
	got, err := s.GetMovie(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMovie: %v", err)
	}
	if got.Status != StatusWanted || got.LibraryPath != nil {
		t.Errorf("round-tripped as status %q, library_path %v", got.Status, got.LibraryPath)
	}
}

// Re-downloading a film must not fork its identity into two rows.
func TestUpsertWantedMovieTwiceYieldsOneRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, err := s.UpsertWantedMovie(ctx, "Interstellar", 2014, nil)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := s.UpsertWantedMovie(ctx, "Interstellar", 2014, nil)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("second upsert produced id %d, want the first's %d", second.ID, first.ID)
	}

	movies, err := s.ListMovies(ctx)
	if err != nil {
		t.Fatalf("ListMovies: %v", err)
	}
	if len(movies) != 1 {
		t.Fatalf("got %d rows for the same title and year, want 1", len(movies))
	}
	if !movies[0].AddedAt.Equal(first.AddedAt) {
		t.Errorf("added_at = %v, want the first upsert's %v — it is when curator first wanted the film",
			movies[0].AddedAt, first.AddedAt)
	}
}

// A tmdb_id is the film itself; a title is only how somebody spelled it. So a
// second request carrying the same id finds the row even when the title differs.
func TestUpsertWantedMovieMatchesOnTMDBIDBeforeTitle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, err := s.UpsertWantedMovie(ctx, "Interstellar", 2014, ptrInt64(157336))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.TMDBID == nil || *first.TMDBID != 157336 {
		t.Fatalf("tmdb_id = %v, want 157336", first.TMDBID)
	}

	// Same film, different spelling and even a wrong year — the id decides.
	second, err := s.UpsertWantedMovie(ctx, "Interstellar (IMAX)", 2015, ptrInt64(157336))
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("id = %d, want the first's %d — the tmdb_id matched", second.ID, first.ID)
	}
	if second.Title != "Interstellar" || second.Year != 2014 {
		t.Errorf("existing row came back as %q (%d); the match should be returned untouched, not rewritten",
			second.Title, second.Year)
	}

	movies, err := s.ListMovies(ctx)
	if err != nil {
		t.Fatalf("ListMovies: %v", err)
	}
	if len(movies) != 1 {
		t.Fatalf("got %d rows for one tmdb_id, want 1", len(movies))
	}
}

// library_path and tmdb_id are both UNIQUE, and both are NULL on every wanted
// movie. SQLite permits many NULLs in a UNIQUE column, which is the fact this
// whole design rests on; if it ever stopped being true, this fails first.
func TestManyWantedMoviesCoexistWithNullUniqueColumns(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	titles := []string{"Interstellar", "Dune - Part Two", "Sinners"}
	ids := map[int64]bool{}
	for i, title := range titles {
		m, err := s.UpsertWantedMovie(ctx, title, 2014+i, nil)
		if err != nil {
			t.Fatalf("UpsertWantedMovie %s: %v", title, err)
		}
		if m.LibraryPath != nil || m.TMDBID != nil {
			t.Errorf("%s: library_path = %v, tmdb_id = %v, want both nil", title, m.LibraryPath, m.TMDBID)
		}
		ids[m.ID] = true
	}
	if len(ids) != len(titles) {
		t.Errorf("got %d distinct ids for %d wanted movies", len(ids), len(titles))
	}

	movies, err := s.ListMovies(ctx)
	if err != nil {
		t.Fatalf("ListMovies: %v", err)
	}
	if len(movies) != len(titles) {
		t.Fatalf("got %d rows, want %d — a UNIQUE column rejecting a second NULL would show up here",
			len(movies), len(titles))
	}
}

// The phase 1 scan and this phase's wanted rows key on different things —
// library_path and (title, year) — so running both must leave the path upsert
// exactly as idempotent as it was on its own.
//
// It does mean a film wanted before it was on disk ends up with a second row once
// the scanner finds the folder: UpsertMovieByPath keys on library_path, and a
// wanted row has none to match. That is still exactly what happens — phase 4 did
// not change the scan upsert, and every assertion below still holds.
//
// What phase 4 added is the other end of it. The two rows are not meant to stay
// separate for ever: MarkImported folds them back into one at import time,
// keeping the scanned row (it is what every future UpsertMovieByPath finds) and
// deleting the wanted one. TestWantedThenScanThenMarkImported in imports_test.go
// picks up exactly where this test stops, so the two read together.
func TestWantedMovieDoesNotDisturbTheScanUpsert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const path = "/movies/Avengers - Infinity War (2018)"

	want, err := s.UpsertWantedMovie(ctx, "Avengers - Infinity War", 2018, nil)
	if err != nil {
		t.Fatalf("UpsertWantedMovie: %v", err)
	}

	scan, wasInsert, err := s.UpsertMovieByPath(ctx, scanned(path))
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if !wasInsert {
		t.Error("first scan reported an update; a wanted row has no library_path to match")
	}
	rescan, wasInsert, err := s.UpsertMovieByPath(ctx, scanned(path))
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if wasInsert {
		t.Error("rescan reported an insert; the path upsert must stay idempotent alongside wanted rows")
	}
	if rescan.ID != scan.ID {
		t.Errorf("rescan id = %d, want the scan's %d", rescan.ID, scan.ID)
	}

	movies, err := s.ListMovies(ctx)
	if err != nil {
		t.Fatalf("ListMovies: %v", err)
	}
	if len(movies) != 2 {
		t.Fatalf("got %d rows, want 2 (one wanted, one scanned) — a third means the rescan duplicated", len(movies))
	}

	// The scan does not touch the wanted row.
	stillWanted, err := s.GetMovie(ctx, want.ID)
	if err != nil {
		t.Fatalf("GetMovie: %v", err)
	}
	if stillWanted.Status != StatusWanted {
		t.Errorf("wanted row status = %q after a scan, want %q", stillWanted.Status, StatusWanted)
	}
	if stillWanted.LibraryPath != nil {
		t.Errorf("wanted row library_path = %q after a scan, want nil", *stillWanted.LibraryPath)
	}
	if scan.LibraryPath == nil || *scan.LibraryPath != path {
		t.Errorf("scanned row library_path = %v, want %q", scan.LibraryPath, path)
	}
}

// The other order, which is the one that must not create a duplicate at all:
// asking to download a film already in the library returns the library's row.
func TestUpsertWantedMovieReturnsAnAlreadyImportedMovie(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const path = "/movies/Avengers - Infinity War (2018)"

	imported, _, err := s.UpsertMovieByPath(ctx, scanned(path))
	if err != nil {
		t.Fatalf("UpsertMovieByPath: %v", err)
	}

	got, err := s.UpsertWantedMovie(ctx, "Avengers - Infinity War", 2018, nil)
	if err != nil {
		t.Fatalf("UpsertWantedMovie: %v", err)
	}
	if got.ID != imported.ID {
		t.Errorf("id = %d, want the imported row's %d — downloading a film we have must not fork its identity",
			got.ID, imported.ID)
	}
	if got.Status != StatusImported {
		t.Errorf("status = %q, want %q — a download request does not un-import the copy on disk",
			got.Status, StatusImported)
	}
	if got.LibraryPath == nil || *got.LibraryPath != path {
		t.Errorf("library_path = %v, want %q preserved", got.LibraryPath, path)
	}

	movies, err := s.ListMovies(ctx)
	if err != nil {
		t.Fatalf("ListMovies: %v", err)
	}
	if len(movies) != 1 {
		t.Fatalf("got %d rows, want 1", len(movies))
	}
}

func TestUpsertWantedMovieRejectsBlankTitle(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.UpsertWantedMovie(context.Background(), "   ", 2014, nil); err == nil {
		t.Error("blank title accepted; every such row would match every other blank-titled one")
	}
}

func TestInsertDownloadStoresTheRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := seedWantedMovie(t, s)

	in := dispatchedDownload(m.ID, testInfoHash)
	got, err := s.InsertDownload(ctx, in)
	if err != nil {
		t.Fatalf("InsertDownload: %v", err)
	}
	if got.ID == 0 {
		t.Error("id = 0, want the database's")
	}
	if got.MovieID != m.ID {
		t.Errorf("movie_id = %d, want %d", got.MovieID, m.ID)
	}
	if got.TorrentHash != testInfoHash {
		t.Errorf("torrent_hash = %q, want %q", got.TorrentHash, testInfoHash)
	}
	if got.Indexer != in.Indexer || got.ReleaseName != in.ReleaseName || got.Magnet != in.Magnet {
		t.Errorf("got %+v, want indexer/release_name/magnet from %+v", got, in)
	}
	if got.State != DownloadQueued {
		t.Errorf("state = %q, want %q for a download nobody has reported on yet", got.State, DownloadQueued)
	}
	if got.Progress != 0 {
		t.Errorf("progress = %v, want 0", got.Progress)
	}
	if got.AddedAt.IsZero() {
		t.Error("added_at is zero, want the clock's reading")
	}
	if got.CompletedAt != nil {
		t.Errorf("completed_at = %v, want nil — a zero time here would read as a finished download", *got.CompletedAt)
	}
}

// Re-dispatching after a restart converges rather than failing: qBittorrent
// treats an already-present magnet as idempotent, and so does this.
func TestInsertDownloadOnAnExistingHashReturnsTheFirstRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := seedWantedMovie(t, s)

	first, err := s.InsertDownload(ctx, dispatchedDownload(m.ID, testInfoHash))
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// Same torrent, described differently — the stored row wins, nothing is
	// rewritten, and no error is returned.
	again := dispatchedDownload(m.ID, strings.ToLower(testInfoHash))
	again.Indexer = "yts"
	again.ReleaseName = "Interstellar.2014.2160p.WEB-DL"
	again.State = DownloadDownloading
	again.Progress = 0.5

	second, err := s.InsertDownload(ctx, again)
	if err != nil {
		t.Fatalf("second insert of the same hash: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("id = %d, want the first's %d", second.ID, first.ID)
	}
	if second.ReleaseName != first.ReleaseName || second.Indexer != first.Indexer {
		t.Errorf("second insert rewrote the row: %+v, want %+v", second, first)
	}
	if second.State != first.State || second.Progress != first.Progress {
		t.Errorf("second insert clobbered live state: %q/%v, want %q/%v",
			second.State, second.Progress, first.State, first.Progress)
	}

	downloads, err := s.ListDownloads(ctx)
	if err != nil {
		t.Fatalf("ListDownloads: %v", err)
	}
	if len(downloads) != 1 {
		t.Fatalf("got %d rows for one torrent hash, want 1", len(downloads))
	}
}

// qBittorrent reports lower-case hashes and indexer.InfoHash produces upper-case.
// Everything is stored upper-case so the two can meet.
func TestInsertDownloadUpperCasesTheHash(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := seedWantedMovie(t, s)

	got, err := s.InsertDownload(ctx, dispatchedDownload(m.ID, strings.ToLower(testInfoHash)))
	if err != nil {
		t.Fatalf("InsertDownload: %v", err)
	}
	if got.TorrentHash != testInfoHash {
		t.Errorf("torrent_hash = %q, want it stored upper-case as %q", got.TorrentHash, testInfoHash)
	}

	// And in the column itself, not only in what the insert handed back.
	var stored string
	if err := s.db.QueryRowContext(ctx,
		`SELECT torrent_hash FROM downloads WHERE id = ?`, got.ID).Scan(&stored); err != nil {
		t.Fatalf("read torrent_hash: %v", err)
	}
	if stored != testInfoHash {
		t.Errorf("stored torrent_hash = %q, want %q", stored, testInfoHash)
	}
}

func TestGetDownloadByHashMatchesLowerCase(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := seedWantedMovie(t, s)

	inserted, err := s.InsertDownload(ctx, dispatchedDownload(m.ID, testInfoHash))
	if err != nil {
		t.Fatalf("InsertDownload: %v", err)
	}

	// Exactly what a qBittorrent torrents/info response would hand us.
	got, err := s.GetDownloadByHash(ctx, strings.ToLower(testInfoHash))
	if err != nil {
		t.Fatalf("GetDownloadByHash with a lower-case hash: %v", err)
	}
	if got.ID != inserted.ID {
		t.Errorf("id = %d, want %d", got.ID, inserted.ID)
	}
	if got.TorrentHash != testInfoHash {
		t.Errorf("torrent_hash = %q, want %q", got.TorrentHash, testInfoHash)
	}
}

func TestGetDownloadByHashNotFound(t *testing.T) {
	s := newTestStore(t)

	_, err := s.GetDownloadByHash(context.Background(), "0000000000000000000000000000000000000000")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want one wrapping ErrNotFound", err)
	}
}

func TestUpdateDownloadProgressMovesStateAndProgress(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := seedWantedMovie(t, s)

	if _, err := s.InsertDownload(ctx, dispatchedDownload(m.ID, testInfoHash)); err != nil {
		t.Fatalf("InsertDownload: %v", err)
	}

	// A tick that only reports progress, in the case qBittorrent uses.
	if err := s.UpdateDownloadProgress(ctx, strings.ToLower(testInfoHash), DownloadDownloading, 0.42, "", nil); err != nil {
		t.Fatalf("UpdateDownloadProgress: %v", err)
	}
	got, err := s.GetDownloadByHash(ctx, testInfoHash)
	if err != nil {
		t.Fatalf("GetDownloadByHash: %v", err)
	}
	if got.State != DownloadDownloading {
		t.Errorf("state = %q, want %q", got.State, DownloadDownloading)
	}
	if got.Progress != 0.42 {
		t.Errorf("progress = %v, want 0.42", got.Progress)
	}
	if got.CompletedAt != nil {
		t.Errorf("completed_at = %v for a download still running, want nil — nothing here invents one",
			*got.CompletedAt)
	}

	// The tick that finishes it. The caller decides the moment; sub-second
	// precision is asserted because the fixed-width timestamp format exists for it.
	done := time.Date(2026, 8, 12, 9, 30, 0, 123456789, time.UTC)
	if err := s.UpdateDownloadProgress(ctx, testInfoHash, DownloadCompleted, 1, "", &done); err != nil {
		t.Fatalf("UpdateDownloadProgress: %v", err)
	}
	got, err = s.GetDownloadByHash(ctx, testInfoHash)
	if err != nil {
		t.Fatalf("GetDownloadByHash: %v", err)
	}
	if got.State != DownloadCompleted || got.Progress != 1 {
		t.Errorf("state/progress = %q/%v, want %q/1", got.State, got.Progress, DownloadCompleted)
	}
	if got.CompletedAt == nil {
		t.Fatal("completed_at = nil after being told a completion time")
	}
	if !got.CompletedAt.Equal(done) {
		t.Errorf("completed_at = %v, want %v", *got.CompletedAt, done)
	}

	// A later tick with no completion time must not erase the one already there —
	// the poller passes nil on every tick after the first.
	if err := s.UpdateDownloadProgress(ctx, testInfoHash, DownloadCompleted, 1, "", nil); err != nil {
		t.Fatalf("UpdateDownloadProgress: %v", err)
	}
	got, err = s.GetDownloadByHash(ctx, testInfoHash)
	if err != nil {
		t.Fatalf("GetDownloadByHash: %v", err)
	}
	if got.CompletedAt == nil || !got.CompletedAt.Equal(done) {
		t.Errorf("completed_at = %v after a nil update, want %v preserved", got.CompletedAt, done)
	}
}

// T55: the reason travels with the state, and the state is what makes it true.
//
// The three writes are the three things that happen to a stalled download: it
// gains an explanation, the explanation changes while the state does not, and
// it starts moving. The last one is the one worth having a test for — a reason
// that survived recovery would sit under a `downloading` badge saying nobody is
// seeding the file that is arriving.
func TestUpdateDownloadProgressCarriesTheReason(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := seedWantedMovie(t, s)

	if _, err := s.InsertDownload(ctx, dispatchedDownload(m.ID, testInfoHash)); err != nil {
		t.Fatalf("InsertDownload: %v", err)
	}

	// A fresh row has no reason: nothing has polled it yet, and InsertDownload
	// ignores the field for exactly that reason.
	if got, err := s.GetDownloadByHash(ctx, testInfoHash); err != nil {
		t.Fatalf("GetDownloadByHash: %v", err)
	} else if got.Reason != "" {
		t.Errorf("Reason = %q on a freshly inserted download, want empty", got.Reason)
	}

	const noPeers = "no peers are connected — nobody appears to be seeding this release"
	if err := s.UpdateDownloadProgress(ctx, testInfoHash, DownloadStalled, 0, noPeers, nil); err != nil {
		t.Fatalf("UpdateDownloadProgress: %v", err)
	}
	got, err := s.GetDownloadByHash(ctx, testInfoHash)
	if err != nil {
		t.Fatalf("GetDownloadByHash: %v", err)
	}
	if got.State != DownloadStalled || got.Reason != noPeers {
		t.Errorf("state/reason = %q/%q, want %q/%q", got.State, got.Reason, DownloadStalled, noPeers)
	}

	// Same state, same progress, a different sentence. The poller's write
	// condition has to notice this one, and the column has to take it.
	const noData = "peers are connected but none of them is sending data"
	if err := s.UpdateDownloadProgress(ctx, testInfoHash, DownloadStalled, 0, noData, nil); err != nil {
		t.Fatalf("UpdateDownloadProgress: %v", err)
	}
	if got, err = s.GetDownloadByHash(ctx, testInfoHash); err != nil {
		t.Fatalf("GetDownloadByHash: %v", err)
	} else if got.Reason != noData {
		t.Errorf("Reason = %q, want the newer %q — a stale sentence is worse than none", got.Reason, noData)
	}

	// It starts moving. An empty reason CLEARS, unlike completed_at, which the
	// same call preserves — the two are next to each other in the signature and
	// they are deliberately not the same rule.
	if err := s.UpdateDownloadProgress(ctx, testInfoHash, DownloadDownloading, 0.1, "", nil); err != nil {
		t.Fatalf("UpdateDownloadProgress: %v", err)
	}
	if got, err = s.GetDownloadByHash(ctx, testInfoHash); err != nil {
		t.Fatalf("GetDownloadByHash: %v", err)
	} else if got.Reason != "" {
		t.Errorf("Reason = %q after the download started moving, want it cleared", got.Reason)
	}

	// And it is NULL rather than '': one spelling of empty in the column, so
	// nothing downstream has to test for two.
	var isNull bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT reason IS NULL FROM downloads WHERE torrent_hash = ?`, testInfoHash).Scan(&isNull); err != nil {
		t.Fatalf("read reason: %v", err)
	}
	if !isNull {
		t.Error("a cleared reason is stored as '' rather than NULL; the column now has two spellings of empty")
	}
}

// ListDownloads reads the column too — it is a different SELECT from
// GetDownloadByHash only by its WHERE clause, and GET /api/downloads uses this
// one. A reason that arrived on one path and not the other would be invisible
// in every test above and missing on the only screen that renders it.
func TestListDownloadsCarriesTheReason(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := seedWantedMovie(t, s)

	if _, err := s.InsertDownload(ctx, dispatchedDownload(m.ID, testInfoHash)); err != nil {
		t.Fatalf("InsertDownload: %v", err)
	}
	const why = "no peers are connected — nobody appears to be seeding this release"
	if err := s.UpdateDownloadProgress(ctx, testInfoHash, DownloadStalled, 0, why, nil); err != nil {
		t.Fatalf("UpdateDownloadProgress: %v", err)
	}

	all, err := s.ListDownloads(ctx)
	if err != nil {
		t.Fatalf("ListDownloads: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("downloads = %d, want 1", len(all))
	}
	if all[0].Reason != why {
		t.Errorf("Reason = %q, want %q", all[0].Reason, why)
	}
}

// An empty reason is omitted from the JSON rather than serialised as "". The
// assertion is against the raw bytes, because a typed test would pass on a
// `"reason":""` that the UI then has to know is not a sentence.
func TestDownloadJSONOmitsAnEmptyReason(t *testing.T) {
	blank, err := json.Marshal(Download{TorrentHash: testInfoHash, State: DownloadDownloading})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(blank), "reason") {
		t.Errorf("JSON = %s, want no reason key at all when there is nothing to explain", blank)
	}

	const why = "no peers are connected — nobody appears to be seeding this release"
	stalled, err := json.Marshal(Download{TorrentHash: testInfoHash, State: DownloadStalled, Reason: why})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(stalled), why) {
		t.Errorf("JSON = %s, want the reason carried verbatim", stalled)
	}
}

// The poller writes the same values every tick for a stalled torrent. That is not
// a missing row, so it must not come back as ErrNotFound — SQLite counts a row an
// UPDATE touched even when nothing about it changed.
func TestUpdateDownloadProgressWithUnchangedValuesSucceeds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := seedWantedMovie(t, s)

	if _, err := s.InsertDownload(ctx, dispatchedDownload(m.ID, testInfoHash)); err != nil {
		t.Fatalf("InsertDownload: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := s.UpdateDownloadProgress(ctx, testInfoHash, DownloadQueued, 0, "", nil); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
}

// A torrent in our category with no row is reported, never adopted, so the store
// must say clearly that there was nothing to update.
func TestUpdateDownloadProgressUnknownHashIsNotFound(t *testing.T) {
	s := newTestStore(t)

	err := s.UpdateDownloadProgress(context.Background(),
		"0000000000000000000000000000000000000000", DownloadDownloading, 0.1, "", nil)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want one wrapping ErrNotFound", err)
	}
}

// movie_id is NOT NULL REFERENCES movies(id) and the pragma is on: a download for
// a movie that does not exist is exactly what the reference is there to stop.
func TestInsertDownloadRequiresAnExistingMovie(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.InsertDownload(context.Background(), dispatchedDownload(404, testInfoHash)); err == nil {
		t.Error("insert against a nonexistent movie_id succeeded; the foreign key is the point")
	}
}

func TestInsertDownloadRejectsBlankHash(t *testing.T) {
	s := newTestStore(t)
	m := seedWantedMovie(t, s)

	if _, err := s.InsertDownload(context.Background(), dispatchedDownload(m.ID, "")); err == nil {
		t.Error("blank torrent_hash accepted; the second one would collide on UNIQUE for the wrong reason")
	}
}

func TestListDownloadsEmptyIsNotNil(t *testing.T) {
	s := newTestStore(t)

	downloads, err := s.ListDownloads(context.Background())
	if err != nil {
		t.Fatalf("ListDownloads on an empty table: %v", err)
	}
	if downloads == nil {
		t.Error("ListDownloads returned nil; it would marshal as JSON null, not []")
	}
	if len(downloads) != 0 {
		t.Errorf("got %d rows on an empty table", len(downloads))
	}
}

func TestListDownloadsNewestFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Frozen and stepped, so the assertion is about added_at rather than about how
	// fast the machine is. The offsets are sub-second because added_at is TEXT and
	// ORDER BY compares it as text — a variable-width fraction sorts wrong.
	base := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	offsets := []time.Duration{0, 500 * time.Millisecond, time.Second}
	var tick int
	s.now = func() time.Time { return base.Add(offsets[tick]) }

	m := seedWantedMovie(t, s)

	var want []int64
	for i, hash := range []string{
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		"CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC",
	} {
		tick = i
		d, err := s.InsertDownload(ctx, dispatchedDownload(m.ID, hash))
		if err != nil {
			t.Fatalf("InsertDownload %d: %v", i, err)
		}
		want = append([]int64{d.ID}, want...) // newest first
	}

	downloads, err := s.ListDownloads(ctx)
	if err != nil {
		t.Fatalf("ListDownloads: %v", err)
	}
	if len(downloads) != len(want) {
		t.Fatalf("got %d downloads, want %d", len(downloads), len(want))
	}
	for i, d := range downloads {
		if d.ID != want[i] {
			t.Errorf("position %d: id %d (%s), want id %d", i, d.ID, d.TorrentHash, want[i])
		}
	}
}

// Two downloads for two movies, both readable, with their nullable completion
// times independent of one another.
func TestListDownloadsCarriesCompletedAtPerRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	running := seedWantedMovie(t, s)
	finished, err := s.UpsertWantedMovie(ctx, "Dune - Part Two", 2024, ptrInt64(693134))
	if err != nil {
		t.Fatalf("UpsertWantedMovie: %v", err)
	}

	const otherHash = "1111111111111111111111111111111111111111"
	if _, err := s.InsertDownload(ctx, dispatchedDownload(running.ID, testInfoHash)); err != nil {
		t.Fatalf("InsertDownload: %v", err)
	}
	if _, err := s.InsertDownload(ctx, dispatchedDownload(finished.ID, otherHash)); err != nil {
		t.Fatalf("InsertDownload: %v", err)
	}
	done := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	if err := s.UpdateDownloadProgress(ctx, otherHash, DownloadCompleted, 1, "", &done); err != nil {
		t.Fatalf("UpdateDownloadProgress: %v", err)
	}

	downloads, err := s.ListDownloads(ctx)
	if err != nil {
		t.Fatalf("ListDownloads: %v", err)
	}
	if len(downloads) != 2 {
		t.Fatalf("got %d downloads, want 2", len(downloads))
	}
	for _, d := range downloads {
		switch d.TorrentHash {
		case testInfoHash:
			if d.CompletedAt != nil {
				t.Errorf("running download completed_at = %v, want nil", *d.CompletedAt)
			}
		case otherHash:
			if d.CompletedAt == nil || !d.CompletedAt.Equal(done) {
				t.Errorf("finished download completed_at = %v, want %v", d.CompletedAt, done)
			}
		default:
			t.Errorf("unexpected torrent_hash %q", d.TorrentHash)
		}
	}
}
