# T105 — the engine's sixty-second stall, filed at last

**Owns** nothing yet: this is a specification, and the flake it names is unfixed · **takes**
[T74](T74-no-network-beyond-loopback.md)'s diagnostic and the stall report `await` gained on the
`await-names-its-cause` branch · **does not** propose suppressing it, for the reasons T74 already
argued and this file restates

An `await` in `internal/engine` gives up after sixty seconds because a **1 MB payload did not move
between two in-process clients over loopback**. It has fired three times in front of a gate, twice of
those in front of a publish. It has never had a task file, and the reason to write one now is that
the third occurrence **excludes the reading the second one landed on**, so the evidence is no longer
a pile of anecdotes — it is a narrowing.

## What this is NOT

**It is not a network test, and it is not a runner artifact.** Since T74 every engine here is built
with `Config.hermetic`, which sets `NoDHT`, binds `anacrolix.LoopbackListenHost` and empties
`DhtStartingNodes`. The `error bootstrapping during bucket refresh: no initial nodes` lines all over
the failing log are **the confinement working** — `engine.go:293-306` says so in as many words: *"An
empty list is a no-op rather than a failure: the announcer finds no initial nodes, says so once, and
sleeps."*

So the failing scenario is: one seeder and one leecher, both in the same process, connected by an
explicit `lt.AddClientPeer(seeder.client)`, moving 32 pieces of 32 KB across loopback. That is the
thing that takes longer than a minute.

## The three occurrences

| | when | run | what the dump said |
|---|---|---|---|
| 1 | T74 | `32094278975` | metadata in, peers up, **zero payload, for ever** |
| 2 | in front of the **v0.4.0 publish** | `32447250968` | `read data=180224`, `pieces complete=3` of 32 — payload **moved and then quit** |
| 3 | in front of this release, `TestDeleteTorrentRemovesItsOwnFiles` | `32556473382` attempt 1, 2026-08-22 | `read data=16384`, `pieces complete=0`, **not choked, and asking** |

Occurrence 2 excluded all four cases `swarmState` enumerates and was reasoned down to
choked-versus-never-asked, which is why `await` gained the client status dump. Occurrence 3 is the
first one that dump has answered.

### Occurrence 3, verbatim

```
--- FAIL: TestDeleteTorrentRemovesItsOwnFiles (60.10s)
    engine_test.go:606: torrent never reached "completed"; last seen {Hash:BDF1F9A1... State:downloading Progress:0 ...}
          polled 2941 times over 1m0.015s; last poll returned 5ms before the deadline
          awaited: peers active=2 seeders=2 halfopen=1 pending=0 | pieces complete=0 |
                   read data=16384 metadata-chunks=2 wasted-chunks=0 bad-pieces=0 |
                   piece0 priority=normal ok=true complete=false | runs 8. 1.P 23.
```

and the two peer lines the stall report exists to produce:

```
reqq: 1+0/(4/1024):0/1024, flags: i:e,v1:
reqq: 1+0/(2/1024):0/1024, flags: i:U,e,v1:
```

## What occurrence 3 establishes

**We are not being choked.** `await-names-its-cause` recorded the format and the tell: *"the trailing
`c` is the whole point: that peer is choking us."* Neither line here ends in `c`.

**We are asking.** `reqq: 1+0` on both, and `piece0 priority=normal ok=true`. A request is
outstanding and the completion database can answer.

**The observation is not stale.** This is the half of `await` that exists to separate "the download
is fine and `TorrentByHash` was blocking" from "the download really stopped": 2,941 polls over
60.015 s against a measured healthy baseline of 2,868 polls / 60.01 s, last poll 5 ms before the
deadline against a baseline of 10 ms. The poller was healthy. **The transfer was not.**

So the state is *asked, unchoked, connected, and served nothing* — one 16 KB chunk in sixty seconds.
That is a fourth case, and none of the three prior readings covers it.

**The duplicate connection is present again.** `peers active=2 seeders=2` and `pex: 2 conns` for the
**one** seeder the test adds, and two peer lines in the report. Occurrence 2 showed the same shape and
it was noted and not pursued. Two of two dumps that printed per-peer lines have shown it; it is the
most concrete un-chased lead in the file, and `i:e,v1:` versus `i:U,e,v1:` says the two connections
are *not* in the same state.

## Do

1. **Chase the duplicate connection first.** Two connections to one peer, in different states, on a
   test that adds exactly one — establish whether both are the leecher→seeder direction, whether one
   is `AddClientPeer` racing an inbound accept on the same loopback socket, and whether the
   half-open one is what holds the outstanding request. It is the only structural anomaly that has
   appeared twice.
2. **Reproduce with a request-level trace**, not another status dump. The status dump has now done
   its job — it named the case — and what is unanswered is what happened to the request that
   `reqq: 1+0` says is outstanding: sent and unanswered, or never written to the wire.
3. **Decide whether the storage seam is in play.** T74 recorded, and it is still true, that
   `go test -race` requires cgo so **every test runs against SQLite piece-completion while every
   release runs against boltdb** (`storage/sqlite-piece-completion.go` is `cgo && !nosqlite`). A
   piece-completion fault would reproduce on exactly one side of that line, and the one this stalls
   on is the side no user runs.
4. **Wire the stall report into `TestLiveEngineOverTunnel`.** T74 flagged it, `await-names-its-cause`
   flagged it again as "the obvious next move and not this task's". It is the one place the
   signature has been reproduced away from CI — 2026-08-18, 0.00 MB from 3–4 real peers for five
   minutes — and it is still the only occurrence with no dump behind it.

## Do not

- **Do not raise the deadline, add a retry, pass `-count`, or `-short` it out of the gate.** T74
  refused all four and the reasoning holds: sixty seconds is already twenty times the healthy run,
  a retry converts a defect into latency, and a test that does not run cannot fail usefully. The
  flake is the only evidence anybody has.
- **Do not treat a re-run passing as the answer.** It passes on re-run every time. It passed six
  times locally in a worktree gate on the same commit that failed CI.
- **Do not file it as CI flakiness.** It is loopback and in-process. Whatever this is, it is
  reachable by a user with a slow swarm, and the 2026-08-18 tunnel run is the evidence it already
  has been.

## Why it is worth a task rather than another paragraph in a handoff

It has now cost **two publishes** a re-run — v0.4.0 and this one — and `release.yml` calls the same
`check` workflow before it will publish an image, deliberately, so that `latest` cannot be a commit
that fails the race detector. Every future release rolls this die. That is a small, recurring,
entirely predictable tax, and the three dumps between them have narrowed the mechanism from "unknown"
to "one of the two connections to a single loopback peer is holding a request that is never served".
