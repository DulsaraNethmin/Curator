package download

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// seedRows puts recorded downloads in the fake store, as a dispatch would have.
func seedRows(st *fakeStore, hashes ...string) []store.Download {
	out := make([]store.Download, 0, len(hashes))
	for i, h := range hashes {
		row := store.Download{
			ID: int64(i + 1), MovieID: 7, TorrentHash: h,
			Indexer: "yts", ReleaseName: "Release " + h,
			Magnet: testMagnet(h), State: store.DownloadDownloading, Progress: 0.4,
		}
		st.byHash[h] = row
		out = append(out, row)
	}
	return out
}

func pauseService(client TorrentClient, st Store) *Service {
	return NewService(client, st, newResolver(testMagnet(testHash)), "curator", quiet())
}

// **The one that matters for the list.** Downloads has always promised to stay
// answerable when the backend is down — "which is exactly when someone is most
// likely to be looking" — and joining a live read onto it is precisely the
// change that could break that promise quietly.
func TestDownloadsStillListsEveryRowWhenTheBackendIsDown(t *testing.T) {
	st := newFakeStore()
	seedRows(st, "AAAA", "BBBB")

	client := &fakeClient{listErr: errors.New("connection refused")}
	rows, err := pauseService(client, st).Downloads(context.Background())
	if err != nil {
		t.Fatalf("Downloads = %v, want the rows and no error — a dead backend is not a dead list", err)
	}
	if len(rows) != 2 {
		t.Fatalf("Downloads = %d rows, want 2", len(rows))
	}
	for _, r := range rows {
		if r.DownloadRate != 0 || r.SizeBytes != 0 || r.ETASeconds != 0 {
			t.Errorf("%s carries live fields with no backend to have supplied them: %+v", r.TorrentHash, r)
		}
	}
}

// The join itself, and an ETA derived here rather than taken from a backend.
func TestDownloadsJoinsTheLiveRateAndDerivesTheETA(t *testing.T) {
	st := newFakeStore()
	seedRows(st, "AAAA")

	client := &fakeClient{torrents: []torrent.Torrent{{
		Hash:         "AAAA",
		Category:     "curator",
		State:        torrent.StateDownloading,
		Progress:     0.25,
		SizeBytes:    8 << 30, // 8 GiB
		DownloadRate: 4 << 20, // 4 MiB/s
	}}}

	rows, err := pauseService(client, st).Downloads(context.Background())
	if err != nil {
		t.Fatalf("Downloads: %v", err)
	}
	got := rows[0]
	if got.DownloadRate != 4<<20 || got.SizeBytes != 8<<30 {
		t.Fatalf("live fields = %d B/s, %d bytes; want 4 MiB/s and 8 GiB", got.DownloadRate, got.SizeBytes)
	}
	// Six of eight gibibytes left, at four mebibytes a second.
	if want := int64(float64(6<<30) / float64(4<<20)); got.ETASeconds != want {
		t.Errorf("ETASeconds = %d, want %d", got.ETASeconds, want)
	}

	// A row the backend does not know about is still listed, without the keys.
	seedRows(st, "ZZZZ")
	rows, _ = pauseService(client, st).Downloads(context.Background())
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want both", len(rows))
	}
	for _, r := range rows {
		if r.TorrentHash == "ZZZZ" && (r.DownloadRate != 0 || r.ETASeconds != 0) {
			t.Errorf("a row the backend never mentioned carries live fields: %+v", r)
		}
	}
}

// No rate, no size, or nothing left to fetch: there is no honest number, so
// there is no key. Omitted rather than 0 — a screen cannot tell a computed zero
// from an absent one.
func TestTheETAIsOmittedRatherThanInvented(t *testing.T) {
	cases := []struct {
		name     string
		size     int64
		progress float64
		rate     int64
	}{
		{"no rate", 8 << 30, 0.5, 0},
		{"no metadata yet", 0, 0, 1 << 20},
		{"already finished", 8 << 30, 1, 1 << 20},
	}
	for _, tc := range cases {
		if got := eta(tc.size, tc.progress, tc.rate); got != 0 {
			t.Errorf("%s: eta = %d, want 0 — nothing here is derivable", tc.name, got)
		}
	}
}

// **The restart trap.** Service.Resume re-adds every non-imported row by magnet
// at boot, and the embedded engine rebuilds its torrents with no memory of a
// preference — so without the re-pause, the first reboot silently restarts
// everything somebody deliberately stopped.
func TestResumeReAppliesAPauseAfterARestart(t *testing.T) {
	st := newFakeStore()
	rows := seedRows(st, "AAAA")
	paused := rows[0]
	paused.State = store.DownloadPaused
	st.byHash["AAAA"] = paused

	client := &fakeClient{} // nothing held: this is a cold start
	if err := pauseService(client, st).Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	if len(client.paused) != 1 || client.paused[0] != "AAAA" {
		t.Fatalf("paused %v after the restart, want exactly AAAA", client.paused)
	}
	if client.pauseCategory != "curator" {
		t.Errorf("re-paused in category %q, want curator", client.pauseCategory)
	}

	// The ORDER is the point: the magnet has to be added before it can be
	// stopped, because a paused download the client does not hold at all cannot
	// be resumed later.
	joined := strings.Join(client.calls, ",")
	if strings.Index(joined, "add") > strings.Index(joined, "pause") {
		t.Errorf("calls = %v, want the add before the pause", client.calls)
	}
}

// A row that was NOT paused is left running, which is the other half of the same
// assertion — a re-pause that fired for everything would be worse than none.
func TestResumeDoesNotPauseARowThatWasRunning(t *testing.T) {
	st := newFakeStore()
	seedRows(st, "AAAA")

	client := &fakeClient{}
	if err := pauseService(client, st).Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(client.paused) != 0 {
		t.Errorf("paused %v, want nothing — that row was downloading when the box went down", client.paused)
	}
}

// An imported row has nothing running to stop. Refusing is what keeps the screen
// from offering a button that fails.
func TestPausingAnImportedDownloadIsRefused(t *testing.T) {
	st := newFakeStore()
	rows := seedRows(st, "AAAA")
	imported := rows[0]
	imported.State = store.DownloadImported
	st.byHash["AAAA"] = imported

	client := &fakeClient{}
	if _, err := pauseService(client, st).PauseDownload(context.Background(), "AAAA"); !errors.Is(err, ErrNotRunning) {
		t.Errorf("PauseDownload on an imported row = %v, want ErrNotRunning", err)
	}
	if len(client.paused) != 0 {
		t.Error("the backend was asked anyway")
	}
}

// A hash with no row is a request about a torrent this instance did not start.
func TestPausingAHashWithNoRowIsNotFound(t *testing.T) {
	st := newFakeStore()
	st.getErr = store.ErrNotFound
	client := &fakeClient{}

	_, err := pauseService(client, st).PauseDownload(context.Background(), "CCCC")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want store.ErrNotFound", err)
	}
	if len(client.paused) != 0 {
		t.Error("the backend was asked for a hash curator has no row for")
	}
}

// Unconfigured downloads refuse before anything else is consulted.
func TestPausingWithNoBackendIsUnconfigured(t *testing.T) {
	_, err := pauseService(nil, newFakeStore()).PauseDownload(context.Background(), "AAAA")
	if !errors.Is(err, ErrUnconfigured) {
		t.Errorf("err = %v, want ErrUnconfigured", err)
	}
}

// The row is written on the click rather than left to the next poll: the screen
// has to change now, and the row is what makes the pause survive a restart.
func TestPausingWritesTheRowImmediately(t *testing.T) {
	st := newFakeStore()
	seedRows(st, "AAAA")
	svc := pauseService(&fakeClient{}, st)

	if _, err := svc.PauseDownload(context.Background(), "AAAA"); err != nil {
		t.Fatalf("PauseDownload: %v", err)
	}
	if len(st.updates) != 1 || st.updates[0].state != store.DownloadPaused {
		t.Fatalf("updates = %+v, want one write of `paused`", st.updates)
	}

	if _, err := svc.ResumeDownload(context.Background(), "AAAA"); err != nil {
		t.Fatalf("ResumeDownload: %v", err)
	}
	// Not `downloading`: what it actually is now is the backend's to say on the
	// next tick, and claiming it here would be this layer guessing.
	if len(st.updates) != 2 || st.updates[1].state != store.DownloadQueued {
		t.Fatalf("updates = %+v, want the resume to write `queued`", st.updates)
	}
}

// A backend that refuses leaves the row alone. Writing `paused` for a stop that
// did not happen would put a Resume button on a torrent that is still running.
func TestABackendRefusalLeavesTheRowUntouched(t *testing.T) {
	st := newFakeStore()
	seedRows(st, "AAAA")
	client := &fakeClient{pauseErr: torrent.WrongCategory{Hash: "AAAA", Actual: "radarr", Required: "curator"}}

	_, err := pauseService(client, st).PauseDownload(context.Background(), "AAAA")
	if !errors.Is(err, torrent.ErrWrongCategory) {
		t.Fatalf("err = %v, want ErrWrongCategory to reach the caller", err)
	}
	if !errors.Is(err, ErrClient) {
		t.Errorf("err = %v, want it to carry ErrClient so the API answers 502-or-409 from one place", err)
	}
	if len(st.updates) != 0 {
		t.Errorf("wrote %+v after a refused pause, want nothing", st.updates)
	}
}
