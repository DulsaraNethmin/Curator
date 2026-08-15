// Package download turns a picked release into a running torrent and keeps the
// database honest about what that torrent is doing.
//
// The torrent client, the store and the release resolver are reached through
// interfaces declared here rather than their concrete types, so dispatch is
// exercised against fakes instead of a live qBittorrent and a real database —
// the same pattern as internal/api's Store and Searcher.
package download

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/DulsaraNethmin/curator/internal/indexer"
	"github.com/DulsaraNethmin/curator/internal/library"
	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// TorrentClient is the part of a torrent client this package uses.
//
// It says nothing about which one. internal/qbit asks a qBittorrent over HTTP
// and the embedded engine downloads it in this process; both answer in
// torrent.Torrent, and every test in this package runs on a fake, so a new
// backend is validated by tests that already exist (docs/decisions.md D22).
//
// It is add-and-read plus one delete. There is no pause or resume because there
// is none in either client either: the *arr stack shares that qBittorrent until
// the cutover, and the narrowest possible interface is the cheapest guarantee
// that nothing here can disturb it.
type TorrentClient interface {
	AddMagnet(ctx context.Context, magnet, category string) error
	TorrentByHash(ctx context.Context, hash string) (*torrent.Torrent, error)
	Torrents(ctx context.Context, category string) ([]torrent.Torrent, error)

	// DeleteTorrent is the one destructive call, added for D19. It takes the
	// category it requires and refuses a torrent that is not in it, so curator
	// cannot remove one of the *arr stack's torrents even by mistake.
	DeleteTorrent(ctx context.Context, hash, requireCategory string, deleteFiles bool) error
}

// Store is the persistence dispatch, polling and deletion need.
type Store interface {
	UpsertWantedMovie(ctx context.Context, title string, year int, tmdbID *int64) (store.Movie, error)
	InsertDownload(ctx context.Context, d store.Download) (store.Download, error)
	UpdateDownloadProgress(ctx context.Context, hash, state string, progress float64, reason string, completedAt *time.Time) error
	GetDownloadByHash(ctx context.Context, hash string) (store.Download, error)
	ListDownloads(ctx context.Context) ([]store.Download, error)

	// The delete path (D19). The reads come first because the rows are removed
	// last, and after the DELETE there is nothing left to look a torrent up by.
	GetMovie(ctx context.Context, id int64) (store.Movie, error)
	DownloadsForMovie(ctx context.Context, movieID int64) ([]store.Download, error)
	DeleteMovie(ctx context.Context, id int64) (store.Deleted, error)
}

// Resolver hands out the magnet and the metadata behind a release id. Phase 2's
// aggregator satisfies it.
type Resolver interface {
	ResolveMagnet(ctx context.Context, id string) (string, error)
	Release(id string) (indexer.Found, bool)
}

// ErrUnconfigured reports that downloads have no qBittorrent credentials, so
// dispatch cannot run. It is a configuration state, not a failure of the request,
// and the API layer answers it with a 503 naming the variable rather than a 500.
var ErrUnconfigured = errors.New("downloads are not configured: set QBIT_USER and QBIT_PASS")

// ErrClient reports that the torrent client could not be reached, refused us, or
// took a magnet without producing a torrent.
//
// It exists so the API layer can answer 502 — the request was fine and a
// dependency was not — and keep 500 for curator's own failures, such as the
// database. Without the distinction every downstream problem would be reported as
// ours, which is the same dishonesty as a 200 carrying a failure, pointed the
// other way.
//
// It stopped being called "qbittorrent" in phase 6: which client this is became
// configuration rather than a constant, and an error naming the wrong one is
// worse than an error naming none.
var ErrClient = errors.New("the torrent client")

// ErrUnprotected reports that a dispatch was refused because the download would
// not have gone through a VPN.
//
// It is separated so the API can answer 503 and name the cause: the request is
// well-formed, curator is working, and the refusal is deliberate. A mandatory
// VPN that fails open is not one (docs/decisions.md D27).
var ErrUnprotected = errors.New("refusing to dispatch: the download would not be protected")

// ErrNotCompleted reports that an import was asked for a torrent that is still
// downloading. It is separated so the API can answer 409 — the request is
// well-formed and will be fine later — rather than 500 or a silent no-op.
var ErrNotCompleted = errors.New("the torrent has not finished downloading")

// ManualImporter is phase 4's hardlink step as a synchronous caller uses it.
//
// It is a different, narrower interface from the Poller's Importer on purpose.
// A tick must not be able to fail, so its methods return nothing; somebody who
// asked for an import over HTTP and is waiting for the answer deserves the
// reason it did not work. The same *importer.Importer satisfies both.
type ManualImporter interface {
	Import(ctx context.Context, t torrent.Torrent, d store.Download) (store.Movie, error)

	// RemoveFromLibrary deletes a movie's folder. It is the importer's because
	// the importer created it and holds LIBRARY_MOVIES, which is what makes its
	// containment check meaningful (docs/decisions.md D19).
	RemoveFromLibrary(libraryPath string) error
}

// Request is one dispatch: the release to grab, and the film it is for.
//
// Title and Year come from the caller because a release id says nothing about
// which film it belongs to — the client picked it out of a search it made, and it
// is the only party that knows. The release's own name and indexer are not taken
// from the caller: those the server already holds.
type Request struct {
	ReleaseID string
	Title     string
	Year      int
	TMDBID    *int64
}

// Service dispatches releases into qBittorrent and records them.
type Service struct {
	client   TorrentClient
	store    Store
	resolver Resolver
	category string
	log      *slog.Logger
	now      func() time.Time

	// importer is phase 4's, nil until attached — the same nilable-field shape
	// as the poller's, and for the same reason: phase 3's constructor and its
	// tests keep their shape.
	importer ManualImporter

	// guard is phase 6's, and nil means there is nothing to check. See
	// WithGuard.
	guard Guard
}

// Guard is a check that runs before a magnet is handed to the torrent client,
// and refuses the dispatch when it returns an error.
//
// It is a func rather than an interface because there is exactly one question —
// "is this download going to be protected?" — and the two answers to it look
// nothing alike. With the embedded engine the tunnel either carries the traffic
// or the dial fails, so the check is about configuration; with an external
// qBittorrent curator cannot route the traffic at all and the check compares
// exit addresses. internal/vpn owns both, because it owns what can be promised.
type Guard func(ctx context.Context) error

// WithGuard attaches the dispatch check and returns the service. A builder for
// the same reason WithImporter is one: every existing constructor call keeps
// its shape.
func (s *Service) WithGuard(g Guard) *Service {
	s.guard = g
	return s
}

// WithImporter attaches the importer and returns the service.
func (s *Service) WithImporter(im ManualImporter) *Service {
	s.importer = im
	return s
}

// NewService builds a Service. A nil client means downloads are unconfigured;
// dispatch then fails with ErrUnconfigured rather than panicking, which is what
// keeps an unconfigured qBittorrent from taking the rest of the service down.
func NewService(client TorrentClient, st Store, resolver Resolver, category string, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		client: client, store: st, resolver: resolver,
		category: category, log: log, now: time.Now,
	}
}

// Dispatch resolves a picked release, adds it to qBittorrent, confirms it landed,
// and only then records it.
//
// The order is the whole point, and it is not the obvious one. Writing the row
// first and adding second would leave the database claiming a download no torrent
// client has ever heard of, every single time qBittorrent is down — and the
// poller would then report that row as missing for ever. Nothing is written until
// qBittorrent has been asked and answered.
func (s *Service) Dispatch(ctx context.Context, req Request) (store.Download, error) {
	if s.client == nil {
		return store.Download{}, ErrUnconfigured
	}
	if strings.TrimSpace(req.Title) == "" {
		return store.Download{}, fmt.Errorf("dispatch %s: title is required", req.ReleaseID)
	}

	// Before anything is resolved, added or written. A refusal here must cost
	// nothing and leave nothing behind — the same discipline as the release
	// lookup below, which fails an expired search before qBittorrent or the
	// database has been touched.
	if s.guard != nil {
		if err := s.guard(ctx); err != nil {
			// Wrapped in ErrUnprotected here rather than by the guard itself,
			// so that internal/vpn owns the reason and this package owns the
			// class. It also means a guard that cannot ANSWER is refused too:
			// "I could not establish that this is protected" and "this is not
			// protected" have the same consequence for a mandatory tunnel.
			return store.Download{}, fmt.Errorf("dispatch %s: %w: %w", req.ReleaseID, ErrUnprotected, err)
		}
	}

	// The release is read before anything else: an expired search fails here,
	// having touched neither qBittorrent nor the database.
	release, ok := s.resolver.Release(req.ReleaseID)
	if !ok {
		return store.Download{}, fmt.Errorf("dispatch %s: %w", req.ReleaseID, indexer.ErrReleaseExpired)
	}

	magnet, err := s.resolver.ResolveMagnet(ctx, req.ReleaseID)
	if err != nil {
		return store.Download{}, fmt.Errorf("dispatch %s: %w", req.ReleaseID, err)
	}

	// A magnet with no info hash cannot be identified afterwards, so it is caught
	// before qBittorrent is asked to swallow it.
	hash := indexer.InfoHash(magnet)
	if hash == "" {
		return store.Download{}, fmt.Errorf("dispatch %s: magnet carries no info hash", req.ReleaseID)
	}

	// Already downloading: return what we have. Both the add and the insert below
	// are idempotent, but answering from the database first means a re-dispatch
	// costs no round trip at all.
	if existing, err := s.store.GetDownloadByHash(ctx, hash); err == nil {
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Download{}, fmt.Errorf("dispatch %s: %w", req.ReleaseID, err)
	}

	// The add is ADVISORY; the lookup below is what is authoritative.
	//
	// torrents/add answers "200 Ok." for a magnet it ignored just as readily as
	// for one it accepted, so a success proves nothing. Measured against the
	// real qBittorrent 5.1.2, the converse is true as well: it answers "Fails."
	// for a magnet it ALREADY HOLDS. Phase 3 assumed a duplicate add was
	// silently idempotent — it is not, and that assumption survived only because
	// it was never run against a real client.
	//
	// So a rejected add is carried rather than returned. Re-dispatching a
	// release after a database reset, a restart, or a second click has to
	// converge on the existing torrent, which is exactly what phase 3 promised
	// and what this makes true.
	addErr := s.client.AddMagnet(ctx, magnet, s.category)

	// Named t, not `torrent`: that is the package holding the type now.
	t, err := s.client.TorrentByHash(ctx, hash)
	if err != nil {
		return store.Download{}, fmt.Errorf("dispatch %s: confirming the add: %w: %w", req.ReleaseID, ErrClient, err)
	}
	if t == nil {
		// Nothing there, so the add really did fail — now the error matters, and
		// it is the more specific of the two.
		if addErr != nil {
			return store.Download{}, fmt.Errorf("dispatch %s: %w: %w", req.ReleaseID, ErrClient, addErr)
		}
		return store.Download{}, fmt.Errorf(
			"dispatch %s: %w accepted the magnet but has no torrent %s — it was ignored",
			req.ReleaseID, ErrClient, hash)
	}
	if addErr != nil {
		// The torrent is there and the add was refused: qBittorrent already had
		// it. Worth a line, because it is also what a duplicate click looks like.
		s.log.Info("qBittorrent already had this torrent; the add was refused and the existing one is used",
			"hash", hash, "err", addErr)
	}

	movie, err := s.store.UpsertWantedMovie(ctx, req.Title, req.Year, req.TMDBID)
	if err != nil {
		return store.Download{}, fmt.Errorf("dispatch %s: %w", req.ReleaseID, err)
	}

	saved, err := s.store.InsertDownload(ctx, store.Download{
		MovieID:     movie.ID,
		TorrentHash: hash,
		Indexer:     strings.Join(release.Indexers, ","),
		ReleaseName: release.Title,
		Magnet:      magnet,
		State:       t.State,
		Progress:    t.Progress,
	})
	if err != nil {
		return store.Download{}, fmt.Errorf("dispatch %s: %w", req.ReleaseID, err)
	}

	s.log.Info("dispatched", "release", release.Title, "hash", hash,
		"indexer", saved.Indexer, "movie_id", movie.ID, "category", s.category)
	return saved, nil
}

// Import hardlinks one already-completed download into the library now, without
// waiting for the next poll tick.
//
// It exists for the case a tick cannot serve: an import that failed for a reason
// since fixed — a library root that was not mounted, a disk that was full —
// where waiting up to DOWNLOAD_POLL_INTERVAL to learn whether the fix worked is
// worse than asking. It runs the identical code path; there is no second
// importer and no second set of rules.
//
// The torrent is fetched fresh rather than taken from the caller, because
// content_path is qBittorrent's to report and a client has no business naming a
// path curator will hardlink from.
func (s *Service) Import(ctx context.Context, hash string) (store.Movie, error) {
	if s.client == nil || s.importer == nil {
		return store.Movie{}, ErrUnconfigured
	}

	row, err := s.store.GetDownloadByHash(ctx, hash)
	if err != nil {
		return store.Movie{}, fmt.Errorf("import %s: %w", hash, err)
	}

	t, err := s.client.TorrentByHash(ctx, row.TorrentHash)
	if err != nil {
		return store.Movie{}, fmt.Errorf("import %s: %w: %w", hash, ErrClient, err)
	}
	if t == nil {
		// The row is ours and the torrent is not there: somebody removed it from
		// qBittorrent. That is qBittorrent's business (D8), and there is nothing
		// here to import from.
		return store.Movie{}, fmt.Errorf(
			"import %s: %w has no torrent with that hash, so there is nothing to import from", hash, ErrClient)
	}

	// The backend's own word for this state does not cross the interface any
	// more, so the message reports curator's. One diagnostic string is what the
	// neutral type cost, and a seventh field to carry it back would be paying
	// for it in the wrong currency.
	if t.State != store.DownloadCompleted {
		return store.Movie{}, fmt.Errorf(
			"import %s: %w — the torrent client reports it as %q", hash, ErrNotCompleted, t.State)
	}

	return s.importer.Import(ctx, *t, row)
}

// Deletion is what DeleteMovie removed, so the caller can say so precisely.
type Deletion struct {
	Title           string `json:"title"`
	Year            int    `json:"year"`
	LibraryPath     string `json:"library_path,omitempty"`
	TorrentsRemoved int    `json:"torrents_removed"`
	BytesFreed      int64  `json:"bytes_freed"`

	// FolderLeft names a folder that was NOT removed because it is outside
	// LIBRARY_MOVIES and therefore not ours to delete. Absent in the ordinary
	// case, like jellyfin_url and remux_url — and present rather than silent
	// because the screen says "the library folder was removed", which would be a
	// lie for exactly this delete.
	FolderLeft string `json:"folder_left,omitempty"`
}

// DeleteMovie removes a film from the library and from the disk.
//
// The order is D19's and it is not arbitrary: the torrent and its downloaded
// files, then our hardlink, then the rows. Every step is idempotent, so a
// failure part-way through leaves something a retry can finish — and the rows
// survive longest on purpose, because a row pointing at files that are already
// gone is repairable by hand, while files with no row are silently re-adopted
// by the next scan.
//
// qBittorrent deletes the download, not curator. It created that file and owns
// it, and asking it avoids the state a direct unlink produces: a torrent whose
// files have vanished underneath it, which qBittorrent reports as missingFiles
// and the poller then records as `failed` for a download nobody failed to get.
//
// One error in step 2 is not a failure: library.ErrOutsideRoot means the folder
// is not ours to delete, so there is nothing of ours in there and the rows still
// go. The report names the folder that was left, because the screen otherwise
// says it was removed.
func (s *Service) DeleteMovie(ctx context.Context, id int64) (Deletion, error) {
	if s.importer == nil {
		return Deletion{}, ErrUnconfigured
	}

	movie, err := s.store.GetMovie(ctx, id)
	if err != nil {
		return Deletion{}, fmt.Errorf("delete movie %d: %w", id, err)
	}
	downloads, err := s.store.DownloadsForMovie(ctx, id)
	if err != nil {
		return Deletion{}, fmt.Errorf("delete movie %d: %w", id, err)
	}

	report := Deletion{Title: movie.Title, Year: movie.Year}
	if movie.LibraryPath != nil {
		report.LibraryPath = *movie.LibraryPath
	}
	if movie.SizeBytes != nil {
		report.BytesFreed = *movie.SizeBytes
	}

	// 1. The torrents and their files. A nil client means downloads were never
	//    configured, so there is nothing in qBittorrent to remove — the rows are
	//    still cleaned up below.
	for _, download := range downloads {
		if s.client == nil {
			break
		}
		if err := s.client.DeleteTorrent(ctx, download.TorrentHash, s.category, true); err != nil {
			return Deletion{}, fmt.Errorf(
				"delete movie %d: removing torrent %s: %w: %w", id, download.TorrentHash, ErrClient, err)
		}
		report.TorrentsRemoved++
	}

	// 2. Our own hardlink and the folder holding it.
	if err := s.importer.RemoveFromLibrary(report.LibraryPath); err != nil {
		if !errors.Is(err, library.ErrOutsideRoot) {
			return Deletion{}, fmt.Errorf("delete movie %d: %w", id, err)
		}
		// A refusal, not a failure: there is nothing of ours in there to remove,
		// so this is the same answer "already gone" gets. Refusing here would
		// make the row PERMANENTLY undeletable — nothing can serve a library_path
		// outside the root either (stream.go refuses it with the same check) — and
		// that row is exactly the one that most needs deleting. The containment
		// check itself is untouched; only what its caller concludes changed.
		//
		// Warn with both paths because this is the operator's problem — a
		// repointed LIBRARY_MOVIES, a database restored beside a different
		// library — and the log is where it gets diagnosed.
		report.FolderLeft = report.LibraryPath
		s.log.Warn("delete: the library folder is outside LIBRARY_MOVIES, so it is not ours to remove; leaving it on disk and deleting the rows",
			"movie_id", id, "library_path", report.LibraryPath, "err", err)
	}

	// 3. The rows, last.
	if _, err := s.store.DeleteMovie(ctx, id); err != nil {
		return Deletion{}, fmt.Errorf("delete movie %d: %w", id, err)
	}

	s.log.Info("deleted", "movie_id", id, "title", movie.Title, "year", movie.Year,
		"torrents_removed", report.TorrentsRemoved, "bytes_freed", report.BytesFreed)
	return report, nil
}

// Resume re-adds every download that has not been imported, once, at boot.
//
// It needs no new column, no new query and no migration: `downloads` has
// carried the hash **and** the magnet since phase 3, which is everything a
// re-add needs. It works on either backend for the same reason dispatch does —
// AddMagnet is the interface, and what happens behind it is the backend's
// business. On the embedded engine that means reading the info dict off disk
// and carrying on with the network down; on qBittorrent it means nothing at
// all, because that process kept running.
//
// Rows that read `imported` are deliberately left alone, and the consequence is
// stated rather than hidden: **curator stops seeding a film once it has filed
// it and been restarted.** Re-adopting every film ever imported is the one
// behaviour here with an unbounded cost — peers, sockets and resident memory on
// a Pi — and a seeding policy is a feature nobody has asked for yet.
//
// One failure does not stop the rest. A magnet that will not add is one film,
// and the loop is the only chance every other row gets.
func (s *Service) Resume(ctx context.Context) error {
	if s.client == nil {
		return nil
	}

	rows, err := s.store.ListDownloads(ctx)
	if err != nil {
		return fmt.Errorf("resume: %w", err)
	}

	var resumed, held, failed int
	for _, row := range rows {
		if row.State == store.DownloadImported {
			continue
		}
		if strings.TrimSpace(row.Magnet) == "" {
			// Nothing to re-add from. Worth saying once: this row will never
			// move again on its own, and nobody would otherwise know why.
			s.log.Warn("download row has no magnet, so it cannot be resumed",
				"hash", row.TorrentHash, "release", row.ReleaseName)
			failed++
			continue
		}

		// Asked first, so that a client which still holds the torrent is not
		// sent a duplicate add. qBittorrent answers "Fails." to a magnet it
		// already has, which would make every boot log an error for a torrent
		// that is perfectly healthy.
		if existing, err := s.client.TorrentByHash(ctx, row.TorrentHash); err == nil && existing != nil {
			held++
			continue
		}

		if err := s.client.AddMagnet(ctx, row.Magnet, s.category); err != nil {
			s.log.Warn("could not resume a download", "hash", row.TorrentHash,
				"release", row.ReleaseName, "err", err)
			failed++
			continue
		}
		resumed++
	}

	if resumed+held+failed > 0 {
		s.log.Info("resumed downloads", "re_added", resumed, "already_running", held, "failed", failed)
	}
	return nil
}

// Downloads lists every recorded download, newest first.
//
// It reads through to the store rather than qBittorrent: the table is the record
// of what curator dispatched, and it stays answerable when qBittorrent is down —
// which is exactly when someone is most likely to be looking.
func (s *Service) Downloads(ctx context.Context) ([]store.Download, error) {
	return s.store.ListDownloads(ctx)
}
