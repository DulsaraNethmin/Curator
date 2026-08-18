# T77 — a dead host fails loudly, once something proves the machine is online

**Owns:** the name half of the shared live-indexer classifier (`internal/indexer/live_test.go`), the
two live tests that pass it a control name, and the four comments that claimed a protection that did
not exist
**Depends on:** nothing. It closes the gap [T76](T76-a-skip-that-covers-the-call.md) measured and
deliberately left open, and it is the reason [D42](../decisions.md#d42) exists

## Goal

A base URL that has gone NXDOMAIN fails the build — which is what
[D12](../decisions.md#d12--yts-is-reached-at-movies-apiaccelli-not-ytsmx) has been assumed to
guarantee since it was written, and did not.

## What happened

T76 unified the skip/fail rule and then measured its own hole. `classifyLiveFailure` asked the
transport question — *was anything answered?* — and a dead name answers nothing, so it skipped:

```
yts.mx  NXDOMAIN  →  *net.DNSError{IsNotFound:true}  →  is a net.Error  →  skip
```

**D12's own failure went green under the guard D12 paid for.** `yts.mx` is the host D12 was written
about, it is still NXDOMAIN, and a live search against it passed the suite by being skipped.

Four comments asserted otherwise, and all four were wrong in the same direction: `yts_test.go`'s
*"that is how a dead base URL stays green for a week"*, `check.yml`'s *"they are what catches a dead
base URL, which D12 paid for once already"*, `CLAUDE.md`'s summary of the T76 rule, and
`tpb_test.go`'s docstring.

## Why this was not a one-line `IsNotFound` check

The obvious fix is `if dnsErr.IsNotFound { t.Fatalf(...) }`, and on Linux it is correct. **On darwin
it is not, and that is measured rather than argued.**

Go maps both `EAI_NONAME` and `EAI_NODATA` to `errNoSuchHost` (`net/cgo_unix.go:189`), and macOS's
`getaddrinfo` answers that way when there is no network at all. So on a laptop with the WiFi off,
*"this host is gone"* and *"this machine is offline"* are the **same error value** — and the live
tests exist under the opposite promise, that an offline machine does not fail the build.

Measured 2026-08-18, which is what makes the two separable *at all*:

| case | `*net.DNSError` | `IsNotFound` | `IsTemporary` |
|---|---|---|---|
| `yts.mx`, real NXDOMAIN (cgo **and** pure-Go) | yes | **true** | false |
| offline — resolver unreachable, pure-Go | yes | **false** | true |
| offline — resolver refuses, pure-Go | yes | **false** | true |
| offline — darwin `getaddrinfo` | yes | **true** | false |
| connection refused | **no** | — | — |

`IsNotFound` is therefore the right discriminator and an insufficient one. What separates the last
two rows is not the error: it is **whether anything else resolves**.

## The control is the other indexer, so no third party enters the suite

`classifyLiveFailure` takes a `dnsWorks func() bool`, and asks it on exactly one branch. Each live
test passes the **other** indexer's host — `TestTPBLive` asks `movies-api.accel.li`,
`TestYTSLiveSearchInterstellar` asks `apibay.org`. Two properties earned that choice:

- **A refused host still resolves.** apibay answers 403 to a GitHub address range
  ([T73](T73-ci-meets-a-blocked-apibay.md)) and its *name* resolves from that same runner, so the
  control survives the one case CI actually hits.
- **A single dead host is always caught by the test that is not it.** If apibay.org dies,
  `TestTPBLive` asks accel.li, gets an answer, and fails loudly. Only both hosts dying in one run
  hides either — and that is indistinguishable from an offline machine anyway.

## Do

1. **Ask the name question before the transport question.** A `*net.DNSError` *is* a `net.Error`, so
   the order is the fix, not an optimisation.
2. **Inject the control lookup as a func**, so both answers are assertable — a live test can only
   ever take the branch the machine it runs on gives it — and so the lookup happens only on the one
   branch that reads it.
3. **Fix the four comments.** A guard nobody trusts and a guard everybody over-trusts fail the same
   way.

## Do not

- **Trust `IsNotFound` alone.** See the darwin row above.
- **Add a third-party control host.** The sibling indexer is already a dependency of this suite;
  `one.one.one.one` would not be.
- **Fail on `IsTemporary`.** That is the offline machine, and it must stay a skip.
- **Skip on any non-200.** D12, still, and unchanged since T73.

## Verify

`go vet ./...` clean; `gofmt` clean on all three files.

- **Fifteen classifier branches pass**, including the four T77 adds: a dead name with DNS up
  (**fail**), a dead name with the control down (**skip**), an unreachable resolver with DNS up
  (**skip**), and a refusal outranking a name failure (**skip**).
- **`TestTheControlNameIsOnlyLookedUpForANameFailure`** pins the laziness: four verdicts reach an
  answer without a lookup, one asks.
- **`TestEachIndexerIsTheOthersControl`** pins the arrangement against the two constants, so it
  cannot silently stop being a cross-check.
- **Forced end to end through real `SearchMovie` calls**, measured 2026-08-18 — the top two rows are
  the whole task, and the first of them was `skip` before it:

  ```
  yts.mx + real control (apibay.org resolves)   status=0   verdict=fail   <-- WAS skip
  yts.mx + control down (offline machine)       status=0   verdict=skip
  tpb/yts 403 / 401 / 429                       status=4xx verdict=skip
  tpb/yts 500 / 404                             status=5xx verdict=fail
  tpb/yts dead port                             status=0   verdict=skip
  ```

- **Both live tests still run and still pass**, which is the assertion that matters — the fix must
  not turn a working live test into a permanent skip. Serially: `TestTPBLive` **1.30 s**,
  `TestYTSLiveSearchInterstellar` **0.51 s**, and YTS logged five real releases.

## Open, and deliberately not settled here

- **The darwin-offline row is inferred from `cgo_unix.go:189`, not measured with the WiFi off.** The
  control name makes it moot — that path skips either way — so the cost of being wrong about it is
  zero, which is why it was not worth taking the machine offline to confirm.
- **A control host that dies stops being a control silently.** The cross-check above bounds the
  damage to "both hosts dead in one run", and nothing asserts that today.
- **`release.yml` still has no `concurrency:` block**, where `check.yml:23-28` has one. Carried from
  T74 and T76, untouched.
