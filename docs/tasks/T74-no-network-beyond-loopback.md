# T74 — no network beyond loopback, and a failure that says why

**Owns:** `Config.hermetic` and the block that applies it in `New` (`internal/engine/engine.go`),
`start`/`startUnconfined`/`await`/`swarmState` (`internal/engine/engine_test.go`), and the two
documents that describe what CI's tests reach (`CLAUDE.md:152-166`,
`.github/workflows/check.yml:78-97`)
**Depends on:** nothing. It is the second thing the release pipeline found, and the first one
[T73](T73-ci-meets-a-blocked-apibay.md) could not have caught

## Goal

`make check` passes on a GitHub Actions runner *every* time, not half the time. And when the
engine's suite does fail there, the failure names its own cause instead of costing a session.

## What happened

Run `32094278975` (`check` on `main`, `ubuntu-latest`):

```
--- FAIL: TestDeleteTorrentRefusesAnotherCategory (60.09s)
    engine_test.go:334: torrent never reached "completed"; last seen {Hash:BDF1F9A1AC7677150D3A2FABF896EC11A8CC4762 Name:Interstellar (2014) State:downloading Progress:0 ContentPath:/tmp/.../002/Interstellar (2014) Category:curator Reason:}
FAIL	github.com/DulsaraNethmin/curator/internal/engine	61.691s
make: *** [Makefile:40: race] Error 1
```

**The identical commit passed that same test minutes earlier**, in release run `32094325056`'s
embedded `check` job — same tag, same image, 6m3s instead of 6m50s. That is nondeterminism proven
by controlled experiment, at an observed rate of 1 in 2. `release.yml:45` is `needs: check`, so it
is a coin flip in front of every publish.

One premise this task started with was wrong and is worth killing now: the release pipeline *has*
reached its build step. `32094325056` went green end to end — `check` in 6m3s, then `image` in
30m53s with a `dockerbuild` artifact. This is a tax on publishing, not a wall in front of it.

## It is stuck, not slow, and the deadline is the wrong thing to touch

`State:downloading` is unreachable unless `t.Info() != nil` (`engine.go:516-537`), and the
leecher's `DataDir` is a fresh `t.TempDir()` so the persisted-metainfo shortcut at `engine.go:276`
cannot hit. The metadata can only have crossed a handshaken peer connection. Meanwhile `Progress:0`
is not a rounding artifact — `bytesLeft` subtracts `numDirtyBytes`, so one 16 KiB chunk would print
`0.015625`. Zero means zero payload bytes.

And the arithmetic settles it. The package took `61.691s`; the failing test alone took `60.09s`.
**Every other test in it, including seven more 60-second completion awaits, finished in about 1.6
seconds.** A merely-slow loopback transfer does not look like that.

So raising the deadline would be actively harmful, not merely useless. There are **eight** awaits
at 60 s in this package (`engine_test.go` 159, 218, 334, 355, 404, 415, 445, 611), `make race` is
a bare `go test -race ./...` with no `-timeout`, and Go's default is ten minutes per package.
60→180 converts a fast, legible test failure into a package-level `panic: test timed out` that
names no test at all.

## The tests said they were hermetic and were not

`engine_test.go:30-33` claimed *"No swarm, no tracker, no public torrent, and no network beyond
loopback."* Nothing enforced it.

`New` builds from `anacrolix.NewDefaultClientConfig()` (`engine.go:176-186`) and never sets
`NoDHT`, `DisableTCP` or `DisableUTP`. Those live in `bindConfig` (`network.go:57-60`), and
`engine.go:210` reaches it **only inside `if cfg.Network != nil`** — which no hermetic test
supplies. So every engine in the suite opened wildcard sockets, started two DHT servers
bootstrapping to the public internet, announced a fixed test info hash, and ran a UPnP SSDP
discovery.

**Counted, because the first estimate was half:** a full `go test ./internal/engine` builds
**26 clients** — eleven from `seed()`, fifteen more built directly — sequentially, with no
`t.Parallel()` anywhere. Twenty-four of them had no `Network`. That is twenty-four SSDP sweeps and
forty-eight DHT servers reaching the public internet from a unit test.

The comment described the intent. The code did not implement it on the default path.

**Measured 2026-08-18, serially, on a 10-core laptop — and the first measurement was wrong, which
is the useful part.** `go test -race -count=1 ./internal/engine` appeared to go from 67.7 s to
18.8–25.8 s, and that number was nonsense: this machine has a `.env`, so `TestLiveEngineOverTunnel`
was inside every reading, downloading 8 MB over a real WireGuard tunnel from a real swarm. It alone
ranges from 12 s to over 300 s.

With the live tests skipped — which is the state CI runs in — the honest figure is
`go test -race -short -count=1 ./internal/engine` at **2.46 / 2.48 / 2.16 s before and 2.21 / 2.26 /
2.12 s after**. **The isolation buys no wall clock at all.** UPnP discovery and DHT bootstrap both
run on goroutines that block nothing, so on a fast machine they cost scheduler noise rather than
seconds.

That is worth saying plainly: this change is about removing nondeterminism, not about speed, and
anybody who reads a big timing win into it will be measuring their own tunnel. What it removes is
twenty-four SSDP sweeps, forty-eight DHT servers, a public announce of a fixed info hash, and any
peer the DHT would have returned to compete for the twelve half-open and twenty-four established
connection slots with a seeder standing on loopback.

### And `NoDHT` does not finish the job

`attachNetwork` (`network.go:162`) calls `e.client.NewAnacrolixDhtServer(e.socket)`
**unconditionally** whenever a `Network` is supplied, and that server reads `DhtStartingNodes`
directly rather than asking whether the DHT is off. `NewDefaultClientConfig` wires that to
`dht.GlobalBootstrapAddrs`. So `NoDHT` alone leaves `TestDownloadThroughANetwork` — itself one of
the eight sixty-second awaits — still bootstrapping to `router.bittorrent.com` over `loopback{}`,
and still announcing the fixed test info hash. Emptying `DhtStartingNodes` is what actually closes
it, and an empty list is a no-op rather than a failure: the announcer finds no initial nodes, says
so once, and sleeps.

One further consequence was concrete rather than theoretical. With a wildcard `ListenHost`,
`Client.ListenAddrs()` returns `0.0.0.0:N` and `[::]:N`, and `AddClientPeer` (`torrent.go:3027`)
handed *those* to the leecher as the seeder's address. It works because dialling `0.0.0.0` reaches
localhost, which is a quirk to rely on rather than a design.

## A mechanism that fits every fact, and the measurement that rules it out

`Torrent.setCachedPieceCompletionFromStorage` (anacrolix `torrent.go:1779-1786`) does this when
the piece-completion database cannot answer, and `setInitialPieceCompletionFromStorage` calls it
for every piece inside `onSetInfo` — at the exact instant metadata arrives, which is the instant
this torrent stopped:

```go
uncached := t.pieceCompleteUncached(piece)
if uncached.Err != nil {
	t.slogger().Error("error getting piece completion", "err", uncached.Err)
	t.disallowDataDownloadLocked()
}
```

Both halves wedge the torrent permanently. `storageCompletionOk` goes false and
`Piece.ignoreForRequests` (`piece.go:299`) returns true for that piece for ever; and
`dataDownloadDisallowed` makes `getDesiredRequestState` (`requesting.go:212`) return empty, which
**nothing in curator ever clears** — `AllowDataDownload()` has no caller. Either way: peer
connected, metadata present, zero bytes, for ever. It is the only mechanism found that fits every
observed fact.

**And it is ruled out, by the log that is not there.** The chain was checked rather than assumed:

- `NewDefaultClientConfig` never sets `Logger`, so `cc.Logger` is the zero value.
- `WithFilterLevel` (anacrolix/log `logger-core.go:44-47`) copies the struct and sets
  `filterLevel` **without setting `nonZero`**.
- `Client.getLoggers` (`client.go:241-243`) then does `if logger.IsZero() { logger = log.Default }`,
  and `log.Default` filters at `Warning` onto stderr (`init.go:14-20`).

**Measured 2026-08-18:** `cc.Logger.IsZero()` is `true` both before and after
`FilterLevel(anacrolixQuiet)`, and a synthetic `Error` emitted through that exact chain prints as
`[ERR ...] msg="error getting piece completion"` in the test output. So the line would have been in
run `32094278975`'s log, under the failing package, and it was not. **`engine.go:207` is inert and
its comment is false** — see the open questions — and the piece-completion path is evidence-refuted
rather than merely unproven.

That leaves T74's mechanism unknown, which is exactly what the diagnostic is for. `PieceState.Ok`
stays in the dump anyway: it is cheap, it is the one reading that separates "not asking" from
"asking and not being served", and ruling a thing out twice costs less than the session it cost to
rule it out once.

## The build tags say the tests do not exercise the storage that ships

Worth recording while it is in hand. `storage/sqlite-piece-completion.go` is
`//go:build cgo && !nosqlite`, commented *"sqlite is always the default when available"*;
`default-dir-piece-completion-boltdb.go` is `(!cgo || nosqlite)`. `go test -race` requires cgo. So
**every test in this repo runs against a SQLite piece-completion database while every release runs
against boltdb.** `engine.go:17-25` already calls `CGO_ENABLED=0` load-bearing for uTP; this is the
same seam, one layer down, and it means a storage-layer fault reproduces on exactly one side of the
line. Not this task's to fix — see the open questions.

## Do

1. **Confine the test engines to loopback**, with an unexported `Config.hermetic` applied in `New`
   outside the `Network` branch: `NoDHT`, an empty `DhtStartingNodes`, `NoDefaultPortForwarding`,
   and `ListenHost = anacrolix.LoopbackListenHost`. Unexported because it is not a deployment option
   and must never become one — a real magnet carries no tracker and no peer list, so the DHT is the
   only way curator finds anybody.
2. **Set it in `start`, not at the call sites**, so a test cannot reach the internet by forgetting
   something. `startUnconfined` keeps the old behaviour for the one caller that needs it.
3. **Make `await` self-diagnosing.** On the deadline it dumps `swarmState` for the awaited engine
   and every peer engine it was handed: peer counts, `PiecesComplete`, `BytesReadData`,
   `MetadataChunksRead`, `ChunksReadWasted`, `PiecesDirtiedBad`, and — once there is an info dict —
   `PieceState(0)`'s priority, `Ok` and `Complete` plus `PieceStateRuns()`.

## Do not

- **Set `DisableTrackers` in the hermetic config**, even though anacrolix's own `TestingConfig`
  does. [T56](T56-udp-tracker-panic.md)'s regression test asserts that a `udp4` announcer *was*
  started (`network_test.go:94-96`), and switching trackers off would make it pass for the wrong
  reason. The hermetic magnets carry no trackers, so there is nothing to announce to anyway.
- **Route either live swarm test through `start`.** `TestLiveDownloadPeakRSS` downloads 755 MB from
  strangers with no `Network`, and the peak-RSS number it exists to take is only comparable to the
  spike's if it is still doing that. `TestLiveEngineOverTunnel` is the less obvious one — it *does*
  pass a `Network`, so `bindConfig` covers it, but its own sentence is "metadata from the swarm
  means DHT or a tracker answered", and the DHT it relies on is the one `attachNetwork` builds and
  the empty `DhtStartingNodes` would starve. Both use `startUnconfined`, and they are the only two
  that should.
- **Raise the 60 s deadline.** See above. If measurement ever shows genuine slowness, the number
  becomes a named constant carrying a measured two-core figure, not a bare literal.
- **Add a retry, `continue-on-error`, or `-count`.** This repo has never quarantined a test, and
  `docs/progress.md:926-933` is the established shape: reproduce, name the mechanism in a document,
  then decide.
- **Run CI with `-short`.** [T73](T73-ci-meets-a-blocked-apibay.md) refused it for a reason that
  still holds — it would also silence `TestYTSLiveSearchInterstellar`, the canary for a dead base
  URL.
- **Trim `TestDeleteTorrentRefusesAnotherCategory`'s download.** It fetches 1 MiB only so
  `os.Stat(got.ContentPath)` has a real file after the refused delete, and the other seven awaits
  are exposed to the same bug. Cutting the one site that caught it hides the symptom and keeps the
  cause.
- **Settle the cgo divergence here.** See the open question below.

## Verify

**`make check` does not pass on this machine, and it does not pass on `main` either.** Its `race`
target runs `TestLiveEngineOverTunnel`, which needs the `.env` a runner does not have, and that
test spent the afternoon taking metadata through the tunnel and then moving nothing — see the open
questions. So the gate was run in parts, and every part CI can reach is green:

- `make ui go vet cross` — clean.
- `go test -race -short -count=1 ./...` — all twenty packages ok. `-short` reproduces CI's
  *effective* state, since a runner has no `.env` and every credential-gated test skips there.
- The three tests `-short` skips but CI runs, separately and by name: `TestTPBLive`,
  `TestYTSLiveSearchInterstellar`, `TestFindMovieGivesUpOnAWedgedJellyfin` — all ok.
- `go test -race -count=1 -skip TestLiveEngineOverTunnel ./internal/engine` — ok.

The honest proof is still owed and still needs a push: the bar is two consecutive green `check`
runs on `main`.

What the individual assertions bought:

- **`TestDownloadThroughANetwork`, `TestAUDPTrackerDoesNotTakeTheProcessDown` and
  `TestTheClientIsToldWhichFamiliesTheNetworkCarries` all still pass.** These are the three that
  exercise `bindConfig`, and the second is the sharp one: it fails immediately if the hermetic
  config oversteps into `DisableTrackers`, and it is also the test that proves the empty
  `DhtStartingNodes` did not take a tracker announcer down with it.
- **`internal/engine` wall clock, taken serially and with `-short`**, per `CLAUDE.md`'s warning
  that parallel timings are noise — and with the live tunnel test excluded, because on a machine
  with a `.env` it is inside the number and swamps it: **2.46 / 2.48 / 2.16 s before, 2.21 / 2.26 /
  2.12 s after**. No change, and that is the correct result rather than a disappointing one. Do not
  quote the 67 s → 21 s figure this task first produced; it was one variable tunnel download.
- **`GOMAXPROCS=2 go test -race -run TestDeleteTorrentRefusesAnotherCategory -count 20
  ./internal/engine` — clean 20, in 3.698 s.** Evidence, not proof: this flake has never once
  reproduced off a runner.
- **The dump was read, not assumed.** Both branches were forced with a throwaway test and a
  shortened deadline, and this is what a next session will actually get:

  ```
  awaited: peers active=0 seeders=0 halfopen=0 pending=0 | pieces complete=0 | read data=0 metadata-chunks=0 wasted-chunks=0 bad-pieces=0 | no info dict yet, so nothing has been asked for
  peer 0:  peers active=0 seeders=0 halfopen=0 pending=0 | pieces complete=32 | read data=0 metadata-chunks=0 wasted-chunks=0 bad-pieces=0 | piece0 priority=none ok=true complete=true | runs 32C
  ```

- **`TestLiveDownloadPeakRSS` and `TestLiveEngineOverTunnel` still reach a swarm** — not run here,
  because one needs 755 MB from strangers and the other needs a NordVPN credential. `make live` and
  `make live-rss` are the commands, and this is the part of the verification that is owed rather
  than done.

## Open, and deliberately not settled here

- **`cc.Logger.FilterLevel(anacrolixQuiet)` at `engine.go:207` is inert, and its comment is false.**
  Measured above. anacrolix has therefore been writing every warning and error to stderr from every
  curator process and every test run since phase 6, unattributed and unfiltered, and the line that
  claims to discard it does nothing. Fixing it is a one-line change —
  `analog.Default.WithFilterLevel(...)` starts from a `nonZero` logger — but *which* level is the
  argument: `Critical` honours the existing comment and would hide exactly the
  `"error getting piece completion"` line this task used as evidence, while `Error` keeps it. That
  is a decision about what a user reporting a stuck download can paste, and it does not belong in a
  flake fix.
- **The T74 signature reproduces on a real swarm, on `main`, with no CI involved.** On 2026-08-18
  `TestLiveEngineOverTunnel` failed at 301 s having taken metadata through the tunnel in 4.03 s and
  then moved **0.00 MB from 3–4 connected peers for the next five minutes** — metadata in, peers
  up, zero payload, exactly what run `32094278975` printed. It fails the same way on unmodified
  `main` (301.28 s, metadata in 6.126 s), so it is not this change, and it had passed in 12.02 s
  earlier the same afternoon, so it is not permanent either. This is the strongest evidence
  available that T74 is not a runner artifact. It is also why `make check` is not a gate anybody
  with a `.env` can trust: it runs a live test against a real VPN and a real swarm, and CI does not.
  That live test does not use `await`, so the new diagnostic does not reach it — wiring it in is
  the obvious next move and is not this task's.
- **`docs/progress.md:926-933`'s tunnel race reproduced, and its count should be updated.** That
  note records the `netstack.(*netTun).Close()` / gvisor `WriteNotify` race as *"seen once under a
  full-suite run and not reproduced in six targeted runs"*. It fired here on 2026-08-18, once in
  roughly ten full-suite `go test -race ./internal/engine` runs, with the same two stacks and the
  same trigger — `TestLiveEngineOverTunnel`'s `t.Cleanup` closing the tunnel while a DHT query is
  still writing through it. Still third-party, still only when `.env` supplies a `VPN_CONFIG_FILE`,
  and still not this task's. But it is now twice, not once, and it means a developer running
  `make check` with a `.env` has a flake CI does not.
- **`go test -race` does not test the stack that ships.** `-race` requires cgo; under cgo anacrolix
  selects C libutp and SQLite piece completion, while production is `CGO_ENABLED=0`
  (`Dockerfile:64`) and gets pure-Go uTP and boltdb. `engine.go:17-25` already calls
  `CGO_ENABLED=0` load-bearing. That the race-detector run cannot exercise the shipped storage is a
  real finding and its own argument.
- **A disallowed data download is unrecoverable in production.** Nothing calls
  `AllowDataDownload()`, so if anacrolix ever does set the flag, `describe()` reports `downloading`
  for five minutes and then `stalled` with *"peers are connected but none of them is sending
  data"* — true, and completely misleading. A `PieceState(0).Priority == PiecePriorityNone` check
  inside `stalled()` would let the reason say what actually happened.
- **`TestYTSLiveSearchInterstellar` has no status escape hatch.** It classifies transport errors
  only (`yts_test.go:486-491`); a 403 from `movies-api.accel.li` would fail CI exactly as apibay
  did, with T73's fix sitting one file away and not applicable.
- **`release.yml` has no `concurrency:` block** where `check.yml:23-28` has one. Two tags pushed
  close together would both build and race to move `:latest`.
