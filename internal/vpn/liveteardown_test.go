package vpn

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/engine"
	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// The same payload every measurement since phase 6 has used — 755.0 MB in
// 3020 × 256 KiB pieces — so the numbers here are comparable with T33's spike
// and with internal/engine's own live test. Only a few megabytes of it are
// wanted: this is not a download, it is something moving that can be stopped.
// window is how long a byte reading is taken over, on both sides of the kill,
// and noiseFloor is the most that may cross a tunnel whose peer is gone.
const (
	window     = 20 * time.Second
	noiseFloor = 4 << 10

	teardownInfoHash = "481b6e3617be4c88f96cb25e47c9d8272130071e"
	teardownMagnet   = "magnet:?xt=urn:btih:" + teardownInfoHash +
		"&dn=debian-13.6.0-amd64-netinst.iso" +
		"&tr=http%3A%2F%2Fbttracker.debian.org%3A6969%2Fannounce"
)

// How the peer is killed, and the first two ways did not work.
//
// Repointing the peer's endpoint at a black hole does NOT stop it, and that is
// worth writing down because it looks like it should. A peer's endpoint is
// where we SEND; inbound packets are matched by their key index and not by
// source address, so the far end went on streaming to our bind port and the
// device counter climbed by 39 MB in the thirty seconds after the repoint.
// Roaming would have undone it in any case.
//
// Removing the peer works but changes the shape of the failure: a device with
// no peer has never handshaken, so the verdict is `waiting` rather than
// `stale`, and the 180 s the guarantee is written in is never exercised.
//
// Swapping the device's PRIVATE KEY is the one that reproduces reality.
// SetPrivateKey (device.go:228) keeps the peer, recomputes the static-static
// DH against a key the far end does not know, and expires every current
// keypair — so nothing can be decrypted in either direction — while
// lastHandshakeNano is written only on a successful handshake (timers.go:183)
// and is left alone. The tunnel is therefore up, configured, handshaken in the
// past, and carrying nothing: exactly a VPN server that has stopped answering,
// and the state HandshakeStale exists to catch.
func killPeer(t *testing.T, tunnel *Tunnel) {
	t.Helper()
	var bogus [32]byte
	if _, err := rand.Read(bogus[:]); err != nil {
		t.Fatalf("generating a key the far end will not know: %v", err)
	}
	if err := tunnel.dev.IpcSet("private_key=" + hex.EncodeToString(bogus[:]) + "\n"); err != nil {
		t.Fatalf("swapping the device key: %v", err)
	}
}

// revivePeer puts the real identity back. The peer, its endpoint and its
// allowed IPs were never touched, so the next handshake simply succeeds.
func revivePeer(t *testing.T, tunnel *Tunnel, privateKey string) {
	t.Helper()
	if err := tunnel.dev.IpcSet("private_key=" + privateKey + "\n"); err != nil {
		t.Fatalf("restoring the device key: %v", err)
	}
}

// TestLiveTheTunnelIsTornDownUnderADownload is the acceptance test D27 has owed
// since phase 6 — docs/progress.md's "kill the tunnel mid-download and confirm
// traffic stops", carried unrun through four phases and named again in T85's
// "Not done here".
//
//	VPN_CONFIG_FILE=~/wg0.conf go test -run TestLiveTheTunnelIsTornDownUnderADownload -v -timeout 20m ./internal/vpn
//
// # Why it is here and not in internal/engine
//
// Killing the peer means reaching the WireGuard device, and `dev` is this
// package's. The alternative was blocking the endpoint at the host firewall,
// which needs root, cannot run unattended, and measures the same thing less
// precisely. Neither package imports the other, so a test in this one may
// import the engine; the engine still knows nothing about VPNs.
//
// # What it proves that the hermetic tests cannot
//
// internal/engine's TestATunnelLostMidDownloadFailsRatherThanFallsBack takes a
// FAKE network away and watches bytes stop, on loopback, on every commit. This
// takes a real peer away from a real WireGuard device carrying a real swarm's
// payload, and reads the byte counter off the device rather than out of
// curator's own accounting. Then it drives the whole chain the kill switch is
// actually made of — Checker, Sentinel, Hold, Release — instead of asserting
// against a verdict somebody constructed.
//
// # Why it takes minutes
//
// A dead peer is not visible to the cheap device read until the last handshake
// is older than HandshakeStale, because until then the device has nothing to
// report but an idle tunnel — which is a legitimate state and is exactly what
// TestAnIdleTunnelWithNoKeepaliveIsStillDispatchable pins. 180 s is
// REJECT_AFTER_TIME and it is the number the guarantee is written in, so the
// test waits it out rather than shortening it and measuring something else.
func TestLiveTheTunnelIsTornDownUnderADownload(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the live teardown in -short mode")
	}
	text := liveConfig(t)
	if text == "" {
		t.Skip("no VPN_CONFIG or VPN_CONFIG_FILE in the environment or ../../.env; skipping")
	}

	cfg, err := ParseConfig(text)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	privateKey, err := key(cfg.PrivateKey)
	if err != nil {
		t.Fatalf("the config's private key: %v", err)
	}

	tunnel, err := New(cfg, quiet())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Registered first so it runs LAST: cleanups are LIFO, and closing the
	// tunnel under a live engine kills the uTP read loop with a noise nobody
	// can place. main gets this right with defers; this has to model it.
	t.Cleanup(func() { _ = tunnel.Close() })

	live := awaitHandshake(t, tunnel, 30*time.Second)
	t.Logf("handshaken with %s", live.Endpoint)

	// A real engine on the real tunnel. Not engine's own test helper: that one
	// is hermetic, and a hermetic engine has no swarm to get bytes from.
	e, err := engine.New(engine.Config{
		DataDir: t.TempDir(), Category: "curator", Network: tunnel, Log: quiet(),
	})
	if err != nil {
		t.Fatalf("engine.New over the tunnel: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	// The chain as cmd/curator builds it, because a paraphrase of it would be a
	// second implementation and this is the one test that can catch the two
	// disagreeing. active is always true here: something IS downloading, which
	// is the condition the sentinel's expensive check is gated on.
	checker := NewChecker(tunnel, DefaultIPCheckURL, nil, 0, quiet())
	sentinel := NewSentinel(checker, func(context.Context) bool { return true }, quiet())
	sentinel.Subscribe(func(v Verdict) {
		if v.OK() {
			_ = e.Release()
			return
		}
		_ = e.Hold(v.Detail)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sentinel.Run(ctx)

	if err := e.AddMagnet(ctx, teardownMagnet, "curator"); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}
	hash := torrent.NormalizeHash(teardownInfoHash)

	// Moving, in bytes, before anything is taken away. The tunnel's own device
	// counter is the measurement: it is read out of WireGuard rather than out
	// of curator, so nothing curator believes about itself can make it climb.
	moving := awaitPayload(t, tunnel, e, hash, 4*time.Minute)

	// What this tunnel is carrying, over the same window the reading after the
	// kill will use, so the two are compared against each other rather than
	// against a rate picked in advance.
	opening := received(t, tunnel)
	time.Sleep(window)
	carrying := received(t, tunnel) - opening
	t.Logf("download live: %d B across %s, progress %.6f", carrying, window, moving.progress)
	if carrying < noiseFloor {
		t.Fatalf("the tunnel carried %d B in %s before anything was taken away, which is not a download "+
			"to interrupt; the swarm is the suspect, not the code under test", carrying, window)
	}

	// ---- the peer dies -----------------------------------------------------

	killPeer(t, tunnel)
	t.Logf("device key swapped — the peer is still configured on %s and can no longer be talked to",
		live.Endpoint)

	// Settle first: packets already decrypted when the keys went are not a
	// leak, and a reading taken in that instant would be of the wreckage.
	stopped := settle(t, tunnel, 5*time.Second, 30*time.Second)
	frozen := progressOf(t, e, hash)
	leaked := watch(t, tunnel, window) - stopped

	// Not "exactly zero", and the difference is the point rather than a
	// concession. SetPrivateKey expires every keypair (device.go:277), so no
	// transport packet can be decrypted and no payload can move — but both ends
	// go on trying to rekey a session that is gone, and WireGuard's own
	// protocol traffic is still counted as received. Measured at 320 B across
	// 20 s on 2026-08-20, against megabytes in the same window before the kill.
	// A floor that says "nothing at all" would be a test that fails on protocol
	// noise; one that allowed a percentage would grow with the leak it exists
	// to catch. A few KB is neither.
	if leaked > noiseFloor {
		t.Errorf("%d B crossed the tunnel in %s after its peer was taken away, against %d B in the same "+
			"window before — the far end is unreachable and every keypair is expired, so this is payload "+
			"arriving by some route the kill switch does not cover", leaked, window, carrying)
	}
	t.Logf("bytes stopped: %d B in %s, from %d B — a %.0fx collapse",
		leaked, window, carrying, float64(carrying)/float64(max(leaked, 1)))

	// And the download itself stopped, which is the claim the byte counter is
	// evidence for rather than a restatement of it.
	if moved := progressOf(t, e, hash); moved != frozen {
		t.Errorf("the download advanced from %.6f to %.6f with no tunnel under it", frozen, moved)
	}

	// ---- and curator notices, on its own, without being asked --------------

	// HandshakeStale plus the sentinel's tick plus one exit check that has to
	// time out through a tunnel with no far end. Nothing here calls Check: the
	// point is that the watchdog gets there by itself.
	reason := awaitHold(t, e, HandshakeStale+SentinelInterval+ipCheckTimeout+30*time.Second)
	t.Logf("downloads held after %s, reason: %s", HandshakeStale, reason)

	if !strings.Contains(reason, live.Endpoint) {
		t.Errorf("the hold reason does not name the tunnel's endpoint %s: %q", live.Endpoint, reason)
	}
	// The Activity screen's half of it: a held torrent says why instead of
	// blaming the swarm, which is the failure T78 removed and this could
	// reintroduce from a new direction.
	view, err := e.TorrentByHash(ctx, hash)
	if err != nil {
		t.Fatalf("TorrentByHash: %v", err)
	}
	if view == nil {
		t.Fatal("the torrent vanished from the engine while it was held")
	}
	if !strings.Contains(view.Reason, "would not be protected") {
		t.Errorf("a held torrent reports Reason %q, which does not say curator stopped it", view.Reason)
	}
	if view.Progress < frozen {
		t.Errorf("progress went backwards while held: %.6f, was %.6f", view.Progress, frozen)
	}

	// ---- the peer comes back ----------------------------------------------

	revivePeer(t, tunnel, privateKey)
	t.Logf("device key restored; the peer on %s is reachable again", live.Endpoint)

	recovered := awaitRelease(t, e, tunnel, SentinelInterval+ipCheckTimeout+90*time.Second)
	t.Logf("downloads released; device counter at %d", recovered)

	if dead := stopped + leaked; recovered <= dead {
		t.Errorf("the tunnel carried nothing after the peer came back: %d, was %d", recovered, dead)
	}

	// And it picked up rather than started again, which is the whole reason
	// Hold is not Drop: a tunnel blip must not cost a half-finished download.
	after, err := e.TorrentByHash(ctx, hash)
	if err != nil {
		t.Fatalf("TorrentByHash after the release: %v", err)
	}
	if after == nil {
		t.Fatal("the torrent was dropped rather than held")
	}
	if after.Progress < frozen {
		t.Errorf("progress restarted rather than resumed: %.4f after the release, %.4f while held",
			after.Progress, frozen)
	}
	t.Logf("resumed at %.4f, having been held at %.4f", after.Progress, frozen)
}

type reading struct {
	received int64
	progress float64
}

func awaitHandshake(t *testing.T, tunnel *Tunnel, within time.Duration) Status {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		status, err := tunnel.Status()
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status.Handshaken() {
			return status
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("no handshake within %s; this test needs a tunnel that works before it can break one", within)
	return Status{}
}

// awaitPayload waits for the swarm, not for the tunnel. Bytes on the device
// alone are not enough — a handshake and a tracker announce are bytes — so it
// also requires payload on disk, which is what "mid-download" has to mean.
func awaitPayload(t *testing.T, tunnel *Tunnel, e *engine.Engine, hash string, within time.Duration) reading {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		view, err := e.TorrentByHash(context.Background(), hash)
		if err != nil {
			t.Fatalf("TorrentByHash: %v", err)
		}
		if view != nil && view.Progress > 0 {
			status, err := tunnel.Status()
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			return reading{received: status.Received, progress: view.Progress}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("no payload arrived within %s, so there was never a download to interrupt; "+
		"the swarm or the tunnel is the suspect here, not the code under test", within)
	return reading{}
}

// settle returns the device's received counter once it has stopped climbing for
// quiet, which is how long a reading has to hold still before it counts as
// stopped rather than slow.
func settle(t *testing.T, tunnel *Tunnel, quiet, within time.Duration) int64 {
	t.Helper()
	deadline := time.Now().Add(within)
	last := received(t, tunnel)
	still := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		if got := received(t, tunnel); got != last {
			last, still = got, time.Now()
			continue
		}
		if time.Since(still) >= quiet {
			return last
		}
	}
	t.Fatalf("the tunnel was still carrying bytes %s after its peer was taken away; last %d B", within, last)
	return last
}

// watch asserts nothing by itself: it returns the counter after a window, so
// the caller can say what a change means.
func watch(t *testing.T, tunnel *Tunnel, window time.Duration) int64 {
	t.Helper()
	time.Sleep(window)
	return received(t, tunnel)
}

func received(t *testing.T, tunnel *Tunnel) int64 {
	t.Helper()
	status, err := tunnel.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	return status.Received
}

func progressOf(t *testing.T, e *engine.Engine, hash string) float64 {
	t.Helper()
	view, err := e.TorrentByHash(context.Background(), hash)
	if err != nil {
		t.Fatalf("TorrentByHash: %v", err)
	}
	if view == nil {
		t.Fatal("the torrent is no longer in the engine")
	}
	return view.Progress
}

func awaitHold(t *testing.T, e *engine.Engine, within time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if held, reason := e.Held(); held {
			return reason
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("downloads were never held, %s after the peer was taken away — the watchdog did not "+
		"get there on its own, which is the whole reason it exists", within)
	return ""
}

func awaitRelease(t *testing.T, e *engine.Engine, tunnel *Tunnel, within time.Duration) int64 {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if held, _ := e.Held(); !held {
			// Released is not the same as carrying again: give the swarm a
			// moment to start sending before reading the counter.
			time.Sleep(10 * time.Second)
			return received(t, tunnel)
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("downloads were still held %s after the peer came back", within)
	return 0
}
