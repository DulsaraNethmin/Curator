# T48 — the release pipeline

**Owns:** `.github/workflows/`
**Depends on:** [T47](T47-image.md)

## Goal

A tag produces a multi-arch image at `ghcr.io/dulsaranethmin/curator` that the compose file can name,
and every push runs the gate this repository already has.

`docs/progress.md:28` names three things for this phase — *"the image, the release pipeline, minter
on demand"*. This is the middle one. **There is no CI in this repository at all** — checked — so
`make check` has only ever run where somebody remembered to type it.

## Do

1. **Two workflows, because they answer different questions.** `check` on push and pull request;
   `release` on a `v*` tag. A single workflow that builds an image on every push spends most of its
   life producing artifacts nobody pulls.

2. **`check` runs `make check` and nothing else.** The gate is already defined —
   npm export, `go build`, `go vet`, `go test -race`, arm64 cross-compile — and CI's job is to run
   the repository's gate, not to grow a second definition of it that drifts. Node and Go both need
   setting up; the Makefile does the rest.

3. **`release` builds with buildx for `linux/amd64,linux/arm64`** and pushes to GHCR with the tag,
   the major.minor, and `latest`. `GITHUB_TOKEN` with `packages: write` — no PAT.

4. **The compose file names a tag it can actually get.** `latest` on the first release, and
   [T51](T51-documents.md) is where the README's quickstart stops being aspirational. Until an image
   exists, `docker compose up -d` fails on a pull, which is a worse first experience than no
   quickstart at all.

5. **Cache the Go build and the npm install**, or every run pays for a fresh `node_modules` and a
   cold module cache. `actions/setup-go` and `actions/setup-node` both do it with one line.

## Do not

- **Run the live tests in CI.** `make live` brings up a real WireGuard tunnel against a real NordVPN
  endpoint with credentials from `.env`. Those are `TestLive*`, they are excluded from the default
  run by design, and they need secrets and a network CI has no business having.
- **Treat a `TestLiveEngineOverTunnel` failure as a signal**, if it ever does run. There is a known
  **intermittent pre-existing race** there, inside `wireguard/tun/netstack.(*netTun).Close()` racing
  gvisor's `WriteNotify` — seen once under the full suite and not reproduced in six targeted runs
  across two commits. Re-run before believing it.
- **Publish on every push to `main`.** A tag is a decision; a push is a Tuesday.
- **Add a second lint** — `golangci-lint`, a formatter check, a coverage gate. `go vet` is what this
  repository has agreed to, and CI is not the place to introduce a new standard that will fail on
  code nobody is looking at.
- **Auto-merge, auto-tag or auto-bump anything.** Pushing and merging are Nethmin's, everywhere,
  including here.

## Verify

- `check` fails on a deliberately broken commit — a `go vet` error and a failing test, separately —
  and passes on `main`
- a tag produces an image, and `docker manifest inspect` shows both platforms
- `docker run` of the **published** image serves the UI, from a machine that never built it
- the run time is recorded, because `go test -race` plus an npm build is the number that decides
  whether anyone keeps this enabled
