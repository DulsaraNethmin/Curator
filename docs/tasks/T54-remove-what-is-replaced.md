# T54 — remove radarr, seerr and recyclarr, and nothing else

**Owns:** `/opt/docker/compose.yml` on the Pi, and the removal itself
**Depends on:** [T52](T52-arr-config-backup.md) (a restored backup) and
[T53](T53-run-alongside.md) (parity proved). Bounded by
[D26](../decisions.md#d26--television-keeps-its-stack-the-cutover-removes-only-what-curator-replaces-for-movies)

## Goal

The three containers curator has actually replaced stop running, television keeps working, and the
compose file says what the Pi is now.

## The list, and it is exactly three

| Removed | Why it may go |
|---|---|
| `radarr` | curator is the movie manager; T53 proved 29/16 parity |
| `seerr` | curator's own UI is the request surface |
| `recyclarr` | measured: its only config is `radarr.yml`, `base_url: http://radarr:7878` |

| Kept, and why it may **not** go | |
|---|---|
| `gluetun`, `qbittorrent` | sonarr's download path — same client, still enabled |
| `prowlarr` | Prowlarr syncs to `Radarr` **and** `Sonarr`; removing it strips sonarr's indexers |
| `flaresolverr` | sonarr's indexers need it |
| `jellyfin` | curator adopts this server rather than replacing it, and it holds the watch history |
| `sonarr`, `portainer`, `watchtower`, `homepage` | curator never replaced any of them |

`byparr` is already exited and was superseded by minter
([D1](../decisions.md#d1--keep-1337x-build-our-own-cloudflare-solver)); removing its stanza is
tidying, not a cutover step.

**13 services become 10.** Not six, and not seven — see D26 for why every earlier count was wrong.

## Do

1. **Stop before deleting.** `docker compose stop radarr seerr recyclarr` and leave the Pi like that
   long enough to find out what breaks. A stanza deleted is a stanza somebody has to reconstruct from
   a backup; a stopped container is a decision that reverses in one command.
2. **Then remove the three stanzas from `compose.yml`**, and `docker compose up -d` to reconcile.
   The project is `docker` at `/opt/docker` and there is only one, so a stray `-p` reconciles the
   wrong thing.
3. **Leave the config directories on disk.** radarr's 136 MB and recyclarr's 78 MB cost nothing next
   to 13 GB free, and they are the fastest possible rollback. Deleting them is a separate decision
   nobody needs to take today.
4. **Say what the Pi is now**, in `compose.yml`'s own comments: which services belong to television
   and which to curator, because the next reader will not have D26 in their head.

## Do not

- **Remove `prowlarr`, `qbittorrent`, `gluetun` or `flaresolverr`.** D26. Television is live — 3
  series, 40 GB, an episode imported 2026-08-17.
- **Remove or edit anything about `jellyfin`.** curator connects to a server somebody is already
  watching and changes nothing about it.
- **`docker compose down`.** It takes the whole project, television included.
- **Delete the library, the downloads, or anything under `/media/storage/media/`.** Nothing in this
  task touches the USB disk.
- **Run before T52's restore has been proved and T53's parity has been seen.**

## Verify

- `docker ps` shows **10 services**, and radarr, seerr and recyclarr are not among them
- **sonarr still downloads**: an episode grabbed and imported after the removal, through the
  qbittorrent and prowlarr that stayed. This is the assertion the whole decision rests on and it is
  not optional
- curator still lists 29/16 and still plays a film
- Jellyfin is untouched — same libraries, same users, same watch state
- `compose.yml` in git-or-backup form still exists for the three removed stanzas, so the rollback is
  a paste rather than a reconstruction
- **`/opt/docker/backups/` is no longer empty**, because T52 ran

## Open

- **`qbittorrent` is exited (255) as of 2026-08-18** and nothing restarted it. Television's download
  path is therefore already broken independently of this task, and "sonarr still downloads" cannot be
  verified until somebody starts it. Find out why it stopped before reading a failure here as
  something T54 caused.
- **Nothing here reduces the two-tunnel cost** D26 accepted. If that becomes unwanted, the lever is
  retiring television, and that is a new decision rather than a revision of this task.
