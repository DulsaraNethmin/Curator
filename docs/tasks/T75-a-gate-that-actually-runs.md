# T75 — a gate that runs the tests it reports on

**Owns:** the `race` target (`Makefile:40`) and the paragraph in `CLAUDE.md` that already warned
about this cache without noticing it applied to CI
**Depends on:** nothing. It was found while trying to collect [T74](T74-no-network-beyond-loopback.md)'s
second green run, and it is the reason that run does not count

## Goal

`make check` cannot report a pass for tests it did not execute.

## What happened

[T74](T74-no-network-beyond-loopback.md) set its bar at two consecutive green `check` runs on
`main`. The first went green — run `32102797120`, 7m15s, `internal/engine` in 3.547 s. Re-running
the same commit to collect the second sample produced this:

```
ok  github.com/DulsaraNethmin/curator/internal/engine  (cached)
```

**Measured 2026-08-18:** the re-run passed in **1m58s** against the **7m15s** of the run that
actually tested, and `(cached)` was returned for **thirteen of twenty packages** — `internal/api`,
`internal/indexer`, `internal/library`, `internal/tmdb`, `internal/web` and `cmd/curator` ran; the
rest, `internal/engine` among them, did not. The workflow log says where it came from:

```
Cache restored from key: setup-go-Linux-x64-ubuntu24-go-1.25.4-686ea1553a…
```

`actions/setup-go@v7` restores `$GOCACHE`, and Go's test cache lives inside it and is keyed on the
test binary and its inputs rather than on the run. `make race` is a bare `go test -race ./...`, so
it took the cache at its word.

So T74's second sample is void, and no amount of re-running would ever produce one: every attempt
restores the same cache and skips the same tests. That is the immediate cost. The real one is
larger.

## It is not "a slow gate got faster"

`release.yml:45` is `needs: check`, and `check.yml`'s own header calls this the thing that buys
*"that `latest` cannot be a commit that fails vet or the race detector, on an image whose whole
promise is that a stranger can run it."* A gate that can be green without executing does not buy
that. It buys the claim that some earlier commit, on some earlier runner, with inputs Go judged
equivalent, was green once.

For most of this repo that equivalence is sound — Go's cache is careful, and a package whose
sources and dependencies have not changed genuinely does not need re-running to prove the same
thing. **But the entire subject of T74 is a test that passes half the time on identical input.**
Caching is exactly wrong for that class of failure: the one flake this repo has ever had is
invisible to a cached run by construction, because the cache's whole premise is that the same
inputs give the same answer, and the flake is the counterexample.

`CLAUDE.md:186-190` has warned about this cache since phase 7 — *"machine-global and
content-keyed, so one agent's `go test ./...` can return another's cached results in milliseconds
and look like a fast run"* — in the section on running agents in parallel. Nobody noticed the same
sentence describes a runner.

## Do

1. **Pass `-count=1` in `make race`.** It is the documented way to bypass the test result cache,
   and it leaves the *build* cache alone, so compilation is still incremental and `setup-go`'s
   restore still earns its keep.
2. **Say why, at the target**, with the two numbers, so nobody deletes it to make `make check`
   faster.
3. **Extend `CLAUDE.md`'s existing cache paragraph** to say the trap also applied to CI, and that
   `make test` is deliberately still cached.

## Do not

- **Put `-count=1` on `make test`.** That is the fast iteration run and cache reuse is the point of
  it. The gate is `race`, and only the gate needs to distrust the cache.
- **Add `go clean -testcache` to the workflow instead.** It would throw away the build cache's
  neighbour for no extra safety, make every run a cold compile, and put the fix in CI where a
  developer running `make check` locally would not get it. The gate should mean the same thing in
  both places.
- **Disable `setup-go`'s caching.** The build cache is worth minutes per run and was never the
  problem.
- **Read T74's green run as void too.** Run `32102797120`'s first attempt genuinely executed —
  `internal/engine` in 3.547 s, not `(cached)` — as did both halves of the original red/green pair
  that motivated T74 (`32094278975` at 61.691 s, `32094325056` at 5.022 s). Exactly one post-fix
  sample is real, and one is not two.

## Verify

`make check`, which now takes as long as it should.

- **`make race` executes every package.** The `(cached)` marker is absent from the output, locally
  and on a runner. That is the whole assertion, and it is visible in the log rather than inferred.
- **`make test` still caches**, so the fast loop stays fast. A second `make test` with no edits is
  still instant.
- **T74's bar can now be met by re-running**, which is what it should have meant all along: two
  green `check` runs on the same commit are two genuine samples once the cache cannot answer for
  either.

`make check` itself is still red here, for two reasons that are both older than this change and
neither of which is `-count=1`:

- **`TestLiveEngineOverTunnel`**, which fails the same way on `main` and which CI never runs. See
  [T74](T74-no-network-beyond-loopback.md)'s open questions.
- **`TestTPBLive`**, and this one is worth its own task. It failed at `tpb_test.go:546` with
  `context deadline exceeded (Client.Timeout exceeded while awaiting headers)` and passed on the
  next attempt. **T73's classifier is one call too early.** It reads the *probe*'s status —
  transport error skips, 403/401/429 skips, other non-200 fails — and then `SearchMovie` at
  `tpb_test.go:545` does `t.Fatalf` on *any* error, transport failures included. A probe can answer
  200 and the search that follows can still time out, which is what happened. By `yts_test.go:486`'s
  own rule — *"only a transport failure is 'no network'"* — that should have skipped.

This change makes the second one more visible rather than more likely: the gate now runs that live
test every time instead of sometimes answering from cache. That is the point of the change, and it
is also the argument for fixing the classifier next.
