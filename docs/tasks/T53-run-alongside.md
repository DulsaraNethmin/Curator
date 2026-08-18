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

## Do

1. **Deploy the tagged image.** `ghcr.io/dulsaranethmin/curator` published at `v0.1.0`
   ([T48](T48-release-pipeline.md)), multi-arch, and the Pi is arm64. This is the first time the
   shipped artefact runs anywhere but a laptop.
2. **`LIBRARY_MOVIES=/media/storage/media/movies`** and a downloads directory on the **same
   filesystem**, because [D8](../decisions.md#d8--import-by-hardlink) hardlinks and a cross-device
   link fails. `/media/storage` has 870 GB; `/` has 13 GB and is the wrong disk.
3. **Give curator the tunnel.** `NORDVPN_USERNAME` and `NORDVPN_PASSWORD` are in the `.env` captured
   by T52 — that is the reason it was captured. Prove the exit address before trusting it;
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
- **A 2-core-class box, not the 10-core laptop.** Every timing in this repo was taken on the laptop.
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
