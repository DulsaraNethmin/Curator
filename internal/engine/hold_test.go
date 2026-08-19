package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// TestABadVerdictHoldsEveryTorrent. Not the one being dispatched, not the ones
// added since — every one the client has, because a tunnel that stopped
// protecting downloads stopped protecting all of them at once.
func TestABadVerdictHoldsEveryTorrent(t *testing.T) {
	_, mi, ih := seed(t)
	e := start(t, Config{DataDir: t.TempDir(), Category: "curator"})
	if err := e.AddMagnet(context.Background(), magnetFor(t, mi, ih), "curator"); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}

	if held, _ := e.Held(); held {
		t.Fatal("a fresh engine reports itself held")
	}

	const reason = "the tunnel's exit address is the host's own"
	if err := e.Hold(reason); err != nil {
		t.Fatalf("Hold: %v", err)
	}

	held, why := e.Held()
	if !held || why != reason {
		t.Errorf("Held = %v, %q; want true and the reason it was given", held, why)
	}

	rows, err := e.Torrents(context.Background(), "curator")
	if err != nil {
		t.Fatalf("Torrents: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no rows; a hold must not lose them")
	}
	for _, row := range rows {
		if row.State != torrent.StateStalled {
			t.Errorf("%s is %q while held, want stalled", row.Name, row.State)
		}
		// The whole point of carrying the reason. Without it the Activity
		// screen says "nobody appears to be seeding this release" about a
		// download curator switched off itself — T78's failure, arriving from a
		// new direction.
		if !strings.Contains(row.Reason, reason) {
			t.Errorf("reason = %q, want it to name the tunnel rather than the swarm", row.Reason)
		}
		if strings.Contains(row.Reason, "seeding") {
			t.Errorf("reason = %q, which blames the swarm for a hold curator applied", row.Reason)
		}
	}

	// Every torrent had its peers dropped, not just its data stopped.
	for _, tor := range e.client.Torrents() {
		if got := tor.SetMaxEstablishedConns(0); got != 0 {
			t.Errorf("%s still allows %d connections while held", tor.Name(), got)
		}
	}
}

// TestAGoodVerdictReleasesThem, and puts back each torrent's own limit rather
// than a global default.
func TestAGoodVerdictReleasesThem(t *testing.T) {
	_, mi, ih := seed(t)
	e := start(t, Config{DataDir: t.TempDir(), Category: "curator", MaxConns: 7})
	if err := e.AddMagnet(context.Background(), magnetFor(t, mi, ih), "curator"); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}

	if err := e.Hold("the tunnel went away"); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if err := e.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	if held, why := e.Held(); held {
		t.Errorf("still held after Release, reason %q", why)
	}
	for _, tor := range e.client.Torrents() {
		old := tor.SetMaxEstablishedConns(7)
		if old != 7 {
			t.Errorf("%s allows %d connections after release, want the 7 it had before the hold", tor.Name(), old)
		}
	}

	rows, err := e.Torrents(context.Background(), "curator")
	if err != nil {
		t.Fatalf("Torrents: %v", err)
	}
	for _, row := range rows {
		if row.State == torrent.StateStalled && strings.Contains(row.Reason, "would not be protected") {
			t.Errorf("%s still reports the hold after release: %q", row.Name, row.Reason)
		}
	}
}

// TestHoldDoesNotLoseProgress is the reason this is Hold and not Drop.
//
// A tunnel blip that cost a half-finished download would make the kill switch
// more expensive than what it protects against, and a safety feature that
// expensive gets turned off.
func TestHoldDoesNotLoseProgress(t *testing.T) {
	dataDir := t.TempDir()
	_, mi, ih := seed(t)
	e := start(t, Config{DataDir: dataDir, Category: "curator"})
	if err := e.AddMagnet(context.Background(), magnetFor(t, mi, ih), "curator"); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}

	before, err := e.TorrentByHash(context.Background(), ih.HexString())
	if err != nil {
		t.Fatalf("TorrentByHash: %v", err)
	}

	if err := e.Hold("the tunnel went away"); err != nil {
		t.Fatalf("Hold: %v", err)
	}

	after, err := e.TorrentByHash(context.Background(), ih.HexString())
	if err != nil {
		t.Fatalf("TorrentByHash while held: %v", err)
	}
	if after == nil {
		t.Fatal("the torrent is gone; Hold must not Drop")
	}
	if after.Progress != before.Progress {
		t.Errorf("progress moved from %v to %v across a hold", before.Progress, after.Progress)
	}
	if after.ContentPath != before.ContentPath {
		t.Errorf("content path changed across a hold: %q -> %q", before.ContentPath, after.ContentPath)
	}

	if err := e.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	resumed, err := e.TorrentByHash(context.Background(), ih.HexString())
	if err != nil || resumed == nil {
		t.Fatalf("the torrent did not survive release: %v", err)
	}
}

// TestHoldingTwiceIsFree. A sentinel that sees blocked, then stale, then unknown
// calls Hold three times for one failure. If the second call re-read the
// connection limits it would record the zeroes the first one installed, and
// Release would then restore nothing.
func TestHoldingTwiceIsFree(t *testing.T) {
	_, mi, ih := seed(t)
	e := start(t, Config{DataDir: t.TempDir(), Category: "curator", MaxConns: 5})
	if err := e.AddMagnet(context.Background(), magnetFor(t, mi, ih), "curator"); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}

	for _, reason := range []string{"blocked", "stale", "unknown"} {
		if err := e.Hold(reason); err != nil {
			t.Fatalf("Hold(%s): %v", reason, err)
		}
	}
	if err := e.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	for _, tor := range e.client.Torrents() {
		if old := tor.SetMaxEstablishedConns(5); old != 5 {
			t.Errorf("%s allows %d connections after three holds and one release, want 5 — "+
				"the second hold recorded the zeroes the first installed", tor.Name(), old)
		}
	}

	// And releasing something that was never held is not an error: on a curator
	// that has been running happily, the sentinel's first good verdict arrives
	// with nothing to release.
	if err := e.Release(); err != nil {
		t.Errorf("releasing an unheld engine: %v", err)
	}
}

// TestAHoldNeedsAReason. The reason is not decoration — it is what the screen
// renders instead of blaming the swarm — so an empty one is refused rather than
// silently producing a stalled row with no explanation.
func TestAHoldNeedsAReason(t *testing.T) {
	e := start(t, Config{DataDir: t.TempDir(), Category: "curator"})
	if err := e.Hold(""); err == nil {
		t.Error("a hold with no reason was accepted")
	}
	if held, _ := e.Held(); held {
		t.Error("the refused hold took effect anyway")
	}
}
