# T86 — a tracker's name is a leak

**Owns:** how a `udp://` tracker's hostname is resolved
**Found by** [T85](T85-the-capture-that-settles-it.md), the acceptance capture, and by nothing else
**Amends** [D47](../decisions.md#d47--every-torrent-network-operation-is-tunnel-bound-or-disabled),
which listed four leaks and can now count

## The leak

`bindConfig` hands anacrolix a tracker socket that belongs to the tunnel, and that was always true.
What no ClientConfig field can reach is what happens before that socket is used:

```go
// tracker/udp/conn-client.go:82
addr, err := net.ResolveUDPAddr(me.network, me.address)
return me.pc.WriteTo(p, addr)
```

`me.pc` is curator's tunnel socket. `net.ResolveUDPAddr` is the stdlib, on the **host** resolver. The
announce is encrypted; the question that produced its destination is not.

That is the leak `internal/vpn/tunnel.go` has named since phase 6 as the whole reason DNS belongs to
that package — *a name looked up on the host would announce what is being downloaded before a single
encrypted byte moved* — arriving from the one direction nobody looked.

## Measured, on the Pi

During a real 2.4 GB download, `tcpdump -i wlan0 port 53`. Eight of the twelve trackers in one magnet
were queried on `192.168.1.1`:

```
glotorrents.pw            open.stealth.si          p4p.arenabg.com
public.popcorn-tracker.org  tracker.coppersurfer.tk  tracker.dler.org
tracker.internetwarriors.net  tracker.torrent.eu.org
```

— while **99,563 of 101,043 packets** on the wire were WireGuard to the endpoint. Attributed by
`SIGSTOP`ping the process and watching them stop.

## Why the classification test did not catch it

`LookupTrackerIp` is the hook anacrolix provides for exactly this. In v1.61.0 it is **declared and
called nowhere**, and its own comment is a TODO to wire it back in.
`TestEveryEgressFieldOfTheClientConfigIsClassified` saw it, and T82 classified it `inert` — correctly,
about the field. What that missed is that the capability it was meant to replace still runs, through
the stdlib instead.

**That is worth knowing about the test.** It proves every field is accounted for. It does not prove
every egress path has a field.

## What was built

`resolveTrackers` rewrites `udp://host:port` to `udp://ip:port`, resolving through `Network.LookupHost`
— the method T82 added for the DHT bootstrap, earning its second caller. Both add paths go through
`TorrentSpec` now: the magnet's `tr=` params and a saved `.torrent`'s announce-list, which are the
only two ways a tracker name reaches the client. BEP 9 transfers the info dict, which carries no
trackers, so nothing arrives later from the swarm.

**Only `udp://`.** HTTP trackers are dialled through `cc.TrackerDialContext`, so they resolve inside
the tunnel already — and rewriting one would break TLS, which matches on the name. A `udp://` tracker
has no certificate and no Host header, so an address is as good as a name.

## Traps

- **An unresolvable tracker keeps its name**, and says so once. Dropping it leaves a torrent with
  fewer announce targets and no explanation; keeping it means anacrolix resolves it on the host, which
  is the leak. The trade is stated rather than silent.
- **`Engine.Binding` must not read the new `network` field.** The engine keeps its Network for this
  and nothing else — the binding answer has to come off the socket, or it is a claim rather than a
  reading.

## Verify

- `TestATrackerNameIsResolvedThroughTheTunnel`, `TestOnlyUdpTrackersAreRewritten`,
  `TestAnUnresolvableTrackerIsKeptAndSaidOutLoud`, `TestNoNetworkLeavesTrackersAlone`.
- **And by the instrument that found it**: three minutes of active downloading on the Pi, progress
  moving, and **not one tracker or DHT hostname** on the host resolver — only `www.cloudflare.com`,
  which is the exit check and is deliberately asked both ways.
