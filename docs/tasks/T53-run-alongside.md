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

   The image tag is **`0.1.0`, not `v0.1.0`**, which this document said until the
   pull failed on it. `release.yml:64` strips the prefix (`version="${tag#v}"`), so
   the git tag is `v0.1.0` and the registry holds `0.1.0`, `0.1` and `latest`. The
   wrong one answers `manifest unknown` — indistinguishable at a glance from a
   release that never published.

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

## What happened, measured 2026-08-18

All six checks under Verify pass. curator runs on the Pi and the first film arrived through it.

**Deploy.** `0.1.0`, arm64, `user=1000:1000`, 31 MB, pulled anonymously once the ghcr package was
made public. The overlay did its job: `curator-media` is bind-backed to `/media/storage/media`, and
the payload never touched the 29 GB SD card.

**The empty scan, which had only ever run in `t.TempDir()`.**
`POST /api/scan` → `{"scanned":0,"added":0,"matched":0,"unmatched":0,"empty":0,"removed":0,"missing":0}`
with HTTP 200, and `/api/movies` → `[]`. Zero films is answered as a number, not an error.

**The tunnel, up on this hardware for the first time.** `vpn tunnel up
endpoint=187.15.101.96:51820 mtu=1420`, engine `tunnelled=true`, and at dispatch curator said it
itself: *"vpn check passed: the tunnel is up and traffic leaves from somewhere else."* That sentence
is the exit-address check — `guard.go:67` runs `CheckExit` before every dispatch, so a tunnel whose
exit equalled the host's would have refused the download rather than leaked it.

**Three indexers, and minter really was used.** `yts 3, tpb 20, 1337x 20` — 43 releases for
Deadpool (2016). minter's own access log carries the proof rather than curator's word for it:
`172.18.0.2 → POST /fetch 200 OK`, `fetched https://1337x.to/... in 12651ms (646570 bytes,
solved=True)`, where `172.18.0.2` is curator and `solved=True` is Cloudflare cleared.

**The download: 837,272,173 bytes, and the first real-swarm numbers this project has.** Pure-Go uTP
with boltdb piece completion, `CGO_ENABLED=0`, on arm64 — the combination
[T78](T78-a-stall-that-says-why.md) warned had never downloaded anything. It downloads. 35 minutes
wall clock, and the rate did not hold still:

```
21:28  38.91%   866.7 KB/s
21:30  45.62%   650.0 KB/s
21:34  53.70%    79.7 KB/s
21:38  57.74%     4.1 KB/s   <- plateau begins
21:41  57.85%     ~5 KB/s
21:42  63.93%   recovered, then ~1.6 MB/s to completion
```

**T78's plateau reproduced on real hardware, and it recovered.** Four minutes at 4-6 KB/s — a
trickle, not a freeze, exactly the shape T78 measured on the laptop. `torrent_stall_after` is
`5m0s`, so it came within about a minute of firing `stallReport` and did not. The mid-stall snapshot
is still unread; this run declined to produce one.

**The hardlink, which is what D43 deleted 201 GB to stop paying for.** Both paths are **inode
35127311** with **`links=2`**, and `df` reports **802 MB used** for an 837 MB file — stored once:

```
links=2  /media/storage/media/movies/Deadpool (2016)/Deadpool (2016).mp4
links=2  /media/storage/media/downloads/Deadpool (2016) [YTS.AG]/Deadpool.2016.720p...mp4
```

**It plays.** `ffprobe` over `/api/movies/1/stream` from the laptop: `h264 1280x534`, `aac` 2ch,
`duration=6486.24` (108.1 min, Deadpool's real runtime). The endpoint answers `206` with
`Content-Range: bytes 0-1048575/837272173`, so it is seekable rather than a blob.

**Jellyfin sees it, with no \*arr involved.** `/movies/Deadpool (2016)/Deadpool (2016).mp4`,
`ProviderIds {"Tmdb":"293660","Imdb":"tt1431045"}`, `ProductionYear 2016` — which is exactly what
[D32](../decisions.md) keys *Open in Jellyfin* on.

## Two things found here that belong to other tasks

**minter's probe is broken, and the search path hides it.** `GET /api/indexers/minter/probe` reports
`unreachable / "nothing answered"` for a minter that is up and serving. Measured: minter's `/health`
takes **6.5 s under load and 8.0 s idle**, while `probeTimeout` is **5 s**
(`internal/api/settings.go:22`). The probe returns at the deadline every time — 5.009, 5.011, 5.008,
5.012, 5.011 s across five runs, two of them after minter's own container healthcheck had gone
`healthy`. `Minter.Probe` funnels *every* transport error into `ErrUnreachable`, so a healthy minter
is reported as nothing listening and the Settings screen tells the user to run a compose command for
a container that is already running.

**It is consistent, not intermittent** — this was first read as a load effect and the idle
measurement refutes that. `/health` on this minter image is simply slower than the deadline, so the
probe is broken for every install running it, not for unlucky ones. Because curator
cancels before minter answers, uvicorn never logs the request, so minter's own logs agree it "never
happened". `indexers.go:123` defends the short deadline as "affordable precisely because it is
`/health` and not a page fetch" — that is the assumption that is wrong. **This is T49's code and it
needs its own task.**

**Jellyfin is carrying 18 ghost records.** It lists 19 movies; exactly **one** file exists on disk.
The other 18 are pre-wipe database rows for files D43 deleted, and Jellyfin will keep showing them
until something makes it rescan. That belongs to [T54](T54-remove-what-is-replaced.md).

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
