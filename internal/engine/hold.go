package engine

import (
	"fmt"

	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// Hold stops every torrent moving data, and drops the peers it has.
//
// Not Drop: the rows stay, the files stay, the metainfo stays, and Release
// resumes exactly where this left off. A tunnel blip must not cost a
// half-finished download — that would make the kill switch more expensive than
// the thing it protects against, which is how a safety feature gets turned off.
//
// Three calls per torrent, because two of them are not enough.
// DisallowDataDownload and DisallowDataUpload stop pieces moving but leave the
// connections open and the torrent announcing; SetMaxEstablishedConns(0) drops
// the peers. Together they mean a held torrent is doing nothing at all rather
// than quietly maintaining a swarm presence from an address that may no longer
// be the tunnel's.
//
// It is belt-and-braces over the structural guarantee, deliberately. With the
// tunnel truly down every dial already fails and this changes nothing. It exists
// for the state the structure does not cover: a tunnel that is up and
// handshaking and no longer changing where traffic leaves from, where the dials
// succeed and the bytes are the problem.
//
// Idempotent: a sentinel that sees blocked, then stale, then unknown calls this
// three times for one failure, and the second and third must be free.
func (e *Engine) Hold(reason string) error {
	if reason == "" {
		return fmt.Errorf("engine: a hold needs a reason; it is what the Activity screen shows instead of blaming the swarm")
	}

	e.mu.Lock()
	already := e.heldReason != ""
	e.heldReason = reason
	e.mu.Unlock()

	if already {
		return nil
	}

	for _, t := range e.client.Torrents() {
		t.DisallowDataDownload()
		t.DisallowDataUpload()
		// Remembered per torrent rather than assumed to be cfg.MaxConns: a
		// future per-torrent limit would otherwise be silently flattened to the
		// global one by the release.
		old := t.SetMaxEstablishedConns(0)
		e.mu.Lock()
		e.heldConns[hashOf(t)] = old
		e.mu.Unlock()
	}

	e.log.Warn("downloads held: nothing will move until this clears", "reason", reason)
	return nil
}

// Release undoes Hold, restoring each torrent's own connection limit.
//
// Idempotent in the same way and for the same reason. Releasing something that
// was never held is not an error — on a curator that has been running happily
// the sentinel's first good verdict arrives with nothing to release.
func (e *Engine) Release() error {
	e.mu.Lock()
	held := e.heldReason != ""
	e.heldReason = ""
	conns := e.heldConns
	e.heldConns = map[string]int{}
	e.mu.Unlock()

	if !held {
		return nil
	}

	for _, t := range e.client.Torrents() {
		t.AllowDataDownload()
		t.AllowDataUpload()
		if old, ok := conns[hashOf(t)]; ok && old > 0 {
			t.SetMaxEstablishedConns(old)
		}
	}

	e.log.Info("downloads released: the tunnel is protecting them again")
	return nil
}

// Held reports whether downloads are held, and why.
//
// The reason is what the Activity screen renders under a stalled row. Without
// it the screen says "nobody appears to be seeding this release" about a
// download that is not being seeded to because curator switched it off, which
// is the failure T78 existed to remove, arriving from a new direction.
func (e *Engine) Held() (bool, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.heldReason != "", e.heldReason
}

// holdReason returns the sentence a held torrent reports instead of a stall
// diagnosis, or "" when nothing is held.
func (e *Engine) holdReason() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.heldReason == "" {
		return ""
	}
	return "curator has stopped this download because it would not be protected: " + e.heldReason
}

// heldState is what describe reports for a torrent that is not allowed to move.
//
// Stalled rather than a state of its own, and that is a deliberate reuse: the
// row IS stalled — nothing is arriving — and torrent.Torrent's Reason field
// accompanies StateStalled and nothing else. A new state would need a case in
// the poller, the store, the API and the UI's badge switch, all to say something
// the reason already says better.
func heldState(out torrent.Torrent, reason string) torrent.Torrent {
	if out.State == torrent.StateCompleted {
		// A finished download is not held: there is nothing left to stop, and
		// rewriting it to stalled would keep the importer away from a payload
		// that is on disk and ready — the poller imports on `completed`.
		return out
	}
	out.State = torrent.StateStalled
	out.Reason = reason
	return out
}
