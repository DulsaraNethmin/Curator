package download

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/library"
	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// fakeImporter satisfies both seams: Importer, which the poll tick uses and
// whose methods cannot fail, and ManualImporter, which the API uses and whose
// method can. *importer.Importer satisfies both the same way.
type fakeImporter struct {
	tried     []importCall
	refreshes int
	movie     store.Movie
	err       error

	// D19's delete path.
	removed   []string
	removeErr error
}

type importCall struct {
	hash        string
	contentPath string
}

func (f *fakeImporter) TryImport(_ context.Context, t torrent.Torrent, d store.Download) {
	f.tried = append(f.tried, importCall{hash: d.TorrentHash, contentPath: t.ContentPath})
}

func (f *fakeImporter) Refresh(context.Context) { f.refreshes++ }

func (f *fakeImporter) RemoveFromLibrary(path string) error {
	f.removed = append(f.removed, path)
	return f.removeErr
}

func (f *fakeImporter) Import(_ context.Context, t torrent.Torrent, d store.Download) (store.Movie, error) {
	f.tried = append(f.tried, importCall{hash: d.TorrentHash, contentPath: t.ContentPath})
	if f.err != nil {
		return store.Movie{}, f.err
	}
	return f.movie, nil
}

func completedTorrent() torrent.Torrent {
	return torrent.Torrent{
		Hash: testHash, Name: "Interstellar.2014.1080p", State: torrent.StateCompleted,
		Progress: 1, ContentPath: "/downloads/complete/curator/Interstellar.2014.1080p",
		Category: "curator",
	}
}

func TestTickImportsACompletedTorrent(t *testing.T) {
	client := &fakeClient{torrents: []torrent.Torrent{completedTorrent()}}
	st := newFakeStore()
	st.byHash[testHash] = store.Download{TorrentHash: testHash, State: store.DownloadDownloading, Progress: 0.9}

	im := &fakeImporter{}
	if err := newPoller(client, st).WithImporter(im).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if len(im.tried) != 1 {
		t.Fatalf("TryImport called %d times, want 1", len(im.tried))
	}
	if im.tried[0].hash != testHash {
		t.Errorf("hash = %q, want the row's %q", im.tried[0].hash, testHash)
	}
	// The tick already holds the torrent, content_path included, which is what
	// makes D14 cost nothing.
	if im.tried[0].contentPath != completedTorrent().ContentPath {
		t.Errorf("content_path = %q, want the torrent's", im.tried[0].contentPath)
	}
}

// The test the `continue`-to-`if` change in Tick exists for, and the one that
// fails against phase 3's control flow.
//
// On every tick after the one that saw the torrent finish, the state and the
// progress are unchanged and completed_at is already stamped — so a `continue`
// past the write also skipped the import, and an import that failed once would
// never be attempted again. D14's whole point is that the recovery path and the
// normal path are the same code.
func TestTickRetriesTheImportOnEveryTickWhileTheStateHolds(t *testing.T) {
	client := &fakeClient{torrents: []torrent.Torrent{completedTorrent()}}
	st := newFakeStore()

	// The row as it looks after the tick that recorded completion: nothing about
	// it will change again.
	at := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	st.byHash[testHash] = store.Download{
		TorrentHash: testHash, State: store.DownloadCompleted, Progress: 1, CompletedAt: &at,
	}

	im := &fakeImporter{}
	poller := newPoller(client, st).WithImporter(im)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := poller.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}

	if len(im.tried) != 3 {
		t.Errorf("TryImport called %d times over three ticks, want 3 — a failing import must keep being retried", len(im.tried))
	}
	// And nothing was written: the row genuinely did not move.
	if len(st.updates) != 0 {
		t.Errorf("updates = %d, want 0 — nothing about the row changed", len(st.updates))
	}
}

// imported is where a download stops. Phase 3 already skips those rows, and the
// importer must never see one, or every tick would re-link a film for as long as
// its torrent kept seeding.
func TestTickDoesNotImportARowThatIsAlreadyImported(t *testing.T) {
	client := &fakeClient{torrents: []torrent.Torrent{completedTorrent()}}
	st := newFakeStore()
	st.byHash[testHash] = store.Download{TorrentHash: testHash, State: store.DownloadImported, Progress: 1}

	im := &fakeImporter{}
	if err := newPoller(client, st).WithImporter(im).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(im.tried) != 0 {
		t.Errorf("TryImport called %d times on an imported row, want 0", len(im.tried))
	}
}

func TestTickDoesNotImportAnUnfinishedTorrent(t *testing.T) {
	// Curator's vocabulary, not qBittorrent's: which of its 23 states produce
	// these — including `moving`, which is a finished torrent being relocated and
	// must NOT be imported from mid-move — is asserted in internal/qbit's state
	// test, where that vocabulary lives.
	for _, state := range []string{torrent.StateDownloading, torrent.StateQueued, torrent.StateFailed} {
		tor := completedTorrent()
		tor.State = state
		tor.Progress = 0.5

		client := &fakeClient{torrents: []torrent.Torrent{tor}}
		st := newFakeStore()
		st.byHash[testHash] = store.Download{TorrentHash: testHash, State: store.DownloadQueued}

		im := &fakeImporter{}
		if err := newPoller(client, st).WithImporter(im).Tick(context.Background()); err != nil {
			t.Fatalf("%s: Tick: %v", state, err)
		}
		if len(im.tried) != 0 {
			t.Errorf("%s: TryImport called on an unfinished torrent", state)
		}
	}
}

// POST /Library/Refresh is a whole-library scan, so a batch of imports must
// produce one, not one each.
func TestTickRefreshesOncePerTick(t *testing.T) {
	var torrents []torrent.Torrent
	st := newFakeStore()
	for _, hash := range []string{testHash, "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"} {
		tor := completedTorrent()
		tor.Hash = hash
		torrents = append(torrents, tor)
		st.byHash[hash] = store.Download{TorrentHash: hash, State: store.DownloadDownloading, Progress: 0.9}
	}

	im := &fakeImporter{}
	poller := newPoller(&fakeClient{torrents: torrents}, st).WithImporter(im)

	if err := poller.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(im.tried) != 3 {
		t.Errorf("TryImport called %d times, want 3", len(im.tried))
	}
	if im.refreshes != 1 {
		t.Errorf("Refresh called %d times for three imports in one tick, want 1", im.refreshes)
	}

	if err := poller.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	if im.refreshes != 2 {
		t.Errorf("Refresh called %d times over two ticks, want 2 — it is once per tick, and the importer no-ops when nothing was imported", im.refreshes)
	}
}

// A nil importer must leave the poller behaving exactly as phase 3 shipped it.
// That is what lets every phase 3 test pass without being touched.
func TestTickWithNoImporterIsPhaseThree(t *testing.T) {
	client := &fakeClient{torrents: []torrent.Torrent{completedTorrent()}}
	st := newFakeStore()
	st.byHash[testHash] = store.Download{TorrentHash: testHash, State: store.DownloadDownloading, Progress: 0.9}

	if err := newPoller(client, st).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(st.updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(st.updates))
	}
	if st.updates[0].state != store.DownloadCompleted || st.updates[0].completedAt == nil {
		t.Errorf("update = %+v, want completed with a completed_at", st.updates[0])
	}
}

// --- the manual import ------------------------------------------------------

func newImportService(client TorrentClient, st Store, im ManualImporter) *Service {
	s := NewService(client, st, newResolver("magnet:?xt=urn:btih:"+testHash), "curator", quiet())
	if im != nil {
		s = s.WithImporter(im)
	}
	return s
}

func TestServiceImportRunsTheSameImporter(t *testing.T) {
	tor := completedTorrent()
	client := &fakeClient{byHash: &tor}
	st := newFakeStore()
	st.byHash[testHash] = store.Download{TorrentHash: testHash, State: store.DownloadCompleted, Progress: 1}

	im := &fakeImporter{movie: store.Movie{ID: 7, Title: "Interstellar", Status: store.StatusImported}}

	got, err := newImportService(client, st, im).Import(context.Background(), strings.ToLower(testHash))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got.ID != 7 {
		t.Errorf("movie = %+v, want the importer's row", got)
	}
	if len(im.tried) != 1 {
		t.Fatalf("Import called %d times, want 1", len(im.tried))
	}
	// The content path came from the torrent client, not from the caller: a
	// client naming a path curator will hardlink from is the mistake D10 refused.
	if im.tried[0].contentPath != tor.ContentPath {
		t.Errorf("content_path = %q, want the client's %q", im.tried[0].contentPath, tor.ContentPath)
	}
}

func TestServiceImportWithoutCredentialsOrImporterIsUnconfigured(t *testing.T) {
	st := newFakeStore()
	st.byHash[testHash] = store.Download{TorrentHash: testHash, State: store.DownloadCompleted}

	// No qBittorrent client: QBIT_USER unset.
	if _, err := newImportService(nil, st, &fakeImporter{}).Import(context.Background(), testHash); !errors.Is(err, ErrUnconfigured) {
		t.Errorf("nil client: err = %v, want ErrUnconfigured", err)
	}

	// No importer attached at all.
	tor := completedTorrent()
	if _, err := newImportService(&fakeClient{byHash: &tor}, st, nil).Import(context.Background(), testHash); !errors.Is(err, ErrUnconfigured) {
		t.Errorf("nil importer: err = %v, want ErrUnconfigured", err)
	}
}

func TestServiceImportUnknownHash(t *testing.T) {
	tor := completedTorrent()
	im := &fakeImporter{}

	_, err := newImportService(&fakeClient{byHash: &tor}, newFakeStore(), im).
		Import(context.Background(), testHash)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want store.ErrNotFound", err)
	}
	if len(im.tried) != 0 {
		t.Error("the importer ran for a download that does not exist")
	}
}

func TestServiceImportReportsTheClient(t *testing.T) {
	st := newFakeStore()
	st.byHash[testHash] = store.Download{TorrentHash: testHash, State: store.DownloadCompleted}
	im := &fakeImporter{}

	// Unreachable.
	client := &fakeClient{byHashErr: errors.New("dial tcp: connection refused")}
	if _, err := newImportService(client, st, im).Import(context.Background(), testHash); !errors.Is(err, ErrClient) {
		t.Errorf("unreachable: err = %v, want ErrClient", err)
	}

	// Reachable, but the torrent is gone — somebody removed it by hand, which is
	// qBittorrent's business (D8) and leaves nothing here to import from.
	if _, err := newImportService(&fakeClient{}, st, im).Import(context.Background(), testHash); !errors.Is(err, ErrClient) {
		t.Errorf("missing torrent: err = %v, want ErrClient", err)
	}
	if len(im.tried) != 0 {
		t.Error("the importer ran without a torrent to import from")
	}
}

func TestServiceImportRefusesAnUnfinishedTorrent(t *testing.T) {
	tor := completedTorrent()
	tor.State = "downloading"
	tor.Progress = 0.4

	st := newFakeStore()
	st.byHash[testHash] = store.Download{TorrentHash: testHash, State: store.DownloadDownloading}
	im := &fakeImporter{}

	_, err := newImportService(&fakeClient{byHash: &tor}, st, im).Import(context.Background(), testHash)
	if !errors.Is(err, ErrNotCompleted) {
		t.Fatalf("err = %v, want ErrNotCompleted", err)
	}
	if !strings.Contains(err.Error(), "downloading") {
		t.Errorf("err = %q, want it to name the state qBittorrent reported", err)
	}
	if len(im.tried) != 0 {
		t.Error("the importer ran for a torrent that has not finished")
	}
}

// Measured against the real qBittorrent 5.1.2, not assumed: torrents/add
// answers "Fails." for a magnet it ALREADY HOLDS. Phase 3's spec says a
// duplicate add is silently idempotent — it is not, and that survived only
// because it was never run against a real client.
//
// So the add is advisory and the hash lookup is authoritative. Re-dispatching
// after a database reset, a restart, or a second click has to converge on the
// existing torrent rather than 502.
func TestDispatchConvergesWhenQBittorrentAlreadyHasTheTorrent(t *testing.T) {
	tor := completedTorrent()
	client := &fakeClient{
		addErr: errors.New(`qbit torrents/add: qBittorrent answered "Fails.", having accepted no magnet`),
		byHash: &tor,
	}
	st := newFakeStore()

	saved, err := newImportService(client, st, nil).
		Dispatch(context.Background(), Request{ReleaseID: "r1", Title: "Interstellar", Year: 2014})
	if err != nil {
		t.Fatalf("Dispatch: %v — a refused duplicate add must converge, not fail", err)
	}
	if saved.TorrentHash == "" {
		t.Error("no row was written for a torrent qBittorrent already had")
	}
	// The row is still only written after the hash lookup confirmed it.
	if got := strings.Join(client.calls, ","); got != "add,confirm" {
		t.Errorf("client calls = %q, want add then confirm", got)
	}
}

// The other half: a refused add AND no torrent afterwards is a real failure,
// and it must carry the add's own reason rather than a vaguer one.
func TestDispatchStillFailsWhenTheAddWasRefusedAndNothingIsThere(t *testing.T) {
	client := &fakeClient{
		addErr: errors.New("qbittorrent is out of disk"),
		byHash: nil,
	}
	st := newFakeStore()

	_, err := newImportService(client, st, nil).
		Dispatch(context.Background(), Request{ReleaseID: "r1", Title: "Interstellar", Year: 2014})
	if !errors.Is(err, ErrClient) {
		t.Fatalf("err = %v, want ErrClient", err)
	}
	if !strings.Contains(err.Error(), "out of disk") {
		t.Errorf("err = %q, want the add's own reason", err)
	}
	if st.writeCount != 0 {
		t.Errorf("wrote %d times; nothing may be recorded when there is no torrent", st.writeCount)
	}
}

// --- delete (D19) -----------------------------------------------------------

func newDeleteService(client TorrentClient, st Store, im ManualImporter) *Service {
	return NewService(client, st, newResolver("magnet:?xt=urn:btih:"+testHash), "curator", quiet()).
		WithImporter(im)
}

// The order is the decision: torrent and its files, then our hardlink, then the
// rows. A failure at any step has to leave something a retry can finish, and the
// rows survive longest because files with no row are silently re-adopted by the
// next scan.
func TestDeleteMovieRemovesTorrentThenLinkThenRows(t *testing.T) {
	path := "/library/movies/Interstellar (2014)"
	size := int64(3_219_186_473)

	client := &fakeClient{}
	st := newFakeStore()
	st.movie = store.Movie{ID: 1, Title: "Interstellar", Year: 2014, LibraryPath: &path, SizeBytes: &size}
	st.byHash[testHash] = store.Download{TorrentHash: testHash, State: store.DownloadImported}
	im := &fakeImporter{}

	report, err := newDeleteService(client, st, im).DeleteMovie(context.Background(), 1)
	if err != nil {
		t.Fatalf("DeleteMovie: %v", err)
	}

	if report.TorrentsRemoved != 1 || report.BytesFreed != size {
		t.Errorf("report = %+v, want 1 torrent and %d bytes", report, size)
	}
	// Deleting the files is the whole point: a hardlink removal alone frees
	// nothing, because the download copy still holds the inode.
	if !client.deleteFiles {
		t.Error("deleteFiles was false; the disk would not actually be freed")
	}
	if client.deleteCategory != "curator" {
		t.Errorf("required category = %q, want curator", client.deleteCategory)
	}
	if len(im.removed) != 1 || im.removed[0] != path {
		t.Errorf("removed from library = %v, want %q", im.removed, path)
	}
	if !st.deletedMovie {
		t.Error("the rows were not deleted")
	}

	// And the order.
	order := strings.Join(st.calls, ",") + "|" + strings.Join(client.calls, ",")
	if !strings.Contains(order, "delete-movie") || !strings.HasSuffix(strings.Join(st.calls, ","), "delete-movie") {
		t.Errorf("store calls = %v, want the row delete last", st.calls)
	}
}

// A torrent that could not be removed stops everything: the film is still in the
// library and still on disk, which is a state a retry can finish.
func TestDeleteMovieStopsWhenTheTorrentCannotBeRemoved(t *testing.T) {
	path := "/library/movies/Interstellar (2014)"

	client := &fakeClient{deleteErr: errors.New("connection refused")}
	st := newFakeStore()
	st.movie = store.Movie{ID: 1, Title: "Interstellar", Year: 2014, LibraryPath: &path}
	st.byHash[testHash] = store.Download{TorrentHash: testHash}
	im := &fakeImporter{}

	_, err := newDeleteService(client, st, im).DeleteMovie(context.Background(), 1)
	if !errors.Is(err, ErrClient) {
		t.Fatalf("err = %v, want ErrClient", err)
	}
	if len(im.removed) != 0 {
		t.Error("the library folder was removed even though the torrent was not")
	}
	if st.deletedMovie {
		t.Error("the rows were deleted even though the torrent was not removed")
	}
}

// The guard reaching the caller: a torrent in someone else's category must
// surface as a refusal, not as a partial delete.
func TestDeleteMovieSurfacesTheCategoryGuard(t *testing.T) {
	path := "/library/movies/Interstellar (2014)"

	client := &fakeClient{deleteErr: fmt.Errorf("category radarr: %w", torrent.ErrWrongCategory)}
	st := newFakeStore()
	st.movie = store.Movie{ID: 1, Title: "Interstellar", Year: 2014, LibraryPath: &path}
	st.byHash[testHash] = store.Download{TorrentHash: testHash}
	im := &fakeImporter{}

	_, err := newDeleteService(client, st, im).DeleteMovie(context.Background(), 1)
	if !errors.Is(err, torrent.ErrWrongCategory) {
		t.Fatalf("err = %v, want ErrWrongCategory to reach the caller", err)
	}
	if st.deletedMovie || len(im.removed) != 0 {
		t.Error("something was deleted despite the category guard firing")
	}
}

// A film scanned off disk has no torrent at all, and deleting it must not need
// one.
func TestDeleteMovieWithNoDownloads(t *testing.T) {
	path := "/library/movies/Interstellar (2014)"

	client := &fakeClient{}
	st := newFakeStore()
	st.movie = store.Movie{ID: 1, Title: "Interstellar", Year: 2014, LibraryPath: &path}
	im := &fakeImporter{}

	report, err := newDeleteService(client, st, im).DeleteMovie(context.Background(), 1)
	if err != nil {
		t.Fatalf("DeleteMovie: %v", err)
	}
	if report.TorrentsRemoved != 0 {
		t.Errorf("removed %d torrents, want 0", report.TorrentsRemoved)
	}
	if len(im.removed) != 1 || !st.deletedMovie {
		t.Error("the folder and the row should still have been removed")
	}
}

// A library_path outside LIBRARY_MOVIES is the row that most needs deleting, and
// until this it was the one that could not be: RemoveMovieFolder refuses such a
// path, the error propagated, step 3 never ran, and the request 500'd with the row
// still there. Nothing can serve that row either, so it was a row nothing could
// use and nothing could remove.
//
// The refusal here is the REAL one — produced by RemoveMovieFolder rather than
// hand-written — so this fails if the sentinel stops being wrapped.
func TestDeleteMovieLeavesAFolderOutsideTheRootAndStillRemovesTheRows(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	outside := filepath.Join(elsewhere, "Interstellar (2014)")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	client := &fakeClient{}
	st := newFakeStore()
	st.movie = store.Movie{ID: 1, Title: "Interstellar", Year: 2014, LibraryPath: &outside}
	im := &fakeImporter{removeErr: library.RemoveMovieFolder(root, outside)}
	if im.removeErr == nil {
		t.Fatal("RemoveMovieFolder accepted a path outside the root; this test proves nothing")
	}

	report, err := newDeleteService(client, st, im).DeleteMovie(context.Background(), 1)
	if err != nil {
		t.Fatalf("DeleteMovie: %v — a refusal to touch a folder must not block the rows", err)
	}
	if !st.deletedMovie {
		t.Error("the rows were not deleted")
	}
	if report.FolderLeft != outside {
		t.Errorf("folder_left = %q, want %q — the report must not claim the folder was removed", report.FolderLeft, outside)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("the folder outside the root was removed anyway: %v", err)
	}
}

// Every other failure still stops the delete. The narrowing is to one sentinel,
// not to "errors from step 2 do not count".
func TestDeleteMovieStopsWhenTheFolderCannotBeRemoved(t *testing.T) {
	path := "/library/movies/Interstellar (2014)"

	client := &fakeClient{}
	st := newFakeStore()
	st.movie = store.Movie{ID: 1, Title: "Interstellar", Year: 2014, LibraryPath: &path}
	im := &fakeImporter{removeErr: errors.New("remove: permission denied")}

	if _, err := newDeleteService(client, st, im).DeleteMovie(context.Background(), 1); err == nil {
		t.Fatal("DeleteMovie succeeded despite the folder not being removed")
	}
	if st.deletedMovie {
		t.Error("the rows were deleted while the folder is still there")
	}
}

// The ordinary case is untouched: inside the root, the folder goes and there is
// nothing to report about it.
func TestDeleteMovieInsideTheRootReportsNoFolderLeft(t *testing.T) {
	path := "/library/movies/Interstellar (2014)"

	client := &fakeClient{}
	st := newFakeStore()
	st.movie = store.Movie{ID: 1, Title: "Interstellar", Year: 2014, LibraryPath: &path}
	im := &fakeImporter{}

	report, err := newDeleteService(client, st, im).DeleteMovie(context.Background(), 1)
	if err != nil {
		t.Fatalf("DeleteMovie: %v", err)
	}
	if report.FolderLeft != "" {
		t.Errorf("folder_left = %q, want empty", report.FolderLeft)
	}
}

// A wanted film was never on disk: there is no folder, and asking to remove one
// must not fail the delete.
func TestDeleteMovieThatWasNeverImported(t *testing.T) {
	client := &fakeClient{}
	st := newFakeStore()
	st.movie = store.Movie{ID: 1, Title: "Interstellar", Year: 2014} // LibraryPath nil
	im := &fakeImporter{}

	report, err := newDeleteService(client, st, im).DeleteMovie(context.Background(), 1)
	if err != nil {
		t.Fatalf("DeleteMovie: %v", err)
	}
	if report.LibraryPath != "" {
		t.Errorf("library_path = %q, want empty", report.LibraryPath)
	}
	if !st.deletedMovie {
		t.Error("the row was not deleted")
	}
}
