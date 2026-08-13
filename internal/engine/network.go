package engine

import (
	"context"
	"fmt"
	"net"

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
func bindConfig(ctx context.Context, cc *anacrolix.ClientConfig, n Network) {
	cc.DisableTCP = true
	cc.DisableUTP = true
	cc.NoDHT = true

	cc.HTTPDialContext = n.DialContext
	cc.TrackerDialContext = n.DialContext
	cc.TrackerListenPacket = func(network, addr string) (net.PacketConn, error) {
		return n.ListenPacket(ctx, network, addr)
	}
}

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
