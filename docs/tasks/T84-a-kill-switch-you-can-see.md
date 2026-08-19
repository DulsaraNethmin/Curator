# T84 — a kill switch you can see

**Owns:** the screen the guarantee is read off, and making `VPN_REQUIRED` apply without a restart
**Settled by:** [D47](../decisions.md#d47--every-torrent-network-operation-is-tunnel-bound-or-disabled),
amending [D29](../decisions.md#d29--a-written-setting-applies-at-the-next-start-the-password-applies-at-once)
**Follows** [T82](T82-a-kill-switch-that-can-be-proved.md) and
[T83](T83-a-tunnel-that-is-watched.md), which made something worth showing

## Why now

The requirement was never only that downloads are protected — it is that somebody can **see** that
they are. Before this, the only evidence a person could reach was a green "reachable" cell in the
settings integrations table, which meant the device had handshaken once.

## What was built

**`GET /api/vpn` and `POST /api/vpn/check`**, on `update.go`'s shape. Always 200, because the state
is the answer; a process with no wiring at all is the single 503.

`protected` is an **AND of four independently-read facts** and not `state == up` dressed up: a fresh
handshake, the engine's socket inside the tunnel, traffic leaving from somewhere else, and nothing
held. A tunnel up while the engine's socket sits outside it is exactly the disagreement worth
showing.

**The exit address does not leave the process without a password in front of it.** This endpoint has
no authentication by default, like `GET /api/logs`, and where traffic leaves from is the one fact the
tunnel exists to keep ([D18](../decisions.md#d18--the-log-tail-is-readable-without-authentication-so-it-is-redacted-at-the-source)).
The masked form (`203.0.113.x`) and the boolean are always present; the address is gated on
`Auth.Enforcing()` — the live answer, not the stored setting, because `auth.install` refuses to
enforce a switch that is on with no password behind it.

**`?subsystem=` on `GET /api/logs`**, so the screen shows its own tail through the buffer and
endpoint that already exist. `log.With("subsystem", "vpn")` is the entire producing side.

**`VPN_REQUIRED` becomes `Immediate`**, with `vpn.Enforced` reading it per dispatch and
`api.AccessFunc` composing two immediate subsystems behind the one hook.

**The screen**: `web/app/vpn/page.tsx` plus five components, `logline.tsx` extracted from the Logs
screen rather than copied.

## Traps

- **The lying checkbox, and the fix is not where it looks.** `settingsStates` reported an Immediate
  setting's live value as the *stored* text. Right for the two authentication settings, wrong for this
  one — and the difference was never "immediate", it is whether there is a `*config.Config` field to
  read the value back from. With nothing stored the resolution is `""`, and `VPN_REQUIRED` defaults to
  **true**, so a fresh install would have drawn the kill switch **off** while it was being enforced.
- **`main_test.go`'s skip had to move to the same map.** Left on `item.Immediate` it would have
  silently dropped `vpn_required` out of `TestEverySettingHasAnEffectiveValue` — the very check that
  catches a setting with no line in `effective()`. Either half alone still passes; the test
  reintroduces both.
- **Enforcement must resolve through `config.Load`, not the stored string.** `ParseBool("")` is false,
  and clearing the setting is how somebody returns to the default.
- **The toggle applies to the check and cannot conjure an engine.** A process that booted with no
  tunnel and enforcement on has none, so turning it off there still needs a restart. Reported as
  `engine_started`.
- **The environment wins over the settings table.** With `VPN_REQUIRED` in the environment a write
  answers 409, so the switch is drawn read-only and names the variable — the shape the settings screen
  has used for shadowed fields since phase 7. **Found by clicking it**, not by a test.
- **`vpn.Advisory` is deleted.** It downgraded a refusal to a warning, but was chosen at start-up and
  wrapped the qBittorrent branch only — so `VPN_REQUIRED=false` with a tunnel configured still refused
  a dispatch through a broken one, on the default backend.

## Verify

- `TestTheExitAddressIsWithheldUnlessAPasswordIsInFrontOfIt` — three cases including the
  configured-but-not-enforced one, and it asserts the address appears **nowhere** in the body.
- `TestProtectedIsAnAndOfEveryFactRatherThanARestatementOfOne`, `TestEveryTunnelStateIsA200`,
  `TestTheForcedCheckIsRateLimited`, `TestAFailedCheckIsStillA200`.
- `TestAnImmediateSettingWithAConfigFieldReportsItsResolvedDefault`,
  `TestTheKillSwitchIsReadLiveRatherThanAtStartUp`, `TestTheLogTailCanBeFilteredToOneSubsystem`.
- **Run against the real NordVPN tunnel**, on its own port, 8090 untouched: boot with no handshake
  yet holds downloads and says so; the handshake lands and the next tick releases them;
  `inside_tunnel` true from two independent reads; the exit address absent with no password;
  `PUT vpn_required=false` changes what the next dispatch does with **no restart**, and
  `/api/settings` reports `restart_required: false` with nothing pending.

## Not done here

**The page reports; it does not capture.** Everything on it is curator's own reading of curator.
Proving the claim from outside the process is [T85](T85-the-capture-that-settles-it.md).
