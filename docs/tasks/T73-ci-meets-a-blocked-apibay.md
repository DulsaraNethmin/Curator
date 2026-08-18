# T73 — CI meets an apibay that will not serve it

**Owns:** `TestTPBLive`'s reachability probe (`internal/indexer/tpb_test.go`) and the classifier it
now asks
**Depends on:** nothing. It is the first thing the release pipeline found when it finally ran

## Goal

`make check` can pass on a GitHub Actions runner. Until it does, no tag publishes an image, and
[T51](T51-documents.md) and phase 9's stranger test stay blocked behind it.

## What happened

The first three workflow runs this project has ever had all failed, and all three failed the same
way — `check` is a dependency of `image`, so nothing downstream of it ran:

```
--- FAIL: TestTPBLive (0.42s)
    tpb_test.go:533: live SearchMovie: tpb search "Interstellar": apibay returned 403 Forbidden
make: *** [Makefile:40: race] Error 1
```

**Measured 2026-08-18: apibay answers `200` from a home connection and `403` from a GitHub Actions
runner.** It is an address-range decision, not an outage — YTS's live test hits the network from the
same runner and passes. So `make check` was green on the laptop and could never go green in CI.

## Why the existing guard did not catch it

`tpb_test.go:517-525` probes with a HEAD and skips if the client returns an error:

```go
resp, err := client.Do(req)
if err != nil {
	t.Skipf("apibay unreachable, skipping live test: %v", err)
}
resp.Body.Close()
```

**A 403 is a successful HTTP transaction.** `err` is nil, the guard passes, `resp.StatusCode` is
never read, and the test walks into the real search — which fails on the same 403 and calls
`t.Fatalf`. The guard checked that apibay *answered*, not that it answered *usefully*.

It is also worth naming why this one test was exposed at all. `CLAUDE.md` says all ten `TestLive*`
take their skip in CI because a runner has no `.env`. That is true and complete — those ten are in
`internal/jellyfin`, `internal/tmdb`, `internal/engine`, `internal/vpn` and `internal/qbit`, and each
gates on a credential. `TestTPBLive` and `TestYTSLiveSearchInterstellar` are a **second category the
document does not mention**: live tests for the two indexers that need no credentials, so there is no
missing variable to skip on. They gate on `-short`, and `make check` does not pass it.

## The distinction this must not lose

`yts_test.go:486-488` already argued the opposite case, and it is right:

> *Only a transport failure is "no network". A decode failure or a status YTS should not be returning
> is a real regression and must not be skipped past — that is how a dead base URL stays green for a
> week.*

That is [D12](../decisions.md#d12--yts-is-reached-at-movies-apiaccelli-not-ytsmx) paid for once
already, when `yts.mx` went NXDOMAIN. So "skip on any non-200" is exactly the wrong fix.

`403`, `401` and `429` are not that. They are decisions about **the caller**, and they say nothing
about whether curator can still parse what apibay returns. Every other non-200 keeps failing.

## Do

1. **Read the probe's status**, and split it three ways: transport error → skip (unchanged),
   caller refused → skip, anything else non-200 → **fail**.
2. **Put the split in a named function**, `refusedTheNetwork`, so the decision is a thing with a test
   rather than a condition inside a test that cannot assert its own skip.

## Do not

- **Skip on any non-200.** See above. A dead apibay must stay loud.
- **Run CI with `-short`.** It would fix this by silencing `TestYTSLiveSearchInterstellar` too, which
  is the test that would catch the next dead base URL. The failure here is one endpoint refusing one
  address range, not a reason to stop checking the network at all.
- **Delete the live test.** It is the only thing that reads apibay's real response shape, and
  `docs/progress.md` has carried "apibay.org still not re-checked" as an open question for weeks.
  This run is the re-check: it works, from a residential address.

## Verify

`make check`, which now passes here and — the point of the task — can pass on a runner.

- **`TestARefusedNetworkIsNotABrokenApibay`** pins both halves of the classifier: 403/401/429 are a
  refused caller, while 200/404/500/502/503 are not and must still fail the run.
- **`TestTPBLive` still runs and passes locally**, where apibay answers 200. The fix must not turn a
  working live test into a permanent skip.

The honest proof is the next tagged run going green, which needs a push.
