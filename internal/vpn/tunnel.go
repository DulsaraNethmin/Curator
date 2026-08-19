package vpn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// DefaultIPCheckURL answers with `ip=…` on one line among a few others. It is
// Cloudflare's, which is about as durable an endpoint as the internet offers,
// and it is overridable because a durable endpoint is still somebody else's.
const DefaultIPCheckURL = "https://www.cloudflare.com/cdn-cgi/trace"

// ipCheckTimeout bounds one exit-address lookup. It runs at start-up and behind
// a settings probe, and a tunnel that is up but not passing traffic must fail
// this rather than hang the thing that asked.
const ipCheckTimeout = 20 * time.Second

// Tunnel is a WireGuard device with a network stack of its own.
type Tunnel struct {
	dev   *device.Device
	net   *netstack.Net
	log   *slog.Logger
	peer  string // the resolved endpoint, for Status
	built time.Time

	// addrs is what the tunnel answers to, from the config's Address line. It
	// is kept because netstack cannot invent one: see ListenPacket.
	addrs []netip.Addr
}

// New brings up the device and returns once it is configured — not once it has
// handshaken, which is a fact about the far end and is reported by Status.
//
// The endpoint's hostname is resolved on the HOST, deliberately and
// unavoidably: it is the address of the tunnel's own far end, so there is
// nothing to resolve it through yet. That is the one DNS query in this design
// that legitimately leaves outside, and it says only which VPN provider is in
// use — not what is being downloaded, which is the fact the tunnel exists to
// keep.
func New(cfg Config, log *slog.Logger) (*Tunnel, error) {
	if log == nil {
		log = slog.Default()
	}

	endpoint, err := resolveEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	uapi, err := cfg.uapi(endpoint)
	if err != nil {
		return nil, err
	}

	tun, tnet, err := netstack.CreateNetTUN(cfg.Addresses, cfg.DNS, cfg.MTU)
	if err != nil {
		return nil, fmt.Errorf("vpn: creating the netstack device: %w", err)
	}

	// wireguard-go's logger writes to stdout in its own format. Curator's log
	// is structured and goes through the buffer that GET /api/logs serves, so
	// this one is silenced rather than interleaved — and, more to the point, it
	// prints key material at its debug level.
	dev := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, "wireguard: "))
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return nil, fmt.Errorf("vpn: configuring the device: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("vpn: bringing the device up: %w", err)
	}

	log.Info("vpn tunnel up", "endpoint", endpoint, "mtu", cfg.MTU, "dns", len(cfg.DNS))
	return &Tunnel{dev: dev, net: tnet, log: log, peer: endpoint, built: time.Now(), addrs: cfg.Addresses}, nil
}

// DialContext dials through the tunnel. It satisfies the engine's Network.
//
// A hostname is resolved by the netstack's own resolver, over the tunnel,
// against the DNS servers the config named — which is the whole reason DNS is
// part of this package. A tracker hostname looked up on the host would announce
// what is being downloaded before a single encrypted byte moved.
func (t *Tunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return t.net.DialContext(ctx, network, address)
}

// LookupHost resolves a name inside the tunnel. It satisfies the engine's
// Network, and it is the resolver DialContext has been using implicitly since
// phase 6 — exposed now because the DHT bootstrap has names to resolve and no
// dial to hang them on.
//
// It answers nothing when the config named no DNS server: netstack has no
// resolver to ask, and there is deliberately no fallback to the host's. A
// hostname tracker already fails the same way through DialContext, so this adds
// no new failure mode — it declines to add the one failure mode that would
// matter, which is answering correctly by leaking the question.
func (t *Tunnel) LookupHost(ctx context.Context, host string) ([]string, error) {
	return t.net.LookupContextHost(ctx, host)
}

// ListenPacket opens a UDP socket inside the tunnel. uTP, DHT and UDP trackers
// all run over one of these.
//
// **The local address is always filled in, and that is not a nicety.** A
// net.UDPAddr with a nil IP — which is what ":0" and "" mean everywhere else in
// Go — leaves netstack unable to tell IPv4 from IPv6, and gvisor does not
// return an error for that: it PANICS, `invalid protocol number = 0`, four
// frames down inside the transport layer. Measured the first time this package
// met a real tunnel, on the exact call the engine makes at start-up. So an
// unspecified host becomes the address the tunnel answers to, which is what a
// client on a real interface would have bound to anyway.
func (t *Tunnel) ListenPacket(_ context.Context, network, address string) (net.PacketConn, error) {
	if !strings.HasPrefix(network, "udp") {
		return nil, fmt.Errorf("vpn: %s is not a packet network this tunnel carries", network)
	}

	laddr := &net.UDPAddr{}
	if address != "" {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("vpn: listen address %q: %w", address, err)
		}
		if host != "" {
			parsed, err := netip.ParseAddr(host)
			if err != nil {
				return nil, fmt.Errorf("vpn: listen address %q: %w", address, err)
			}
			laddr.IP = parsed.AsSlice()
		}
		if laddr.Port, err = strconv.Atoi(port); err != nil {
			return nil, fmt.Errorf("vpn: listen port %q: %w", port, err)
		}
	}

	if laddr.IP == nil {
		local, err := t.localAddr(network)
		if err != nil {
			return nil, err
		}
		laddr.IP = local.AsSlice()
	}
	return t.net.ListenUDP(laddr)
}

// localAddr picks the tunnel address to bind to, in the family the caller asked
// for. "udp" means either, and prefers IPv4: a v6 address on a tunnel whose
// peer has no v6 route fails in a way that looks exactly like a dead tunnel.
func (t *Tunnel) localAddr(network string) (netip.Addr, error) {
	want6 := network == "udp6"
	want4 := network == "udp4"

	var fallback netip.Addr
	for _, addr := range t.addrs {
		switch {
		case addr.Is4() && !want6:
			return addr, nil
		case addr.Is6() && !want4:
			if !fallback.IsValid() {
				fallback = addr
			}
		}
	}
	if fallback.IsValid() {
		return fallback, nil
	}
	return netip.Addr{}, fmt.Errorf(
		"vpn: the tunnel has no %s address of its own to listen on; check the config's Address line", network)
}

// HTTPClient is an http.Client whose transport dials through the tunnel. It is
// what the exit-address check uses, and what phase 8 would use if anything else
// ever needs to fetch something from inside.
func (t *Tunnel) HTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{DialContext: t.DialContext},
	}
}

// Close takes the device down. Every dial made through it fails afterwards,
// which is the intended behaviour and not a bug to work around.
func (t *Tunnel) Close() error {
	t.dev.Close()
	return nil
}

// Status is what the tunnel can say about itself without asking anybody.
type Status struct {
	Endpoint      string
	LastHandshake time.Time
	Received      int64
	Sent          int64
}

// Handshaken reports whether the far end has ever answered. A device can be up,
// configured and completely alone.
func (s Status) Handshaken() bool { return !s.LastHandshake.IsZero() }

// Status reads the device's own counters through the same IPC a `wg show`
// would use.
func (t *Tunnel) Status() (Status, error) {
	var b strings.Builder
	if err := t.dev.IpcGetOperation(&b); err != nil {
		return Status{}, fmt.Errorf("vpn: reading device status: %w", err)
	}

	status := Status{Endpoint: t.peer}
	for _, line := range strings.Split(b.String(), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}
		switch key {
		case "endpoint":
			status.Endpoint = value
		case "last_handshake_time_sec":
			if sec, err := strconv.ParseInt(value, 10, 64); err == nil && sec > 0 {
				status.LastHandshake = time.Unix(sec, 0)
			}
		case "rx_bytes":
			status.Received, _ = strconv.ParseInt(value, 10, 64)
		case "tx_bytes":
			status.Sent, _ = strconv.ParseInt(value, 10, 64)
		}
	}
	return status, nil
}

// ErrSameExit reports that the tunnel is carrying traffic out of the same
// address the host uses, which means it is not doing the one thing it is for.
var ErrSameExit = errors.New("the tunnel's exit address is the host's own")

// CheckExit fetches the exit address twice — once through the tunnel and once
// through the host — and refuses to agree with itself.
//
// Equal addresses do not mean "no VPN detected", they mean the tunnel is
// pointed at something that is not one, and a download dispatched through it
// would leave from the address the tunnel was installed to hide. That is worth
// a refusal rather than a warning.
//
// The tunnelled address is returned so a caller can log it at debug and show a
// boolean on screen; it is never logged here, because GET /api/logs is readable
// by anyone on the LAN (docs/decisions.md D18).
func (t *Tunnel) CheckExit(ctx context.Context, url string, host *http.Client) (tunnelled string, err error) {
	if strings.TrimSpace(url) == "" {
		url = DefaultIPCheckURL
	}
	if host == nil {
		host = &http.Client{Timeout: ipCheckTimeout}
	}

	tunnelled, err = exitAddress(ctx, t.HTTPClient(ipCheckTimeout), url)
	if err != nil {
		return "", fmt.Errorf("vpn: asking %s through the tunnel: %w", url, err)
	}
	direct, err := exitAddress(ctx, host, url)
	if err != nil {
		// The host's own answer is the comparison, not the subject. Failing to
		// get it is not evidence against the tunnel, so it is reported and the
		// tunnelled address still comes back.
		return tunnelled, fmt.Errorf("vpn: asking %s directly, to compare: %w", url, err)
	}
	if tunnelled == direct {
		return tunnelled, fmt.Errorf("vpn: %w (%s) — the tunnel is up but it is not changing where traffic leaves from", ErrSameExit, direct)
	}
	return tunnelled, nil
}

// exitAddress reads one address out of an IP-echo endpoint. Cloudflare's trace
// answers `ip=1.2.3.4` among other lines; a bare-IP service answers the address
// and nothing else. Both are handled, because the URL is configurable and
// somebody will point it at the other kind.
func exitAddress(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("answered %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	// Capped: this is a handful of lines, and an endpoint that answers with a
	// megabyte should not be read into memory because it said 200.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return "", err
	}

	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if value, found := strings.CutPrefix(line, "ip="); found {
			line = strings.TrimSpace(value)
		}
		if addr, err := netip.ParseAddr(line); err == nil {
			return addr.String(), nil
		}
	}
	return "", fmt.Errorf("no IP address in the reply from %s", url)
}

// resolveEndpoint turns `host:port` into `ip:port`.
//
// wireguard-go's default bind parses an endpoint with netip.ParseAddrPort and
// rejects a hostname outright, so a provider config naming one — most of them
// do — would fail to configure with a message about an invalid address. The
// lookup happens here, on the host, for the reason New's doc comment gives.
func resolveEndpoint(endpoint string) (string, error) {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", fmt.Errorf("vpn: endpoint %q: %w", endpoint, err)
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return net.JoinHostPort(addr.String(), port), nil
	}

	ips, err := net.LookupHost(host)
	if err != nil {
		return "", fmt.Errorf("vpn: resolving the endpoint %q: %w", host, err)
	}
	for _, ip := range ips {
		// IPv4 first: the default bind can carry either, but a v6 endpoint on a
		// host with no v6 route fails in a way that looks like a dead tunnel.
		if addr, err := netip.ParseAddr(ip); err == nil && addr.Is4() {
			return net.JoinHostPort(addr.String(), port), nil
		}
	}
	if len(ips) > 0 {
		return net.JoinHostPort(ips[0], port), nil
	}
	return "", fmt.Errorf("vpn: endpoint %q resolved to nothing", host)
}
