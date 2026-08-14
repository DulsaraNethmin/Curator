package download

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/torrent"
)

func newPoller(c TorrentClient, st Store) *Poller {
	p := NewPoller(c, st, "curator", 10*time.Millisecond, quiet())
	at := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return at }
	return p
}

func TestTickWritesStateAndProgress(t *testing.T) {
	client := &fakeClient{torrents: []torrent.Torrent{
		{Hash: testHash, Name: "Interstellar", State: torrent.StateDownloading, Progress: 0.42},
	}}
	st := newFakeStore()
	st.byHash[testHash] = store.Download{TorrentHash: testHash, State: store.DownloadQueued, Progress: 0}

	if err := newPoller(client, st).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(st.updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(st.updates))
	}
	got := st.updates[0]
	if got.state != store.DownloadDownloading || got.progress != 0.42 {
		t.Errorf("update = %+v, want downloading at 0.42", got)
	}
	if got.hash != testHash {
		t.Errorf("hash = %q, want the normalised upper-case form", got.hash)
	}
	if got.completedAt != nil {
		t.Error("completed_at set on a running download")
	}
}

// T55: the reason is written in the same call as the state it explains, and it
// comes off the torrent rather than being composed here. The poller sees a
// percentage that did not move; it cannot tell "nobody has this" from "nobody
// will send it", which is precisely the distinction worth showing.
func TestTickWritesTheStallReasonWithTheState(t *testing.T) {
	const why = "no peers are connected — nobody appears to be seeding this release"
	client := &fakeClient{torrents: []torrent.Torrent{
		{Hash: testHash, State: torrent.StateStalled, Progress: 0, Reason: why},
	}}
	st := newFakeStore()
	st.byHash[testHash] = store.Download{TorrentHash: testHash, State: store.DownloadQueued}

	if err := newPoller(client, st).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(st.updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(st.updates))
	}
	if st.updates[0].state != store.DownloadStalled || st.updates[0].reason != why {
		t.Errorf("update = %+v, want stalled with the backend's own sentence", st.updates[0])
	}
}

// The sentence can change while the state and the progress do not: a torrent
// sits at 0% and `stalled` while the explanation moves from "nobody answered"
// to "peers are connected but none of them is sending data". Without the reason
// in the write condition the row keeps serving the first one for ever, and this
// is the only test that fails when it is dropped — state and progress are
// deliberately identical on both sides.
func TestTickWritesWhenOnlyTheReasonChanged(t *testing.T) {
	const newer = "peers are connected but none of them is sending data"
	client := &fakeClient{torrents: []torrent.Torrent{
		{Hash: testHash, State: torrent.StateStalled, Progress: 0, Reason: newer},
	}}
	st := newFakeStore()
	st.byHash[testHash] = store.Download{
		TorrentHash: testHash, State: store.DownloadStalled, Progress: 0,
		Reason: "no peers are connected — nobody appears to be seeding this release",
	}

	if err := newPoller(client, st).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(st.updates) != 1 {
		t.Fatalf("updates = %d, want 1 — the reason moved and nothing else did", len(st.updates))
	}
	if st.updates[0].reason != newer {
		t.Errorf("reason = %q, want the newer %q", st.updates[0].reason, newer)
	}
}

// A peer appears. The state goes back to downloading and the explanation has to
// go with it — an empty reason overwrites the stale one rather than being
// skipped as "no news", or the row would say nobody is seeding a file that is
// arriving.
func TestTickClearsTheReasonWhenATorrentStartsMoving(t *testing.T) {
	client := &fakeClient{torrents: []torrent.Torrent{
		{Hash: testHash, State: torrent.StateDownloading, Progress: 0.1},
	}}
	st := newFakeStore()
	st.byHash[testHash] = store.Download{
		TorrentHash: testHash, State: store.DownloadStalled, Progress: 0,
		Reason: "no peers are connected — nobody appears to be seeding this release",
	}

	if err := newPoller(client, st).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(st.updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(st.updates))
	}
	if st.updates[0].reason != "" {
		t.Errorf("reason = %q on a moving download, want it cleared", st.updates[0].reason)
	}
}

// A torrent that is fine, polled repeatedly, writes nothing — the reason must
// not become a third value that differs every tick and turns an idle poll into
// a write. Both sides are empty and both sides stay empty.
func TestTickWithNothingMovingAndNoReasonWritesNothing(t *testing.T) {
	client := &fakeClient{torrents: []torrent.Torrent{
		{Hash: testHash, State: torrent.StateDownloading, Progress: 0.42},
	}}
	st := newFakeStore()
	st.byHash[testHash] = store.Download{
		TorrentHash: testHash, State: store.DownloadDownloading, Progress: 0.42,
	}

	p := newPoller(client, st)
	for i := 0; i < 3; i++ {
		if err := p.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if len(st.updates) != 0 {
		t.Errorf("updates = %d, want 0 — nothing about this torrent changed", len(st.updates))
	}
}

// The moment a download finishes is stamped once, here. Which of qBittorrent's
// spellings mean "finished" — stoppedUP, pausedUP, uploading, stalledUP — is
// asserted in internal/qbit's state test now that the mapping happens in the
// backend rather than in this loop.
func TestTickStampsCompletedAtOnTheTransition(t *testing.T) {
	client := &fakeClient{torrents: []torrent.Torrent{
		{Hash: testHash, State: torrent.StateCompleted, Progress: 1},
	}}
	st := newFakeStore()
	st.byHash[testHash] = store.Download{TorrentHash: testHash, State: store.DownloadDownloading, Progress: 0.9}

	if err := newPoller(client, st).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(st.updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(st.updates))
	}
	if st.updates[0].state != store.DownloadCompleted {
		t.Errorf("state = %q, want completed", st.updates[0].state)
	}
	if st.updates[0].completedAt == nil {
		t.Error("completed_at not stamped on the transition into completed")
	}
}

func TestTickStampsCompletedAtOnceAndNeverClearsIt(t *testing.T) {
	client := &fakeClient{torrents: []torrent.Torrent{
		{Hash: testHash, State: torrent.StateCompleted, Progress: 1},
	}}
	st := newFakeStore()
	earlier := time.Date(2026, 8, 12, 11, 0, 0, 0, time.UTC)
	// Already completed, with a completion moment recorded.
	st.byHash[testHash] = store.Download{
		TorrentHash: testHash, State: store.DownloadCompleted, Progress: 1, CompletedAt: &earlier,
	}

	if err := newPoller(client, st).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(st.updates) != 0 {
		t.Errorf("updates = %+v, want none — nothing moved, so the row is left alone", st.updates)
	}
}

// A completed torrent that phase 4 has imported must not be dragged back to
// "completed" on the next tick.
func TestTickDoesNotOverwriteImported(t *testing.T) {
	client := &fakeClient{torrents: []torrent.Torrent{
		{Hash: testHash, State: torrent.StateCompleted, Progress: 1},
	}}
	st := newFakeStore()
	st.byHash[testHash] = store.Download{TorrentHash: testHash, State: store.DownloadImported, Progress: 1}

	if err := newPoller(client, st).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(st.updates) != 0 {
		t.Errorf("imported row was updated to %+v — imported is phase 4's to set", st.updates)
	}
}

func TestTickSkipsATorrentWithNoRow(t *testing.T) {
	client := &fakeClient{torrents: []torrent.Torrent{
		{Hash: testHash, Name: "someone else's", State: torrent.StateDownloading},
		{Hash: "AAAA000000000000000000000000000000000000", Name: "ours", State: torrent.StateDownloading, Progress: 0.5},
	}}
	st := newFakeStore()
	st.byHash["AAAA000000000000000000000000000000000000"] = store.Download{
		TorrentHash: "AAAA000000000000000000000000000000000000", State: store.DownloadQueued,
	}

	if err := newPoller(client, st).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v — an unknown torrent must not fail the run", err)
	}
	if len(st.updates) != 1 {
		t.Fatalf("updates = %d, want only ours", len(st.updates))
	}
	if st.updates[0].hash != "AAAA000000000000000000000000000000000000" {
		t.Errorf("updated %q, want only the row we own", st.updates[0].hash)
	}
}

func TestTickReturnsTheClientError(t *testing.T) {
	client := &fakeClient{listErr: errors.New("connection refused")}
	if err := newPoller(client, newFakeStore()).Tick(context.Background()); err == nil {
		t.Fatal("Tick swallowed a client failure")
	}
}

// A failing tick must not end the loop: qBittorrent restarting, or a VPN
// reconnecting under it, has to be survivable.
func TestRunSurvivesAFailingTickAndStopsOnCancel(t *testing.T) {
	client := &fakeClient{listErr: errors.New("connection refused")}
	p := newPoller(client, newFakeStore())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	// Let several ticks fail.
	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after its context was cancelled — the goroutine outlives its owner")
	}

	var listCalls int
	for _, c := range client.calls {
		if c == "list" {
			listCalls++
		}
	}
	if listCalls < 2 {
		t.Errorf("list called %d times, want the loop to have kept going after a failure", listCalls)
	}
}

func TestRunStopsPromptlyOnCancel(t *testing.T) {
	p := NewPoller(&fakeClient{}, newFakeStore(), "curator", time.Hour, quiet())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run waited for its hour-long ticker instead of its context")
	}
}
