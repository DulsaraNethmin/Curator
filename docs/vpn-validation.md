# Checking that curator's downloads are actually going through your VPN

curator makes a strong claim about its own engine and a much weaker one about anybody else's. This
page separates them, because the difference decides what you have to check yourself.

---

## Which of the two you are running

Look at **`TORRENT_BACKEND`**, or at the VPN screen, which says.

| | `embedded` (the default) | `qbittorrent` |
|---|---|---|
| Who owns the socket | curator | qBittorrent |
| What curator can promise | every torrent network operation is tunnel-bound or disabled | nothing — it can only compare exit addresses |
| What you must check yourself | that it is doing what it says, once | **everything below, and it is your job** |

---

## The embedded engine

### The claim

> Every network operation of the torrent subsystem — payload, peers, uTP, DHT, trackers, webseeds,
> WebRTC, DNS and local discovery — is tunnel-bound or disabled. There is no third option.

It holds because the torrent client is built with no way to open a socket of its own: it is handed a
dialer and a listener that belong to the tunnel, and everything else is switched off. A dead tunnel
is a failed dial rather than a leak. See
[D47](decisions.md#d47--every-torrent-network-operation-is-tunnel-bound-or-disabled).

**Two limits are part of the claim.** It is about the **torrent** subsystem: searches, TMDB, artwork,
the update check and Jellyfin leave from this machine's address on purpose, so a bad tunnel cannot
lock you out of the screen that fixes it. And if you run the `1337x` profile, **minter** browses with
a real Firefox in its own container, which curator does not route.

### What to check, in five minutes

1. **Open the VPN screen.** The headline is `PROTECTED` only when four separate things are true. If
   it is green, read the four rows underneath anyway — they are what it is made of.
2. **Confirm the socket is inside the tunnel.** "The download engine's socket is inside the tunnel"
   should name an address on your tunnel's subnet (`10.x`, `172.x` — whatever your provider's
   `Address =` line says), never your LAN address and never `0.0.0.0`.
3. **Confirm traffic comes out somewhere else.** "Traffic leaves from somewhere other than this
   machine" is a comparison curator makes between an address fetched *through* the tunnel and one
   fetched directly. Set a password if you want to see the address itself; without one curator
   deliberately will not print it, because that screen has no authentication in front of it.
4. **Break it on purpose.** Stop the tunnel — change `VPN_CONFIG` to a bad key, or block the endpoint
   at your router — and watch the headline go red within about three minutes and your downloads
   report themselves held, with the tunnel named as the reason rather than the swarm. Put it back and
   they resume where they were.

### The check that settles it, if you want certainty

Everything above is curator reporting on curator. To prove it from outside, capture on the host's
real interface while a download runs:

```bash
sudo tcpdump -i eth0 -n not port 22
```

You should see **nothing but UDP to your VPN endpoint**. Any TCP to a stranger, any DNS query for a
tracker or a `dht.*` hostname, any SSDP multicast to `239.255.255.250` — those would be a leak, and
none of them should appear.

---

## An external qBittorrent

**curator cannot protect this client and does not claim to.** What it does instead is refuse to
dispatch when qBittorrent reports the same external address curator has, because that means whatever
protects this machine protects that client by accident rather than by design.

**Be exact about what a passing check proves.** Different addresses mean the client's traffic leaves
by a different route from curator's. They do **not** mean that route is a VPN, that it is the VPN you
configured, or that it will still be there in an hour. curator sees an address, once, at dispatch.

So the whole of the checking is yours:

1. **Route the client, not curator.** gluetun as a sidecar with `network_mode: "service:gluetun"`, or
   qBittorrent's own binding to a VPN interface. Setting `VPN_CONFIG` in curator does nothing for it.
2. **Bind the interface.** In qBittorrent, Settings → Advanced → *Network interface*, set to the VPN
   interface rather than "Any". Without it a tunnel that drops falls back to your real connection
   silently, which is the exact failure this page exists to prevent.
3. **Verify from inside that container**, not from the host:
   ```bash
   docker exec <qbittorrent> wget -qO- https://www.cloudflare.com/cdn-cgi/trace | grep ^ip=
   ```
4. **Re-check after every restart.** A sidecar that comes up in the wrong order leaves the client on
   the host's network with no error — the Pi has done exactly this, which is why
   [D43](decisions.md#d43--the-pi-is-a-clean-slate-television-is-retired-and-curator-is-the-only-downloader)
   retired that seam rather than repairing it.

**`VPN_REQUIRED=false` silences curator's check into a warning.** That is the documented setting for a
machine that is already entirely behind a VPN — an arrangement curator cannot tell apart from having
none. It still says so on every dispatch, deliberately.
