# T80 — updating curator from the app

**Owns:** the Updates card, the release check behind it, and the updater that installs
**Settled by:** [D44](../decisions.md#d44--curator-reads-the-version-something-else-installs-it)
**Asked for** after [T53](T53-run-alongside.md) put curator on a Pi and
[T79](T79-the-download-button.md) produced a fix that could not reach it

## Why now

T79 fixed a button that had never worked in a browser. The fix is compiled into the binary, the Pi
runs a published image, and there was **no mechanism to get one to the other** — no decision, no
document, nothing in the repository about updating at all. A project that ships an image needs an
answer to "how does the box get the next one" before it has users, not after.

## What was built

**`internal/update`** — a `Checker` that reads the release feed and a `Updater` that asks something
else to install. Neither touches Docker. The package comment carries the reason.

**`GET /api/update`** returns current version, latest, notes, whether an update is available,
whether anything here can install it, the command if not, and whether a download is in flight.
**`POST /api/update`** triggers, and answers **202 `restarting`** — not 200, because the work belongs
to something else and this process is about to be replaced by it.

**The Updates card** on Settings, which renders three states rather than one: a button, a command, or
"this is the newest release" / "checks are off".

**An `updater` profile** in `compose.yaml`: watchtower with `--http-api-periodic-polls=false`, scoped
to curator, with a mandatory token.

**A GitHub Release step** in `release.yml`, without which none of the above could ever have worked.

## Traps, all three found by running it

- **A probe that performs the action.** watchtower's `/v1/update` is a GET. The first version pinged
  it to decide whether to show the button, which on a tokenless updater would have pulled and
  restarted curator every time the Settings page loaded. Removed rather than made cleverer; there is
  no safe endpoint to probe. Pinned by `TestProbingIsRefusedBecauseTheUpdateEndpointIsAGet`.
- **`0.10.0` sorts before `0.9.0`.** A string compare would silently stop offering updates at the
  tenth release of a 0.x line. `Newer` parses; `TestTenIsNewerThanNineEvenThoughItSortsBefore` pins it.
- **The git tag and the image tag differ by a `v`.** `release.yml:64` strips it, so the feed says
  `v0.1.0` while the binary says `0.1.0`. Compared naively, every install believes it is behind
  itself, forever.

## Verify

- `GET /api/update` on an install with an updater: `available`, `can_install`, notes, and repeated
  page loads that **never contact the updater** — checked with a request counter
- `POST /api/update` → 202, and the updater receives exactly one request carrying `Bearer <token>`
- with no `UPDATER_URL`: `can_install` false, `POST` → **409** naming the command to paste
- an unreleased repository (feed 404) reports "no releases have been published yet", not a fault

## Not done here

**The Pi is not wired to an updater yet.** It runs the published `0.1.0`, which predates all of this,
and its watchtower belongs to the `/opt/docker` project with no HTTP API. Until then the Pi's card
correctly shows the command rather than a button. Wiring it is [T54](T54-remove-what-is-replaced.md)'s
neighbourhood: that task is already rebuilding what runs on the box, and watchtower is in the four
D43 keeps.
