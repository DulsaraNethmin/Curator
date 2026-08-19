# T83 — a tunnel that is watched

**Owns:** noticing that the tunnel stopped protecting downloads, and stopping them when it does
**Settled by:** [D47](../decisions.md#d47--every-torrent-network-operation-is-tunnel-bound-or-disabled)
**Follows** [T82](T82-a-kill-switch-that-can-be-proved.md), which closed the leaks it could not watch

## Why now

Bytes stop when a tunnel dies — every dial fails, structurally, since phase 6. What has never been
true is that anything noticed. Rows sat there reporting "nobody appears to be seeding this release"
about a swarm that was fine, and every check curator made was synchronous with something a person
did: a dispatch, or a settings probe. On a box in a cupboard that is a spot check, not a kill switch.

**This supersedes the earlier decision not to have a watchdog.** That was taken when the goal was a
monitor, and a monitor that runs only while somebody has the page open is a reasonable monitor.

## What was built

**One implementation of "is this tunnel good".** The deciding moved out of the `Tunnelled` closure
into a `Checker` returning a `Verdict`, and `Tunnelled` became three lines over it. There are two
callers now, and two implementations would eventually disagree — at which point the screen and the
refusal say different things about the same tunnel and neither is obviously wrong.

A `Verdict` is a value rather than an error because the callers want different things: dispatch wants
a refusal with a sentence, and a watchdog wants a state it can compare with the last one to notice a
transition. Six states, because the instructions differ — `blocked` is the interesting one, a tunnel
up and handshaking and no longer changing where traffic leaves from, the single state where bytes
really do leave the real address while everything else reads healthy.

**A `Sentinel`, on two cadences.** The cheap tick is 15 s and is a device read in this process: no
third party, so it still works when the tunnel is what is broken. The expensive one is `CheckExit`,
every 5 minutes, and only while something is downloading or the last verdict is bad. An idle healthy
curator makes none at all.

**`Hold`/`Release`, and the hold reaches the screen.** Three calls per torrent, because stopping the
data leaves the connections open and the torrent announcing. The reason is carried into the stall
reason, applied last in `describe`.

**`Resume` asks the guard**, and the sentinel's first good verdict runs it again.

## Traps

- **A transition is measured against the previous CHEAP read, not the last verdict.** Against the
  verdict, a tunnel sitting in one bad state looks like a fresh transition every tick and forces an
  exit check every 15 s at exactly the moment the exit cannot be reached.
- **Announcements are measured on the final verdict, in one place.** Folded into the transition
  bookkeeping, the cheap read and the check that follows it disagree by construction — the cheap read
  cannot conclude `up` — and every tick announces.
- **`Cheap()` must not consult the dispatch cache's TTL.** It did, and an expiring cache then looked
  like a state change and forced an exit check every ten minutes for ever, on an idle box.
- **A held download does not report itself as downloading.** So `active` is false exactly when the
  tunnel is broken, and a cadence keyed on it alone would switch off the only thing that could ever
  release them. That is a deadlock, not a slow recovery.
- **`Hold` must be idempotent.** A sentinel that sees blocked, then stale, then unknown calls it three
  times for one failure; a second call that re-read the connection limits would record the zeroes the
  first installed and `Release` would restore nothing.
- **The first verdict must not warn.** `Run` proves the tunnel the moment the process starts,
  routinely before the first handshake completes, so a healthy boot warned that downloads were not
  protected and cleared it 15 s later — every time. Found by running it, not by a test.

## Verify

- `TestTheSentinelDoesNotCheckTheExitWhileIdle` and `TestTheSentinelChecksTheExitWhileDownloading` —
  the cadence, both directions, reintroduced and seen to fail.
- `TestABadVerdictKeepsBeingReCheckedEvenWithNothingDownloading` — the recovery deadlock.
- `TestABrokenTunnelIsNotHammered`, `TestSubscribersFireOnTransitionsRatherThanOnTicks`,
  `TestTheFirstVerdictIsNotAWarning`.
- `TestABadVerdictHoldsEveryTorrent`, `TestAGoodVerdictReleasesThem`, `TestHoldDoesNotLoseProgress`,
  `TestHoldingTwiceIsFree`.
- `TestResumeAtBootAsksTheGuard` and `TestResumeRunsAgainOnceTheGuardPasses`.

## Not done here

**Nothing pauses on a per-piece basis.** The worst case for a silently-degraded exit is bounded at
about five minutes while downloading, not at zero. Checking on every piece costs more than it buys,
and the number is stated on the page rather than hidden.
