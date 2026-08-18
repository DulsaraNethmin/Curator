# T52 — the \*arr configs, backed up off the Pi and proved by restoring one

**Owns:** the backup of `/opt/docker` — configs, `compose.yml`, `.env` — and the restore that proves it
**Depends on:** nothing. Everything else in [phase 10](../phase-10.md) depends on **this**, and has
since phase 6: [`phase-6.md`](../phase-6.md):301, [`phase-7.md`](../phase-7.md):267,
[`phase-9.md`](../phase-9.md):265 and :485 all defer the cutover behind "T52 comes first"

## Goal

Every piece of state the cutover could destroy exists somewhere that is not the Pi, and one of them
has been restored to prove it.

## Why a file copy is the wrong backup, measured

The obvious `cp -r /opt/docker/configs` produces a corrupt radarr. Measured 2026-08-18:

```
radarr.db       4517888 bytes   17:25... written 16:46
radarr.db-wal   2550312 bytes   17:33   <-- 47 minutes NEWER, and 2.5 MB of it
radarr.db-shm     32768 bytes   17:32
```

**The write-ahead log holds newer data than the database it belongs to**, and radarr is running, so
the two are never consistent with each other on disk. Copying `radarr.db` alone restores a stale
snapshot that opens cleanly and answers plausibly — which is the worst failure available, because
nothing about it looks broken. This is exactly the trap [T51](T51-documents.md) documents for
curator's own `curator.db-wal` (951 KB, two days newer), found again in somebody else's application.

## The apps already solve this, and it is free

Radarr, Sonarr and Prowlarr each take their own scheduled backup — a consistent zip, written weekly,
already on the box:

```
radarr_backup_v5.27.5.10198_2026.08.15_11.46.59.zip    869060 bytes
sonarr_backup_v4.0.15.2941_2026.08.15_12.56.02.zip
prowlarr_backup_v2.0.5.5160_2026.08.17_14.34.10.zip
```

**869 KB, against 136 MB of radarr config directory.** The zip is the database plus `config.xml`
plus the settings, taken by the application while it holds the lock — which is the only party that
can take a consistent one. The 136 MB is mostly `MediaCover/` artwork that re-downloads itself.

So the backup is: **trigger a fresh one through each app's API, then copy the zips off** — not a
`tar` of a live directory. The newest scheduled radarr zip is three days old and the cutover is
today; three days of a live library is exactly the window that matters.

## What a zip does not cover, and must be copied anyway

- **`/opt/docker/compose.yml`** — the 13-service definition itself, which [T54](T54-remove-what-is-replaced.md)
  edits. Nothing else on the box records what the stack was.
- **`/opt/docker/.env`** — `NORDVPN_USERNAME` and `NORDVPN_PASSWORD` among 16 keys. It is `-rw-------`
  and **every copy must stay `-rw-------`**, including the one that leaves the Pi.
- **qBittorrent and gluetun configs** (9.2 MB and 7.1 MB) — no app-level backup exists for either,
  and qBittorrent's holds the category and save-path setup phase 3 measured
  ([D13](../decisions.md#d13--downloads-are-scoped-by-a-qbittorrent-category-with-its-own-save-path)).
- **Jellyfin, 295 MB** — the biggest and the one with real watch history. curator adopts this server
  rather than replacing it ([T66](T66-adopt-jellyfin.md)), so the cutover should not touch it — which
  is a reason to have it backed up, not a reason to skip it.

## Do

1. **Trigger a fresh backup in radarr, sonarr and prowlarr** through their APIs, and wait for each to
   finish before reading the zip.
2. **Copy off the Pi**: the three fresh zips, `compose.yml`, `.env`, and the qbittorrent, gluetun and
   jellyfin config directories. `/opt/docker/backups/` is **empty and on the SD card**, which has
   13 GB free and is the same device as everything it would be protecting — it is a staging area at
   best, never the destination.
3. **Restore one, somewhere that is not the Pi**, and open it. A radarr container pointed at a
   restored config that lists 29 movies is the deliverable; the copy is not.
4. **Record the sizes and the SHA of what was taken**, so a later restore can tell a truncated copy
   from a complete one.

## Do not

- **`cp` or `tar` a live \*arr database.** See the WAL measurement. If a directory copy is taken
  anyway for the non-database services, take it with the container stopped or accept that its
  database half is worthless.
- **Leave the only copy on the Pi.** Same disk, same card, same failure.
- **Change anything about the running stack.** T52 reads and copies. Restarting a container to get a
  quiet database is a change, and it belongs to [T53](T53-run-alongside.md) if it is needed at all.
- **Print a secret.** `.env` and every `config.xml` carry API keys; the task copies them, it does not
  echo them into a terminal or a commit.
- **Back up to a path inside `/media/storage/media/`.** That is the library, and the importer walks it.

## Verify

- **A restored radarr lists 29 movies, 16 with a file** — the same numbers
  [`phase-10.md`](../phase-10.md#parity-has-an-exact-number-and-curator-already-matches-it) records
  from the live one. Anything else means the backup captured a different moment than it claims.
- The fresh zips are **newer than 2026-08-18** and larger than zero.
- `.env` in the backup is `-rw-------` and its 16 keys are present.
- `compose.yml` in the backup still names all 13 services, so T54's edit has something to diff against.
- The restore was done on a machine that is not the Pi, because a restore that needs the Pi to work
  is not a recovery.

## What was taken — **2026-08-18**

Held at `~/curator-pi-backup/2026-08-18/` on the laptop, **outside the repository**, because it
carries `.env` and three `config.xml` files. 19 MB total.

```
sha256 (first 16)   bytes  file
0da14c11b1ce54d3    207026  arr-zips/prowlarr_backup_v2.0.5.5160_2026.08.18_17.55.39.zip
7c6530c89e5dc30e    867224  arr-zips/radarr_backup_v5.27.5.10198_2026.08.18_17.55.38.zip
af43ef4ea3d57b03    802916  arr-zips/sonarr_backup_v4.0.15.2941_2026.08.18_17.55.39.zip
4b5ab2f14c271417       686  compose/.env                     (mode 600, preserved)
7633255f8021bc77      6319  compose/compose.yml              (all 13 services)
742aeb78d15e0fea    372119  configs/gluetun.tar.gz
6800128a49290239  12047942  configs/jellyfin-nometa.tar.gz
7282eab337206d7a   4934443  configs/qbittorrent.tar.gz
```

The full manifest is `MANIFEST.txt` beside them.

**The restore was done and it matches.** `radarr_backup_…2026.08.18` was unzipped into a
`lscr.io/linuxserver/radarr:5.27.5` container on the laptop — not the Pi — which migrated the schema
to 242 and started. Its API then answered **29 movies tracked, 16 with a file, 29 monitored**: the
same three numbers the Pi's live radarr reports, and the same ones
[`phase-10.md`](../phase-10.md) sets as T53's parity target. The container was removed afterwards.

### Three things the doing of it settled

- **The app's zip really is consistent, and this is the proof.** It contains exactly `config.xml`,
  `INFO` and a single `radarr.db` of 4,517,888 bytes — **with no `-wal` and no `-shm` beside it.**
  Radarr checkpoints the write-ahead log into the database before zipping, which is the thing a
  `cp -r` cannot do and the whole reason this task does not use one.
- **Jellyfin is worse than radarr was, and was handled differently.** `library.db-wal` is
  **10,876,832 bytes against a 3,825,664-byte `library.db`** — a WAL nearly 3× its own database. Its
  265 MB `metadata/` is regenerable artwork and was excluded, taking 295 MB down to 12 MB.
  Jellyfin was **running** during the copy, so its archive is crash-consistent at best; that is
  accepted rather than solved, because the cutover does not touch Jellyfin
  ([D26](../decisions.md#d26--television-keeps-its-stack-the-cutover-removes-only-what-curator-replaces-for-movies))
  and a guaranteed copy needs it stopped.
- **qBittorrent's copy is the clean one, by accident.** Its container has been exited since
  2026-08-18T01:38:35Z, so nothing was writing to its config while it was read. gluetun's directory
  holds only `servers.json`; its credentials are in `.env`, which is why 372 KB is complete rather
  than truncated.

## Open

- **This is one copy, on the laptop, in the same room.** It satisfies "not the Pi" and does not
  satisfy "somewhere else". A second destination is still wanted and is not blocking T53.
- **Where "off the Pi" is has not been chosen.** The laptop is the obvious first copy and is not by
  itself an answer, since it is the same room and the same person's hardware.
- **Nothing here schedules a repeat.** This is the one-shot backup the cutover needs; a habit is a
  different task and does not block phase 10.
