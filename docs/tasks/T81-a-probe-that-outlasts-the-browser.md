# T81 — a probe that outlasts the browser

**Owns:** the budget `GET /api/indexers/minter/probe` gives minter, and what a deadline is reported as
**Settled by:** [D46](../decisions.md#d46--a-probe-budget-belongs-to-the-service-being-probed)
**Asked for** by the handoff that closed [T51](T51-documents.md), which carried this as the one known
defect with no task number

## Why now

Every other task was done. This was the only outstanding item that a user could actually hit, and it
had been carried across handoffs as prose rather than work.

## The bug, which is two bugs

**The budget was below minter's healthy floor.** `probeTimeout` is 5 s, and the comment that sized it
is in `Minter.Probe`: *"a question /health answers in milliseconds"*. That sentence was never
measured. minter serves `/health` from the process that drives its browser and waits on the same
lock, so it answers when the browser is free rather than on demand.

Read out of minter's own HEALTHCHECK log on the Pi — two consecutive checks, both returning
`{"ok":true}`:

```
23:40:54 -> 23:41:02   8.61 s   {"ok":true,"version":"0.1.0","user_agent":"Mozilla/5.0 …Firefox/151.0"}
23:56:02 -> 23:56:09   6.73 s   {"ok":true,…}
```

So a **healthy** minter exceeded curator's budget on every check. minter's own HEALTHCHECK allows
`1m30s` for the identical call; curator allowed 5 s.

**And a deadline was reported as "nothing answered".** `indexer.unreachable{}` deliberately carries
`ErrUnreachable` *alongside* the transport error, so both `errors.Is` answers are truthful — its own
comment says so. `handleMinterProbe` tested `ErrUnreachable` first, so every timeout matched there.
The screen renders that state as **"minter is not running"** above `docker compose --profile 1337x up
-d`, which does nothing for a container that is already running. The handler's own comment on the
branch below it argues against exactly this: *"running the compose command again will not help an
address that already has something on it"*.

The second bug is the worse one, and the Pi proved it. minter was `Up 2 hours (unhealthy)` with a
wedged browser, and `/health` had stopped answering entirely — 30 s, three times, zero bytes, on a
port that still accepted the connection:

```
mint queued 854456ms behind an in-flight browser
```

No timeout value fixes that state. Only the report does.

## What was built

- **`minterProbeTimeout`, 20 s**, in `internal/api/indexers.go` — minter's own budget, not the
  integrations table's. `probeTimeout` stays 5 s and stays right for services that answer at once.
- **The deadline is tested before the sentinel**, and lands in `unhealthy` — which the screen renders
  as "minter is not ready … until it settles", with a detail naming the budget that was exceeded. The
  API contract is unchanged: still three states, still always 200.
- **One probe in flight at a time** in `web/components/settings/minter.tsx`. The interval is 5 s; a
  20 s budget without this would collect a request per tick against a minter already queued behind
  its browser — the one thing that makes the reported state worse.
- **The sentence that caused it is corrected** in `Minter.Probe`, with the measurement in its place.

## Traps

- **A budget sized from a comment rather than a measurement.** The comment was plausible, adjacent to
  the code, and wrong by three orders of magnitude.
- **A sentinel that is deliberately not exclusive.** `unreachable{}` matches two `errors.Is` checks on
  purpose, so a `switch` over it is order-dependent and silently so. Both files now say this at the
  point where it matters.
- **Polling harder at a service that is slow because it is busy.** Raising a timeout without an
  in-flight guard converts one late answer into a queue of them.

## Verify

- `TestABusyMinterIsNotReportedAsOneThatWasNeverStarted` — `errors.Join(DeadlineExceeded,
  ErrUnreachable)` reproduces the real error's shape; asserts the state is not `unreachable`, and
  that the detail neither says "nothing answered" nor omits the budget
- `TestTheProbeBudgetClearsMinterHealthyLatency` — pins the budget above the 8.61 s measured, as a
  floor rather than an exact value, and pins that it has not collapsed back into `probeTimeout`
- **both were reintroduced and seen to fail**: the original case order gives
  `state = "unreachable"`, and a 5 s budget gives `not above the 8.61s a HEALTHY minter was measured
  taking`

## Not done here

**minter's wedge is minter's bug, not curator's.** `curator-minter-1` on the Pi has an in-flight
browser that never finished and serialises everything behind it, `/health` included. curator's job is
to report that honestly, which it now does; making minter answer its health check while busy belongs
to the minter repository. The container was left running and untouched so the state survives for
whoever picks that up.
