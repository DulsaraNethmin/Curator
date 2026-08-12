package download

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/qbit"
	"github.com/DulsaraNethmin/curator/internal/store"
)

// fakeImporter satisfies both seams: Importer, which the poll tick uses and
// whose methods cannot fail, and ManualImporter, which the API uses and whose
// method can. *importer.Importer satisfies both the same way.
type fakeImporter struct {
	tried     []importCall
	refreshes int
	movie     store.Movie
	err       error
}

type importCall struct {
	hash        string
	contentPath string
}

func (f *fakeImporter) TryImport(_ context.Context, t qbit.Torrent, d store.Download) {
	f.tried = append(f.tried, importCall{hash: d.TorrentHash, contentPath: t.ContentPath})
}

func (f *fakeImporter) Refresh(context.Context) { f.refreshes++ }

func (f *fakeImporter) Import(_ context.Context, t qbit.Torrent, d store.Download) (store.Movie, error) {
	f.tried = append(f.tried, importCall{hash: d.TorrentHash, contentPath: t.ContentPath})
	if f.err != nil {
		return store.Movie{}, f.err
	}
	return f.movie, nil
}

func completedTorrent() qbit.Torrent {
	return qbit.Torrent{
		Hash: strings.ToLower(testHash), Name: "Interstellar.2014.1080p", State: "stalledUP",
		Progress: 1, ContentPath: "/downloads/complete/curator/Interstellar.2014.1080p",
		Category: "curator", SavePath: "/downloads/complete/curator",
	}
}

func TestTickImportsACompletedTorrent(t *testing.T) {
	client := &fakeClient{torrents: []qbit.Torrent{completedTorrent()}}
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
	client := &fakeClient{torrents: []qbit.Torrent{completedTorrent()}}
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
	client := &fakeClient{torrents: []qbit.Torrent{completedTorrent()}}
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
	for _, state := range []string{"downloading", "stalledDL", "metaDL", "queuedDL", "moving", "error"} {
		torrent := completedTorrent()
		torrent.State = state
		torrent.Progress = 0.5

		client := &fakeClient{torrents: []qbit.Torrent{torrent}}
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

// `moving` deserves its own note: qBittorrent reports it while relocating a
// finished torrent, and phase 3 maps it to downloading. That is what keeps the
// importer away from a content_path that is mid-move, which is the one thing
// the unmeasured complete-vs-incomplete question could have bitten us on.
func TestTickDoesNotImportATorrentBeingMoved(t *testing.T) {
	torrent := completedTorrent()
	torrent.State = "moving"

	client := &fakeClient{torrents: []qbit.Torrent{torrent}}
	st := newFakeStore()
	st.byHash[testHash] = store.Download{TorrentHash: testHash, State: store.DownloadDownloading, Progress: 1}

	im := &fakeImporter{}
	if err := newPoller(client, st).WithImporter(im).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(im.tried) != 0 {
		t.Error("a torrent being relocated was imported; its content_path is mid-move")
	}
}

// POST /Library/Refresh is a whole-library scan, so a batch of imports must
// produce one, not one each.
func TestTickRefreshesOncePerTick(t *testing.T) {
	var torrents []qbit.Torrent
	st := newFakeStore()
	for _, hash := range []string{testHash, "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"} {
		torrent := completedTorrent()
		torrent.Hash = strings.ToLower(hash)
		torrents = append(torrents, torrent)
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
	client := &fakeClient{torrents: []qbit.Torrent{completedTorrent()}}
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
	torrent := completedTorrent()
	client := &fakeClient{byHash: &torrent}
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
	// The content path came from qBittorrent, not from the caller: a client
	// naming a path curator will hardlink from is the mistake D10 refused.
	if im.tried[0].contentPath != torrent.ContentPath {
		t.Errorf("content_path = %q, want qBittorrent's %q", im.tried[0].contentPath, torrent.ContentPath)
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
	torrent := completedTorrent()
	if _, err := newImportService(&fakeClient{byHash: &torrent}, st, nil).Import(context.Background(), testHash); !errors.Is(err, ErrUnconfigured) {
		t.Errorf("nil importer: err = %v, want ErrUnconfigured", err)
	}
}

func TestServiceImportUnknownHash(t *testing.T) {
	torrent := completedTorrent()
	im := &fakeImporter{}

	_, err := newImportService(&fakeClient{byHash: &torrent}, newFakeStore(), im).
		Import(context.Background(), testHash)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want store.ErrNotFound", err)
	}
	if len(im.tried) != 0 {
		t.Error("the importer ran for a download that does not exist")
	}
}

func TestServiceImportReportsQBittorrent(t *testing.T) {
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
	torrent := completedTorrent()
	torrent.State = "downloading"
	torrent.Progress = 0.4

	st := newFakeStore()
	st.byHash[testHash] = store.Download{TorrentHash: testHash, State: store.DownloadDownloading}
	im := &fakeImporter{}

	_, err := newImportService(&fakeClient{byHash: &torrent}, st, im).Import(context.Background(), testHash)
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
