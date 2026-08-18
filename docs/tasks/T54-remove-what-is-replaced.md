# T54 — remove the nine, and leave four plus curator

**Owns:** `/opt/docker/compose.yml` on the Pi, and the removal itself
**Depends on:** [T52](T52-arr-config-backup.md) (done) and [T53](T53-run-alongside.md) (curator
proved on the Pi). Bounded by
[D43](../decisions.md#d43--the-pi-is-a-clean-slate-television-is-retired-and-curator-is-the-only-downloader),
which replaced [D26](../decisions.md#d26--television-keeps-its-stack-the-cutover-removes-only-what-curator-replaces-for-movies)

## Goal

Everything curator replaced stops running, and the compose file says what the Pi is now.

## The list, and it is nine

D26 bounded this at **three**, because sonarr shared qBittorrent, Prowlarr and flaresolverr and
television was live. D43 retired television, so the whole dependency chain goes with it:

| Removed | Why it may now go |
|---|---|
| `radarr` | curator is the movie manager |
| `seerr` | curator's own UI is the request surface |
| `recyclarr` | its only config is `radarr.yml`, `base_url: http://radarr:7878` |
| `sonarr` | television retired (D43) |
| `prowlarr` | it synced to Radarr and Sonarr; both are gone |
| `flaresolverr` | it existed for those indexers; minter replaces it ([D1](../decisions.md#d1--keep-1337x-build-our-own-cloudflare-solver)) |
| `qbittorrent` | curator's own engine ([D22](../decisions.md#d22--the-torrent-engine-moves-inside-the-binary-and-qbittorrent-becomes-the-second-backend)); also dead since 2026-08-18T01:38:35Z |
| `gluetun` | curator's own tunnel ([D27](../decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket)) |
| `byparr` | superseded by minter; already exited |

| Kept | |
|---|---|
| `jellyfin` | curator adopts this server rather than replacing it; it holds the watch history |
| `portainer`, `watchtower`, `homepage` | curator never replaced any of them |
| **curator** | the point |

**Thirteen services become five.** The README's original "thirteen become six" is not merely
corrected by this, it is beaten — and [T51](T51-documents.md) owns the sentence.

## Do

1. **Stop before deleting.** `docker compose stop` the nine and leave the Pi like that long enough to
   find out what breaks. A stopped container reverses in one command; a deleted stanza has to be
   reconstructed.
2. **Then remove the nine stanzas from `compose.yml`** and `docker compose up -d` to reconcile. The
   project is `docker` at `/opt/docker` and there is only one, so a stray `-p` reconciles the wrong
   thing.
3. **Leave the config directories on disk.** They total 627 MB against 13 GB free on the SD card and
   are the fastest rollback there is. Deleting them is a separate decision.
4. **Say what the Pi is now**, in `compose.yml`'s own comments, because the next reader will not have
   D43 in their head.

## Do not

- **`docker compose down`.** It takes the whole project, Jellyfin included.
- **Remove or edit `jellyfin`.** Untouched, and it is where the films are watched.
- **Delete `/opt/docker/configs/*`.** See above — that is the rollback.
- **Re-add anything "just in case".** The backup is the just-in-case, and it has been restored once
  already so it is known to work.

## What happened, 2026-08-18

**The nine are gone and the four remain.** Stopped first and left stopped while curator, Jellyfin
and a playback check were exercised against the box; only then were the stanzas cut and
`docker compose up -d --remove-orphans` run to reconcile.

The box now runs **six containers, which is five services**: `jellyfin`, `portainer`, `watchtower`
and `homepage` from `/opt/docker`, plus `curator` and `minter` from `/opt/curator`. This document
said five containers because it was written before [T53](T53-run-alongside.md) deployed minter under
compose's `1337x` profile — minter is the second half of curator's own bundle rather than a
survivor of the \*arr stack, and D43's count is unaffected.

**The edit removed the nine and nothing else, checked rather than eyeballed.** `docker compose
config` was rendered for each kept service before and after and diffed: `jellyfin`, `portainer`,
`watchtower` and `homepage` all resolve **identically**. The file went from 332 lines to 149.

**Verified after the removal:** curator answers, still holds the film T53 imported, and still streams
it — `ffprobe` over `/api/movies/1/stream` reports `6486.239s`, the same runtime as before. Jellyfin
is untouched at 10.10.7 with all four libraries (`Shows`, `Movies`, `Home Videos and Photos`,
`Music`) intact.

**One tunnel and one torrent client**, which is what D26 could not have. No container on the box runs
a torrent client — curator's engine is in-process — and the only WireGuard interface `ip link` can
see belongs to the Pi's own NordVPN CLI, which reports `Disconnected`. **curator's tunnel has no host
interface at all**, because it is a userspace device on a gVisor netstack ([D27](../decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket));
the correct count here is one visible interface that is idle and one working tunnel that is invisible.

## What the removal broke, and what was done about it

**homepage advertised five services that no longer exist.** Its `services.yaml` listed Sonarr,
Radarr, Prowlarr, qBittorrent and Seerr beside Jellyfin, and removing the containers leaves the
box's own front page pointing at five dead tiles. Rewritten to one **Curator** tile plus Jellyfin,
with the previous file kept as `services.yaml.pre-t54`. Nothing else in homepage's configuration was
touched.

The `HTTP 400` seen while checking it is **not** a fault introduced here:
`HOMEPAGE_ALLOWED_HOSTS=100.108.251.229:3000,homepage:3000` predates this task, so a request to the
LAN address is refused by design and the Tailscale host answers 200.

**A 1337x search failed during the stop-and-watch step and it was not the removal.** minter had a
browser wedged in-flight — its log read `mint queued 220856ms behind an in-flight browser` — because
this session had queued several searches against a service that runs one browser at a time. A
restart cleared it and 1337x reported `ok` again. curator never used flaresolverr or byparr; it uses
minter, so nothing removed here could have affected that indexer. Worth knowing anyway: **minter
serialises, and curator's `search_timeout` is 30 s**, so a backlog makes the 1337x indexer fail
rather than wait.

Also measured while there: minter's `/health` answered in **8.34 s**, against the **5 s**
`probeTimeout` T53 recorded. The probe bug T53 found is real and consistent, not a one-off.

## Verify

- `docker ps` shows **five** — jellyfin, portainer, watchtower, homepage, curator
- curator still serves the library it built in T53, and still plays a film
- Jellyfin is untouched: same server, same users, same libraries
- **only one tunnel and one torrent client exist on the box**, which is the thing D26 could not have
  and D43 bought
- `compose.yml` for the removed stanzas still exists in the T52 backup, so a rollback is a paste

## Open

- **Nothing here reduces what a rollback costs.** Restoring television means the backup, the configs
  and re-downloading 46 GB. That is the accepted price of D43.
- **`/opt/docker/configs/` keeps 627 MB of dead configuration** until somebody decides otherwise.
