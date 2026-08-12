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
		return store.Download{}, fmt.Errorf("dispatch %s: %w", req.ReleaseID, err)
	}

	// torrents/add answers 200 Ok. for a magnet it ignored just as readily as for
	// one it accepted, and returns no hash, so the add is only real once the
	// torrent can be found. This is the step that earns the right to write a row.
	torrent, err := s.client.TorrentByHash(ctx, hash)
	if err != nil {
		return store.Download{}, fmt.Errorf("dispatch %s: confirming the add: %w", req.ReleaseID, err)
	}
	if torrent == nil {
		return store.Download{}, fmt.Errorf(
			"dispatch %s: qBittorrent accepted the magnet but has no torrent %s — it was ignored", req.ReleaseID, hash)
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
