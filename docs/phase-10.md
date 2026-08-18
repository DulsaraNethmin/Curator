# Phase 10 — Cutover: run alongside, prove parity, remove

The last phase. Every phase so far has built the replacement and changed nothing on the Pi
([`phase-6.md`](phase-6.md#out-of-scope), [`phase-7.md`](phase-7.md), [`phase-9.md`](phase-9.md#out-of-scope)
all defer here). This is where that expires — and it expires **after** [T52](tasks/T52-arr-config-backup.md),
not before.

**Done when** curator serves the movie half of this Pi, the containers it replaces are gone, and the
one thing it never claimed to do — television — either still works or has been deliberately retired.

---

## The reading this is built on — **confirmed 2026-08-18**

Taken read-only over `ssh pi`, against the running stack. Nothing was changed.

| | |
|---|---|
| Host | Pi 5, `Linux 6.18.34+rpt-rpi-2712 aarch64`, up 10 h |
| Compose project | one, `docker`, working dir `/opt/docker`, file `compose.yml`, **13 services** |
| Running | 11 — homepage, gluetun, portainer, prowlarr, radarr, flaresolverr, watchtower, seerr, jellyfin, recyclarr, sonarr |
| Exited | 2 — **`qbittorrent` (exit 255, since 2026-08-18T01:38:35Z)** and `byparr` |
| SD card `/` | 29 G, 16 G used, **13 G free** — this is where `/opt/docker` lives |
| USB `/media/storage` | 916 G, 363 G used, **507 G free** — library and downloads |
| Library | **29 folders, 16 video files** |
| Configs | **627 MB** total under `/opt/docker/configs/` |

Per service: jellyfin 295 M, radarr 136 M, recyclarr 78 M, sonarr 71 M, prowlarr 23 M,
qbittorrent 9.2 M, seerr 7.4 M, gluetun 7.1 M, portainer 564 K, homepage 284 K.

### Parity has an exact number, and curator already matches it

Radarr's own API, same day: **29 movies tracked, 16 with a file, 29 monitored.** curator's scanner
finds **29 folders and 16 video files** off the same disk. That is the parity assertion for
[T53](tasks/T53-run-alongside.md), and it is a count both sides already agree on.

Two root folders are configured in radarr — `/media/movies` **and** `/movies` — which is container-path
drift rather than two libraries, and worth knowing before reading a parity diff as a fault.

### The three indexers are the same three

Radarr's enabled indexers, all supplied by Prowlarr: **1337x, The Pirate Bay, YTS.** Those are
exactly the three curator implements ([`phase-2.md`](phase-2.md#the-three-indexers)), which is why
the movie half is a like-for-like swap rather than a migration.

---

## The finding that decides this phase's shape: **removing the download stack breaks television**

Sonarr is not a bystander. Measured the same day, from both APIs:

- **radarr and sonarr use the same download client** — one `QBittorrent`, enabled, in each.
- **Prowlarr syncs to both** — its Applications list is exactly `Radarr` and `Sonarr`.
- Sonarr's indexers are 1337x, EZTV and The Pirate Bay, so it needs Prowlarr and flaresolverr too.

curator replaces radarr's half of that and **nothing of sonarr's** — television is out of scope and
has been since phase 1. So the containers curator makes redundant are *not* the containers that can
be removed:

| Service | Replaced by curator | Removable |
|---|---|---|
| radarr | the whole product | **yes** |
| seerr | curator's own UI | **yes** |
| recyclarr | quality profiles curator does not have | yes, if sonarr's profiles are hand-kept |
| byparr | minter ([D1](decisions.md#d1--keep-1337x-build-our-own-cloudflare-solver)) | yes — already exited |
| prowlarr | `internal/indexer` | **no — sonarr needs it** |
| flaresolverr | minter | **no — sonarr needs it** |
| qbittorrent | the embedded engine ([D22](decisions.md#d22--the-torrent-engine-moves-inside-the-binary-and-qbittorrent-becomes-the-second-backend)) | **no — sonarr needs it** |
| gluetun | the embedded tunnel ([D27](decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket)) | **no — sonarr needs it** |
| jellyfin | nothing; curator adopts the server instead | no, and never |
| sonarr, portainer, watchtower, homepage | nothing | no |

**So "remove seven containers" is not available**, and `roadmap.md` still promises it. Without a
decision about television the honest removal set is **three** — radarr, seerr, recyclarr — and the
project's own headline number was wrong in a way no document caught, because every count so far was
taken against what curator replaces rather than against what still depends on it.

> **Superseded 2026-08-18 by [D43](decisions.md#d43--the-pi-is-a-clean-slate-television-is-retired-and-curator-is-the-only-downloader).**
> The media disk was emptied — 363 GB deleted, 870 GB free — and television was retired by choice.
> D26's reading below still stands as a record of what the stack depended on; its *conclusion* does
> not. The removal set is **nine**, not three, and thirteen services become **five**. T53 has no
> parity target any more and T54 removes the whole download stack.

### That decision was [D26](decisions.md#d26--television-keeps-its-stack-the-cutover-removes-only-what-curator-replaces-for-movies), and D43 has since reversed it

**Television keeps its stack.** The cutover removes radarr, seerr and recyclarr — three, not seven —
and `gluetun`, `qbittorrent`, `prowlarr` and `flaresolverr` stay up for sonarr, which is live: 3
series, 40.0 GB, an episode imported 2026-08-17. The accepted cost is two tunnels and two torrent
clients on one Pi, affordable at 6.2 GB available of 7.9 GB and 4 cores. The options it was chosen
from were:

1. **Movies-only cutover.** Remove radarr, seerr, recyclarr; keep gluetun, qbittorrent, prowlarr and
   flaresolverr running for sonarr. The cost is concrete and should be measured rather than assumed:
   **two VPN tunnels and two torrent clients on one Pi**, curator's WireGuard beside gluetun's, each
   with its own exit address.
2. **Retire television.** Remove sonarr too and the whole download stack goes with it — this is the
   only branch where the container count actually collapses.
3. **Keep both indefinitely**, and accept that phase 10 removes three containers and is mostly a
   parity exercise.

**`qbittorrent` has been down since 01:38 today (exit 255), so television is not downloading
anything right now either.** That is worth knowing before option 1's cost is priced, and it means
[T53](tasks/T53-run-alongside.md) cannot compare against a live downloader without restarting it.

---

## Tasks

| Task | What | Owns |
|---|---|---|
| [T52](tasks/T52-arr-config-backup.md) | back up the \*arr configs, off the Pi, restorably | the backup and its restore proof |
| [T53](tasks/T53-run-alongside.md) | run curator on the Pi beside the stack, prove parity | 29/16, and the two-tunnel cost |
| [T54](tasks/T54-remove-what-is-replaced.md) | remove what D26 says may go | `compose.yml`, and nothing until D26 |

T52 is a hard prerequisite for both others and for anything else in this phase.

All three have task files, written once D26 settled what T53 and T54 actually do.

---

## What must not change

- **Nothing is removed before T52's restore has been proved**, and a backup nobody has restored is
  not a backup. `/opt/docker/backups/` is **empty** today.
- **Jellyfin stays.** curator connects to a server somebody is already watching and changes nothing
  about it ([T66](tasks/T66-adopt-jellyfin.md)). Its 295 MB of config is the largest on the box and
  the one with real watch history in it.
- **The library is never rewritten.** curator hardlinks and has never moved, copied over, renamed or
  deleted a source file ([D8](decisions.md#d8--import-by-hardlink)); the cutover does not become the
  exception.
- **`/opt/docker/.env` holds `NORDVPN_USERNAME` and `NORDVPN_PASSWORD`.** It is `-rw-------` and must
  stay that way in every copy of it, including the backup.
- **The SD card has 13 GB free** and the configs are 627 MB. A backup written to `/opt/docker/backups`
  is on the same card as the thing it protects, which is not a backup either.

## Verification

The phase's own definition of done, on the Pi:

- curator's library shows **29 films, 16 with a file**, matching radarr's own numbers above
- a search returns releases from all three indexers, through curator's own engine over its own tunnel
- a download completes and appears in Jellyfin without radarr running
- the removed containers are gone from `compose.yml`, and `docker compose up -d` reconciles cleanly
- **television still works, or has been retired on purpose** — whichever D26 says
- the backup from T52 has been restored somewhere and shown to produce a working radarr

## Out of scope

- **Television.** Still not this product; D26 decides whether it keeps its stack or stops.
- **Migrating radarr's database into curator.** curator scans the disk and asks TMDB; there is
  nothing to import and 29 folders is not a migration.
- **The Pi's other services** — portainer, watchtower, homepage — which curator has never touched
  and does not replace.
