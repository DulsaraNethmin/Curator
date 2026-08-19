# T82 — a kill switch that can be proved

**Owns:** what the torrent engine is allowed to open, and what a handshake's age means
**Settled by:** [D47](../decisions.md#d47--every-torrent-network-operation-is-tunnel-bound-or-disabled)
**Asked for** by an audit of the real byte paths against
[D27](../decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket)'s claim, rather than
against its design comment

## Why now

D27 says every byte goes through a dialer the tunnel handed out, and T33 measured zero OS sockets.
Both are true. Neither is a statement about the paths anacrolix opens without asking a dialer at all,
and nobody had ever gone looking for those.

## The four leaks, all verified against `anacrolix/torrent@v1.61.0`

**WebTorrent, the only one that carries payload.** `DisableWebtorrent` appeared nowhere in this
repository. `torrent.go:2203` gates it; past that gate `startWebsocketAnnouncer` builds a
`websocket.Dialer` of its own (`webtorrent/tracker-client.go:35`) and WebRTC data channels move
pieces. A `ws://` or `wss://` tracker in a magnet is the whole trigger, and 1337x, YTS and TPB hand
out `udp://` — so it has probably never fired. That is not the promise.

**The DHT bootstrap asked the host's resolver.** Production kept anacrolix's default
`DhtStartingNodes` (`config.go:261`) → `dht.GlobalBootstrapAddrs` → `ResolveHostPorts` → a
package-global `dnscache.Resolver` (`dht/v2@v2.23.0/dns.go:26-30`): eight lookups naming this machine
as a BitTorrent client, on the resolver the tunnel exists to bypass, kept warm by a goroutine every
five minutes.

**UPnP announced on the LAN.** `NoDefaultPortForwarding` was set only under `cfg.hermetic`.
Production ran `go cl.forwardPort()` (`client.go:414`) → `upnp.Discover` → SSDP multicast, for a
mapping that would forward a router port to a listener inside a netstack no LAN packet can reach.

**And the worst one, found last.** `startEngine` left `network` nil when `VPN_REQUIRED` was true with
no tunnel, and called `engine.New` anyway. `bindConfig` runs only when there is a Network, so
`DisableTCP`, `DisableUTP` and `NoDHT` were all false: host sockets and a **DHT node on the household
connection**, on every boot, with no magnet dispatched and the switch reading on. It needs no unusual
configuration — it is the state every fresh install starts in.

**One cause: `cfg.hermetic` hardened the TEST config in ways production never got.** No existing test
could have failed, because every test was already behind the fix.

## What was built

- `DisableWebtorrent` and `NoDefaultPortForwarding` for every engine, lifted out of the hermetic
  block. `DisableWebseeds` deliberately still unset — webseeds go through `HTTPDialContext`, which is
  the tunnel.
- `engine.Network` gains `LookupHost`, and `bindConfig` a `DhtStartingNodes` that resolves the eight
  well-known entry points through it. Replacing the func is what stops `initDnsResolver` ever running,
  so the host resolver and its refresher never start at all.
- `startEngine` returns **no engine** when enforcement is on and there is no tunnel.
- `HandshakeStale` = 180 s (REJECT_AFTER_TIME), `Status.Fresh`/`Age`/`Since`/`Keepalive`,
  `Tunnel.Owns`, `Engine.Binding`.
- The ten-minute blind window closed: `Tunnelled` reads the device **before** consulting its cache.

## Traps

- **The hermetic block and `bindConfig` both write `DhtStartingNodes`.** Run in the old order a
  hermetic test that HAS a Network — `TestDownloadThroughANetwork`, over loopback — would bootstrap to
  `router.bittorrent.com` through the host and announce the fixed test info hash. Nothing else fails.
  hermetic runs last now, and `TestAHermeticEngineWithANetworkStillBootstrapsNowhere` is there because
  the two lines look independent and are not.
- **A nil `*engine.Engine` in a `download.TorrentClient` is a non-nil interface.** Every
  `torrents != nil` guard in `main.go` would have passed and then dereferenced it, and
  `defer engine.Close()` would have panicked at shutdown. The test asserts the interface conversion,
  not just the pointer.
- **`WebTransport` and `MetainfoSourcesClient` must stay nil.** `client.go:284-296` uses them
  *instead of* the transport carrying `HTTPDialContext`, so setting either voids the webseed binding
  without touching `HTTPDialContext`.
- **`Fresh` must never refuse a dispatch.** An idle tunnel with no `PersistentKeepalive` is
  legitimately stale and the first dial rekeys it. `Handshaken` stays the refusal.

## Verify

- `TestTheEngineOpensNoSocketOutsideTheTunnel`, `TestTheDhtBootstrapResolvesThroughTheTunnel`,
  `TestTheDhtBootstrapNeverFallsBackToTheHost`,
  `TestAHermeticEngineWithANetworkStillBootstrapsNowhere` — each reintroduced and seen to fail.
- `TestEveryEgressFieldOfTheClientConfigIsClassified` — the one that survives an upgrade. Verified in
  both directions: a field removed from the map, and a field named that anacrolix no longer has.
- `TestNoTunnelAndVpnRequiredBuildsNoEngineAtAll` and
  `TestAnEngineIsStillBuiltWithoutATunnelWhenEnforcementIsOff`.
- `TestAStaleHandshakeInvalidatesACachedPass`, `TestAnIdleTunnelWithNoKeepaliveIsStillDispatchable`,
  `TestTheTunnelKnowsWhichAddressesAreItsOwn`, `TestTheBindingIsReadOffTheEngineRatherThanClaimed`.

## Not done here

**The `tcpdump` acceptance capture on the Pi is unrun.** Everything above is a test or a reading of a
dependency's source; none of it is a packet capture, and D27's promise is about packets. It is
[T85](T85-the-capture-that-settles-it.md).
