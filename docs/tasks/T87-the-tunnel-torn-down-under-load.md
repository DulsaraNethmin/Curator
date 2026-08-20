# T87 — the tunnel torn down under a live download

**Owns:** the acceptance test [D27](../decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket)
has owed since phase 6
**Closes** `docs/progress.md`'s *"kill the tunnel mid-download and confirm traffic stops"*, carried
unrun through four phases and named again in [T85](T85-the-capture-that-settles-it.md)'s own
"Not done here"
**Finishes** the T82–T84 plan, which specified seventeen tests and shipped fourteen
**Fixes** one sentence a person actually reads

## Why it was still open

Every other assertion about the kill switch is a reading of a config field, of anacrolix's source, or
of curator reporting on curator. [T85](T85-the-capture-that-settles-it.md) settled where bytes *go*
with a packet capture. Neither answers the question D27 is actually written in: when the tunnel dies
underneath a transfer that is already moving, does anything still arrive?

## How you kill a peer, since the first two ways do not work

Both look like they should, which is why they are written down rather than deleted.

**Repointing the peer's endpoint at a black hole does nothing.** A peer's endpoint is where we
*send*; inbound packets are matched by their key index, not by source address. The far end went on
streaming and the device counter climbed by **39 MB in the thirty seconds after the repoint**.
Roaming would have undone it in any case.

**Removing the peer works, and changes the shape of the failure.** A device with no peer has never
handshaken, so the verdict is `waiting` rather than `stale` — and the 180 s the guarantee is actually
written in is never exercised.

**Swapping the device's private key is the one that reproduces reality.** `SetPrivateKey`
(`device.go:228`) keeps the peer, recomputes the static-static DH against a key the far end does not
know, and expires every current keypair, while `lastHandshakeNano` is written only on a successful
handshake (`timers.go:183`) and is left alone. The tunnel is then up, configured, handshaken in the
past and carrying nothing — exactly a VPN server that has stopped answering, which is the state
`HandshakeStale` exists to catch.

## Measured, on a real NordVPN Singapore endpoint

Against the Debian ISO every measurement since phase 6 has used.

| Reading | Result |
|---|---|
| bytes across 20 s **before** the kill | **61,770,060** |
| bytes across 20 s **after** the kill | **0** |
| held at | **3m0s** — *"the last handshake with 187.15.103.112:51820 was 3m2s ago, longer than the 3m0s a peer will accept a keypair for"* |
| released, and resumed | at **0.0623**, having been held at 0.0623 |

**The floor is a few KB rather than zero, and that is the point rather than a concession.** Both ends
go on trying to rekey a session that is gone, and WireGuard's own protocol traffic counts as
received — 320 B across 20 s on one run, 0 B on another. A floor of "nothing at all" fails on
protocol noise; a percentage would grow with the leak it exists to catch.

It runs on a **second config**: one `Endpoint` line changed, which is T85's finding that every
NordVPN Singapore server publishes the same WireGuard public key. The curator holding `:8090` keeps
its own session on `187.15.102.104` and never flaps.

Four minutes, because 180 s is `REJECT_AFTER_TIME` and shortening it would measure something else.

## The sentence that blamed cloudflare

`Check` already chose `StateStale` over `StateBlocked` on purpose — its own comment says the
handshake is the *cause* and blocked would name the *symptom*. The `Detail` did not follow that rule.
It carried `err.Error()` from the exit check, and `Verdict.Detail` is not a diagnostic: `Sentinel`
hands it to `Engine.Hold`, `holdReason` wraps it, and the Activity screen renders it verbatim under
the stalled row. So a VPN server that stopped answering produced, on screen:

> curator has stopped this download because it would not be protected: vpn: asking
> `https://www.cloudflare.com/cdn-cgi/trace` through the tunnel: Get
> "https://www.cloudflare.com/cdn-cgi/trace": context deadline exceeded

Three things wrong with one sentence: it names a third party as the culprit for a failure that had
nothing to do with them, it puts the check URL on a screen that needs no authentication
([D18](../decisions.md#d18--the-log-tail-is-readable-without-authentication-so-it-is-redacted-at-the-source)),
and it withholds the diagnosis the function had already reached two lines earlier. T83's claim is
that a held download names the tunnel instead of blaming the swarm; this named cloudflare instead,
which is the same failure wearing a different coat.

It now reports what `Cheap` reports for the same tunnel, through one shared `staleDetail` — not
tidiness, but the property the sentinel depends on: its transition detection compares states and its
announce carries the sentence, so two descriptions of one stale tunnel would surface as two
explanations for one failure depending on which half looked. The cause is untouched, so `errors.Is`
still answers and the log chain still carries the timeout that actually happened.

**Found by the live teardown**, which is the only thing that reads that string the way a user does —
every unit test in the package asserts on `State`.

## The three tests the plan specified and never shipped

**`TestATunnelLostMidDownloadFailsRatherThanFallsBack`** — the in-process half. The seam is the
`PacketConn`, not the `Conn`: `listenTunnel` wraps what `Network.ListenPacket` hands out in a
`utp.Socket` and every peer byte crosses that, while `DialContext` carries trackers and webseeds and
no payload at all — so a fake that closed dials would have taken nothing away from a download in
flight and passed while proving nothing. "Mid-download" is a fact rather than a race against
loopback: the fake freezes reads at a fifth of the payload and only then tells the test.

**`TestTheClientHoldsNoDialerOrListenerButTheTunnels`** — what the client was *asked for* and what it
*ended up holding* are two different claims, and the guarantee rests on the second. Exactly one
listener, and it is the tunnel's own socket; the advertised port read off that same listener; and
exactly one DHT server, because D47 moves the DHT onto the tunnel rather than switching it off, and
zero would mean cold-start peer discovery had been silently disabled.

**`TestTheEngineOpensNoSocketOutsideTheTunnel`'s runtime twin** — snapshots `/proc/self/fd` around
construction and first announce and requires every socket the process gained to be one the `Network`
handed out, **matched by inode**: an address can be reused and a port can be guessed at, an inode
names one socket. The automated form of T85's `ss -tunap` reading.

## Traps

- **A socket inventory cannot see the fifth leak, and that is why this does not retire T85.** A
  snapshot sees only sockets still open when it is taken; a tracker name resolved by
  `net.ResolveUDPAddr` opens a UDP socket and closes it inside one stdlib call. That whole class
  stays invisible to any inventory — which is exactly what [T86](T86-a-tracker-name-is-a-leak.md)
  found, and why looking at the wire is still a separate task.
- **The dialer half is not assertable.** anacrolix keeps `cl.dialers` unexported with no accessor.
  The comment says so rather than implying the name covers it; the teardown test is what would catch
  a fallback dialer.
- **The live test lives in `internal/vpn`** because killing a peer means reaching the WireGuard
  device and `dev` is that package's. Neither package imports the other, so a test there may import
  the engine; the engine still knows nothing about VPNs. Blocking the endpoint at the host firewall
  was the alternative — it needs root, cannot run unattended, and measures the same thing less
  precisely.

## Verify

- `TestAStaleTunnelIsReportedAsTheCauseAndNotTheSymptom` pins the sentence for CI, which never runs a
  live test: it fails on all three counts with the old line restored.
- The two engine tests were verified by **reintroducing the leak they are aimed at** — with
  `cc.DisableTCP = false` restored in `bindConfig`, the whole 1 MB arrived over TCP with the tunnel
  carrying not one byte, and the socket test reports `tcp 127.0.0.1:41467, tcp6 [::1]:41467` and
  names them as ways out of the machine. Eight consecutive runs under `-race`, no flake.
- The `/proc` test is Linux-gated on `runtime.GOOS` and was measured on `linux/arm64` in a
  `golang:1.25` container, since this laptop skips it: **sixteen executions, no failure**, and the
  full package green under `-race` there.
- The live one is deliberate and takes four minutes:
  ```bash
  VPN_CONFIG_FILE=~/wg0.conf go test -run TestLiveTheTunnelIsTornDownUnderADownload -v -timeout 20m ./internal/vpn
  ```
