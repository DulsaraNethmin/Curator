package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	anacrolix "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"github.com/DulsaraNethmin/curator/internal/download"
	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// The point of the whole exercise, asserted by the compiler: this engine is a
// download.TorrentClient, so every test in that package — all of which run on
// fakes — describes behaviour this satisfies. It is in the test file because
// the engine has no business importing the orchestration layer at run time.
var _ download.TorrentClient = (*Engine)(nil)

// The whole package is tested against itself: one engine seeds a payload
// generated in t.TempDir(), another downloads it. No swarm, no tracker, no
// public torrent, and no network beyond loopback — which is the only way a
// torrent client's test suite can be run a hundred times a day.
//
// That claim is asserted by start, not by intent. Until T74 it was prose: every
// engine here was built from anacrolix's default config, which opens wildcard
// sockets and bootstraps two DHT servers to the public internet, because the
// three kill switches live in bindConfig and bindConfig only runs when a
// Network is supplied. start now sets Config.hermetic, which is what the
// sentence above describes.
const (
	payloadName = "Interstellar (2014)"
	payloadFile = "Interstellar (2014).mkv"
	payloadSize = 1 << 20
	pieceLength = 32 << 10
)

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// seed writes a payload, builds its metainfo, and starts an engine serving it.
func seed(t *testing.T) (*Engine, *metainfo.MetaInfo, metainfo.Hash) {
	t.Helper()

	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, payloadName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("payload directory: %v", err)
	}
	// Deterministic but incompressible-looking: identical bytes in every piece
	// would make a corrupted download indistinguishable from a good one.
	body := make([]byte, payloadSize)
	if _, err := rand.New(rand.NewSource(1)).Read(body); err != nil {
		t.Fatalf("payload bytes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, payloadFile), body, 0o644); err != nil {
		t.Fatalf("payload: %v", err)
	}

	info := metainfo.Info{PieceLength: pieceLength}
	if err := info.BuildFromFilePath(dir); err != nil {
		t.Fatalf("building the info dict: %v", err)
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatalf("bencoding the info dict: %v", err)
	}
	mi := &metainfo.MetaInfo{InfoBytes: infoBytes}

	engine := start(t, Config{DataDir: dataDir, Category: "curator", Log: quiet()})
	if _, err := engine.client.AddTorrent(mi); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	return engine, mi, mi.HashInfoBytes()
}

// start builds an engine confined to loopback and closes it with the test. The
// confinement is set here rather than at the call sites so that a test cannot
// reach the public internet by forgetting something.
func start(t *testing.T, cfg Config) *Engine {
	t.Helper()
	cfg.hermetic = true
	return startUnconfined(t, cfg)
}

// startUnconfined is start with the swarm left on. It is a second function
// rather than a parameter so that "this test downloads from strangers" is
// legible at the call site instead of being a false somebody has to look up.
// Its only two callers are TestLiveDownloadPeakRSS and TestLiveEngineOverTunnel,
// both of which need a real swarm to mean anything, and they need to stay the
// only two.
func startUnconfined(t *testing.T, cfg Config) *Engine {
	t.Helper()
	if cfg.Log == nil {
		cfg.Log = quiet()
	}
	engine, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return engine
}

// magnetFor is what a real indexer would hand dispatch: an info hash and a
// display name, and nothing about where to get it.
func magnetFor(t *testing.T, mi *metainfo.MetaInfo, ih metainfo.Hash) string {
	t.Helper()
	info, err := mi.UnmarshalInfo()
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	return mi.Magnet(&ih, &info).String()
}

// await polls the engine the way the poller does, because that is the only view
// of a download the rest of curator ever gets.
//
// On the deadline it dumps swarmState for every engine it was handed, and that
// is the whole reason others exists. T74's flake printed `State:downloading
// Progress:0` and nothing else, which is the least informative view this
// codebase can produce: it is compatible with four different faults, three of
// which are not "slow", and telling them apart cost a session. See swarmState.
func await(t *testing.T, e *Engine, hash string, want string, others ...*Engine) torrent.Torrent {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var last torrent.Torrent
	for time.Now().Before(deadline) {
		found, err := e.TorrentByHash(context.Background(), hash)
		if err != nil {
			t.Fatalf("TorrentByHash: %v", err)
		}
		if found != nil {
			last = *found
			if last.State == want {
				return last
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "torrent never reached %q; last seen %+v", want, last)
	fmt.Fprintf(&b, "\n  awaited: %s", swarmState(e, hash))
	for i, other := range others {
		fmt.Fprintf(&b, "\n  peer %d:  %s", i, swarmState(other, hash))
	}
	t.Fatalf("%s", b.String())
	return last
}

// swarmState is everything the client will say about a torrent that the
// poller's view throws away, and it exists because the poller's view cannot
// tell these four apart:
//
//   - ok=false — the piece completion database could not answer, which makes
//     ignoreForRequests true for that piece for ever (piece.go:299) and is
//     invisible in every counter above. It is worth reading even though the
//     accompanying anacrolix line does print: New's
//     `cc.Logger.FilterLevel(anacrolixQuiet)` is inert, because WithFilterLevel
//     does not set nonZero and Client.getLoggers then swaps the whole logger for
//     log.Default at Warning. Measured — a synthetic Error at that call site
//     reaches the test output. Which is why T74 is NOT this: the CI log for run
//     32094278975 carried no such line. Keep the field anyway; it is the one
//     reading that separates "not asking" from "asking and not served", and
//     under `go test -race` cgo is on, so that database is SQLite where a
//     CGO_ENABLED=0 build gets boltdb — these tests do not exercise the storage
//     the release ships.
//   - priority=None — DownloadAll never ran, so Engine.start's goroutine wedged
//     somewhere between GotInfo and its own end.
//   - active=0 with metadata chunks already read — the connection established,
//     moved the metadata and then died, and nothing re-adds it: openNewConns
//     pops from a peer set these trackerless magnets give no way to refill.
//   - all of the above healthy and read data climbing — genuinely slow, and
//     only then is the deadline the right thing to touch.
func swarmState(e *Engine, hash string) string {
	ih, err := parseHash(hash)
	if err != nil {
		return "unreadable hash: " + err.Error()
	}
	t, ok := e.client.Torrent(ih)
	if !ok {
		return "this client is not holding that torrent"
	}
	s := t.Stats()
	out := fmt.Sprintf(
		"peers active=%d seeders=%d halfopen=%d pending=%d | pieces complete=%d | "+
			"read data=%d metadata-chunks=%d wasted-chunks=%d bad-pieces=%d",
		s.ActivePeers, s.ConnectedSeeders, s.HalfOpenPeers, s.PendingPeers, s.PiecesComplete,
		s.BytesReadData.Int64(), s.MetadataChunksRead.Int64(),
		s.ChunksReadWasted.Int64(), s.PiecesDirtiedBad.Int64())
	if t.Info() == nil {
		return out + " | no info dict yet, so nothing has been asked for"
	}
	ps := t.PieceState(0)
	return fmt.Sprintf("%s | piece0 priority=%s ok=%v complete=%v | runs %v",
		out, priorityName(ps.Priority), ps.Ok, ps.Complete, t.PieceStateRuns())
}

// stallReport is swarmState plus everything the client itself will say, and it
// exists because swarmState cannot reach the one reading that separates the two
// remaining explanations for T74's signature.
//
// That signature — measured on a real swarm on unmodified main, 2026-08-18 — is
// metadata through the tunnel in 4.03 s, 3-4 peers connected, and then
// **0.00 MB of payload for five minutes**. swarmState answers "are we asking?"
// (piece0 priority, ok, complete) and "did anything arrive?" (read data,
// metadata chunks). If it comes back priority=normal ok=true active=3 read=0,
// every one of its four cases is excluded and it has nothing left to say.
//
// Client.WriteStatus has the rest, per peer, in one line each:
//
//	reqq: <ours>+<cancelled>/(<nominalMax>/<peerMax>):<theirs>/<localReqq>, flags: <us>:<conn>:<them>
//
// The last segment is the peer's state and `c` in it means **the peer is
// choking us** (peerconn.go:1632). Choked by every peer is a swarm answer and
// not a curator bug; `reqq` starting at 0 with nobody choking is the opposite,
// and means the requester never asked. Neither is visible from the poller, from
// swarmState, or from the per-tick line the live tests already print — which is
// why five minutes of those lines identified nothing.
//
// It is only ever called on a failure path. WriteStatus takes the client's read
// lock and prints every torrent, every peer and the DHT servers, which is far
// too much for a passing run and exactly enough for a stalled one.
func stallReport(e *Engine, hash string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", swarmState(e, hash))
	fmt.Fprintf(&b, "  --- client status, for the choked-vs-never-asked question swarmState cannot answer ---\n")
	var status strings.Builder
	e.client.WriteStatus(&status)
	for _, line := range strings.Split(strings.TrimRight(status.String(), "\n"), "\n") {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	return b.String()
}

// priorityName spells the priority out, because PiecePriority is a byte with no
// String method and `priority=0` is the single most important reading in the
// dump: none means nothing ever asked for this piece.
func priorityName(p anacrolix.PiecePriority) string {
	switch p {
	case anacrolix.PiecePriorityNone:
		return "none"
	case anacrolix.PiecePriorityNormal:
		return "normal"
	case anacrolix.PiecePriorityHigh:
		return "high"
	case anacrolix.PiecePriorityReadahead:
		return "readahead"
	case anacrolix.PiecePriorityNext:
		return "next"
	case anacrolix.PiecePriorityNow:
		return "now"
	}
	return fmt.Sprintf("%d", p)
}

// The stall report exists for one reading, so that reading is what gets pinned:
// the per-peer line, which is the only place "the peer is choking us" and "we
// never asked" are distinguishable. Those two are what is left after swarmState
// excludes its four cases, and they are the live question in T74.
//
// The pair here is two leechers and no seeder, which is the only hermetic shape
// that stays connected long enough to report on: measured, a real transfer from
// seed() completes in ~50 ms and anacrolix drops the connection as soon as both
// ends are done, so polling for a PeerConn on a working download finds nothing
// even at 20 ms. Two peers that both want the payload and neither has it hold
// active=2 indefinitely — connected, and moving nothing.
//
// What this pins is the report's SHAPE, not T74 itself. These two never get an
// info dict, where T74's signature has metadata in and payload at zero; the
// per-peer line is present either way, and it is the line that would silently
// stop answering the question if anacrolix v1.61.0's `reqq: … flags: …` format
// ever changed.
func TestAStallReportReachesThePeerLine(t *testing.T) {
	_, mi, ih := seed(t)
	hash := torrent.NormalizeHash(ih.HexString())
	magnet := magnetFor(t, mi, ih)

	a := start(t, Config{DataDir: t.TempDir(), Category: "curator"})
	b := start(t, Config{DataDir: t.TempDir(), Category: "curator"})
	for _, e := range []*Engine{a, b} {
		if err := e.AddMagnet(context.Background(), magnet, "curator"); err != nil {
			t.Fatalf("AddMagnet: %v", err)
		}
	}
	at, ok := a.client.Torrent(ih)
	if !ok {
		t.Fatal("the torrent is not in the client after AddMagnet")
	}
	at.AddClientPeer(b.client)

	// Wait for the connection rather than for a result: the report is about a
	// transfer that is not progressing, so what it needs present is a peer.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && len(at.PeerConns()) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if len(at.PeerConns()) == 0 {
		t.Fatal("no peer connection was established, so there is nothing to report on")
	}

	report := stallReport(a, hash)

	for _, want := range []struct{ substr, why string }{
		{"peers active=", "swarmState's half must survive: it is the four-way discriminator"},
		{"read data=", "the counter the live tests watch has to be in the same dump"},
		{"# Torrents:", "the client's own status never began"},
		{"reqq:", "how many pieces WE have outstanding — 0 with nobody choking means we never asked"},
		{"flags:", "the segment whose trailing c means the peer is choking us"},
	} {
		if !strings.Contains(report, want.substr) {
			t.Errorf("stall report is missing %q — %s. Full report:\n%s", want.substr, want.why, report)
		}
	}
}

// TestDownloadFromAPeer is the whole adapter in one pass: add a magnet, fetch
// the metadata from a peer, download the payload, and report it the way the
// poller and the importer will read it.
func TestDownloadFromAPeer(t *testing.T) {
	seeder, mi, ih := seed(t)
	hash := torrent.NormalizeHash(ih.HexString())

	leecher := start(t, Config{DataDir: t.TempDir(), Category: "curator"})

	// Before the add: empty, and not an error.
	before, err := leecher.Torrents(context.Background(), "curator")
	if err != nil {
		t.Fatalf("Torrents: %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("torrents = %+v, want none before an add", before)
	}

	if err := leecher.AddMagnet(context.Background(), magnetFor(t, mi, ih), "curator"); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}

	// A magnet knows an info hash and nothing else, so somebody has to be found
	// before there is anything to download. In the world this stands in for,
	// that is the DHT; here it is one peer, named.
	lt, ok := leecher.client.Torrent(ih)
	if !ok {
		t.Fatal("the torrent is not in the client after AddMagnet")
	}
	lt.AddClientPeer(seeder.client)

	got := await(t, leecher, hash, torrent.StateCompleted, seeder)

	if got.Hash != hash {
		t.Errorf("Hash = %q, want curator's upper-case %q", got.Hash, hash)
	}
	if got.Progress != 1 {
		t.Errorf("Progress = %v, want 1", got.Progress)
	}
	if got.Category != "curator" {
		t.Errorf("Category = %q, want curator", got.Category)
	}

	// ContentPath is the promise the importer relies on: a path curator can
	// open, being the folder for a multi-file torrent.
	if _, err := os.Stat(got.ContentPath); err != nil {
		t.Fatalf("ContentPath %q: %v", got.ContentPath, err)
	}
	downloaded, err := os.ReadFile(filepath.Join(got.ContentPath, payloadFile))
	if err != nil {
		t.Fatalf("reading the payload: %v", err)
	}
	original, err := os.ReadFile(filepath.Join(seeder.dataDir, payloadName, payloadFile))
	if err != nil {
		t.Fatalf("reading the original: %v", err)
	}
	if !bytes.Equal(downloaded, original) {
		t.Error("the downloaded payload differs from the seeded one")
	}

	// The info dict is persisted, because anacrolix does not persist it and a
	// magnet cannot resume offline without it (T36).
	if _, err := os.Stat(leecher.metainfoPath(ih)); err != nil {
		t.Errorf("metainfo not written: %v", err)
	}
}

// TestDownloadThroughANetwork runs the same transfer with the client stripped of
// every socket of its own — DisableTCP, DisableUTP and NoDHT — so that the only
// way a byte can move is through the Network it was handed. The Network here is
// plain loopback rather than a tunnel, which is exactly the point: the binding
// is what is under test, and it can be tested without a VPN.
func TestDownloadThroughANetwork(t *testing.T) {
	seeder, mi, ih := seed(t)
	hash := torrent.NormalizeHash(ih.HexString())

	leecher := start(t, Config{DataDir: t.TempDir(), Category: "curator", Network: loopback{}})
	if leecher.socket == nil {
		t.Fatal("no shared uTP socket was opened for the network")
	}

	if err := leecher.AddMagnet(context.Background(), magnetFor(t, mi, ih), "curator"); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}
	lt, ok := leecher.client.Torrent(ih)
	if !ok {
		t.Fatal("the torrent is not in the client after AddMagnet")
	}
	lt.AddClientPeer(seeder.client)

	got := await(t, leecher, hash, torrent.StateCompleted, seeder)
	if got.Progress != 1 {
		t.Errorf("Progress = %v, want 1", got.Progress)
	}
}

// loopback is a Network that is not a tunnel. It proves the wiring without
// proving anything about privacy, which is what internal/vpn's own tests are
// for.
type loopback struct{}

func (loopback) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

func (loopback) ListenPacket(_ context.Context, network, address string) (net.PacketConn, error) {
	return net.ListenPacket(network, address)
}

// TestAddMagnetRejectsTheUnusable — both of these would otherwise become a
// torrent that can never finish, reported as `queued` for ever.
func TestAddMagnetRejectsTheUnusable(t *testing.T) {
	e := start(t, Config{DataDir: t.TempDir(), Category: "curator"})

	for name, magnet := range map[string]string{
		"empty":       "   ",
		"no infohash": "magnet:?dn=Interstellar",
	} {
		if err := e.AddMagnet(context.Background(), magnet, "curator"); err == nil {
			t.Errorf("%s: AddMagnet accepted it", name)
		}
	}
	if held := e.client.Torrents(); len(held) != 0 {
		t.Errorf("client holds %d torrents after rejected adds", len(held))
	}
}

// TestAddMagnetRefusesAnotherCategory catches a wiring mistake rather than a
// user one: this engine has exactly one category.
func TestAddMagnetRefusesAnotherCategory(t *testing.T) {
	_, mi, ih := seed(t)
	e := start(t, Config{DataDir: t.TempDir(), Category: "curator"})

	if err := e.AddMagnet(context.Background(), magnetFor(t, mi, ih), "radarr"); err == nil {
		t.Fatal("AddMagnet accepted a category this engine does not have")
	}
}

func TestTorrentByHash(t *testing.T) {
	e := start(t, Config{DataDir: t.TempDir(), Category: "curator"})

	// Absence is a normal answer, not an error.
	found, err := e.TorrentByHash(context.Background(), "1111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("TorrentByHash: %v", err)
	}
	if found != nil {
		t.Errorf("TorrentByHash = %+v, want nil for a torrent we do not have", found)
	}

	// A malformed hash is a caller bug and is reported as one, because it would
	// otherwise look exactly like a torrent that was never added.
	for name, hash := range map[string]string{"empty": "  ", "too short": "abcd", "not hex": "zz"} {
		if _, err := e.TorrentByHash(context.Background(), hash); err == nil {
			t.Errorf("%s: TorrentByHash accepted %q", name, hash)
		}
	}
}

// TestTorrentsFiltersByCategory: the filter is honoured even though it cannot
// fail here, because the interface is shared with a backend where it is real.
func TestTorrentsFiltersByCategory(t *testing.T) {
	_, mi, ih := seed(t)
	e := start(t, Config{DataDir: t.TempDir(), Category: "curator"})
	if err := e.AddMagnet(context.Background(), magnetFor(t, mi, ih), ""); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}

	ours, err := e.Torrents(context.Background(), "curator")
	if err != nil {
		t.Fatalf("Torrents: %v", err)
	}
	if len(ours) != 1 {
		t.Fatalf("torrents in curator = %d, want 1", len(ours))
	}
	// No metadata yet and no peers to get it from, which is the honest answer:
	// queued, not downloading, and certainly not failed.
	if ours[0].State != torrent.StateQueued {
		t.Errorf("State = %q, want queued while the metadata is unknown", ours[0].State)
	}
	if ours[0].ContentPath != "" {
		t.Errorf("ContentPath = %q, want empty until the info dict names the payload", ours[0].ContentPath)
	}

	theirs, err := e.Torrents(context.Background(), "radarr")
	if err != nil {
		t.Fatalf("Torrents: %v", err)
	}
	if len(theirs) != 0 {
		t.Errorf("torrents in radarr = %d, want none — this engine holds only ours", len(theirs))
	}
}

// TestDeleteTorrentRefusesAnotherCategory is D19's guard, kept even though this
// engine cannot hold somebody else's torrent: the check is what makes that a
// property rather than an assumption.
func TestDeleteTorrentRefusesAnotherCategory(t *testing.T) {
	seeder, mi, ih := seed(t)
	hash := torrent.NormalizeHash(ih.HexString())

	e := start(t, Config{DataDir: t.TempDir(), Category: "curator"})
	if err := e.AddMagnet(context.Background(), magnetFor(t, mi, ih), "curator"); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}
	lt, _ := e.client.Torrent(ih)
	lt.AddClientPeer(seeder.client)
	got := await(t, e, hash, torrent.StateCompleted, seeder)

	err := e.DeleteTorrent(context.Background(), hash, "radarr", true)
	if !errors.Is(err, torrent.ErrWrongCategory) {
		t.Fatalf("err = %v, want ErrWrongCategory", err)
	}
	if _, err := os.Stat(got.ContentPath); err != nil {
		t.Errorf("the payload was removed for a refused delete: %v", err)
	}
}

func TestDeleteTorrentRemovesItsOwnFiles(t *testing.T) {
	seeder, mi, ih := seed(t)
	hash := torrent.NormalizeHash(ih.HexString())

	e := start(t, Config{DataDir: t.TempDir(), Category: "curator"})
	if err := e.AddMagnet(context.Background(), magnetFor(t, mi, ih), "curator"); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}
	lt, _ := e.client.Torrent(ih)
	lt.AddClientPeer(seeder.client)
	got := await(t, e, hash, torrent.StateCompleted, seeder)

	if err := e.DeleteTorrent(context.Background(), hash, "curator", true); err != nil {
		t.Fatalf("DeleteTorrent: %v", err)
	}
	if _, err := os.Stat(got.ContentPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("payload still present after a delete: %v", err)
	}
	if _, err := os.Stat(e.metainfoPath(ih)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("metainfo still present after a delete: %v", err)
	}
	if found, _ := e.TorrentByHash(context.Background(), hash); found != nil {
		t.Errorf("the torrent is still held after a delete: %+v", found)
	}

	// A second delete is success. Delete is retried after a partial failure,
	// and a retry that fails because the work was done is one nobody can finish.
	if err := e.DeleteTorrent(context.Background(), hash, "curator", true); err != nil {
		t.Errorf("second DeleteTorrent: %v, want success", err)
	}
}

func TestNewRequiresADataDirectory(t *testing.T) {
	if _, err := New(Config{Category: "curator", Log: quiet()}); err == nil {
		t.Fatal("New accepted an empty DataDir")
	}
}

// --- resume and stall (T36) --------------------------------------------------

// TestResumeUsesTheSavedMetainfo is the offline restart. The engine is closed
// and rebuilt over the same data directory with nothing to talk to: no seeder,
// no DHT, no tracker. Without the persisted info dict this would sit at `queued`
// for ever waiting for a stranger to send the metadata.
func TestResumeUsesTheSavedMetainfo(t *testing.T) {
	seeder, mi, ih := seed(t)
	hash := torrent.NormalizeHash(ih.HexString())
	magnet := magnetFor(t, mi, ih)
	dataDir := t.TempDir()

	first, err := New(Config{DataDir: dataDir, Category: "curator", Log: quiet(), hermetic: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := first.AddMagnet(context.Background(), magnet, "curator"); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}
	lt, _ := first.client.Torrent(ih)
	lt.AddClientPeer(seeder.client)
	before := await(t, first, hash, torrent.StateCompleted, seeder)
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Second boot, same directory, and the seeder is never introduced.
	second := start(t, Config{DataDir: dataDir, Category: "curator"})
	if err := second.AddMagnet(context.Background(), magnet, "curator"); err != nil {
		t.Fatalf("AddMagnet on resume: %v", err)
	}

	after := await(t, second, hash, torrent.StateCompleted, seeder)
	if after.ContentPath != before.ContentPath {
		t.Errorf("ContentPath moved across a restart: %q then %q", before.ContentPath, after.ContentPath)
	}
	st, _ := second.client.Torrent(ih)
	stats := st.Stats()
	if got := stats.BytesReadData.Int64(); got != 0 {
		t.Errorf("%d bytes were read from peers on resume, want 0", got)
	}
}

// TestResumeVerifiesWhatWasAlreadyOnDisk: pieces this process did not write are
// unverified, however confident the completion database is. A payload corrupted
// while curator was not running must come back INCOMPLETE rather than being
// hardlinked into the library and called a film.
func TestResumeVerifiesWhatWasAlreadyOnDisk(t *testing.T) {
	seeder, mi, ih := seed(t)
	hash := torrent.NormalizeHash(ih.HexString())
	magnet := magnetFor(t, mi, ih)
	dataDir := t.TempDir()

	first, err := New(Config{DataDir: dataDir, Category: "curator", Log: quiet(), hermetic: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := first.AddMagnet(context.Background(), magnet, "curator"); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}
	lt, _ := first.client.Torrent(ih)
	lt.AddClientPeer(seeder.client)
	done := await(t, first, hash, torrent.StateCompleted, seeder)
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Damage the payload behind the engine's back, exactly as a bad disk would.
	//
	// The chmod is not incidental: the engine finishes a file as 0444, which is
	// what retires docs/phase-4.md's "qBittorrent writes 0644, so the importer
	// needs no chmod". A hardlink carries the source's bits, so the library copy
	// is 0444 too — readable, which is all Jellyfin needs. This line is the
	// hermetic proof of that measurement; without it the write below fails with
	// permission denied.
	payload := filepath.Join(done.ContentPath, payloadFile)
	if info, err := os.Stat(payload); err != nil {
		t.Fatalf("stat: %v", err)
	} else if info.Mode().Perm() != 0o444 {
		t.Errorf("finished file is %v, want 0444 — phase-4.md's permissions note depends on this", info.Mode().Perm())
	}
	if err := os.Chmod(payload, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	f, err := os.OpenFile(payload, os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("opening the payload: %v", err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{0}, 64<<10), 0); err != nil {
		t.Fatalf("corrupting the payload: %v", err)
	}
	f.Close()

	second := start(t, Config{DataDir: dataDir, Category: "curator"})
	if err := second.AddMagnet(context.Background(), magnet, "curator"); err != nil {
		t.Fatalf("AddMagnet on resume: %v", err)
	}

	// It must not claim to be complete. With no peer to repair from it stays
	// incomplete, which is the honest answer.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		found, err := second.TorrentByHash(context.Background(), hash)
		if err != nil {
			t.Fatalf("TorrentByHash: %v", err)
		}
		if found != nil && found.State == torrent.StateCompleted {
			t.Fatal("a corrupted payload was reported complete; the verify did not happen")
		}
		if st, ok := second.client.Torrent(ih); ok && st.Info() != nil && st.BytesMissing() > 0 {
			return // the damage was found, which is the whole point
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the corrupted pieces were never noticed")
}

// TestStalledAfterNoProgress uses the engine's clock seam rather than waiting
// five real minutes. What it asserts is the transition in both directions: a
// torrent that gains nothing says so, and one that gains a byte stops saying it.
func TestStalledAfterNoProgress(t *testing.T) {
	_, mi, ih := seed(t)
	e := start(t, Config{DataDir: t.TempDir(), Category: "curator", StallAfter: time.Minute})

	// Added, with nobody introduced: no metadata, no peers, no progress. That
	// is the live bug this exists for — a magnet nobody is seeding looked
	// exactly like one that had not started yet.
	if err := e.AddMagnet(context.Background(), magnetFor(t, mi, ih), "curator"); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}

	at := time.Now()
	e.now = func() time.Time { return at }

	first, err := e.Torrents(context.Background(), "curator")
	if err != nil {
		t.Fatalf("Torrents: %v", err)
	}
	if first[0].State != torrent.StateQueued {
		t.Fatalf("State = %q on the first look, want queued — nothing has had time to stall", first[0].State)
	}
	if first[0].Reason != "" {
		t.Errorf("Reason = %q on a torrent that has not stalled, want empty", first[0].Reason)
	}

	at = at.Add(90 * time.Second)
	stalled, _ := e.Torrents(context.Background(), "curator")
	if stalled[0].State != torrent.StateStalled {
		t.Errorf("State = %q after 90s with no progress, want stalled", stalled[0].State)
	}
	// T55: the sentence rides on the torrent rather than only reaching the log.
	// Nobody was introduced to this client, so it is the no-peers case.
	if stalled[0].Reason == "" {
		t.Error("Reason is empty on a stalled torrent; the row has nothing to explain the badge with")
	}
	if !strings.Contains(stalled[0].Reason, "no peers") {
		t.Errorf("Reason = %q, want the no-peers sentence — nobody was introduced to this client", stalled[0].Reason)
	}

	// Every tick, not only the first. The log line is written once and the
	// reason is not: a row is rewritten on every poll, so a reason produced
	// only alongside the warning would be missing from every write after it.
	again, _ := e.Torrents(context.Background(), "curator")
	if again[0].Reason != stalled[0].Reason {
		t.Errorf("Reason = %q on the second stalled tick, want the same %q", again[0].Reason, stalled[0].Reason)
	}

	// A byte arriving clears it. Stalled is a description, not a verdict.
	e.mu.Lock()
	e.progress[stalled[0].Hash] = mark{bytes: -1, since: at}
	e.mu.Unlock()

	recovered, _ := e.Torrents(context.Background(), "curator")
	if recovered[0].State == torrent.StateStalled {
		t.Error("still stalled after progress moved; the state has to be able to recover")
	}
	if recovered[0].Reason != "" {
		t.Errorf("Reason = %q after progress moved, want empty — a reason must not outlive its state",
			recovered[0].Reason)
	}
}

// The reason is prose and the distinction it draws is the point of it: "nobody
// has this" and "somebody has it and will not send it" are different problems
// and only one of them is fixed by picking another release.
func TestStallReasonDistinguishesWhatIsWrong(t *testing.T) {
	cases := []struct {
		name       string
		peers      int
		noMetadata bool
	}{
		{"no peers, no metadata", 0, true},
		{"no peers, metadata in hand", 0, false},
		{"peers, no metadata", 3, true},
		{"peers, metadata, nothing arriving", 3, false},
	}

	seen := map[string]string{}
	for _, c := range cases {
		got := stallReason(c.peers, c.noMetadata)
		if got == "" {
			t.Errorf("%s: reason is empty", c.name)
			continue
		}
		// Rendered verbatim under a badge since T55, so it has to read as a
		// sentence rather than as a code the screen has to translate.
		if strings.ToUpper(got[:1]) == got[:1] && got[:1] != strings.ToLower(got[:1]) {
			t.Errorf("%s: reason %q starts capitalised; it is a clause, not a heading", c.name, got)
		}
		if before, ok := seen[got]; ok {
			t.Errorf("%s: reason %q is the same sentence as %q — the distinction is the whole point", c.name, got, before)
		}
		seen[got] = c.name
	}
}

// A completed torrent gains no more bytes for ever, which must not be mistaken
// for a stall.
func TestCompletedIsNeverStalled(t *testing.T) {
	seeder, mi, ih := seed(t)
	hash := torrent.NormalizeHash(ih.HexString())
	e := start(t, Config{DataDir: t.TempDir(), Category: "curator", StallAfter: time.Minute})

	if err := e.AddMagnet(context.Background(), magnetFor(t, mi, ih), "curator"); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}
	lt, _ := e.client.Torrent(ih)
	lt.AddClientPeer(seeder.client)
	await(t, e, hash, torrent.StateCompleted, seeder)

	at := time.Now().Add(time.Hour)
	e.now = func() time.Time { return at }

	found, err := e.TorrentByHash(context.Background(), hash)
	if err != nil {
		t.Fatalf("TorrentByHash: %v", err)
	}
	if found.State != torrent.StateCompleted {
		t.Errorf("State = %q an hour after completing, want completed", found.State)
	}
}
