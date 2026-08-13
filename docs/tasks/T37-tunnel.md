# T37 — the tunnel

**Owns:** `internal/vpn/` — `config.go`, `tunnel.go`, `status.go` and their tests; one read-only
method on `internal/qbit`; a dispatch guard in `internal/download`
**Depends on:** T33 (gate passed)

## Goal

curator brings up its own WireGuard tunnel, in this process, with no `NET_ADMIN`, no privileged
container and no sidecar — and **only the torrent engine's traffic goes through it**.

The kill switch is not a setting. The engine is built with `DisableTCP`, `DisableUTP` and `NoDHT`,
so it has no way to open a socket of its own: in the spike it opened **zero**. Every byte has to go
through a dialer this package hands it, and a dead tunnel is therefore a failed dial rather than a
leak. That property is worth more than any amount of configuration, and it is lost the first time
one code path falls back to `net.Dial` "just for trackers".

## Do

1. **A wg-quick `.conf` is the input**, from `VPN_CONFIG` (the file's text, inline) or
   `VPN_CONFIG_FILE` (a path). That is the artifact every provider hands out, it is what phase 7's
   Settings screen will accept in a textarea, and one parser serves both. `[Interface]`
   `PrivateKey`, `Address`, `DNS`, `MTU`; `[Peer]` `PublicKey`, `PresharedKey`, `Endpoint`,
   `AllowedIPs`, `PersistentKeepalive`. Keys are base64 in the file and **hex in the UAPI** the
   device speaks — convert once, in the parser, and never log either form.
2. **A gVisor netstack TUN, not a kernel device.** `wireguard-go`'s device over
   `tun/netstack.CreateNetTUN`, MTU 1420. Nothing outside this process sees an interface, which is
   exactly why no capability is needed.
3. **Expose a network, not a VPN.** `DialContext`, `ListenPacket` and `Resolver` — the shape
   `internal/engine`'s `Network` interface asks for. The engine does not import this package; `main`
   wires one into the other. Anything that needs "is it up" asks `Status`.
4. **DNS is inside the tunnel or it is a leak.** Tracker URLs carry hostnames, and resolving them on
   the host announces what is being downloaded to the ISP before an encrypted byte moves. The
   resolver dials the `.conf`'s `DNS` over netstack — the same UDP path T33 measured first, at
   51–93 ms.
5. **The wiring that actually works**, from the spike, because most of the engine's surface is the
   wrong way to do this:

   ```
   utp.NewSocketFromPacketConn(...)                      uTP over the tunnel's PacketConn
   Client.AddDialer / Client.AddListener                 peer connections
   Client.NewAnacrolixDhtServer(...)                     DHT on the same PacketConn
   Client.AddDhtServer(torrent.AnacrolixDhtServerWrapper{...})
   ClientConfig.HTTPDialContext                          webseeds
   ClientConfig.TrackerDialContext                       HTTP trackers
   ClientConfig.TrackerListenPacket                      UDP trackers
   ```

6. **`VPN_REQUIRED` defaults to `true`.** With the embedded backend and no tunnel configured,
   dispatch answers "not configured" and names the variable — the same posture an unset `QBIT_USER`
   has had since phase 3, and the same sentence shape. `VPN_REQUIRED=false` is the deliberate,
   documented escape for a laptop. A mandatory VPN that defaults to off is a slogan.
7. **Prove the tunnel changes the exit.** One plain-text IP lookup through the tunnel's dialer, one
   through the host's, at start-up and behind the settings probe: `VPN_IP_CHECK_URL`, default
   Cloudflare's `/cdn-cgi/trace`, which answers `ip=…` in one line. **Equal addresses mean the
   tunnel is carrying traffic somewhere that is not a VPN** — refuse, loudly, naming both. Store the
   result in `Status` for phase 7's screen. Never log the tunnelled address at info level.
8. **The honest floor for an external qBittorrent.** Its peer traffic is not curator's to route, but
   it is observable: `GET /api/v2/sync/maindata` carries `server_state.last_external_address_v4` —
   what libtorrent last learned about itself from the swarm. Measured against the local
   qBittorrent 5.1.2 container today: it answers `187.14.240.8`, which is **identical to curator's
   own exit IP**, and that container has no VPN. So:

   | `last_external_address_v4` | Meaning | curator does |
   |---|---|---|
   | equals curator's exit address | the client leaves by the same route curator does, so curator can vouch for nothing | **refuse to dispatch**, saying so — unless `VPN_REQUIRED=false`, which makes it a warning |
   | differs | it is going out somewhere else | dispatch |
   | empty | libtorrent has not talked to the swarm yet | dispatch, with a warning that says it is unproven |

   Equality is **not** proof that the client has no VPN, and the message must not say it is: a
   machine that is itself behind a tunnel produces exactly this reading while being perfectly
   protected — by something curator did not choose and cannot watch fail.

   Empty is not a refusal. A fresh qBittorrent with no torrents has never learned its address, and
   blocking the first dispatch for ever on a fact that only arrives *after* a dispatch is a deadlock
   with a security-shaped excuse.
9. The guard hangs off `download.Service` as an optional check, attached the way `WithImporter`
   already is, and fails with a named error so the API answers **503** and names the cause rather
   than a bare 500. `internal/qbit` gains exactly one read-only method for it; it still cannot pause,
   resume or reprioritise anything.

## Do not

- Route the web UI through the tunnel. A bad tunnel config would then lock you out of the app that
  configures the tunnel, which is the one failure this design must not have.
- Route TMDB, the indexers, minter or Jellyfin through it either — **and say so out loud** in
  [D27](../decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket). This phase protects
  peer traffic. A 1337x search still leaves from the host address. Every one of those takes an
  `*http.Client`, so moving them later is a wiring change and not a rewrite; pretending otherwise
  today would be the more expensive mistake.
- Fall back to a direct dial, anywhere, for anything, when the tunnel is down. That single line is
  the whole difference between a kill switch and a preference.
- Log a private key, a preshared key, or the tunnelled exit address. `logs.Buffer` scrubs the
  secrets it is handed at start-up ([D18](../decisions.md#d18--the-log-tail-is-readable-without-authentication-so-it-is-redacted-at-the-source));
  hand it these too.
- Reach for OpenVPN. gluetun remains the escape hatch for anyone whose provider is not WireGuard,
  and Settings says that plainly in phase 7.

## Verify

Hermetic, and most of this package is:

- the `.conf` parser round-trips a real provider file, converts base64 keys to hex, and **errors on
  a missing `PrivateKey`, `PublicKey` or `Endpoint`** rather than bringing up a device that can
  never handshake
- a malformed key, a bad MTU and an endpoint with no port each fail with a message naming the field
- `Status` on a device that has never handshaken says so; the handshake age is parsed from the
  device's own `IpcGet` output
- with no tunnel and `VPN_REQUIRED=true`, dispatch returns the unconfigured error naming
  `VPN_CONFIG`, and the API answers 503
- the qBittorrent guard: equal addresses refuse, different addresses pass, empty passes with a
  warning — three cases against an `httptest.Server`, no container needed

Then live, with a real provider config, on the laptop:

- the device handshakes, and the exit IP through the tunnel **differs** from the host's
- a DHT bootstrap over the tunnel finds nodes, and an announce returns peers — 51 and 518 in the
  spike
- a real torrent completes with every byte through netstack, at a like-for-like ratio near the
  measured 0.69
- **`lsof` on the process shows no torrent socket of its own** — the structural claim, checked
  rather than believed
- bring the tunnel down mid-download and confirm transfer **stops**, measured in bytes rather than
  in log lines, and resumes when it comes back
