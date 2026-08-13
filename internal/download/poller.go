package download

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// Poller reconciles the torrent client into the downloads table on an interval.
// Which client is a wiring decision it never sees.
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

	// importer is phase 4's, and is nil until it is attached. Nil leaves this
	// poller behaving exactly as phase 3 shipped it, which is why every phase 3
	// test still passes without being touched.
	importer Importer
}

// Importer is phase 4's hardlink-into-the-library step, as a poll tick uses it.
//
// Neither method returns an error, and that is the point rather than an
// oversight: an import must not be able to fail a tick, because the other
// torrents in the same list still need reconciling, and a Jellyfin refresh must
// not be able to fail an import (decisions.md D15). Putting it in the type
// means there is nothing here for a future caller to mishandle.
type Importer interface {
	TryImport(ctx context.Context, t torrent.Torrent, d store.Download)
	Refresh(ctx context.Context)
}

// WithImporter attaches the importer and returns the poller.
//
// It is a builder rather than a parameter to NewPoller so that phase 3's
// constructor — and every test that calls it — keeps its shape. A hook that
// forced an existing test to change would be a hook in the wrong place.
func (p *Poller) WithImporter(im Importer) *Poller {
	p.importer = im
	return p
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
		// Already in the table's case: the backend normalises on the way out,
		// which is where a wire format's quirks belong.
		hash := t.Hash

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

		state := t.State

		// completed_at is stamped once, on the transition into completed, and never
		// cleared: the store preserves it when passed nil, so every later tick
		// leaves the moment the download finished exactly where it was.
		var completedAt *time.Time
		if state == store.DownloadCompleted && row.CompletedAt == nil {
			at := p.now().UTC()
			completedAt = &at
		}

		// Nothing moved: skip the write, but NOT the rest of the iteration.
		//
		// This was a `continue`, and turning it into a condition around the write
		// is what makes phase 4's retry possible. On every tick after the one
		// that saw a torrent finish, the state and the progress are unchanged, so
		// a `continue` here skipped the import too — and an import that failed
		// once would never be attempted again.
		if state != row.State || t.Progress != row.Progress || completedAt != nil {
			if err := p.store.UpdateDownloadProgress(ctx, hash, state, t.Progress, completedAt); err != nil {
				return err
			}
			if completedAt != nil {
				p.log.Info("download completed", "hash", hash, "name", t.Name, "content_path", t.ContentPath)
			}
		}

		// The trigger is a STATE, not the transition into it (decisions.md D14).
		// Rows already reading `imported` were skipped further up, so anything
		// completed at this point has not been imported yet — including one whose
		// import failed on an earlier tick. That is the whole design: the
		// recovery path and the normal path are the same code, so the recovery
		// path is the one that actually gets exercised.
		if p.importer != nil && state == store.DownloadCompleted {
			p.importer.TryImport(ctx, t, row)
		}
	}

	// Once per tick, not once per import: POST /Library/Refresh is a
	// whole-library scan, and a batch of six imports asking for six scans of the
	// same library would be worse than asking for none. The importer no-ops when
	// nothing was imported.
	if p.importer != nil {
		p.importer.Refresh(ctx)
	}
	return nil
}
