package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	anacrolix "github.com/anacrolix/torrent"
)

// udpTracker is what every real indexer magnet carries and what none of the
// fixtures did: one `udp://` announce URL. anacrolix turns it into two
// announcers, udp4 and udp6, which is the whole of T56.
const udpTracker = "&tr=udp%3A%2F%2Fnot-a-real-host.invalid%3A6969%2Fannounce"

// v4only is a NordLynx tunnel in the shape that matters: one IPv4 Address line
// and therefore nothing to bind a udp6 socket to. The error is internal/vpn's
// own, verbatim, because the point of the test is what happens to it.
//
// It records what it was asked for, so a test can say which announcers anacrolix
// started rather than only that nothing exploded.
type v4only struct {
	mu    sync.Mutex
	asked []string
}

func (n *v4only) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

func (n *v4only) ListenPacket(_ context.Context, network, address string) (net.PacketConn, error) {
	n.mu.Lock()
	n.asked = append(n.asked, network)
	n.mu.Unlock()

	if network == "udp6" {
		return nil, fmt.Errorf(
			"vpn: the tunnel has no %s address of its own to listen on; check the config's Address line", network)
	}
	// Bound to v4 whatever was asked for: a v4-only tunnel answers "udp" with
	// its one address, which is what vpn.localAddr does.
	return net.ListenPacket("udp4", address)
}

// since returns the networks asked for after a mark, so the start-up probe and
// the shared uTP socket do not have to be subtracted by hand.
func (n *v4only) since(mark int) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.asked[mark:])
}

func (n *v4only) mark() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.asked)
}

// TestAUDPTrackerDoesNotTakeTheProcessDown is the regression test for the crash
// that stopped phase 8, and it is written as a test that panics rather than one
// that fails, because that is what the bug does: the process dies at boot, in
// Resume, before anything can report it.
//
// The path is anacrolix's, entirely: startScrapingTracker splits `udp://` into
// `udp4://` and `udp6://`, the udp6 announcer asks this tunnel for a socket it
// has no address to open, and until T56 that error went to panicif.Err on a
// function with nothing to return it to.
//
// Phase 6's live download missed this for one reason worth keeping: its magnet
// (live_test.go's debianMagnet) carries a single `http://` tracker, and an HTTP
// announcer never asks for a packet socket at all.
func TestAUDPTrackerDoesNotTakeTheProcessDown(t *testing.T) {
	_, mi, ih := seed(t)
	network := &v4only{}
	e := start(t, Config{DataDir: t.TempDir(), Category: "curator", Network: network})

	mark := network.mark()
	if err := e.AddMagnet(context.Background(), magnetFor(t, mi, ih)+udpTracker, "curator"); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}

	// The tracker host is unresolvable on purpose. The panic happened before any
	// DNS — NewConnClient opens the socket first — so a test that needed a real
	// tracker would be testing the network instead of the bug.
	asked := network.since(mark)
	if slices.Contains(asked, "udp6") {
		t.Errorf("a udp6 announcer was started against a v4-only tunnel; asked for %v", asked)
	}
	if !slices.Contains(asked, "udp4") {
		t.Errorf("no udp4 announcer was started, so the fix has switched udp trackers off entirely; asked for %v", asked)
	}
}

// TestTheClientIsToldWhichFamiliesTheNetworkCarries pins the half of the fix
// that keeps udp trackers working: the answer is per-family, not "no udp
// trackers on a tunnel".
func TestTheClientIsToldWhichFamiliesTheNetworkCarries(t *testing.T) {
	for name, tc := range map[string]struct {
		network      Network
		wantDisable4 bool
		wantDisable6 bool
	}{
		"v4-only tunnel": {network: &v4only{}, wantDisable6: true},
		"both families":  {network: loopback{}},
	} {
		t.Run(name, func(t *testing.T) {
			cc := anacrolix.NewDefaultClientConfig()
			bindConfig(context.Background(), cc, tc.network, quiet())

			if cc.DisableIPv6 != tc.wantDisable6 {
				t.Errorf("DisableIPv6 = %v, want %v", cc.DisableIPv6, tc.wantDisable6)
			}
			if cc.DisableIPv4 != tc.wantDisable4 {
				t.Errorf("DisableIPv4 = %v, want %v", cc.DisableIPv4, tc.wantDisable4)
			}
		})
	}
}

// refuses is a network that will not open anything — a tunnel that has been
// closed under a running client, which is the case families cannot pre-empt.
type refuses struct{}

var errRefused = errors.New("vpn: the tunnel is closed")

func (refuses) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errRefused
}

func (refuses) ListenPacket(context.Context, string, string) (net.PacketConn, error) {
	return nil, errRefused
}

// TestTheTrackerHookNeverReturnsAnError is the guard, and it is the test that
// fails if somebody simplifies trackerListenPacket back into a one-line
// passthrough. anacrolix panics on an error from this hook; there is no caller
// between here and panicif.Err that could recover it.
func TestTheTrackerHookNeverReturnsAnError(t *testing.T) {
	hook := trackerListenPacket(context.Background(), refuses{}, quiet())

	pc, err := hook("udp6", ":0")
	if err != nil {
		t.Fatalf("the hook returned an error, which anacrolix turns into a panic: %v", err)
	}
	if pc == nil {
		t.Fatal("the hook returned no error and no conn; anacrolix dereferences it immediately")
	}

	// Failing, not pretending to work: an announce over this must die, it just
	// must die where anacrolix already copes with an announce dying.
	if _, _, err := pc.ReadFrom(make([]byte, 1)); !errors.Is(err, errRefused) {
		t.Errorf("ReadFrom err = %v, want the refusal", err)
	}
	if _, err := pc.WriteTo([]byte("x"), &net.UDPAddr{}); !errors.Is(err, errRefused) {
		t.Errorf("WriteTo err = %v, want the refusal", err)
	}
	if err := pc.SetDeadline(time.Now()); !errors.Is(err, errRefused) {
		t.Errorf("SetDeadline err = %v, want the refusal", err)
	}
	// Closing a socket that was never opened is not a failure, and anacrolix's
	// reader closes it the moment ReadFrom fails.
	if err := pc.Close(); err != nil {
		t.Errorf("Close err = %v, want nil", err)
	}
	if pc.LocalAddr() == nil {
		t.Error("LocalAddr is nil; anacrolix logs it before the first read")
	}
}
