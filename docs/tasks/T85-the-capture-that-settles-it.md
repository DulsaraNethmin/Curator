# T85 — the capture that settles it

**Owns:** the one piece of evidence for [D47](../decisions.md#d47--every-torrent-network-operation-is-tunnel-bound-or-disabled) that does not come from curator
**Done** on the Pi, 2026-08-19, against a real 2.4 GB download over a real NordVPN tunnel
**Found** a fifth leak, which is [T86](T86-a-tracker-name-is-a-leak.md)

## Why it was separate

T82–T84 are verified by tests and by reading a dependency's source, and the VPN screen is curator's
own reading of curator. D27's promise is about packets, and nothing had looked at one. This is also
`docs/progress.md`'s "kill the tunnel mid-download", carried unrun since phase 6.

## How it was run, without touching anything

A second curator beside the pinned 0.2.0, on `PORT=8095`, its own database and downloads directory,
started from `~/t85` and never from `/opt/curator`. Jellyfin untouched, the pinned container untouched
and still serving 8090 throughout.

**The endpoint problem, and its answer.** Two WireGuard sessions on one *(server, client key)* pair
flap, and the Pi's config points at `187.15.101.96`. Measured rather than assumed: **every NordVPN
Singapore server publishes the same WireGuard public key**, so a second config needs only a different
`Endpoint` line. This one used `sg674` at `187.15.103.112`, from the Pi's own `wg0.conf` with one line
changed, and the two ran side by side for an hour with no flapping.

## What the box actually looks like, which the handoff did not say

`ip -o -4 addr` shows a **`nordlynx` interface at `100.108.251.229/10`** — a leftover from the retired
gluetun stack. It is not the default route, `nordvpn status` says *Disconnected*, and the default is
`wlan0 via 192.168.1.1`. **The host is NOT tunnelled**, which is what makes a capture on `wlan0`
mean anything. Had it been, everything would have looked like WireGuard and the capture would have
proved nothing.

## Measured

**The socket inventory, which is the decisive half.** `ss -tunap` filtered to the process, before and
during a live download:

| | before | during |
|---|---|---|
| the WireGuard device's own UDP socket (v4+v6) | ✓ | ✓ |
| `tcp LISTEN *:8095`, the web server | ✓ | ✓ |
| TCP to Cloudflare — the exit check, asked both ways on purpose | ✓ | ✓ |
| **anything else** | **none** | **none** |

70 MB of a 2.4 GB torrent with 1213 seeders arrived between those two readings and **not one new
socket appeared**. A process cannot send a packet without a socket.

**The wire.** 45 s of capture on `wlan0` during a fast stretch: **101,043 packets, of which 99,563
(98.5%) were WireGuard to `187.15.103.112:51820`.** The remainder was the pinned curator's own
tunnel, mDNS, and the other containers.

**And what it found.** See T86. DNS is the one thing a socket inventory cannot show, because the
resolver socket is opened and closed inside a stdlib call.

## Traps, paid for here

- **`not net 192.168.1.0/24` excludes everything.** The Pi *is* `192.168.1.26`, so that clause drops
  every packet it sends or receives. The first capture returned 232 packets for 70 MB of traffic and
  looked like a clean pass. The correct filter is
  `not (src net 192.168.1.0/24 and dst net 192.168.1.0/24)`.
- **A capture on `wlan0` cannot attribute a packet to a process.** Six containers share that
  interface. Attribution came from `ss -tunap` by PID, and from pausing the process with `SIGSTOP`
  and watching the queries stop.
- **DNS to a LAN resolver is LAN-to-LAN**, so the filter above hides it. `/etc/resolv.conf` on the Pi
  is `192.168.1.1`. It needs its own capture, and that is where the leak was.

## Not done here

**The tunnel was never torn down under a live download.** The sentinel was seen holding and releasing
downloads on a *real transient failure* — an `EOF` on the exit check, unstaged — which is the same
path, but it is not the same test. Killing the peer mid-download and watching the bytes stop in bytes
is still unrun.
