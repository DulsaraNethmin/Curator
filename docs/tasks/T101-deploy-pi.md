# T101 — `make deploy-pi`, which verifies before and rolls back after

**Owns** the Pi deploy · **takes**
[D53](../decisions.md#d53--the-pi-deploy-is-a-script-run-from-a-laptop-not-a-github-workflow) ·
**does not replace** [D44](../decisions.md#d44--curator-reads-the-version-something-else-installs-it),
the in-app updater · **bound by** [D43](../decisions.md#d43--the-pi-is-a-clean-slate-television-is-retired-and-curator-is-the-only-downloader)'s
six containers, of which this may touch exactly two

## What it owns

```
$ make deploy-pi DRY_RUN=1
  deploying curator:0.5.0 to pi
  manifest   arm64 present for curator:0.5.0
  release    run for v0.5.0 is green
  currently  0.5.0 (ghcr.io/dulsaranethmin/curator:0.5.0)
  rollback   ghcr.io/dulsaranethmin/curator:0.5.0 is still on the box
  ! 2 download(s) in flight — recreating curator interrupts them until they resume.

  DRY RUN — every check passed and nothing was changed.
```

`scripts/deploy-pi.sh` — the whole thing. `Makefile` — the target.

Overridable: `PI`, `PORT`, `REMOTE`, `PROFILES`, `SERVICE`, `CONTAINER`, `TIMEOUT`, `DRY_RUN`,
`SKIP_RELEASE_CHECK`.

## Why it is not a GitHub workflow

D53 has the argument. The short version is that the Pi is behind NAT with an **outbound-only**
NordVPN tunnel — the `100.x` address on it is `nordlynx` and looks like Tailscale but is not — so a
hosted runner has no route, and the self-hosted runner that would work is refused because **this
repository is public**: a fork's pull request can run code on it, and the box holds the media, the
Jellyfin server D43 protects, and the NordVPN credentials.

## MEASURED, 2026-08-21 — against the real Pi

```
0.5.0 pull on the Pi           18.97 s   (arm64, cold)
container answering /healthz    ~1 s
container reporting healthy      6 s
```

`TIMEOUT` defaults to **90 s** against that 1 s: a runaway backstop, not a target.

**The manifest guard was verified by firing it.** Pinning `9.9.9` and dry-running:

```
make deploy-pi: no manifest for curator:9.9.9 at ghcr.io.
  Registry said: manifest unknown
EXIT=1
```

Nothing on the Pi was touched. The version-mismatch and uncommitted-changes warnings both fired in
the same run.

## TRAPS

**`manifest unknown` means two different things** and they are indistinguishable at the Pi: a tag
that does not exist, and a tag whose release run has not finished building. `compose.pi.yaml`'s own
comment warned that a healthy box gets diagnosed for a missing letter it does not have. Checking
from the laptop is what separates them, and the check is for the **arm64** half specifically —
an amd64-only manifest pulls happily on a laptop and fails on the Pi.

**Wait for the VERSION, not for a 200.** The old container answers `/healthz` perfectly well, so a
deploy that silently did nothing looks identical to one that worked. Then wait for the
**healthcheck** too: a curator answering `/healthz` while its healthcheck fails is a box that looks
fine and is not.

**`rollback()` is defined before its first caller, and that is not style.** Bash needs the
definition to have *executed*, not merely to exist further down the file — a function defined after
the line that calls it does not exist yet, and the recovery path would die with
`rollback: command not found` at the exact moment it is needed.

**Both compose files are shipped and backed up, not just `compose.pi.yaml`.** The Pi's
`compose.yaml` was three days stale at the 0.5.0 deploy — missing the entire `updater` service and
curator's `com.centurylinklabs.watchtower.scope` label from T80 — and nobody had noticed, because
`compose.pi.yaml` supplied everything that was actually being used.

**A rollback needs the old image to still be on the box.** Watchtower's `--cleanup` elsewhere on the
Pi removes images it replaces. The script says whether the previous image is present *before* it
starts, rather than discovering it in the recovery path.

## Deliberately not done

- **Moving the pin.** `compose.pi.yaml` is the input. The pin moves on a `deploy-N-to-the-pi` branch
  somebody reviews, exactly as `release-N` moves the constant — the shape 0.4.0 and 0.5.0 both used.
- **Anything automatic.** D44 refused unattended updating on a timer; this refuses it again by being
  a command somebody runs.
- **Making D44's Update button work on this Pi.** It cannot, as configured: watchtower re-pulls the
  same tag and the Pi pins an exact version so the host's own watchtower cannot take curator.
  Fixing it means a floating tag, which is the protection the pin exists to provide. Left alone.
- **Deploying minter.** `PROFILES` defaults to `--profile 1337x` so minter is in the project, but
  only `curator` is pulled and recreated. minter has its own image and its own cadence.
- **A second box.** `PI` is overridable and nothing else assumes one host, but this has only ever
  been run against one.
