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

	dht "github.com/anacrolix/dht/v2"
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

func (n *v4only) LookupHost(context.Context, string) ([]string, error) {
	return nil, errors.New("v4only resolves nothing")
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

// TestTheEngineOpensNoSocketOutsideTheTunnel is the config-level half of the
// kill switch, and it is three separate leaks in one assertion.
//
// Each of the three was set only under cfg.hermetic, or not at all, so the TEST
// configuration was hardened in ways the production one never was and no
// existing test could have failed. That is the shape of the bug, not a detail
// of it: reintroduce any one line and this goes red.
func TestTheEngineOpensNoSocketOutsideTheTunnel(t *testing.T) {
	network := &resolver{answer: []string{"192.0.2.1"}}
	cc := clientConfig(context.Background(), Config{
		DataDir: t.TempDir(), Category: "curator", Network: network, Log: quiet(),
	}, t.TempDir())

	if !cc.DisableWebtorrent {
		t.Error("DisableWebtorrent is false: a ws:// tracker in a magnet starts a websocket announcer " +
			"with a dialer of its own, and WebRTC data channels then move PAYLOAD outside the tunnel")
	}
	if !cc.NoDefaultPortForwarding {
		t.Error("NoDefaultPortForwarding is false: the client runs upnp.Discover, which is SSDP multicast " +
			"out of the host's real interfaces")
	}
	if !cc.DisableTCP || !cc.DisableUTP || !cc.NoDHT {
		t.Errorf("the client can open a socket of its own: DisableTCP=%v DisableUTP=%v NoDHT=%v",
			cc.DisableTCP, cc.DisableUTP, cc.NoDHT)
	}

	// Not "is it non-nil" — the default is non-nil. anacrolix's own default
	// resolves eight hostnames on the HOST, so the assertion has to be that this
	// is not that one, and the only way to tell is to call it and see who was
	// asked.
	if cc.DhtStartingNodes == nil {
		t.Fatal("DhtStartingNodes is nil")
	}
	if _, err := cc.DhtStartingNodes("udp")(); err != nil {
		t.Fatalf("DhtStartingNodes: %v", err)
	}
	if len(network.lookups()) == 0 {
		t.Error("the DHT bootstrap resolved nothing through the network, so it is still anacrolix's " +
			"default and still asking the host's resolver")
	}
}

// TestAHermeticEngineWithANetworkStillBootstrapsNowhere pins an ORDERING, which
// is the only thing holding it up and is invisible at both call sites.
//
// cfg.hermetic empties DhtStartingNodes and bindConfig points it at the
// network's resolver, and they are now two writes to one field. Run in the wrong
// order a hermetic test that has a Network — TestDownloadThroughANetwork, over
// plain loopback — bootstraps to router.bittorrent.com through the HOST and
// announces the fixed test info hash to the real DHT, which is exactly what the
// hermetic block has existed to prevent since T74. Nothing else fails when the
// two lines are swapped: the download still completes, and the leak is silent.
func TestAHermeticEngineWithANetworkStillBootstrapsNowhere(t *testing.T) {
	network := &resolver{answer: []string{"192.0.2.1"}}
	cc := clientConfig(context.Background(), Config{
		DataDir: t.TempDir(), Category: "curator", Network: network, Log: quiet(), hermetic: true,
	}, t.TempDir())

	addrs, err := cc.DhtStartingNodes("udp")()
	if err != nil || len(addrs) != 0 {
		t.Errorf("a hermetic engine offered %d bootstrap nodes (err %v), want none", len(addrs), err)
	}
	if got := network.lookups(); len(got) != 0 {
		t.Errorf("a hermetic engine resolved %v; hermetic must win over bindConfig, so it has to run after it", got)
	}
}

// TestTheDhtBootstrapResolvesThroughTheTunnel is the positive half: every one of
// dht's well-known entry points is asked of the network, and what comes back is
// what the network said rather than what the host thinks.
func TestTheDhtBootstrapResolvesThroughTheTunnel(t *testing.T) {
	network := &resolver{answer: []string{"192.0.2.7"}}

	addrs, err := dhtBootstrap(context.Background(), network, quiet())
	if err != nil {
		t.Fatalf("dhtBootstrap: %v", err)
	}

	asked := network.lookups()
	if len(asked) != len(dht.DefaultGlobalBootstrapHostPorts) {
		t.Errorf("asked the network for %d names, want %d: %v",
			len(asked), len(dht.DefaultGlobalBootstrapHostPorts), asked)
	}
	for _, hostPort := range dht.DefaultGlobalBootstrapHostPorts {
		host, _, err := net.SplitHostPort(hostPort)
		if err != nil {
			t.Fatalf("SplitHostPort %q: %v", hostPort, err)
		}
		if !slices.Contains(asked, host) {
			t.Errorf("%s was never resolved through the network", host)
		}
	}

	if len(addrs) != len(dht.DefaultGlobalBootstrapHostPorts) {
		t.Fatalf("got %d bootstrap addresses, want %d", len(addrs), len(dht.DefaultGlobalBootstrapHostPorts))
	}
	// The port survives the round trip through the resolver, which is the part a
	// naive rewrite loses: dht.libtorrent.org is on 25401 and two of them on
	// 42069, not the 6881 the other five use.
	ports := map[int]bool{}
	for _, addr := range addrs {
		if got := addr.IP().String(); got != "192.0.2.7" {
			t.Errorf("bootstrap address %s, want the one the network answered", got)
		}
		ports[int(addr.Port())] = true
	}
	for _, want := range []int{6881, 25401, 42069} {
		if !ports[want] {
			t.Errorf("no bootstrap node on port %d; the port was lost resolving the host", want)
		}
	}
}

// TestTheDhtBootstrapNeverFallsBackToTheHost is the negative half, and it is the
// one that matters: a tunnel whose config names no DNS server resolves nothing,
// and the correct behaviour is to return nothing rather than to answer correctly
// by asking the host.
func TestTheDhtBootstrapNeverFallsBackToTheHost(t *testing.T) {
	network := &resolver{fail: true}

	addrs, err := dhtBootstrap(context.Background(), network, quiet())
	if err == nil {
		t.Error("a network that resolves nothing produced no error, so something answered")
	}
	if len(addrs) != 0 {
		t.Errorf("got %d bootstrap addresses from a network that resolves nothing: %v", len(addrs), addrs)
	}
	if len(network.lookups()) != len(dht.DefaultGlobalBootstrapHostPorts) {
		t.Errorf("asked for %d names, want all %d tried before giving up",
			len(network.lookups()), len(dht.DefaultGlobalBootstrapHostPorts))
	}
}

// bound is a network that listens on one named address and remembers which
// socket it gave out, so a test can compare the engine's answer against the
// thing itself rather than against a shape.
type bound struct {
	host string

	mu       sync.Mutex
	handedTo []string
}

func (b *bound) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

func (b *bound) ListenPacket(_ context.Context, network, address string) (net.PacketConn, error) {
	if network == "udp6" {
		// v4-only, like every NordLynx config, so families tells the client the
		// truth rather than this fake quietly answering both.
		return nil, errors.New("bound has no udp6 address")
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		port = "0"
	}
	pc, err := net.ListenPacket("udp4", net.JoinHostPort(b.host, port))
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.handedTo = append(b.handedTo, pc.LocalAddr().String())
	b.mu.Unlock()
	return pc, nil
}

func (b *bound) LookupHost(context.Context, string) ([]string, error) {
	return nil, errors.New("bound resolves nothing")
}

func (b *bound) handedOut() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.handedTo)
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

func (refuses) LookupHost(context.Context, string) ([]string, error) {
	return nil, errRefused
}

// resolver is a Network that answers lookups and records them, which is the
// whole seam TestTheDhtBootstrapResolvesThroughTheTunnel needs. It refuses to
// dial or listen: a bootstrap must be resolvable without either.
type resolver struct {
	mu     sync.Mutex
	asked  []string
	answer []string
	fail   bool
}

func (r *resolver) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errRefused
}

func (r *resolver) ListenPacket(context.Context, string, string) (net.PacketConn, error) {
	return nil, errRefused
}

func (r *resolver) LookupHost(_ context.Context, host string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, host)
	if r.fail {
		return nil, errors.New("the tunnel's resolver answered nothing")
	}
	return r.answer, nil
}

func (r *resolver) lookups() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.asked)
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

// TestTheBindingIsReadOffTheEngineRatherThanClaimed is the fact the VPN page's
// "inside the tunnel" badge is drawn from.
//
// The engine does not keep the Network it was handed, so there is nothing here
// that could report what it was CONFIGURED with instead of what it got. Paired
// with vpn.Tunnel.Owns, two independent reads have to agree before anything on
// screen says the socket is inside the tunnel — neither of them a sentence
// somebody wrote next to the wiring.
func TestTheBindingIsReadOffTheEngineRatherThanClaimed(t *testing.T) {
	t.Run("with a network, the address is the one the network handed out", func(t *testing.T) {
		// Bound to an explicit address rather than the wildcard, which is what a
		// real tunnel does: vpn.Tunnel.ListenPacket always fills the local IP in,
		// because a nil one makes gvisor panic. That is also what makes the
		// answer comparable against Tunnel.Owns.
		network := &bound{host: "127.0.0.1"}
		e := start(t, Config{DataDir: t.TempDir(), Category: "curator", Network: network})

		addr := e.Binding()
		if addr == nil {
			t.Fatal("no binding for an engine that was given a network")
		}
		// Membership, not equality: bindConfig's family probe opens and closes a
		// socket of its own, so the network hands out more than one. The
		// property that matters is that the engine's is among them — it holds
		// nothing the network did not give it.
		if handed := network.handedOut(); !slices.Contains(handed, addr.String()) {
			t.Errorf("Binding = %s, which the network never handed out (it gave %v) — "+
				"the engine is reporting a socket from somewhere other than its Network", addr, handed)
		}
	})

	t.Run("with no network, there is no socket of our own to report", func(t *testing.T) {
		e := start(t, Config{DataDir: t.TempDir(), Category: "curator"})

		if addr := e.Binding(); addr != nil {
			t.Errorf("Binding = %s for an engine with no Network; it holds no socket this package opened", addr)
		}
	})
}

// TestATrackerNameIsResolvedThroughTheTunnel is the fifth leak, and the only
// one a packet capture found rather than a reading of a config field.
//
// bindConfig hands anacrolix a tracker socket that belongs to the tunnel, and
// that was always true. What it cannot reach is
// `tracker/udp/conn-client.go:82` — `net.ResolveUDPAddr` on the stdlib
// resolver, called to produce the destination that socket then writes to. The
// packet is encrypted; the question that produced its address is not.
func TestATrackerNameIsResolvedThroughTheTunnel(t *testing.T) {
	network := &resolver{answer: []string{"198.51.100.4"}}

	tiers := resolveTrackers(context.Background(), network, [][]string{
		{"udp://tracker.opentrackr.org:1337/announce"},
		{"udp://open.stealth.si:80/announce", "udp://tracker.dler.org:6969/announce"},
	}, quiet())

	want := [][]string{
		{"udp://198.51.100.4:1337/announce"},
		{"udp://198.51.100.4:80/announce", "udp://198.51.100.4:6969/announce"},
	}
	if fmt.Sprint(tiers) != fmt.Sprint(want) {
		t.Errorf("trackers = %v, want %v", tiers, want)
	}

	asked := network.lookups()
	if len(asked) != 3 {
		t.Errorf("resolved %d names through the network, want 3: %v", len(asked), asked)
	}
	for _, host := range []string{"tracker.opentrackr.org", "open.stealth.si", "tracker.dler.org"} {
		if !slices.Contains(asked, host) {
			t.Errorf("%s was not resolved through the network, so anacrolix will ask the host", host)
		}
	}
}

// TestOnlyUdpTrackersAreRewritten. HTTP trackers are dialled through
// cc.TrackerDialContext, which is the tunnel, and an http.Transport resolves
// inside its own DialContext — so they are already bound. Rewriting one would
// also break TLS, which matches on the name.
func TestOnlyUdpTrackersAreRewritten(t *testing.T) {
	network := &resolver{answer: []string{"198.51.100.4"}}

	tiers := resolveTrackers(context.Background(), network, [][]string{{
		"https://tracker.example.org:443/announce",
		"http://tracker.example.org/announce",
		"udp://tracker.example.org:1337/announce",
		"udp://198.51.100.9:1337/announce",
	}}, quiet())

	got := tiers[0]
	if got[0] != "https://tracker.example.org:443/announce" || got[1] != "http://tracker.example.org/announce" {
		t.Errorf("an HTTP tracker was rewritten: %v — that breaks TLS and was already tunnel-bound", got[:2])
	}
	if got[2] != "udp://198.51.100.4:1337/announce" {
		t.Errorf("the udp tracker was not rewritten: %s", got[2])
	}
	if got[3] != "udp://198.51.100.9:1337/announce" {
		t.Errorf("a tracker that was already an address was changed: %s", got[3])
	}
	if asked := network.lookups(); len(asked) != 1 {
		t.Errorf("resolved %v; only the udp hostname needs a lookup", asked)
	}
}

// TestAnUnresolvableTrackerIsKeptAndSaidOutLoud.
//
// Dropping it would leave a torrent with fewer announce targets and no
// explanation. Keeping it means anacrolix resolves it on the host, which is the
// leak — so the trade is made explicit rather than silent.
func TestAnUnresolvableTrackerIsKeptAndSaidOutLoud(t *testing.T) {
	network := &resolver{fail: true}

	tiers := resolveTrackers(context.Background(), network, [][]string{
		{"udp://tracker.opentrackr.org:1337/announce"},
	}, quiet())

	if tiers[0][0] != "udp://tracker.opentrackr.org:1337/announce" {
		t.Errorf("an unresolvable tracker was dropped or mangled: %s", tiers[0][0])
	}

	// And it is asked for ONCE per add, not once per URL: each lookup is a
	// round trip through the tunnel.
	network.mu.Lock()
	network.asked = nil
	network.mu.Unlock()
	resolveTrackers(context.Background(), network, [][]string{
		{"udp://a.example:1", "udp://a.example:2"},
		{"udp://a.example:3"},
	}, quiet())
	if got := network.lookups(); len(got) != 1 {
		t.Errorf("resolved the same name %d times in one add: %v", len(got), got)
	}
}

// TestNoNetworkLeavesTrackersAlone. An untunnelled engine (VPN_REQUIRED=false)
// has nothing to resolve through and must not lose its trackers to that.
func TestNoNetworkLeavesTrackersAlone(t *testing.T) {
	in := [][]string{{"udp://tracker.opentrackr.org:1337/announce"}}
	if got := resolveTrackers(context.Background(), nil, in, quiet()); fmt.Sprint(got) != fmt.Sprint(in) {
		t.Errorf("trackers = %v, want them untouched", got)
	}
}
