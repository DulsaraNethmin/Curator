# T76 — a skip that covers the call, not just the probe

**Owns:** the shared live-indexer classifier (`internal/indexer/live_test.go`, new) and the two live
tests that now ask it — `TestTPBLive` (`internal/indexer/tpb_test.go`) and
`TestYTSLiveSearchInterstellar` (`internal/indexer/yts_test.go`)
**Depends on:** nothing. It finishes what [T73](T73-ci-meets-a-blocked-apibay.md) started and what
[T75](T75-a-gate-that-actually-runs.md) found was unfinished

## Goal

The two live indexer tests — the only tests in this repo that reach the public internet with no
credential to skip on, so the only ones that run on every runner — apply one rule, to the call.

## What happened

Each test had one half of the rule, and each was exposed for the half it was missing.

**TPB had the status half and applied it one call too early.** T73 gave it a HEAD probe classified by
`refusedTheNetwork`: transport error skips, 403/401/429 skips, any other non-200 fails. The probe's
verdict does not bind the search that follows it. [T75](T75-a-gate-that-actually-runs.md) caught the
consequence — `tpb_test.go:546` failed with `context deadline exceeded (Client.Timeout exceeded while
awaiting headers)` and passed on the next attempt, because the search's error went straight to
`t.Fatalf` with no classification at all. A probe can answer 200 and the call after it can still time
out.

**YTS had the transport half and no status at all.** `yts_test.go:486-491` classified `net.Error` and
`*url.Error` and argued the rule correctly, but never read a status. This is
[T74](T74-no-network-beyond-loopback.md)'s open question verbatim: *"a 403 from
`movies-api.accel.li` would fail CI exactly as apibay did, with T73's fix sitting one file away and
not applicable."*

So the two tests disagreed about what a failure means, and the disagreement was the bug in both
directions.

## Why the status has to come from a transport, not from the error

Neither indexer returns a typed status error. Both format it into a string —
`tpb search %q: apibay returned %s` (`tpb.go:115`) and `yts search %q: unexpected status %s: %s`
(`yts.go:150`) — so `errors.As` cannot recover it and matching on the text would be pinning a message
nobody has agreed to keep.

`liveRecorder` is an `http.RoundTripper` that remembers the last status it saw, and `liveClient`
hands one to each live test. **Both indexers already take an `*http.Client`** (`NewTPB(c)`,
`NewYTS(httpClient, opts...)`), so this needs no production change at all — the alternative, giving
the indexers a typed status error, would change a shipped error contract for a test's benefit and
would deserve its own decision entry.

Its one non-obvious property is pinned by a test: a request that never gets a response resets the
status to 0, so a previous call's 200 cannot be read as this call's answer.

## The probe is gone, and that is the point

`TestTPBLive` no longer sends a HEAD probe. Once the call itself is classified, the probe answers a
question nobody needs: **the search is its own probe.** Nothing is lost — a 500 on the probe used to
fail with *"neither a working apibay nor a refused network"*, and a 500 on the search now fails
through `classifyLiveFailure` with the same verdict. What is gained is that there is one place where
the decision is made instead of two that can disagree.

`refusedTheNetwork` survives unchanged. It moved to `live_test.go`, and its test moved with it under
the name `TestARefusedNetworkIsNotABrokenIndexer` — it is no longer about apibay.

## Do

1. **One classifier, in one file**, `classifyLiveFailure(err, status) (liveVerdict, string)`, pure so
   every branch can be asserted — a live test cannot assert its own skip, because it takes whichever
   branch the network gives it.
2. **Apply it to every live search**, including `TestTPBLive`'s second one (the absurd-query sentinel
   check), which had the same bare `t.Fatalf`.
3. **Default to failing.** `liveFail` is the zero value on purpose: a failure becomes a skip only by
   matching a named rule.

## Do not

- **Skip on any non-200.** D12, still. Every non-200 outside 403/401/429 stays loud.
- **Give the indexers a typed status error** to make the status readable. See above.
- **Run CI with `-short`.** T73 and T74 both refused it, for the reason that still holds.
- **Close the NXDOMAIN gap here.** See below — it is a decision about whose build goes red.

## Verify

`go vet ./...` clean; `gofmt` clean on all three files.

- **The classifier's own tests pass, every branch**, including the two the old code got wrong: a
  transport failure after a 200 probe (T75's case) and a refused status reaching YTS (T74's).
- **Both live tests still run and still pass**, which is the assertion that matters — the fix must
  not turn a working live test into a permanent skip. Measured 2026-08-18, serially:
  `TestTPBLive` **1.48 s**, `TestYTSLiveSearchInterstellar` **0.66 s**, package **3.511 s**. YTS
  logged five real releases, apibay answered both searches.
- **Every branch was forced end to end**, through real `SearchMovie` calls against an `httptest`
  server rather than through the pure function, and both indexers now answer identically:

  ```
  tpb/yts Forbidden            status=403 verdict=skip
  tpb/yts Unauthorized         status=401 verdict=skip
  tpb/yts Too Many Requests    status=429 verdict=skip
  tpb/yts Internal Server Err  status=500 verdict=fail
  tpb/yts Not Found            status=404 verdict=fail
  tpb/yts dead port            status=0   verdict=skip
  yts     NXDOMAIN (yts.mx)    status=0   verdict=skip   <-- the known gap, below
  ```

## Open, and deliberately not settled here

- **A dead base URL does NOT fail loudly, and never has.** This is the constraint T73, T74 and this
  task all named, and measuring it was this task's most useful finding: **`yts.mx` still does not
  resolve, and a live search against it SKIPS.** The failure is
  `*net.DNSError{Err:"no such host", IsNotFound:true}`, which is a `net.Error`, so the transport rule
  — the rule `yts_test.go` wrote to prevent exactly this — calls it "no network". D12's own failure
  would go green today, under the guard that exists because of D12.

  It is fixable and it is not mechanical. NXDOMAIN is distinguishable: measured 2026-08-18, a dead
  name gives `*net.DNSError` with `IsNotFound=true` while connection-refused is not a `*net.DNSError`
  at all. But "this host is gone" and "this machine is offline" produce the same error, so making
  NXDOMAIN fail turns a developer on a plane red, and the live tests exist under the opposite promise
  — *"so an offline machine does not fail the build."* Separating them needs a control host that
  proves the network works, which is a new external dependency in a test, or a rule that says CI and
  a laptop answer differently. That is a decision, and `docs/decisions.md` is where it goes.
- **`release.yml` still has no `concurrency:` block**, where `check.yml:23-28` has one. Carried from
  T74, untouched.
