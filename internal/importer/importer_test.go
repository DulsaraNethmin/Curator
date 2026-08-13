package importer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/library"
	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// featureSize clears library.DefaultMinFeatureBytes. The files are sparse, so
// this costs no disk and no time — and it means the tests exercise the real
// 50 MiB floor rather than a lowered one.
const featureSize = 60 << 20

const testHash = "AAAABBBBCCCCDDDDEEEEFFFF0000111122223333"

var importAt = time.Date(2026, 8, 12, 19, 30, 0, 0, time.UTC)

// --- the happy path ---------------------------------------------------------

func TestImportHardlinksTheFeatureIntoTheLibrary(t *testing.T) {
	h := newHarness(t)
	content := h.download("Interstellar.2014.1080p.BluRay.x264", "Interstellar.2014.1080p.mkv")

	before := h.snapshotDownloads()

	got, err := h.importer.Import(context.Background(), completed(content), h.dl)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	wantDir := filepath.Join(h.library, "Interstellar (2014)")
	wantFile := filepath.Join(wantDir, "Interstellar (2014).mkv")

	if _, err := os.Stat(wantFile); err != nil {
		t.Fatalf("the library entry is missing: %v", err)
	}

	// Proof one: one inode, two names.
	src := filepath.Join(content, "Interstellar.2014.1080p.mkv")
	if !os.SameFile(statOf(t, src), statOf(t, wantFile)) {
		t.Error("os.SameFile is false: the library entry is a copy, not a hardlink")
	}
	if n := nlinkOf(t, wantFile); n != 2 {
		t.Errorf("link count = %d, want 2", n)
	}

	// Proof two, independent of the first: bytes written through the source show
	// up through the destination.
	writeAt(t, src, "hardlinked, not copied")
	if got := readAt(t, wantFile, len("hardlinked, not copied")); got != "hardlinked, not copied" {
		t.Errorf("destination = %q after writing through the source", got)
	}

	// The store was told about the FOLDER, which is the scanner's identity key.
	if len(h.store.marked) != 1 {
		t.Fatalf("MarkImported called %d times, want 1", len(h.store.marked))
	}
	call := h.store.marked[0]
	if call.hash != testHash {
		t.Errorf("hash = %q, want %q", call.hash, testHash)
	}
	if call.path != wantDir {
		t.Errorf("library_path = %q, want the folder %q", call.path, wantDir)
	}
	if call.size != featureSize {
		t.Errorf("size = %d, want %d", call.size, featureSize)
	}
	if !call.at.Equal(importAt) {
		t.Errorf("at = %v, want the injected clock %v", call.at, importAt)
	}
	if got.Status != store.StatusImported {
		t.Errorf("returned status = %q, want %q", got.Status, store.StatusImported)
	}

	h.assertDownloadsIntact(before)
}

// content_path is the file itself for a single-file torrent, which is a shape
// the configuration cannot choose between in advance.
func TestImportAcceptsASingleFileContentPath(t *testing.T) {
	h := newHarness(t)
	file := filepath.Join(h.downloads, "complete", "curator", "Interstellar.2014.mkv")
	sparseFile(t, file, featureSize)

	if _, err := h.importer.Import(context.Background(), completed(file), h.dl); err != nil {
		t.Fatalf("Import: %v", err)
	}

	dst := filepath.Join(h.library, "Interstellar (2014)", "Interstellar (2014).mkv")
	if !os.SameFile(statOf(t, file), statOf(t, dst)) {
		t.Error("the single-file torrent was not hardlinked")
	}
}

func TestImportPrefersTheLargestAndReportsTheOthers(t *testing.T) {
	h := newHarness(t)
	content := filepath.Join(h.downloads, "complete", "curator", "Double Feature")
	sparseFile(t, filepath.Join(content, "part1.mkv"), featureSize)
	sparseFile(t, filepath.Join(content, "part2.mkv"), featureSize*2)
	sparseFile(t, filepath.Join(content, "sample.mkv"), 4<<20) // under the floor

	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err != nil {
		t.Fatalf("Import: %v", err)
	}

	dst := filepath.Join(h.library, "Interstellar (2014)", "Interstellar (2014).mkv")
	if !os.SameFile(statOf(t, filepath.Join(content, "part2.mkv")), statOf(t, dst)) {
		t.Error("the largest video did not win")
	}
	if h.store.marked[0].size != featureSize*2 {
		t.Errorf("size = %d, want the largest %d", h.store.marked[0].size, featureSize*2)
	}
	// A double feature must be visible rather than silently half-imported.
	if !strings.Contains(h.logs.String(), "more than one video") {
		t.Errorf("the extra video was not logged; log was:\n%s", h.logs.String())
	}
}

// --- the untrusted title ----------------------------------------------------

func TestImportRejectsATitleThatIsNotAName(t *testing.T) {
	for _, title := range []string{"../../etc/cron.d", "Movies/Evil", "..", ".", `back\slash`} {
		h := newHarness(t)
		h.store.movies[1] = store.Movie{ID: 1, Title: title, Year: 2014}
		content := h.download("release", "feature.mkv")

		if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err == nil {
			t.Errorf("title %q was accepted", title)
		}

		// Nothing may have been created, anywhere under the library root.
		if entries, err := os.ReadDir(h.library); err != nil {
			t.Fatalf("read library: %v", err)
		} else if len(entries) != 0 {
			t.Errorf("title %q: the library is not empty: %v", title, names(entries))
		}
		if len(h.store.marked) != 0 {
			t.Errorf("title %q: the store was written to", title)
		}
	}
}

// Belt and braces over DestFolder: whatever the naming rules become, a
// destination outside the library root creates nothing.
func TestAssertInsideLibraryRefusesAnEscape(t *testing.T) {
	h := newHarness(t)

	if err := h.importer.assertInsideLibrary(filepath.Join(h.library, "Interstellar (2014)")); err != nil {
		t.Errorf("a normal destination was refused: %v", err)
	}
	for _, dest := range []string{
		filepath.Join(h.library, "..", "elsewhere"),
		filepath.Join(h.library, "..", "..", "etc"),
		"/etc/cron.d",
	} {
		if err := h.importer.assertInsideLibrary(dest); err == nil {
			t.Errorf("%s was accepted as inside %s", dest, h.library)
		}
	}
}

// --- path translation -------------------------------------------------------

func TestTranslate(t *testing.T) {
	cases := []struct {
		name    string
		paths   Paths
		in      string
		want    string
		wantErr bool
	}{{
		// The laptop case, and the case where curator shares the mount. No
		// configuration at all, and the path is used exactly as reported.
		name:  "unset passes through verbatim",
		paths: Paths{},
		in:    "/downloads/complete/curator/Movie.2014",
		want:  "/downloads/complete/curator/Movie.2014",
	}, {
		name:  "configured rewrites the prefix",
		paths: Paths{Curator: "/media/storage/media/downloads", QBit: "/downloads"},
		in:    "/downloads/complete/curator/Movie.2014",
		want:  "/media/storage/media/downloads/complete/curator/Movie.2014",
	}, {
		name:  "the root itself",
		paths: Paths{Curator: "/media/downloads", QBit: "/downloads"},
		in:    "/downloads",
		want:  "/media/downloads",
	}, {
		name:  "a trailing slash on the configured root",
		paths: Paths{Curator: "/media/downloads", QBit: "/downloads/"},
		in:    "/downloads/complete/x",
		want:  "/media/downloads/complete/x",
	}, {
		// Set but not matching is an error, not a pass-through: someone
		// configured a translation and it did not apply.
		name:    "a path outside the configured root",
		paths:   Paths{Curator: "/media/downloads", QBit: "/downloads"},
		in:      "/var/lib/torrents/Movie.2014",
		wantErr: true,
	}, {
		// The boundary check. A prefix comparison without it maps /downloads2
		// into the middle of the library.
		name:    "a sibling directory sharing the prefix",
		paths:   Paths{Curator: "/media/downloads", QBit: "/downloads"},
		in:      "/downloads2/complete/x",
		wantErr: true,
	}, {
		name:    "an empty content path",
		paths:   Paths{},
		in:      "   ",
		wantErr: true,
	}, {
		name:    "an empty content path with translation configured",
		paths:   Paths{Curator: "/media/downloads", QBit: "/downloads"},
		in:      "",
		wantErr: true,
	}}

	for _, c := range cases {
		im := New(nil, "/library", c.paths, nil, discardLogger())
		got, err := im.translate(c.in)
		switch {
		case c.wantErr && err == nil:
			t.Errorf("%s: translate(%q) = %q, want an error", c.name, c.in, got)
		case !c.wantErr && err != nil:
			t.Errorf("%s: translate(%q): %v", c.name, c.in, err)
		case !c.wantErr && got != filepath.FromSlash(c.want):
			t.Errorf("%s: translate(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// --- nothing to import ------------------------------------------------------

// ErrNoVideo is not a failed download: the torrent fetched what it advertised.
// The row must stay `completed` so the next tick retries, which means the store
// is not touched at all.
func TestImportWithNoVideoIsErrNoVideoAndWritesNothing(t *testing.T) {
	h := newHarness(t)
	content := filepath.Join(h.downloads, "complete", "curator", "release")
	sparseFile(t, filepath.Join(content, "readme.nfo"), 1024)
	sparseFile(t, filepath.Join(content, "sample.mkv"), 4<<20) // under the 50 MiB floor

	_, err := h.importer.Import(context.Background(), completed(content), h.dl)
	if !errors.Is(err, library.ErrNoVideo) {
		t.Fatalf("err = %v, want library.ErrNoVideo", err)
	}
	if len(h.store.marked) != 0 {
		t.Error("the store was written to; the row must stay completed for the next tick")
	}
}

// The ordering test. MkdirAll must come after the source file has been chosen,
// or a failure leaves an empty "Title (Year)/" that the scanner records as a
// zero-size movie — and 15 of the 29 real library folders are already empty, so
// that is not a hypothetical shape.
func TestImportLeavesNoEmptyFolderWhenThereIsNothingToLink(t *testing.T) {
	h := newHarness(t)
	content := filepath.Join(h.downloads, "complete", "curator", "release")
	if err := os.MkdirAll(content, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err == nil {
		t.Fatal("an empty content path imported successfully")
	}

	entries, err := os.ReadDir(h.library)
	if err != nil {
		t.Fatalf("read library: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the library holds %v; a failed import must create nothing", names(entries))
	}
}

// --- retry and failure ------------------------------------------------------

// A process that died between linking and recording finds its own link on the
// next tick. That has to be success, or the import is stuck for ever.
func TestImportOnItsOwnExistingLinkSucceedsAndStillRecords(t *testing.T) {
	h := newHarness(t)
	content := h.download("release", "feature.mkv")
	ctx := context.Background()

	if _, err := h.importer.Import(ctx, completed(content), h.dl); err != nil {
		t.Fatalf("first Import: %v", err)
	}
	if _, err := h.importer.Import(ctx, completed(content), h.dl); err != nil {
		t.Fatalf("second Import: %v — this is the crashed-run retry path", err)
	}

	if len(h.store.marked) != 2 {
		t.Errorf("MarkImported called %d times, want 2 — the retry must still record", len(h.store.marked))
	}
	dst := filepath.Join(h.library, "Interstellar (2014)", "Interstellar (2014).mkv")
	if n := nlinkOf(t, dst); n != 2 {
		t.Errorf("link count = %d after re-importing, want still 2", n)
	}
}

// The link is a fact on disk. Removing it because the database write failed
// would delete the library's only copy if the write actually landed, and the
// next tick re-links onto it harmlessly anyway.
func TestImportKeepsTheLinkWhenTheStoreFails(t *testing.T) {
	h := newHarness(t)
	h.store.markErr = errors.New("database is locked")
	content := h.download("release", "feature.mkv")

	before := h.snapshotDownloads()

	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err == nil {
		t.Fatal("Import succeeded with a failing store")
	}

	dst := filepath.Join(h.library, "Interstellar (2014)", "Interstellar (2014).mkv")
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("the link was removed after the store failed: %v", err)
	}
	h.assertDownloadsIntact(before)
}

func TestImportReportsAMissingMovieRow(t *testing.T) {
	h := newHarness(t)
	h.store.getErr = store.ErrNotFound
	content := h.download("release", "feature.mkv")

	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want store.ErrNotFound", err)
	}
	if entries, _ := os.ReadDir(h.library); len(entries) != 0 {
		t.Error("a folder was created for a movie row that does not exist")
	}
}

// --- TryImport --------------------------------------------------------------

// D14 retries a failing import on every tick, by design. What is suppressed is
// the log, not the retry — a permanently broken import must not write a warning
// every ten seconds for ever.
func TestTryImportSuppressesARepeatedIdenticalFailure(t *testing.T) {
	h := newHarness(t)
	content := filepath.Join(h.downloads, "complete", "curator", "release")
	if err := os.MkdirAll(content, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ctx := context.Background()

	h.importer.TryImport(ctx, completed(content), h.dl)
	h.importer.TryImport(ctx, completed(content), h.dl)
	h.importer.TryImport(ctx, completed(content), h.dl)

	if n := strings.Count(h.logs.String(), "import failed"); n != 1 {
		t.Errorf("logged %d failures for three identical ticks, want 1:\n%s", n, h.logs.String())
	}

	// A different failure for the same torrent is news and is logged.
	h.store.getErr = errors.New("database is locked")
	h.importer.TryImport(ctx, completed(content), h.dl)
	if n := strings.Count(h.logs.String(), "import failed"); n != 2 {
		t.Errorf("a different error logged %d times in total, want 2:\n%s", n, h.logs.String())
	}

	// A success clears the suppression, so a later relapse is reported again.
	h.store.getErr = nil
	sparseFile(t, filepath.Join(content, "feature.mkv"), featureSize)
	h.importer.TryImport(ctx, completed(content), h.dl)
	if !strings.Contains(h.logs.String(), "\"imported\"") && !strings.Contains(h.logs.String(), "msg=imported") {
		t.Errorf("the success was not logged:\n%s", h.logs.String())
	}

	h.store.getErr = errors.New("database is locked")
	h.importer.TryImport(ctx, completed(content), h.dl)
	if n := strings.Count(h.logs.String(), "import failed"); n != 3 {
		t.Errorf("after a success the relapse logged %d in total, want 3:\n%s", n, h.logs.String())
	}
}

// TryImport returns nothing at all, which is how "an import cannot fail a tick"
// is a fact about the type rather than a rule someone has to remember.
func TestTryImportNeverPanics(t *testing.T) {
	h := newHarness(t)
	h.store.getErr = errors.New("boom")

	h.importer.TryImport(context.Background(), torrent.Torrent{}, h.dl)
	h.importer.TryImport(context.Background(), completed("/does/not/exist"), store.Download{})
}

// --- Refresh ----------------------------------------------------------------

func TestRefreshIsOncePerCallAndOnlyAfterAnImport(t *testing.T) {
	h := newHarness(t)
	refresher := &fakeRefresher{}
	h.importer.refresher = refresher
	ctx := context.Background()

	// Nothing imported yet: a tick must not queue a whole-library scan.
	h.importer.Refresh(ctx)
	if refresher.calls != 0 {
		t.Fatalf("refreshed %d times with nothing imported, want 0", refresher.calls)
	}

	// Three imports in one tick, then one Refresh: one scan, not three.
	for i, name := range []string{"a", "b", "c"} {
		h.store.movies[int64(i+1)] = store.Movie{ID: int64(i + 1), Title: "Film " + name, Year: 2014}
		content := h.download("release-"+name, "feature.mkv")
		if _, err := h.importer.Import(ctx, completed(content), store.Download{MovieID: int64(i + 1), TorrentHash: testHash}); err != nil {
			t.Fatalf("Import %s: %v", name, err)
		}
	}
	h.importer.Refresh(ctx)
	if refresher.calls != 1 {
		t.Errorf("refreshed %d times after three imports in one tick, want 1", refresher.calls)
	}

	// A second tick with nothing new must not refresh again.
	h.importer.Refresh(ctx)
	if refresher.calls != 1 {
		t.Errorf("refreshed %d times in total, want 1 — an idle tick must cost nothing", refresher.calls)
	}
}

func TestRefreshWithNoRefresherIsANoOp(t *testing.T) {
	h := newHarness(t) // built with a nil refresher: JELLYFIN_API_KEY unset
	content := h.download("release", "feature.mkv")
	ctx := context.Background()

	if _, err := h.importer.Import(ctx, completed(content), h.dl); err != nil {
		t.Fatalf("Import: %v", err)
	}
	h.importer.Refresh(ctx) // must not panic
}

// D15: whether a media server has noticed is softer than whether the file is in
// the library, so a failure changes nothing observable and does not re-arm.
func TestRefreshFailureChangesNothing(t *testing.T) {
	h := newHarness(t)
	refresher := &fakeRefresher{err: errors.New("jellyfin is down")}
	h.importer.refresher = refresher
	ctx := context.Background()

	content := h.download("release", "feature.mkv")
	if _, err := h.importer.Import(ctx, completed(content), h.dl); err != nil {
		t.Fatalf("Import: %v", err)
	}

	h.importer.Refresh(ctx)
	h.importer.Refresh(ctx)
	if refresher.calls != 1 {
		t.Errorf("refreshed %d times, want 1 — a failure must not re-arm and warn every tick", refresher.calls)
	}
	if !strings.Contains(h.logs.String(), "jellyfin refresh failed") {
		t.Error("the failure was not logged at all")
	}
	// The import stands.
	dst := filepath.Join(h.library, "Interstellar (2014)", "Interstellar (2014).mkv")
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("the import was undone by a failed refresh: %v", err)
	}
}

// --- harness ----------------------------------------------------------------

type harness struct {
	t         *testing.T
	root      string
	downloads string
	library   string
	store     *fakeStore
	importer  *Importer
	logs      *bytes.Buffer
	dl        store.Download
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	downloads := filepath.Join(root, "downloads")
	lib := filepath.Join(root, "library", "movies")
	for _, dir := range []string{downloads, lib} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	st := &fakeStore{movies: map[int64]store.Movie{
		1: {ID: 1, Title: "Interstellar", Year: 2014},
	}}
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Paths.Curator is empty: the fixtures are real paths on this machine, which
	// is the verbatim case. Translation has its own table test.
	im := New(st, lib, Paths{}, nil, log)
	im.now = func() time.Time { return importAt }

	return &harness{
		t: t, root: root, downloads: downloads, library: lib,
		store: st, importer: im, logs: logs,
		dl: store.Download{MovieID: 1, TorrentHash: testHash},
	}
}

// download builds a release folder holding one feature file and returns its path.
func (h *harness) download(folder, file string) string {
	h.t.Helper()
	content := filepath.Join(h.downloads, "complete", "curator", folder)
	sparseFile(h.t, filepath.Join(content, file), featureSize)
	return content
}

// snapshotDownloads records every file under the downloads root by relative
// path, size and inode. Paired with assertDownloadsIntact it is D8 — no source
// file is ever deleted, moved or truncated — as an assertion rather than a
// promise. The link count is deliberately not part of it: it legitimately goes
// from 1 to 2, which is the whole point.
func (h *harness) snapshotDownloads() map[string]string {
	h.t.Helper()
	snapshot := map[string]string{}

	err := filepath.WalkDir(h.downloads, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(h.downloads, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		snapshot[rel] = fmt.Sprintf("%d:%d", info.Size(), inodeOf(h.t, p))
		return nil
	})
	if err != nil {
		h.t.Fatalf("snapshot downloads: %v", err)
	}
	return snapshot
}

func (h *harness) assertDownloadsIntact(before map[string]string) {
	h.t.Helper()
	after := h.snapshotDownloads()

	for rel, want := range before {
		got, ok := after[rel]
		if !ok {
			h.t.Errorf("%s is gone from the download directory; D8 says a source file is never deleted", rel)
			continue
		}
		if got != want {
			h.t.Errorf("%s changed: %s -> %s", rel, want, got)
		}
	}
	for rel := range after {
		if _, ok := before[rel]; !ok {
			h.t.Errorf("%s appeared in the download directory; nothing should be written there", rel)
		}
	}
}

// completed is a finished torrent as a backend hands one over: curator's own
// state vocabulary and its upper-case hash, whichever client produced it.
func completed(contentPath string) torrent.Torrent {
	return torrent.Torrent{
		Hash:        testHash,
		Name:        filepath.Base(contentPath),
		State:       torrent.StateCompleted,
		Progress:    1,
		ContentPath: contentPath,
		Category:    "curator",
	}
}

// --- fakes ------------------------------------------------------------------

type markCall struct {
	hash string
	path string
	size int64
	at   time.Time
}

type fakeStore struct {
	movies  map[int64]store.Movie
	getErr  error
	markErr error
	marked  []markCall
}

func (f *fakeStore) GetMovie(_ context.Context, id int64) (store.Movie, error) {
	if f.getErr != nil {
		return store.Movie{}, f.getErr
	}
	m, ok := f.movies[id]
	if !ok {
		return store.Movie{}, store.ErrNotFound
	}
	return m, nil
}

func (f *fakeStore) MarkImported(_ context.Context, hash, libraryPath string, size int64, at time.Time) (store.Movie, error) {
	if f.markErr != nil {
		return store.Movie{}, f.markErr
	}
	f.marked = append(f.marked, markCall{hash: hash, path: libraryPath, size: size, at: at})
	return store.Movie{ID: 1, Title: "Interstellar", Year: 2014, Status: store.StatusImported, LibraryPath: &libraryPath}, nil
}

type fakeRefresher struct {
	calls int
	err   error
}

func (f *fakeRefresher) RefreshLibrary(context.Context) error {
	f.calls++
	return f.err
}

// --- small helpers ----------------------------------------------------------

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

// sparseFile creates a file of the given apparent size without writing its
// blocks, so the real 50 MiB feature floor is exercised at no cost in disk or
// time.
func sparseFile(t *testing.T, path string, size int64) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatalf("truncate %s: %v", path, err)
	}
	return path
}

func statOf(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}

// nlinkOf and inodeOf convert explicitly, because syscall.Stat_t.Nlink is
// uint16 on darwin and uint64 on linux — an inline comparison compiles on a
// laptop and breaks GOOS=linux GOARCH=arm64 go vet, which is how this ships.
func nlinkOf(t *testing.T, path string) uint64 {
	t.Helper()
	st, ok := statOf(t, path).Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no syscall.Stat_t on this platform")
	}
	return uint64(st.Nlink)
}

func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	st, ok := statOf(t, path).Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no syscall.Stat_t on this platform")
	}
	return uint64(st.Ino)
}

func writeAt(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteAt([]byte(text), 0); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readAt(t *testing.T, path string, n int) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(buf)
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
