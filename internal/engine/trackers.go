package engine

import (
	"context"
	"log/slog"
	"net"
	"net/url"
	"strings"
)

// resolveTrackers rewrites every `udp://host:port` announce URL to
// `udp://ip:port`, resolving the name through the network rather than the host.
//
// # Why this exists, and why no config field could do it
//
// bindConfig hands anacrolix a TrackerListenPacket that belongs to the tunnel,
// so a UDP tracker announce goes out INSIDE it — that part was always true and
// T33 measured it. What neither the config nor any test could see is that the
// tracker's NAME is resolved before that socket is used at all:
//
//	tracker/udp/conn-client.go:82
//	    addr, err := net.ResolveUDPAddr(me.network, me.address)
//	    return me.pc.WriteTo(p, addr)
//
// `me.pc` is curator's tunnel socket. `net.ResolveUDPAddr` is the stdlib, on
// the HOST resolver. So the packet is encrypted and the question that produced
// its destination is not — which is precisely the leak internal/vpn/tunnel.go
// argues against in the sentence this fix exists to make true: a name looked up
// on the host announces what is being downloaded before a single encrypted byte
// moves.
//
// Measured on the Pi with tcpdump during a real 2.4 GB download, which is the
// only reason it was found: eight of the twelve trackers in one magnet were
// queried on 192.168.1.1 while 98.5% of packets on the wire were WireGuard.
// No unit test could have caught it — the config is correct, the socket is
// correct, and the leak is a stdlib call inside a dependency.
//
// anacrolix has a hook for exactly this, `LookupTrackerIp`, and in v1.61.0 it
// is declared and called nowhere; its own comment is a TODO to wire it back in.
// So the interception has to happen before anacrolix sees the URL.
//
// # Only udp://
//
// HTTP and HTTPS trackers are dialled through cc.TrackerDialContext, which is
// the tunnel, and an http.Transport resolves the hostname inside its own
// DialContext — so those are already bound and are deliberately left alone.
// Rewriting them would also break TLS, which matches on the name.
//
// A udp:// tracker has no such problem: there is no certificate to match and
// no Host header, so an address is as good as a name.
func resolveTrackers(ctx context.Context, n Network, tiers [][]string, log *slog.Logger) [][]string {
	if n == nil {
		return tiers
	}

	// Resolved once per name per add, not once per URL: the same tracker
	// appears in several tiers often enough to matter, and each lookup is a
	// round trip through the tunnel.
	seen := map[string]string{}
	out := make([][]string, 0, len(tiers))

	for _, tier := range tiers {
		rewritten := make([]string, 0, len(tier))
		for _, raw := range tier {
			rewritten = append(rewritten, resolveTracker(ctx, n, raw, seen, log))
		}
		out = append(out, rewritten)
	}
	return out
}

// resolveTracker rewrites one announce URL, or returns it untouched when there
// is nothing to do or the lookup fails.
//
// A failed lookup keeps the NAME rather than dropping the tracker, and that is
// a deliberate trade rather than an oversight: the alternative is a torrent
// with fewer announce targets and no explanation. Keeping it means anacrolix
// resolves it on the host — which is the leak — so it is logged, once, with the
// tracker named. On a tunnel whose config carries no `DNS =` line every lookup
// fails, and that install is already unable to reach a hostname tracker at all.
func resolveTracker(ctx context.Context, n Network, raw string, seen map[string]string, log *slog.Logger) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.HasPrefix(parsed.Scheme, "udp") {
		return raw
	}

	host := parsed.Hostname()
	if host == "" || net.ParseIP(host) != nil {
		return raw // already an address; nothing to look up
	}

	address, ok := seen[host]
	if !ok {
		found, err := n.LookupHost(ctx, host)
		if err != nil || len(found) == 0 {
			log.Warn("a tracker's name could not be resolved through the tunnel, so it is being left "+
				"for the torrent client to resolve on the host",
				"tracker", host, "why", "that lookup is visible to whoever runs this machine's resolver")
			seen[host] = ""
			return raw
		}
		// The first answer, and IPv4 preferred for the reason vpn.localAddr
		// prefers it: a v6 address on a tunnel with no v6 route fails in a way
		// that looks exactly like a dead tracker.
		address = found[0]
		for _, candidate := range found {
			if ip := net.ParseIP(candidate); ip != nil && ip.To4() != nil {
				address = candidate
				break
			}
		}
		seen[host] = address
	}
	if address == "" {
		return raw
	}

	parsed.Host = net.JoinHostPort(address, parsed.Port())
	return parsed.String()
}
