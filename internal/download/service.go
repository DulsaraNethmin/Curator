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
	"github.com/DulsaraNethmin/curator/internal/qbit"
	"github.com/DulsaraNethmin/curator/internal/store"
)

// TorrentClient is the part of qBittorrent this package uses.
//
// It is add-and-read on purpose. There is no delete, pause or resume here
// because there is none in the client either: the *arr stack shares that
// qBittorrent until phase 6, and the narrowest possible interface is the cheapest
// guarantee that nothing here can disturb it.
type TorrentClient interface {
	AddMagnet(ctx context.Context, magnet, category string) error
	TorrentByHash(ctx context.Context, hash string) (*qbit.Torrent, error)
	Torrents(ctx context.Context, category string) ([]qbit.Torrent, error)
}

// Store is the persistence dispatch and polling need.
type Store interface {
	UpsertWantedMovie(ctx context.Context, title string, year int, tmdbID *int64) (store.Movie, error)
	InsertDownload(ctx context.Context, d store.Download) (store.Download, error)
	UpdateDownloadProgress(ctx context.Context, hash, state string, progress float64, completedAt *time.Time) error
	GetDownloadByHash(ctx context.Context, hash string) (store.Download, error)
	ListDownloads(ctx context.Context) ([]store.Download, error)
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

// ErrClient reports that qBittorrent could not be reached, refused us, or took a
// magnet without producing a torrent.
//
// It exists so the API layer can answer 502 — the request was fine and a
// dependency was not — and keep 500 for curator's own failures, such as the
// database. Without the distinction every downstream problem would be reported as
// ours, which is the same dishonesty as a 200 carrying a failure, pointed the
// other way.
var ErrClient = errors.New("qbittorrent")

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
	Import(ctx context.Context, t qbit.Torrent, d store.Download) (store.Movie, error)
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

	if err := s.client.AddMagnet(ctx, magnet, s.category); err != nil {
		return store.Download{}, fmt.Errorf("dispatch %s: %w: %w", req.ReleaseID, ErrClient, err)
	}

	// torrents/add answers 200 Ok. for a magnet it ignored just as readily as for
	// one it accepted, and returns no hash, so the add is only real once the
	// torrent can be found. This is the step that earns the right to write a row.
	torrent, err := s.client.TorrentByHash(ctx, hash)
	if err != nil {
		return store.Download{}, fmt.Errorf("dispatch %s: confirming the add: %w: %w", req.ReleaseID, ErrClient, err)
	}
	if torrent == nil {
		return store.Download{}, fmt.Errorf(
			"dispatch %s: %w accepted the magnet but has no torrent %s — it was ignored",
			req.ReleaseID, ErrClient, hash)
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
		State:       qbit.MapState(torrent.State),
		Progress:    torrent.Progress,
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

	torrent, err := s.client.TorrentByHash(ctx, row.TorrentHash)
	if err != nil {
		return store.Movie{}, fmt.Errorf("import %s: %w: %w", hash, ErrClient, err)
	}
	if torrent == nil {
		// The row is ours and the torrent is not there: somebody removed it from
		// qBittorrent. That is qBittorrent's business (D8), and there is nothing
		// here to import from.
		return store.Movie{}, fmt.Errorf(
			"import %s: %w has no torrent with that hash, so there is nothing to import from", hash, ErrClient)
	}

	if state := qbit.MapState(torrent.State); state != store.DownloadCompleted {
		return store.Movie{}, fmt.Errorf(
			"import %s: %w — qBittorrent reports %q, which is %q", hash, ErrNotCompleted, torrent.State, state)
	}

	return s.importer.Import(ctx, *torrent, row)
}

// Downloads lists every recorded download, newest first.
//
// It reads through to the store rather than qBittorrent: the table is the record
// of what curator dispatched, and it stays answerable when qBittorrent is down —
// which is exactly when someone is most likely to be looking.
func (s *Service) Downloads(ctx context.Context) ([]store.Download, error) {
	return s.store.ListDownloads(ctx)
}
