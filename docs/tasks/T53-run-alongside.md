# T53 — curator on the Pi, from an empty disk

**Owns:** the first curator instance that runs on the Pi, and the first film that arrives through it
**Depends on:** [T52](T52-arr-config-backup.md) (done — backup taken and restored). Shaped by
[D43](../decisions.md#d43--the-pi-is-a-clean-slate-television-is-retired-and-curator-is-the-only-downloader),
which replaced this task's original goal

## What this task used to be, and why it is not

It was *"run alongside radarr and prove parity — 29 movies, 16 with a file, from both."* That was the
only independent check that curator reads a real library correctly, and
[D43](../decisions.md#d43--the-pi-is-a-clean-slate-television-is-retired-and-curator-is-the-only-downloader)
deleted the library it would have checked against. **There is no parity target any more.** The
assertion is weaker now and it is labelled rather than quietly dropped.

## Goal

curator runs on the Pi, against an empty disk, and puts the first film on it — search, download,
import, play — with nothing from the \*arr stack involved.

## The starting state, measured 2026-08-18 after the wipe

```
/dev/sda1  916G  2.2M used  870G free  1%   /media/storage
  media/movies      empty, directory kept
  media/tv          empty, directory kept
  media/downloads   complete/ and incomplete/ empty, kept
```

The \*arr containers are still running and now point at nothing: radarr and sonarr will report every
item missing, and Jellyfin's libraries are empty. That is expected and is not a fault to chase — they
are removed in [T54](T54-remove-what-is-replaced.md).

## Settled before anything was deployed, 2026-08-18

Three things stood between this task and a deploy. Two are now answered and one needs an account
that no session has.

**The image could not be pulled at all.** `ghcr.io/dulsaranethmin/curator` is a **private** package:
an anonymous manifest request for `v0.1.0` answers `HTTP 403 DENIED`. The Pi is therefore a `docker
login` or a public package, and the decision was **public** — which also retires
[T51](T51-documents.md)'s blocker, since the README quickstart becomes something a stranger can
actually run. The repository itself stays private; package visibility is a separate switch, and
GitHub has no REST endpoint for it, so it is a manual click in the package settings.

**The tunnel is a wg-quick file, not a username and a password.** This task used to say the T52
backup's `NORDVPN_USERNAME` and `NORDVPN_PASSWORD` were what curator needed. They are not, and
[D43](../decisions.md#d43--the-pi-is-a-clean-slate-television-is-retired-and-curator-is-the-only-downloader)
carried the same error until it was corrected beside this. `internal/vpn/config.go`'s `validate()`
wants `PrivateKey`, peer `PublicKey` and `Endpoint`; gluetun derived those from the credentials and
curator has no equivalent step.

What the laptop already proves, measured rather than assumed: `wg0.conf`'s peer `PublicKey` is
**byte-identical** to the one NordVPN's public recommendations API hands out for its Singapore
group, and the endpoints are neighbours in `187.15.102.0/24`. So the peer key and the endpoint are
public facts this repository can re-derive at any time, and the **private key is the only part that
has to come from the account.**

That is also why the flap [CLAUDE.md](../../CLAUDE.md) warns about is avoidable without a second
key. A WireGuard session is per *(server, client key)* pair, so the laptop on one Singapore server
and the Pi on another are two independent sessions even if the key is shared — NordVPN issues one
NordLynx key per account, so a genuinely separate key may not be on offer. **The Pi must not use
`187.15.102.104`**, which is the laptop's, and that is the whole of the requirement.

**The Pi's own NordVPN CLI is a third tunnel nobody has counted.** `/usr/bin/nordvpn` is installed,
`nordvpnd` is active, and a `nordlynx` interface is UP at index 28 — while `nordvpn status` reports
`Disconnected`. It is inert and it is not gluetun and not curator's. It is left alone here, and it
is worth knowing about before anyone debugs an exit address on this box.

## Do

1. **Make the package public**, then pull. Until that happens every step below stops at a 403.
2. **Deploy with the Pi overlay**: `docker compose -f compose.yaml -f compose.pi.yaml --profile
   1337x up -d`. `compose.pi.yaml` carries the two facts about this box and changes nothing in the
   published bundle, so what runs is still the shipped artefact
   ([T48](T48-release-pipeline.md)) — multi-arch, and the Pi is arm64.

   It exists because `compose.yaml` alone is **wrong on this hardware in a way that does not fail at
   `up -d`**. The `curator-media` volume would land in `/var/lib/docker` on the 29 GB SD card with
   13 GB free, not on the 870 GB at `/media/storage`; the overlay rebinds the volume rather than
   re-declaring the mount, which keeps `/media/movies` and `/media/downloads` inside **one** bind and
   so keeps [D8](../decisions.md#d8--import-by-hardlink)'s `link()` working. Two binds would be two
   filesystems, a fallback to copy, and every film stored twice — the 201 GB D43 just deleted.

   The overlay also **pins `v0.1.0` and sets `com.centurylinklabs.watchtower.enable=false`**, because
   `watchtower` on this Pi runs `--cleanup --interval 86400` with no `--label-enable` and therefore
   updates every container on the box. An unpinned curator here restarts itself mid-download once a
   day. D43 keeps watchtower, so that is permanent.

   The `jellyfin` profile is **not** used: this box already runs Jellyfin 10.10.7 at `:8096` and
   [T66](T66-adopt-jellyfin.md) adopts it. `--profile 1337x` is used, and minter's image does
   publish `linux/arm64` — checked, because "all three indexers" in Verify depends on it.

3. **Give curator the tunnel.** Put a wg-quick `.conf` at `/opt/curator/wg0.conf`, mode **600 owned
   by 1000** — the process reading it is uid 1000, and the overlay mounts it read-only at
   `/run/secrets/wg0.conf`. Create the file **before** `up -d`: the bind has
   `create_host_path: true`, so a missing file becomes a *directory* and curator fails on a config
   it cannot read. An unreadable `VPN_CONFIG_FILE` is fatal rather than a fall-through
   (`internal/settings/resolve.go:66`), which is the behaviour wanted here.

   Its `Endpoint` must not be the laptop's. Prove the exit address before trusting it;
   `internal/vpn`'s live check is the existing proof and it has never run on this hardware.

4. **Scan the empty library first.** Zero films is a valid answer and the scan must say so rather
   than erroring — that path has only ever been exercised in `t.TempDir()`.
5. **Then one film, end to end**: search, pick a release, download through curator's own engine over
   curator's own tunnel, hardlink into the library, and play it.

## Do not

- **Do not restore anything from the T52 backup onto the Pi.** It exists so the \*arr state is
  recoverable, not so it comes back. D43 is deliberate.
- **Do not point curator at qBittorrent.** It is being removed and it is dead anyway (see below).
- **Do not touch Jellyfin's configuration.** curator adopts the server
  ([T66](T66-adopt-jellyfin.md)); its libraries are empty now and will refill from curator's imports.

## What is known to be in the way

- **This is the first `CGO_ENABLED=0` engine to meet a real swarm.** `go test -race` needs cgo, so
  every measurement so far used C libutp and SQLite piece completion; the Pi runs pure-Go uTP and
  boltdb. That combination has never downloaded anything.
- **A 4-core Pi 5, not the 10-core laptop.** Every timing in this repo was taken on the laptop.
- **T78's stall.** Metadata in, then no payload, is a known intermittent shape
  ([T78](T78-a-stall-that-says-why.md)); `stallReport` is the diagnostic and the evidence so far
  points at a swarm that empties rather than at the engine. The Pi's connection is not the laptop's,
  so the frequency is an open number.
- **`qbittorrent` has been dead since 2026-08-18T01:38:35Z** — a boot race Docker's restart policy
  cannot fix, recorded in D43. Nothing about it needs solving; it is being removed.

## Verify

- curator answers on the Pi, and a scan of the **empty** library returns zero films without erroring
- the tunnel is up and its exit address is **not** the home address
- a search returns releases from all three indexers — 1337x through minter, TPB and YTS direct
- one film downloads through curator's own engine, hardlinks into
  `/media/storage/media/movies/Title (Year)/` with **link count 2**, and plays
- it appears in Jellyfin without anything from the \*arr stack being involved
- `df` shows the payload once, not twice — which is the difference from what was just deleted
