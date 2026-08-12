package download

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/DulsaraNethmin/curator/internal/qbit"
	"github.com/DulsaraNethmin/curator/internal/store"
)

// Poller reconciles qBittorrent into the downloads table on an interval.
//
// It reads and writes rows that already exist and nothing else. It never adds a
// torrent, never removes one, and never invents a database row — seeding and
// cleanup stay qBittorrent's business under its own rules, exactly as D8 says for
// the files themselves.
type Poller struct {
	client   TorrentClient
	store    Store
	category string
	interval time.Duration
	log      *slog.Logger
	now      func() time.Time
}

// NewPoller builds a Poller. The interval is a constructor argument rather than a
// package constant because it is a deployment setting, not a property of polling.
func NewPoller(client TorrentClient, st Store, category string, interval time.Duration, log *slog.Logger) *Poller {
	if log == nil {
		log = slog.Default()
	}
	return &Poller{
		client: client, store: st, category: category,
		interval: interval, log: log, now: time.Now,
	}
}

// Run polls until ctx is cancelled, then returns.
//
// It is owned by whoever calls it and dies with them: the caller passes the same
// context that shuts the server down, so there is no Stop to forget and no
// goroutine left running after main returns. A janitor that outlives its owner is
// the leak T10 refused to create for the search cache, and it would be the same
// leak here.
//
// The ticker does not overlap work: one tick is awaited before the next is
// considered, so a slow qBittorrent slows polling down rather than stacking
// requests on top of it.
func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	p.log.Info("download poller started", "interval", p.interval, "category", p.category)
	for {
		select {
		case <-ctx.Done():
			p.log.Info("download poller stopped")
			return
		case <-ticker.C:
			if err := p.Tick(ctx); err != nil {
				// A failed tick is logged and the next one still runs. qBittorrent
				// restarting, or a VPN reconnecting under it, must not end polling
				// for the lifetime of the process — that is the entire reason this
				// is a loop rather than a one-shot.
				p.log.Warn("download poll failed", "err", err)
			}
		}
	}
}

// Tick reconciles once. It is exported so a test can run exactly one pass without
// waiting for a ticker, and so the API could force a refresh later.
func (p *Poller) Tick(ctx context.Context) error {
	// One request covers every download we own, however many are in flight.
	torrents, err := p.client.Torrents(ctx, p.category)
	if err != nil {
		return err
	}

	for _, t := range torrents {
		hash := qbit.NormalizeHash(t.Hash)

		row, err := p.store.GetDownloadByHash(ctx, hash)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// A torrent in our category with no row is reported, never adopted.
				// It is either a leftover from a wiped database or somebody else
				// using the category, and inventing a movies row to hang it off
				// would fabricate a film nobody asked for.
				p.log.Warn("torrent in our category has no download row",
					"hash", hash, "name", t.Name, "category", t.Category)
				continue
			}
			return err
		}

		// imported is phase 4's to set, and a poller that overwrote it would undo
		// an import every ten seconds for as long as the torrent kept seeding.
		if row.State == store.DownloadImported {
			continue
		}

		state := qbit.MapState(t.State)

		// completed_at is stamped once, on the transition into completed, and never
		// cleared: the store preserves it when passed nil, so every later tick
		// leaves the moment the download finished exactly where it was.
		var completedAt *time.Time
		if state == store.DownloadCompleted && row.CompletedAt == nil {
			at := p.now().UTC()
			completedAt = &at
		}

		if state == row.State && t.Progress == row.Progress && completedAt == nil {
			continue // nothing moved; leave the row alone
		}

		if err := p.store.UpdateDownloadProgress(ctx, hash, state, t.Progress, completedAt); err != nil {
			return err
		}
		if completedAt != nil {
			p.log.Info("download completed", "hash", hash, "name", t.Name, "content_path", t.ContentPath)
		}
	}
	return nil
}
