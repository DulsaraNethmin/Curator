# T63 — the compose bundle: curator alone, and Jellyfin only if you ask

**Owns:** `compose.yaml` at the repository root, and the volume layout it commits to
**Depends on:** T47 (there has to be an image to run)

## Goal

`docker compose up -d` starts **curator and nothing else**. `docker compose --profile jellyfin up -d`
adds Jellyfin, sharing one media volume, so that the path curator writes to and the path Jellyfin
reads from are **the same string** and cannot drift.

This is the file that makes the phase's promise true, and the constraint that shapes it is yours:
*"when the main app starts, does it start jellyfin as well even if user has not select jellyfin? if
yes it's not what i expect."*

## Do

1. **Jellyfin and minter both sit behind `profiles:`.** Measured, so it is a property rather than a
   hope: with no profile selected, `docker compose config --services` lists **only `curator`** — the
   profiled services are not merely stopped, they are absent from the model. And
   `--profile jellyfin up -d` starts Jellyfin while **leaving curator running** rather than recreating
   it, so the pasted command is additive and safe against a live install.

2. **One named volume for media, mounted by both services at the same mountpoint.**

   ```
   curator:   media:/media     LIBRARY_MOVIES=/media/movies   DOWNLOADS_DIR=/media/downloads
   jellyfin:  media:/media     library location: /media/movies
   ```

   Two things fall out of that and both were measured in a two-service project rather than reasoned
   about: a file hardlinked from `/media/downloads` into `/media/movies` had **inode 343760 and link
   count 2**, which is [D8](../decisions.md#d8--import-by-hardlink)'s requirement that the two
   directories share a filesystem, satisfied structurally; and the **other** container saw the same
   inode at the same path. That is phase 4's own proof re-run inside the bundle, and it is the whole
   reason [T64](T64-jellyfin-provisioner.md) can hand Jellyfin a path it cannot get wrong.

3. **Publish `8096` to the host.** Not for curator — curator reaches Jellyfin on the compose network
   at `http://jellyfin:8096`. For the **Apple TV**, which is on the LAN and has no idea that network
   exists. Leaving this out produces a bundle that works perfectly in a browser on the host and
   cannot be reached by a single device the feature was built for, and the bug report says "Jellyfin
   is down".

4. **Pin every image**, including Jellyfin at **10.10.7**. `jellyfin/jellyfin:10.10.7` is a
   multi-arch index — amd64, arm64 and arm — and arm64 is **8 layers, 202 MB compressed to download,
   748 MB unpacked**, with a cold start of **17.6 s** from `docker run` to a `200` on
   `/System/Info/Public`. Those are the numbers the Playback screen's polling and the README's
   "how big is this" are built on. The startup API is not a stable documented contract
   ([T64](T64-jellyfin-provisioner.md)), so an unpinned Jellyfin is a provisioning flow that breaks on
   somebody else's release schedule.

5. **A separate volume for curator's own state**, holding the database and the secret key file.
   [D28](../decisions.md#d28--settings-are-writable-secrets-are-encrypted-at-rest-and-write-only-across-the-api)
   is explicit that the key lives beside the database and **anything that copies the volume copies
   both** — so they belong in one volume on purpose, and the README has to say what backing it up
   means.

6. **Environment in the file is the minimum that cannot be configured from the browser**, and
   nothing more. `LIBRARY_MOVIES`, `DOWNLOADS_DIR`, `DB_PATH`, `PORT`, `PUID`/`PGID`. The TMDB key,
   the tunnel, the indexers and Jellyfin's URL are all settings phase 7 made writable, and putting
   them in the compose file would tell a stranger to edit YAML for the thing the Settings screen
   exists to do.

7. **`restart: unless-stopped` on curator**, which is also what makes
   [D29](../decisions.md#d29--a-written-setting-applies-at-the-next-start-the-password-applies-at-once)'s
   "applies at the next start" a sentence with a mechanism behind it — `phase-7.md` deferred a restart
   endpoint to this phase precisely because a container has a supervisor and a `go run` does not.

8. **`depends_on` points from Jellyfin to nothing.** Jellyfin does not need curator and curator must
   start without Jellyfin — that is the opt-in property. curator discovers Jellyfin by probing, which
   is [T65](T65-playback-screen.md)'s.

## Do not

- **Mount `/var/run/docker.sock`.** Not for Jellyfin, not for minter, not for a status badge. It is
  root on the host handed to a service that ships with authentication off by default
  ([D25](../decisions.md#d25--authentication-is-optional-and-off-by-default)). This was ruled out
  deliberately and is the reason one command is pasted by hand
  ([D34](../decisions.md#d34--curator-provisions-a-jellyfin-it-brought-up-and-never-rewrites-one-somebody-is-already-watching)).
- **Give Jellyfin `network_mode: host`** to solve the LAN-address problem. It solves it and it takes
  the compose network away, so curator can no longer reach `http://jellyfin:8096` and the path
  guarantee goes with it.
- **Bind-mount host directories in the shipped file.** A named volume works on every host and every
  platform; `/media/storage/media/movies` is this Pi. Document the bind-mount override for people who
  already have a library — that is a README paragraph, not the default.
- **Use two volumes for downloads and movies.** They must share a filesystem or every import falls
  back from a hardlink to a failure. One volume, two directories.
- **Add `NET_ADMIN` or `--privileged` for the tunnel.** [D27](../decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket)
  put a userspace WireGuard on a gVisor netstack specifically so the container needs no capability at
  all. Adding one back would quietly undo the reason that design was chosen.
- **Default `VPN_REQUIRED=false` to make the first run smoother.** It is `true` on purpose:
  "mandatory VPN" that defaults to off is a slogan
  ([D27](../decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket)). A stranger who has
  not configured a tunnel gets a refused dispatch with a sentence explaining it, which is the correct
  first experience.
- **Name the profile something a typo can hide in.** A misspelled profile is a service that never
  starts and never errors, because a profile is *invisible* rather than stopped.

## Verify

- `docker compose config --services` with no profile lists **only** `curator`
- `docker compose up -d` starts one container; `docker ps` proves Jellyfin is not merely stopped but
  absent
- `docker compose --profile jellyfin up -d` starts Jellyfin **and does not recreate curator** — the
  container id is unchanged
- from inside curator: `http://jellyfin:8096/System/Info/Public` answers; from a **phone on the LAN**:
  `http://<host>:8096` answers. Both, because they are different claims.
- a file hardlinked from `/media/downloads` to `/media/movies` shares an inode and reports link
  count 2, and the same inode is visible from the Jellyfin container at the same path
- `docker compose down` and `up -d` again: the library, the database and the settings survive
- `docker compose --profile 1337x up -d` starts minter and curator's 1337x indexer stops reporting
  itself unconfigured — [T49](T49-minter-on-demand.md)'s half
- **on a machine that has never run this**: `curl -fsSL <raw compose.yaml> -o compose.yaml && docker
  compose up -d` reaches the UI. That is the quickstart in the README, executed rather than written.
