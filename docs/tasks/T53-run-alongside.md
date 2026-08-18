# T53 — curator on the Pi, beside the stack it replaces

**Owns:** the first curator instance that runs on the Pi, and the parity comparison against radarr
**Depends on:** [T52](T52-arr-config-backup.md), absolutely — nothing runs here until a backup has
been restored somewhere. Shaped by [D26](../decisions.md#d26--television-keeps-its-stack-the-cutover-removes-only-what-curator-replaces-for-movies)

## Goal

curator runs on the Pi, against the real library, and agrees with radarr about what is in it — while
radarr is still running and still in charge.

## The number this has to hit

Measured 2026-08-18 from radarr's own API and from the disk:

| | radarr | curator's scanner |
|---|---|---|
| movies | **29** | 29 folders |
| with a file | **16** | 16 video files |
| monitored | 29 | n/a — curator has no such concept |

**29 and 16.** Anything else is the finding, not a rounding difference. Two things will make an
honest diff look wrong and neither is a fault:

- radarr has **two root folders configured, `/media/movies` and `/movies`** — container-path drift
  inside one library, not two libraries.
- `CLAUDE.md` still says 14 video files and 15 empty folders. It is **16 and 13** as of 2026-08-18;
  the library moved and the document did not.

## Do

1. **Run curator alongside, changing nothing.** The \*arr stack keeps serving. curator reads the
   library and writes only its own database and its own downloads directory.
2. **Give it a port nobody is using.** `defaultPort` is 8090 and `Addr()` binds the wildcard; the Pi
   already has jellyfin on 8096, qbittorrent's WebUI on 8080 through gluetun, radarr 7878, sonarr
   8989, prowlarr 9696, portainer and homepage. Check before binding, do not assume.
3. **Point `LIBRARY_MOVIES` at `/media/storage/media/movies`** and let it scan. It must not write
   there — [D8](../decisions.md#d8--import-by-hardlink) is the whole reason imports hardlink.
4. **Use a downloads directory on the USB disk**, not the SD card: `/media/storage` has 507 GB free
   and `/` has 13 GB. Same filesystem as the library is also what keeps hardlinks working.
5. **Bring up curator's own tunnel and prove the exit address**, because from here on there are two
   (D26). `internal/vpn`'s live check is the existing proof and it should be run on the Pi rather
   than trusted from the laptop.
6. **Take one download end to end** — search, dispatch, import, and the film visible in Jellyfin —
   without radarr involved in any of it.

## Do not

- **Touch radarr, sonarr, prowlarr, qbittorrent or gluetun.** Not their configs, not their state, not
  their containers. T54 removes things; this task proves things.
- **Use the `curator` qBittorrent category for anything.** curator runs its own engine here
  ([D22](../decisions.md#d22--the-torrent-engine-moves-inside-the-binary-and-qbittorrent-becomes-the-second-backend)),
  and qbittorrent on this box belongs to sonarr now.
- **Import over a file radarr manages.** The library is shared and radarr still owns those 16 files.
- **Run before T52's restore has been proved.**

## What is already known to be in the way

- **`qbittorrent` has been exited since 2026-08-18T01:38:35Z (exit 255)** and nothing restarted it.
  Television has not downloaded since. That is not curator's problem, but it means a
  "does the old stack still work" comparison needs it started first, and starting it is a change to
  the running stack — so decide whether the comparison is worth it before making one.
- **Two tunnels now exist** (D26). A download that will not start has two VPNs to blame, and
  `internal/vpn`'s exit-address check is what tells them apart.
- **`go test -race` needs cgo and the release does not** — the Pi runs the `CGO_ENABLED=0` build, so
  its engine uses pure-Go uTP and boltdb piece completion, which the laptop's `-race` runs never
  exercise. First time that combination meets a real swarm is here.
- **T78's stall.** A download that takes metadata and then moves nothing is a known intermittent
  shape ([T78](T78-a-stall-that-says-why.md)); `stallReport` is the diagnostic and the evidence so
  far points at a swarm that empties rather than at the engine.

## Verify

- curator lists **29 films, 16 with a file**, matching the table above
- a search returns releases from all three indexers — 1337x through minter, TPB and YTS direct
- one download completes through curator's own engine over curator's own tunnel, hardlinks into the
  library with link count 2, and appears in Jellyfin
- radarr is **still running and still correct** afterwards, with its own 29/16 unchanged
- the library on disk has the same file count before and after, plus exactly the one new film
