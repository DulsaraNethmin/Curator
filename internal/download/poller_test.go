package download

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/qbit"
	"github.com/DulsaraNethmin/curator/internal/store"
)

func newPoller(c TorrentClient, st Store) *Poller {
	p := NewPoller(c, st, "curator", 10*time.Millisecond, quiet())
	at := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return at }
	return p
}

func TestTickMapsStateAndProgress(t *testing.T) {
	client := &fakeClient{torrents: []qbit.Torrent{
		{Hash: strings.ToLower(testHash), Name: "Interstellar", State: "downloading", Progress: 0.42},
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

// qBittorrent 5.x says stoppedUP where 4.x said pausedUP. Both must complete, or
// a finished download never reaches phase 4.
func TestTickTreatsStoppedAndPausedUploadAsCompleted(t *testing.T) {
	for _, state := range []string{"stoppedUP", "pausedUP", "uploading", "stalledUP"} {
		t.Run(state, func(t *testing.T) {
			client := &fakeClient{torrents: []qbit.Torrent{
				{Hash: strings.ToLower(testHash), State: state, Progress: 1},
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
				t.Errorf("%s mapped to %q, want completed", state, st.updates[0].state)
			}
			if st.updates[0].completedAt == nil {
				t.Error("completed_at not stamped on the transition into completed")
			}
		})
	}
}

func TestTickStampsCompletedAtOnceAndNeverClearsIt(t *testing.T) {
	client := &fakeClient{torrents: []qbit.Torrent{
		{Hash: strings.ToLower(testHash), State: "stoppedUP", Progress: 1},
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
	client := &fakeClient{torrents: []qbit.Torrent{
		{Hash: strings.ToLower(testHash), State: "uploading", Progress: 1},
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
	client := &fakeClient{torrents: []qbit.Torrent{
		{Hash: strings.ToLower(testHash), Name: "someone else's", State: "downloading"},
		{Hash: "aaaa000000000000000000000000000000000000", Name: "ours", State: "downloading", Progress: 0.5},
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
