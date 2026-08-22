package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// paused stands up an engine holding one added magnet and returns both.
func pausedFixture(t *testing.T) (*Engine, string) {
	t.Helper()
	_, mi, ih := seed(t)
	e := start(t, Config{DataDir: t.TempDir(), Category: "curator", StallAfter: time.Minute})
	if err := e.AddMagnet(context.Background(), magnetFor(t, mi, ih), "curator"); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}
	list, err := e.Torrents(context.Background(), "curator")
	if err != nil || len(list) != 1 {
		t.Fatalf("Torrents = %v, %v; want one torrent", list, err)
	}
	return e, list[0].Hash
}

func stateOf(t *testing.T, e *Engine) torrent.Torrent {
	t.Helper()
	list, err := e.Torrents(context.Background(), "curator")
	if err != nil {
		t.Fatalf("Torrents: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("Torrents = %d rows, want 1", len(list))
	}
	return list[0]
}

// A pause is a state, so the row can draw a button on it.
func TestAPausedTorrentSaysPausedAndNotStalled(t *testing.T) {
	e, hash := pausedFixture(t)

	if err := e.PauseTorrent(context.Background(), hash, "curator"); err != nil {
		t.Fatalf("PauseTorrent: %v", err)
	}
	got := stateOf(t, e)
	if got.State != torrent.StatePaused {
		t.Errorf("State = %q, want paused", got.State)
	}
	if got.Reason != "" {
		t.Errorf("Reason = %q on a paused torrent, want empty — the badge is the explanation "+
			"and a reason would outlive the state the moment somebody resumes", got.Reason)
	}
	if got.DownloadRate != 0 {
		t.Errorf("DownloadRate = %d on a paused torrent, want 0", got.DownloadRate)
	}

	if err := e.ResumeTorrent(context.Background(), hash, "curator"); err != nil {
		t.Fatalf("ResumeTorrent: %v", err)
	}
	if got := stateOf(t, e); got.State == torrent.StatePaused {
		t.Error("still paused after ResumeTorrent")
	}
}

// **The five-minute lie.** A paused torrent gains no bytes, so without the
// exemption the stall detector is correct that nothing is arriving and wrong
// about why — it would report "no peers are connected" about a download curator
// stopped on request. That is T78's defect arriving from a third direction.
func TestAPausedTorrentNeverGoesStalled(t *testing.T) {
	e, hash := pausedFixture(t)
	at := time.Now()
	e.now = func() time.Time { return at }

	if err := e.PauseTorrent(context.Background(), hash, "curator"); err != nil {
		t.Fatalf("PauseTorrent: %v", err)
	}

	// Well past StallAfter, and then some.
	for _, elapsed := range []time.Duration{90 * time.Second, 10 * time.Minute, time.Hour} {
		at = at.Add(elapsed)
		got := stateOf(t, e)
		if got.State != torrent.StatePaused {
			t.Fatalf("State = %q after %s paused, want paused", got.State, elapsed)
		}
		if strings.Contains(got.Reason, "seeding") {
			t.Fatalf("Reason = %q — the stall detector is blaming the swarm for a pause", got.Reason)
		}
	}
}

// **The regression the map delete exists for.** `mark.since` is when the byte
// count last moved, and a pause is a long stretch of it not moving — so without
// clearing the mark, the first observation after a resume compares against a
// timestamp from before the pause and reports `stalled` immediately.
func TestResumingDoesNotInheritTheStallClockFromThePause(t *testing.T) {
	e, hash := pausedFixture(t)
	at := time.Now()
	e.now = func() time.Time { return at }

	stateOf(t, e) // one observation, so there is a mark to inherit

	if err := e.PauseTorrent(context.Background(), hash, "curator"); err != nil {
		t.Fatalf("PauseTorrent: %v", err)
	}
	at = at.Add(2 * time.Hour) // paused for a long time
	if err := e.ResumeTorrent(context.Background(), hash, "curator"); err != nil {
		t.Fatalf("ResumeTorrent: %v", err)
	}

	if got := stateOf(t, e); got.State == torrent.StateStalled {
		t.Errorf("State = stalled on the first look after a two-hour pause (%q) — "+
			"the stall clock must restart on resume, not carry the pause in it", got.Reason)
	}
}

// **A tunnel blip must not restart what somebody stopped.** Release loops every
// torrent and allows it; without the paused check it would silently resume a
// deliberate pause and nothing on screen would say so.
func TestAHoldAndReleaseLeaveAPauseAlone(t *testing.T) {
	e, hash := pausedFixture(t)

	if err := e.PauseTorrent(context.Background(), hash, "curator"); err != nil {
		t.Fatalf("PauseTorrent: %v", err)
	}
	if err := e.Hold("the tunnel went away"); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	// While held, the kill switch outranks the pause on screen — it is the more
	// important thing to be told.
	if got := stateOf(t, e); got.State != torrent.StateStalled || got.Reason == "" {
		t.Errorf("under a hold: State = %q Reason = %q, want the hold's stalled+reason", got.State, got.Reason)
	}

	if err := e.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := stateOf(t, e); got.State != torrent.StatePaused {
		t.Errorf("State = %q after Release, want paused — a tunnel blip must not "+
			"restart a download somebody deliberately stopped", got.State)
	}
}

// Resume under a hold records the preference and downloads nothing. Pressing it
// must never be a way past the kill switch (D27, D47).
func TestResumeUnderAHoldDoesNotStartDownloading(t *testing.T) {
	e, hash := pausedFixture(t)

	if err := e.PauseTorrent(context.Background(), hash, "curator"); err != nil {
		t.Fatalf("PauseTorrent: %v", err)
	}
	if err := e.Hold("the tunnel went away"); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if err := e.ResumeTorrent(context.Background(), hash, "curator"); err != nil {
		t.Fatalf("ResumeTorrent: %v", err)
	}

	got := stateOf(t, e)
	if got.State != torrent.StateStalled || got.Reason == "" {
		t.Errorf("State = %q Reason = %q after resuming under a hold, want the hold's",
			got.State, got.Reason)
	}
	// And once the tunnel is back, the resume it recorded takes effect.
	if err := e.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := stateOf(t, e); got.State == torrent.StatePaused {
		t.Error("still paused after Release, but Resume was pressed while held — " +
			"the preference was recorded and then ignored")
	}
}

// The category guard DeleteTorrent has, on both new calls. Curator must not be
// able to stop a torrent that is not its own even by mistake, and the refusal is
// torrent.WrongCategory so internal/api answers the same 409 on either backend.
func TestPauseRefusesAnotherAppsCategory(t *testing.T) {
	e, hash := pausedFixture(t)

	for _, call := range []struct {
		name string
		fn   func(context.Context, string, string) error
	}{
		{"pause", e.PauseTorrent},
		{"resume", e.ResumeTorrent},
	} {
		err := call.fn(context.Background(), hash, "radarr")
		if !errors.Is(err, torrent.ErrWrongCategory) {
			t.Errorf("%s in category radarr = %v, want ErrWrongCategory", call.name, err)
		}
	}
	if got := stateOf(t, e); got.State == torrent.StatePaused {
		t.Error("the refused pause paused it anyway")
	}
}

// A hash the backend does not have is not found, and is deliberately NOT the
// "already gone is success" that DeleteTorrent answers: a delete that already
// happened cannot be retried, while pausing a torrent that is not there is a
// request about nothing, and 200 would claim a row is paused that has no row.
func TestPausingAnUnknownHashIsNotFound(t *testing.T) {
	e, _ := pausedFixture(t)
	const missing = "0000000000000000000000000000000000000000"

	if err := e.PauseTorrent(context.Background(), missing, "curator"); !errors.Is(err, torrent.ErrNotFound) {
		t.Errorf("PauseTorrent(unknown) = %v, want ErrNotFound", err)
	}
	if err := e.ResumeTorrent(context.Background(), missing, "curator"); !errors.Is(err, torrent.ErrNotFound) {
		t.Errorf("ResumeTorrent(unknown) = %v, want ErrNotFound", err)
	}
}

// Pausing twice is free, exactly as Hold is: a screen that double-fires a
// button must not corrupt the remembered connection limit.
func TestPauseIsIdempotent(t *testing.T) {
	e, hash := pausedFixture(t)

	for i := 0; i < 3; i++ {
		if err := e.PauseTorrent(context.Background(), hash, "curator"); err != nil {
			t.Fatalf("PauseTorrent %d: %v", i, err)
		}
	}
	if got := stateOf(t, e); got.State != torrent.StatePaused {
		t.Errorf("State = %q, want paused", got.State)
	}
	if err := e.ResumeTorrent(context.Background(), hash, "curator"); err != nil {
		t.Fatalf("ResumeTorrent: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := e.ResumeTorrent(context.Background(), hash, "curator"); err != nil {
			t.Fatalf("ResumeTorrent %d: %v", i, err)
		}
	}
	if got := stateOf(t, e); got.State == torrent.StatePaused {
		t.Error("still paused after three resumes")
	}
}
