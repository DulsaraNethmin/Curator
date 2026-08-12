// Package importer turns a completed download into a movie the library can see.
//
// It is the one place that knows a torrent on disk and a library folder are the
// same film, and the only package that knows anything about deployment paths:
// internal/qbit translates nothing by contract (docs/decisions.md D13) and
// internal/library is a pure package that must not learn about mounts.
//
// It is driven by the poller's existing torrent list rather than by a query of
// its own, and it triggers on a state — "this torrent reads completed and its
// row does not read imported" — rather than on the transition into completed,
// which is not crash safe. See docs/decisions.md D14.
package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DulsaraNethmin/curator/internal/library"
	"github.com/DulsaraNethmin/curator/internal/qbit"
	"github.com/DulsaraNethmin/curator/internal/store"
)

// DefaultQBitDownloads is the downloads root inside qBittorrent's own
// namespace. Its mount on the Pi is host /media/storage/media/downloads →
// container /downloads, so every path it reports starts here.
const DefaultQBitDownloads = "/downloads"

// destDirMode matches what qBittorrent and the *arr stack already create on the
// Pi: directories 0755, files 0644 (measured 2026-08-12). The linked file needs
// no mode of its own — a hardlink is a second name for one inode and carries the
// source's bits — so this is only about the folder being traversable by
// Jellyfin.
const destDirMode = 0o755

// Store is the persistence an import needs. Declared here rather than taken as
// *store.Store so this package is tested against a fake with no database, the
// pattern internal/api and internal/download already use.
type Store interface {
	GetMovie(ctx context.Context, id int64) (store.Movie, error)
	MarkImported(ctx context.Context, hash, libraryPath string, sizeBytes int64, at time.Time) (store.Movie, error)
}

// LibraryRefresher is the media server, if there is one. A nil refresher is a
// supported state and means no API key was configured — the same posture as a
// nil api.Matcher and a nil download.TorrentClient (docs/decisions.md D15).
type LibraryRefresher interface {
	RefreshLibrary(ctx context.Context) error
}

// Paths is the translation pair between the two namespaces.
//
// Curator empty means "use qBittorrent's path verbatim", which is the laptop
// case and the case where curator shares qBittorrent's mount — neither should
// need any configuration at all.
type Paths struct {
	Curator string // DOWNLOADS_PATH: the downloads root as curator sees it
	QBit    string // QBIT_DOWNLOADS_PATH: the same directory as qBittorrent sees it
}

// Importer hardlinks completed downloads into the library.
type Importer struct {
	store      Store
	moviesRoot string
	paths      Paths
	refresher  LibraryRefresher
	log        *slog.Logger
	now        func() time.Time

	mu sync.Mutex
	// warned suppresses a repeat log for a failure that has not changed. D14
	// retries a failing import on every tick by design, and without this a
	// permanently broken one would say so every ten seconds for ever. The retry
	// is not suppressed — only the noise.
	warned map[string]string
	// dirty records that something was imported since the last refresh, so
	// Refresh can be called once per tick and still cost nothing on the ticks
	// where nothing happened.
	dirty bool
}

// New builds an Importer. refresher may be nil; log may be nil, in which case
// the default logger is used.
func New(st Store, moviesRoot string, paths Paths, refresher LibraryRefresher, log *slog.Logger) *Importer {
	if log == nil {
		log = slog.Default()
	}
	return &Importer{
		store: st, moviesRoot: moviesRoot, paths: paths, refresher: refresher,
		log: log, now: time.Now, warned: map[string]string{},
	}
}

// Import hardlinks one completed download into the library and records it.
//
// The order is load-bearing at two points. The destination folder is not
// created until a source file has been chosen, because a failure before that
// would otherwise leave an empty "Title (Year)/" that the scanner faithfully
// records as a zero-size movie. And the link is made before the database is
// written, because a row claiming a file that is not there is worse than a file
// with no row — the file is found by the next scan, the row never heals itself.
func (im *Importer) Import(ctx context.Context, t qbit.Torrent, d store.Download) (store.Movie, error) {
	fail := func(err error) (store.Movie, error) {
		return store.Movie{}, fmt.Errorf("import %s: %w", d.TorrentHash, err)
	}

	movie, err := im.store.GetMovie(ctx, d.MovieID)
	if err != nil {
		return fail(err)
	}

	// The untrusted step. movies.title came from a client via POST
	// /api/downloads, and this is where it becomes a path.
	folder, err := library.DestFolder(movie.Title, movie.Year)
	if err != nil {
		return fail(err)
	}

	src, err := im.translate(t.ContentPath)
	if err != nil {
		return fail(err)
	}

	feature, err := library.FindFeature(src, library.FeatureOpts{})
	if err != nil {
		// Wrapped, not replaced: the caller tests for library.ErrNoVideo to
		// decide that this is not a failed download.
		return fail(err)
	}

	name, err := library.DestName(movie.Title, movie.Year, filepath.Ext(feature.Path))
	if err != nil {
		return fail(err)
	}

	destDir := filepath.Join(im.moviesRoot, folder)
	// Belt and braces over DestFolder's rejection of separators: whatever the
	// naming rules become, the resolved destination has to be inside the library
	// or nothing is created.
	if err := im.assertInsideLibrary(destDir); err != nil {
		return fail(err)
	}

	if err := os.MkdirAll(destDir, destDirMode); err != nil {
		return fail(err)
	}

	dest := filepath.Join(destDir, name)
	if err := library.Link(feature.Path, dest); err != nil {
		return fail(err)
	}

	if feature.Others > 0 {
		im.log.Warn("content path held more than one video; imported the largest",
			"hash", d.TorrentHash, "chose", feature.Path, "others", feature.Others)
	}

	// library_path is the FOLDER: it is the scanner's identity key, and a row
	// holding the .mkv path is a row no future scan would match.
	saved, err := im.store.MarkImported(ctx, d.TorrentHash, destDir, feature.Size, im.now().UTC())
	if err != nil {
		// The link stays where it is. It is a fact on disk, the next tick will
		// re-link onto it harmlessly (os.SameFile is success), and removing it
		// here would delete the library's only copy if the write actually landed.
		return fail(err)
	}

	im.mu.Lock()
	im.dirty = true
	im.mu.Unlock()

	im.log.Info("imported", "hash", d.TorrentHash, "title", saved.Title, "year", saved.Year,
		"library_path", destDir, "size", feature.Size)
	return saved, nil
}

// TryImport is Import for the poll tick: it returns nothing at all.
//
// That is the point. An import must not be able to fail a tick — the other
// torrents in the same list still need reconciling — and a method with no error
// return puts that in the type rather than in a comment somebody has to obey.
func (im *Importer) TryImport(ctx context.Context, t qbit.Torrent, d store.Download) {
	if _, err := im.Import(ctx, t, d); err != nil {
		im.logFailure(d.TorrentHash, t.Name, err)
		return
	}
	im.mu.Lock()
	delete(im.warned, d.TorrentHash)
	im.mu.Unlock()
}

// Refresh asks the media server to rescan, at most once per call and only when
// something has actually been imported since the last one.
//
// It returns nothing, for the same reason TryImport does: whether a media
// server has noticed yet is a softer fact than whether the file is in the
// library, and letting it fail a tick would trade a real outcome for a cosmetic
// one (docs/decisions.md D15).
//
// A failure clears the flag rather than arming a retry. Best-effort means what
// it says: Jellyfin rescans on its own schedule anyway, so the worst case is
// that the film appears later rather than not at all, and a Jellyfin that is
// down for a day must not produce a warning every ten seconds until it is back.
func (im *Importer) Refresh(ctx context.Context) {
	if im.refresher == nil {
		return
	}

	im.mu.Lock()
	dirty := im.dirty
	im.dirty = false
	im.mu.Unlock()

	if !dirty {
		return
	}
	if err := im.refresher.RefreshLibrary(ctx); err != nil {
		im.log.Warn("jellyfin refresh failed; the import stands and Jellyfin will find the file on its own schedule", "err", err)
	}
}

// logFailure reports an import failure once per distinct message per torrent.
//
// The mutex is not decoration: the poller's Run is one goroutine and the manual
// import endpoint is another.
func (im *Importer) logFailure(hash, name string, err error) {
	message := err.Error()

	im.mu.Lock()
	previous, seen := im.warned[hash]
	im.warned[hash] = message
	im.mu.Unlock()

	if seen && previous == message {
		return
	}
	im.log.Warn("import failed", "hash", hash, "name", name, "err", err)
}

// translate maps a path in qBittorrent's namespace onto curator's.
//
// A set-but-not-matching path is an ERROR rather than a silent pass-through.
// Somebody configured a translation and it did not apply; hardlinking from the
// untranslated path would fail three layers away with a "no such file" that
// says nothing about the actual mistake.
func (im *Importer) translate(contentPath string) (string, error) {
	contentPath = strings.TrimSpace(contentPath)
	if contentPath == "" {
		return "", errors.New("qBittorrent reported no content path for this torrent")
	}
	if strings.TrimSpace(im.paths.Curator) == "" {
		// The laptop case, and the case where curator shares qBittorrent's mount.
		return contentPath, nil
	}

	qbitRoot := strings.TrimSpace(im.paths.QBit)
	if qbitRoot == "" {
		qbitRoot = DefaultQBitDownloads
	}

	// qBittorrent reports POSIX paths whatever curator runs on, so the remote
	// side is split with path and the local side joined with filepath.
	rel, ok := relativeTo(qbitRoot, contentPath)
	if !ok {
		return "", fmt.Errorf(
			"content path %q is not under QBIT_DOWNLOADS_PATH %q, so DOWNLOADS_PATH %q cannot be applied to it",
			contentPath, qbitRoot, im.paths.Curator)
	}
	return filepath.Join(im.paths.Curator, filepath.FromSlash(rel)), nil
}

// relativeTo reports whether p is root or below it, POSIX-style, and returns the
// remainder. The boundary check is what stops "/downloads2/x" from matching a
// root of "/downloads".
func relativeTo(root, p string) (string, bool) {
	root, p = path.Clean(root), path.Clean(p)
	if p == root {
		return "", true
	}
	if !strings.HasSuffix(root, "/") {
		root += "/"
	}
	if !strings.HasPrefix(p, root) {
		return "", false
	}
	return strings.TrimPrefix(p, root), true
}

// assertInsideLibrary refuses a destination that has escaped LIBRARY_MOVIES.
func (im *Importer) assertInsideLibrary(dest string) error {
	root, err := filepath.Abs(im.moviesRoot)
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, absolute)
	if err != nil {
		return fmt.Errorf("destination %s is not under the library root %s", dest, im.moviesRoot)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("destination %s escapes the library root %s", dest, im.moviesRoot)
	}
	return nil
}
