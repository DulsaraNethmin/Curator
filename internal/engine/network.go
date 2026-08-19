package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	dht "github.com/anacrolix/dht/v2"
	analog "github.com/anacrolix/log"
	anacrolix "github.com/anacrolix/torrent"
	"github.com/anacrolix/utp"
)

// anacrolixQuiet keeps the engine's own logger out of curator's log. Its
// records are a different format at a different granularity, and nothing in
// them is actionable by somebody running this app; what matters — added,
// completed, stalled — curator logs itself.
var anacrolixQuiet = analog.Critical

// listenTunnel opens the one packet socket everything will share.
//
// uTP and DHT run over the same PacketConn on purpose: that is what utp.Socket
// is for — it dispatches uTP packets to its own conns and hands everything else
// back through ReadFrom, which is exactly what a DHT server wants. One socket
// through the tunnel is also one thing to be sure of when checking that the
// client has no socket of its own.
func listenTunnel(ctx context.Context, n Network) (*utp.Socket, error) {
	pc, err := n.ListenPacket(ctx, "udp", ":0")
	if err != nil {
		return nil, fmt.Errorf("engine: listening on the tunnel: %w", err)
	}
	socket, err := utp.NewSocketFromPacketConn(pc)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("engine: uTP over the tunnel: %w", err)
	}
	return socket, nil
}

// bindConfig takes away the client's ability to open a socket.
//
// DisableTCP, DisableUTP and NoDHT are not preferences here, they are the kill
// switch: with all three set the client has no way to reach the network except
// through the dialers and listeners it is handed, and the spike measured
// exactly that — zero OS sockets opened by the client itself. A fallback to
// net.Dial anywhere, for anything, would quietly turn this from a guarantee
// into a hope.
//
// The three dial hooks cover what is left: webseeds over HTTP, HTTP trackers,
// and UDP trackers. Hostnames are passed through rather than resolved here, so
// that the tunnel's resolver answers them — a name looked up on the host is a
// leak that says what is being downloaded before a single encrypted byte moves.
//
// The DHT bootstrap is the fourth, and it is not a dial hook because it is not a
// dial: see dhtBootstrap.
//
// The address families are settled here too, and that is T56's fix rather than
// a tidy-up: see families and trackerListenPacket.
func bindConfig(ctx context.Context, cc *anacrolix.ClientConfig, n Network, log *slog.Logger) {
	cc.DisableTCP = true
	cc.DisableUTP = true
	cc.NoDHT = true

	cc.HTTPDialContext = n.DialContext
	cc.TrackerDialContext = n.DialContext
	cc.TrackerListenPacket = trackerListenPacket(ctx, n, log)
	cc.DhtStartingNodes = func(string) dht.StartingNodesGetter {
		return func() ([]dht.Addr, error) { return dhtBootstrap(ctx, n, log) }
	}

	has4, has6 := families(ctx, n)
	cc.DisableIPv4 = !has4
	cc.DisableIPv6 = !has6
	if !has6 {
		log.Info("the network has no IPv6 address, so IPv6 peers and udp6 tracker announces are off",
			"why", "a udp6 announce would ask it for a socket it has no address to open")
	}
	if !has4 {
		log.Info("the network has no IPv4 address, so IPv4 peers and udp4 tracker announces are off",
			"why", "a udp4 announce would ask it for a socket it has no address to open")
	}
}

// dhtBootstrap resolves the DHT's well-known entry points through the network
// instead of through the host, and it is the leak with the longest reach.
//
// NoDHT above turns off the client's OWN dht server; attachNetwork then builds
// one on the shared socket anyway, unconditionally, and that server reads
// cc.DhtStartingNodes. Left at anacrolix's default (config.go:261) it calls
// dht.GlobalBootstrapAddrs, which is ResolveHostPorts over a package-global
// dnscache.Resolver on the HOST (dht/v2 dns.go:26-30) — eight lookups saying
// this machine is running a BitTorrent client, on the resolver the tunnel exists
// to bypass, refreshed by a goroutine every five minutes for as long as the
// process lives. It is the same leak internal/vpn/tunnel.go already argues
// against for trackers; the DHT was simply never given the same treatment.
//
// Replacing the func is what stops it completely rather than mostly: dht's
// initDnsResolver is called only from ResolveHostPorts, so nothing here ever
// constructs that resolver or starts its refresher.
//
// Returning nil rather than disabling it: a real magnet carries no peer list, so
// with no bootstrap nodes a torrent has only its trackers. That is a slower cold
// start, not a broken one, which is why a failure here is logged and survived
// rather than fatal — and it is the honest behaviour for a tunnel whose config
// names no DNS server, where DialContext cannot resolve a tracker hostname
// either.
func dhtBootstrap(ctx context.Context, n Network, log *slog.Logger) ([]dht.Addr, error) {
	var addrs []dht.Addr
	for _, hostPort := range dht.DefaultGlobalBootstrapHostPorts {
		host, port, err := net.SplitHostPort(hostPort)
		if err != nil {
			// dht's own ResolveHostPorts panics here. These eight strings are a
			// compile-time constant of a dependency, so this is unreachable
			// today and is a skip rather than a panic if that ever changes.
			continue
		}
		found, err := n.LookupHost(ctx, host)
		if err != nil {
			continue
		}
		for _, ip := range found {
			udp, err := net.ResolveUDPAddr("udp", net.JoinHostPort(ip, port))
			if err != nil {
				continue
			}
			addrs = append(addrs, dht.NewAddr(udp))
		}
	}
	if len(addrs) == 0 {
		// One line, at the moment the DHT asks, naming the consequence rather
		// than the failure: somebody whose downloads are slow to find peers can
		// then see why without knowing what a bootstrap node is.
		log.Warn("no DHT bootstrap node resolved through the tunnel, so this torrent will find peers only through its trackers",
			"why", "the tunnel's resolver answered nothing; a wg config with no DNS line cannot resolve anything at all")
		return nil, errors.New("engine: no DHT bootstrap node resolved through the network")
	}
	return addrs, nil
}

// families asks the network which address families it can actually listen on.
//
// It is the same question anacrolix asks later, asked once here where the
// answer can be acted on. A `udp://` tracker is not one announcer but two:
// startScrapingTracker rewrites the URL into `udp4://` AND `udp6://`
// (torrent.go:2180) and starts an announcer for each, and the udp6 one is
// skipped only when DisableIPv6 is set. Left unset against a v4-only tunnel —
// which is what every NordLynx .conf is — that announcer asks for a udp6 socket
// the tunnel has no address for, and the error takes the process down: see
// trackerListenPacket.
//
// Telling the client the truth is also what the peer filters want
// (client.go:475-478, 593-596). A tunnel with no v6 address cannot reach a v6
// peer either, so offering it one is only a connection attempt that must fail.
//
// A network that can listen on neither family never reaches here: New opens the
// shared uTP socket first, and that fails with a real error a caller can read.
func families(ctx context.Context, n Network) (has4, has6 bool) {
	for _, probe := range []struct {
		network string
		found   *bool
	}{{"udp4", &has4}, {"udp6", &has6}} {
		pc, err := n.ListenPacket(ctx, probe.network, ":0")
		if err != nil {
			continue
		}
		*probe.found = true
		pc.Close()
	}
	return has4, has6
}

// trackerListenPacket is the one hook curator hands anacrolix whose error is
// not an error. **It must never return one.**
//
// anacrolix passes it straight to panicif.Err on a function that returns
// nothing (client-tracker-announcer.go:861, through tracker.NewClient →
// udp.NewConnClient), so an error here is not one failed announce, it is the
// whole process — at boot, where resume re-adds every unfinished row. Audited
// against the rest: HTTPDialContext and TrackerDialContext land in an
// http.Transport, the added dialer's error is one failed connection attempt and
// the added listener's is handled in acceptConnections. This hook is the only
// one of them that panics.
//
// So a network that cannot open the socket yields one that fails instead, and
// the announce dies where anacrolix can already cope with it dying. The lie
// stops at this boundary: internal/vpn keeps returning a real error, because
// its own callers — including New, above — want one.
//
// families should mean this never fires. It fires anyway when the tunnel goes
// down under a live client, and it is what stops an anacrolix upgrade that
// moves the DisableIPv6 check from being a crash.
func trackerListenPacket(ctx context.Context, n Network, log *slog.Logger) func(string, string) (net.PacketConn, error) {
	return func(network, addr string) (net.PacketConn, error) {
		pc, err := n.ListenPacket(ctx, network, addr)
		if err == nil {
			return pc, nil
		}
		log.Warn("a tracker asked for a socket the network would not open; that announce will fail",
			"network", network, "err", err)
		return deadPacketConn{err: err}, nil
	}
}

// deadPacketConn stands in for a socket that was never opened. Every operation
// fails with the reason it was not, which is what turns a panic into one
// tracker's bad afternoon.
type deadPacketConn struct{ err error }

func (d deadPacketConn) ReadFrom([]byte) (int, net.Addr, error) { return 0, nil, d.err }
func (d deadPacketConn) WriteTo([]byte, net.Addr) (int, error)  { return 0, d.err }
func (d deadPacketConn) Close() error                           { return nil }
func (d deadPacketConn) LocalAddr() net.Addr                    { return &net.UDPAddr{} }
func (d deadPacketConn) SetDeadline(time.Time) error            { return d.err }
func (d deadPacketConn) SetReadDeadline(time.Time) error        { return d.err }
func (d deadPacketConn) SetWriteDeadline(time.Time) error       { return d.err }

// attachNetwork gives the client back, one at a time, exactly the capabilities
// bindConfig took away — each of them pointed at the tunnel.
func (e *Engine) attachNetwork() error {
	e.client.AddDialer(utpDialer{socket: e.socket})
	e.client.AddListener(e.socket)

	dhtServer, err := e.client.NewAnacrolixDhtServer(e.socket)
	if err != nil {
		return fmt.Errorf("engine: DHT over the tunnel: %w", err)
	}
	e.client.AddDhtServer(anacrolix.AnacrolixDhtServerWrapper{Server: dhtServer})
	return nil
}

// utpDialer adapts utp.Socket to the client's dialer interface, which has its
// network locked in.
type utpDialer struct{ socket *utp.Socket }

func (d utpDialer) Dial(ctx context.Context, addr string) (net.Conn, error) {
	return d.socket.DialContext(ctx, "udp", addr)
}

func (d utpDialer) DialerNetwork() string { return "udp" }
