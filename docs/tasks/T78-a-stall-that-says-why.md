# T78 — a stall that says why, instead of a deadline that says when

**Owns:** `stallReport` (`internal/engine/engine_test.go`), the stall detection in
`TestLiveEngineOverTunnel` and the failure path of `TestLiveDownloadPeakRSS`
**Depends on:** nothing. It is the "obvious next move" [T74](T74-no-network-beyond-loopback.md) named
and did not take: *"that live test does not use `await`, so the new diagnostic does not reach it"*

## Goal

When the engine stops moving payload, the test says what the swarm was doing — not just that five
minutes elapsed.

## What happened

T74 built `swarmState` to tell four faults apart and wired it into `await`. The one test that
actually reproduces the fault does not use `await`: `TestLiveEngineOverTunnel` has its own polling
loop and ended in a bare `t.Fatalf("less than %d MB moved through the tunnel in 5 minutes")`. So the
best diagnostic in the package was one function call away from the failure it was written for, and
five minutes of per-tick lines identified nothing. `TestLiveDownloadPeakRSS` ended the same way.

## Why swarmState alone was not enough

`swarmState` answers *are we asking?* and *did anything arrive?* If it comes back
`priority=normal ok=true active=3 read=0`, all four of its cases are excluded and it has nothing left
to say. What is left is two explanations it cannot reach, and `Client.WriteStatus` prints both, per
peer, in one line:

```
reqq: <ours>+<cancelled>/(<nominalMax>/<peerMax>):<theirs>/<localReqq>, flags: <us>:<conn>:<them>
```

A trailing `c` in the last segment means **the peer is choking us** (`peerconn.go:1632`) — a swarm
answer, not a curator bug. `reqq` at 0 with nobody choking is the opposite: the requester never
asked. Neither is visible from the poller, from `swarmState`, or from the per-tick line.

## The detector is a rate floor, and the first version was wrong

The first version reported when the payload counter **stopped**. It never fired, and finding out why
was the most useful measurement here.

Measured 2026-08-18 under `-race`: the per-tick line sat at `3.0 MB from 6 peers` from 16 s to 37 s
and looked stopped. `BytesReadData` was still creeping — about **200 KB across the whole 21 s** — so
`read > lastRead` was true on nearly every tick and the timer reset for ever. **The plateau is a
trickle, not a stop**, and the display rounds to 0.1 MB so it is invisible at the log.

So the condition is `read-lastRead >= minProgress` over `stallAfter`: **256 KB per 20 s**, a floor of
12.8 KB/s against a healthy ~600 KB/s. Both numbers come from runs the same day rather than taste.

## Do

1. **`stallReport` = `swarmState` + `Client.WriteStatus`**, on failure paths only. WriteStatus takes
   the client's read lock and prints every torrent, peer and DHT server: far too much for a passing
   run, exactly enough for a stalled one.
2. **Snapshot mid-stall, not at the deadline.** A report taken at 300 s is a report of the wreckage.
3. **Both live swarm tests**, because either can stall the same way.

## Do not

- **Fix the stall here.** This task makes it legible. What to do about it is a separate decision, and
  the reading below is what that decision should be made on.
- **Report on a frozen counter.** See above — it does not freeze.
- **Call `stallReport` on a passing path.** It is a full client dump.

## Verify

`go vet ./...` clean, `gofmt` clean.

- **`TestAStallReportReachesThePeerLine`** pins the report's shape hermetically: `peers active=`,
  `read data=`, `# Torrents:`, `reqq:`, `flags:`. It uses **two leechers and no seeder**, which is
  the only hermetic shape that stays connected — measured, a real transfer from `seed()` completes in
  ~50 ms and anacrolix drops the connection as soon as both ends are done, so polling for a
  `PeerConn` on a working download finds nothing even at 20 ms intervals. Two peers that both want
  the payload and neither has it hold `active=2` indefinitely. 0.04 s.

### What it caught, which is the point

**The stall reproduced once with the diagnostic armed**, and the reading is decisive:

```
ActivePeers: 0   TotalPeers: 0   PendingPeers: 0   HalfOpenPeers: 0
0 peer conns:
BytesReadData: 5832704   ChunksReadWasted: 0   PiecesDirtiedBad: 0   MetadataChunksRead: 15
Next announces:  http://bttracker.debian.org:6969/announce  next ann: 9m58s, last ann: 50 peers
```

It started fine and read 5.83 MB. Nothing arrived corrupt — **zero wasted chunks, zero bad pieces**.
Then every peer went away, and `debianMagnet` carries **one** HTTP tracker whose next announce was
**9m58s** out, inside a 5-minute deadline. This is not "slow": it is a swarm that emptied and a
refill that could not arrive in time. `DhtStartingNodes` is emptied only for `hermetic` engines
(`engine.go:245`), so this test does have DHT — whether it had bootstrapped through the tunnel is
the reading not yet captured.

### Frequency, measured the same day

Ten runs of `TestLiveEngineOverTunnel`: **six passed without `-race`** (14 s, and five more), and
under `-race` **22 s / 21 s plateaus that recovered** (43 s, 45 s, 18 s, 16 s, 22 s, 26 s), **one
genuine 301 s stall**, and **one data race**. It is intermittent and markedly slower under `-race`.

## Open, and deliberately not settled here

- **The mid-stall snapshot has not been read.** The one 301 s reproduction was not captured to a
  file, and the two reproductions after it were the race below rather than a stall. The final report
  is what is quoted above; whether the `STALLED:` line printed in that run was not verified. Catching
  one is the next run's job, and it is what separates "choked" from "never asked".
- **The tunnel `Close()` race fired a third time**, and for the first time in a **targeted
  single-test run** rather than a full-suite one — `netstack.(*netTun).Close()` against gvisor's
  `WriteNotify`, the same two stacks `docs/progress.md:926-933` records. That note says "seen once";
  T74 made it twice; this makes it three, and its trigger is broader than the note claims. Still
  third-party, still only with a `VPN_CONFIG_FILE`.
- **`error reading Socket PacketConn: EOF` still prints** on teardown, though `live_test.go:229`'s
  cleanup ordering exists to prevent exactly it. Not investigated.
- **`release.yml` still has no `concurrency:` block.** Carried from T74, T76 and T77.
