# T56 — a `udp://` tracker stops taking the process down

**Owns:** `internal/engine/network.go` — `bindConfig`, and the two things it now does
**Depends on:** T35 (the engine), T37 (the tunnel)
**Found by:** [T55](T55-stall-reason.md), whose first live run never reached the listener

## Goal

The normal case stops crashing. Every real indexer magnet carries `udp://` trackers, so the magnet
that panicked is the ordinary one and the trackerless magnet T55 fell back to is the synthetic one.
Phase 8 does not start over a crash on the normal path.

```
panic: vpn: the tunnel has no udp6 address of its own to listen on; check the config's Address line
  anacrolix/torrent.(*regularTrackerAnnounceDispatcher).initTrackerClient
  engine.(*Engine).AddMagnet → download.(*Service).Resume → main.run
```

## What it actually is, established before designing anything

Reproduced hermetically in 0.01 s, by a `Network` that refuses `udp6` the way a v4-only tunnel does.
The three things T55 listed as unknown are all answered, and all three are **no**:

1. **It does not need a resolvable tracker host.** `udp.NewConnClient` opens the socket *before* it
   resolves anything, so `not-a-real-host.invalid` panics identically. There is no DNS in this bug.
2. **It is not a particular anacrolix version.** `v1.61.0` since T35, unchanged.
3. **It is not the boot path.** A plain `AddMagnet` panics. Boot is only where it was *seen* first,
   because resume re-adds every unfinished row before anything else runs — which is what makes it a
   crash loop rather than one bad download.

**Why phase 6 missed it**, which is the fact worth keeping: `live_test.go`'s `debianMagnet` carries
exactly one `http://` tracker, and an HTTP announcer never asks for a packet socket at all. The
whole tunnel was verified against the one tracker scheme that cannot reach this code.

The mechanism, in anacrolix `v1.61.0`:

```
torrent.go:2180   startScrapingTracker rewrites `udp://` into TWO announcers, udp4:// and udp6://
torrent.go:2211   the udp6 one is skipped ONLY when config.DisableIPv6 is set
   →  startTrackerAnnouncer → initTrackerClient → tracker.NewClient{UdpNetwork: "udp6"}
   →  udp.NewConnClient → opts.ListenPacket("udp6", ":0")        ← curator's hook
client-tracker-announcer.go:861   panicif.Err(err)               ← on a func that returns nothing
```

## Do

1. **Tell the client which address families the network carries.** `bindConfig` probes the `Network`
   once — `ListenPacket("udp4")`, `ListenPacket("udp6")`, both closed immediately — and sets
   `DisableIPv4`/`DisableIPv6` from the answers. That is the same question anacrolix asks later,
   asked once where the answer can be acted on.

   A probe rather than a new interface method: it works for `loopback{}` and for every future
   `Network` without changing the interface, and it tests the call that will actually be made
   instead of a claim about it. A network that can listen on neither never reaches the probe —
   `New` opens the shared uTP socket first and fails with an error a caller can read.

2. **`TrackerListenPacket` must never return an error.** It is the one hook curator hands anacrolix
   whose error is passed to `panicif`. A network that will not open the socket yields a
   `deadPacketConn` that fails every operation instead, so the announce dies where anacrolix already
   copes with an announce dying.

   **The lie stops at that boundary.** `internal/vpn` keeps returning a real error, because its own
   callers — `listenTunnel`, the live test — want one. Nothing in `internal/vpn` changes.

3. **Audit the rest of what curator hands the library**, because the wider rule is that a curator
   error must never reach a `panicif` on a path that cannot return it. Result, against `v1.61.0`:

   | Handed over | Its error goes to | Verdict |
   |---|---|---|
   | `TrackerListenPacket` | `panicif.Err`, `client-tracker-announcer.go:861` | **the bug** |
   | `HTTPDialContext` | `http.Transport.DialContext` (`client.go:290`) | safe |
   | `TrackerDialContext` | `http.Transport`, and webtorrent's dialer (`client.go:345`) | safe |
   | `AddDialer` → `utpDialer.Dial` | one failed connection attempt | safe |
   | `AddListener` → the uTP socket | `acceptConnections` (`client.go:609`) | safe |
   | `NewAnacrolixDhtServer` | returned from `attachNetwork`, refuses start-up | safe |

   Exactly one of six. Say so in the doc comment, with the line number, so the next person does not
   have to re-derive it — and so nobody deletes the wrapper as ceremony.

## Do not

- **Refuse a v4-only tunnel at config time.** A NordLynx `.conf` has one IPv4 `Address` line and
  that is not a broken config, it is every config. Refusing it would turn a crash into a product
  that will not start.
- **Fall back to `net.ListenPacket` when the tunnel will not.** That is the kill switch, and phase 6
  measured it as zero OS sockets opened by the client. "Just for trackers" is exactly how it is lost.
- **Make `deadPacketConn` pretend to work.** It fails, loudly, with the reason. Turning a panic into
  a silent no-op would trade a crash for a download that never announces and never says why.
- **Switch udp trackers off.** The fix is per-family. A v4-only tunnel still announces over `udp4`,
  which is where the peers are.

## Verify

Hermetic, in `internal/engine/network_test.go`, all three mutation-tested:

| Test | Fails when |
|---|---|
| `TestAUDPTrackerDoesNotTakeTheProcessDown` | `DisableIPv6` is dropped — and **panics** when both guards are, which is the original bug reproduced through anacrolix's own path |
| `TestTheClientIsToldWhichFamiliesTheNetworkCarries` | the probe or either `Disable*` assignment is dropped |
| `TestTheTrackerHookNeverReturnsAnError` | the hook is simplified back into the one-line passthrough |

The first also asserts a `udp4` announcer **was** started, so a "fix" that switched udp trackers off
entirely fails it.

Then live: a real indexer magnet, with its real `udp://` trackers, resumed from the table at boot
against the NordLynx tunnel — the run T55 could not make.

## Done

Verified live on 8097, scratch database, embedded backend, the real NordLynx tunnel up
(`sg701`, `Address = 10.5.0.2/32` — one IPv4 line, no v6, which is the config that crashes). The row
carried the phase 6 Debian infohash with **three real `udp://` trackers** —
`open.demonii.com:1337`, `tracker.opentrackr.org:1337`, `tracker.openbittorrent.com:6969`, the set
YTS puts on every magnet — plus Debian's `http://` one, so both schemes were in the same magnet.

**Both binaries were run against the same row**, the pre-fix one built from `main` (`88e7d82`) in a
detached worktree:

| | Result |
|---|---|
| `main`'s binary | **panicked, exit 2**, `AddMagnet → download.(*Service).Resume → main.run`, before the listener |
| this binary | came up, `resumed downloads re_added=1 failed=0`, listening in **~1 s** |

It then **downloaded**: 0 → 41 % of 755 MB in 113 s, ≈2.7 MB/s, entirely through the tunnel, with
the udp4 announcers doing the work. That is inside phase 6's tunnelled 2.88 MB/s, so the fix costs
no throughput and — more to the point — udp trackers still find peers.

**The backstop never fired, and that is the result to keep.** The log holds the start-up line
`the network has no IPv6 address, so IPv6 peers and udp6 tracker announces are off` exactly **once**
and zero `deadPacketConn` warnings across the whole run. The layering is doing what it is for: the
udp6 announcer was never started, so the hook was never asked. Only the mutation test has ever made
it fire — and with `DisableIPv6` removed and the hook left in, that mutation **did not panic**,
which is the backstop demonstrated through anacrolix's own path rather than argued for.

### Not done here

- **The `panicif.NotNil(u.User)` two lines below the one this fixes** (`client-tracker-announcer.go:863`)
  is the same crash from a different input: a magnet carrying `tr=udp://user@host/announce` takes
  the process down, and that is anacrolix's own assertion rather than a curator error reaching one.
  Untested, unfixed, and cheap to guard in `AddMagnet` if it ever shows up — noted so it is found by
  reading rather than by crashing.
- **No IPv6-only tunnel has ever been run.** `DisableIPv4` is set by the same probe and is covered
  hermetically, but every live run this project has ever made was v4-only.
