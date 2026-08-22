package engine

import (
	"context"
	"fmt"
	"strings"

	anacrolix "github.com/anacrolix/torrent"

	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// PauseTorrent stops one torrent moving data, because somebody asked.
//
// **It is Hold's mechanism applied to one hash, and its opposite in every other
// respect.** A hold is curator protecting itself: process-wide, unattended, and
// reported as a stall with a sentence, because there is no per-row control to
// draw and the reason is the only actionable thing on screen. A pause is a
// person's decision about one download, it has a Resume button, and it is
// therefore a state (docs/decisions.md D55).
//
// The category is required and checked, exactly as DeleteTorrent requires it.
// Curator must not be able to stop a torrent that is not its own even by
// mistake — and on this backend that is a formality, since everything in the
// client is curator's, which is a strengthening rather than a reason to skip it.
func (e *Engine) PauseTorrent(_ context.Context, hash, requireCategory string) error {
	t, err := e.own(hash, requireCategory, "pause")
	if err != nil {
		return err
	}
	hash = torrent.NormalizeHash(hash)

	e.mu.Lock()
	already := e.paused[hash]
	e.paused[hash] = true
	// **The stall clock is reset, and this line is the whole reason resume
	// works.** `mark.since` is when the byte count last moved; a pause is
	// minutes or hours of it not moving, so without this the first observation
	// after a resume compares against a `since` from before the pause and
	// reports `stalled` immediately — five minutes of "nobody appears to be
	// seeding this release" about a download that was switched off on purpose.
	// DeleteTorrent clears the same map for the same kind of reason.
	delete(e.progress, hash)
	e.mu.Unlock()

	if already {
		return nil
	}

	t.DisallowDataDownload()
	t.DisallowDataUpload()
	old := t.SetMaxEstablishedConns(0)
	e.mu.Lock()
	e.pausedConns[hash] = old
	e.mu.Unlock()

	e.log.Info("download paused", "hash", hash, "name", t.Name())
	return nil
}

// ResumeTorrent lets one paused torrent move again.
//
// **It does not lift a hold, and that asymmetry is the point.** If the VPN
// sentinel is holding everything, this clears the pause flag and leaves the
// torrent disallowed: the hash comes off the paused set, so the next Release
// restores it along with everything else. Pressing Resume must never be a way
// to download unprotected (D27, D47).
func (e *Engine) ResumeTorrent(_ context.Context, hash, requireCategory string) error {
	t, err := e.own(hash, requireCategory, "resume")
	if err != nil {
		return err
	}
	hash = torrent.NormalizeHash(hash)

	e.mu.Lock()
	old, hadConns := e.pausedConns[hash]
	delete(e.paused, hash)
	delete(e.pausedConns, hash)
	// Same reasoning as the pause: the time it spent paused is not a stall, and
	// the clock has to start from now rather than from before it stopped.
	delete(e.progress, hash)
	held := e.heldReason != ""
	e.mu.Unlock()

	if held {
		e.log.Info("resume recorded, but downloads are held and this one stays stopped",
			"hash", hash, "name", t.Name())
		return nil
	}

	t.AllowDataDownload()
	t.AllowDataUpload()
	if hadConns && old > 0 {
		t.SetMaxEstablishedConns(old)
	}

	e.log.Info("download resumed", "hash", hash, "name", t.Name())
	return nil
}

// isPaused reports whether this hash was stopped on purpose.
func (e *Engine) isPaused(hash string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.paused[hash]
}

// own finds a torrent by hash and refuses one that is not in the required
// category, in the shape DeleteTorrent established.
//
// The category refusal is torrent.WrongCategory rather than a sentence of this
// package's, so internal/api answers 409 with the same words whichever backend
// is running.
func (e *Engine) own(hash, requireCategory, verb string) (*anacrolix.Torrent, error) {
	ih, err := parseHash(hash)
	if err != nil {
		return nil, err
	}
	if requireCategory != "" && !strings.EqualFold(e.category, requireCategory) {
		return nil, fmt.Errorf("engine: %w", torrent.WrongCategory{
			Hash: ih.HexString(), Actual: e.category, Required: requireCategory,
		})
	}
	t, ok := e.client.Torrent(ih)
	if !ok {
		// Not the same call DeleteTorrent makes. A delete that has already
		// happened is success, because a retry could never complete otherwise;
		// pausing a torrent that is not there is a request about nothing, and
		// answering 200 would tell somebody a row is paused when there is no row.
		return nil, fmt.Errorf("engine: %s %s: %w", verb, torrent.NormalizeHash(ih.HexString()), torrent.ErrNotFound)
	}
	return t, nil
}
