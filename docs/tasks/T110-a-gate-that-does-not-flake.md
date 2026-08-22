# T110 — a gate that does not fail because the machine was busy

**Owns** one constant in `internal/remux`'s test helper · **found by** running the per-commit gate
across eleven commits in one session · **does not touch** the concurrency cap itself

## What was wrong

`waitFor` (`internal/remux/remux_test.go`) gave up after **5 seconds**. What it waits for is fake
ffmpeg processes to spawn and write a line to a file, and `make check` runs `go test -race` across
twenty packages at once — so that spawn is competing with everything else on the machine.

## MEASURED

Twice in one session, at commits that do not touch `internal/remux` at all — one touching only
`web/` and the `Makefile`, one only `internal/torrent`:

```
TestTheCapRefusesTheNextOneAndFreesItsSlot   FAIL   10.94 s
TestTheCapRefusesTheNextOneAndFreesItsSlot   FAIL   10.95 s

the same test, alone                         PASS    1.81 s   1.85 s   1.86 s
the same commit, full gate re-run            PASS
```

Roughly six times its usual runtime, which is `waitFor` hitting its deadline rather than the cap
misbehaving.

## Why 30 s weakens nothing

When the code under test is actually broken the condition **never** becomes true, so the test still
fails — it just takes longer to say so. The only thing traded is how long a genuine failure takes to
report. The thing bought is that a green gate means what it says: a gate that goes red because the
machine was busy is one people learn to re-run rather than believe, which is worse than a slow one.

The cap, its slot release and every assertion are untouched.

## Still outstanding

- **The other `waitFor` callers were not audited individually.** They all get the longer deadline,
  which is right for the same reason, but only this one has been observed to flake.
- CI has not been observed flaking on it. Both occurrences were on a laptop running the gate
  alongside a background curator and a headless Chrome, which is a heavier machine state than
  `actions/setup-go` produces.
