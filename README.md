# curator

Self-hosted movie library automation in a single Go binary — and television, if you turn it on.
Search, download, file, watch.

It replaces the \*arr stack — radarr, sonarr, prowlarr, seerr, recyclarr, flaresolverr and byparr —
with **one container**. The torrent client is inside it, routed through a WireGuard tunnel it owns.
Jellyfin stays if you want it, because transcoding and client apps are a genuinely hard problem that
is not the \*arr problem.

## Quickstart

```bash
curl -fsSL https://raw.githubusercontent.com/DulsaraNethmin/Curator/main/compose.yaml -o compose.yaml
docker compose up -d
```

Then open **`http://<host>:8090`**, add a TMDB key on the Settings screen (free, from
[themoviedb.org](https://www.themoviedb.org/settings/api)), and point it at your films. Television is
off until you ask for it — uncomment `LIBRARY_TV` in the compose file. That is the
whole install — almost nothing is configured in the compose file, because Settings is writable and
asking you to edit YAML for the thing the screen exists to do would be the wrong trade
([D28](docs/decisions.md#d28--settings-are-writable-secrets-are-encrypted-at-rest-and-write-only-across-the-api)).

Downloads need a tunnel before curator will dispatch one. `VPN_REQUIRED` defaults to **true**: paste
a WireGuard `.conf` from your provider into Settings, and curator proves the exit address changed
before it grabs anything. Turning that off has to be typed.

**To watch on a TV**, add Jellyfin — curator provisions it, or adopts the one you already run:

```bash
docker compose --profile jellyfin up -d
```

Three profiles exist and none of them start uninvited: `jellyfin`, `1337x` (adds
[minter](https://github.com/DulsaraNethmin/Minter), which that one indexer needs), and `updater`
(watchtower, scoped to curator, which turns the in-app update notice into a button — see
[D44](docs/decisions.md#d44--curator-reads-the-version-something-else-installs-it)). A profiled
service is invisible rather than stopped: with no profile, `docker compose config --services` lists
`curator` alone.

Two knobs are in the compose file rather than the browser, because they are facts about the host:
`CURATOR_PORT` (8090 is a popular port) and `PUID`/`PGID` (the image is `FROM scratch` and runs as
uid 1000 with no shell, so it cannot `chown` anything into place — a different uid needs volumes
that already belong to it).

## What it does

You search for a film, see ranked releases, and pick one. curator downloads it over its own tunnel,
hardlinks it into the library, tells Jellyfin, and plays it in the browser.

```
browser ──→ curator ──→ TMDB               the film or the show, its year and its poster
                    ──→ indexers           YTS · TPB · 1337x — YTS declines a TV query
                    ──→ torrent engine ──→ WireGuard tunnel ──→ peers
                    ──→ filesystem         hardlink into movies/Title (Year)/
                                           or tv/Show (Year)/Season NN/, one link per episode
                    ──→ Jellyfin           rescan, if you gave it a key
```

**Only the torrent engine goes through the tunnel.** The UI, TMDB, the indexers and Jellyfin stay on
the machine's own connection, so a tunnel that drops cannot lock you out of the app — it stops
downloads, which is what it is for
([D27](docs/decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket)).

**For the engine, "through the tunnel" means all of it.** Every network operation of the torrent
subsystem — payload, peers, uTP, DHT, trackers, webseeds, WebRTC, DNS and local discovery — is
tunnel-bound or disabled, with no third option
([D47](docs/decisions.md#d47--every-torrent-network-operation-is-tunnel-bound-or-disabled)). That is
a claim about the **embedded** engine, which is the default: with `TORRENT_BACKEND=qbittorrent`
curator does not own the socket, so it compares exit addresses and refuses when they match rather
than promising anything. The **VPN screen** shows the whole thing being checked — the handshake, the
socket the engine actually holds, where traffic comes out, and whether downloads are held — and
curator re-proves it on a timer rather than only when you are looking.

**The torrent client is not a container.** It is a pure-Go engine in curator's own process
([D22](docs/decisions.md#d22--the-torrent-engine-moves-inside-the-binary-and-qbittorrent-becomes-the-second-backend)),
which is what makes one tunnel and one process possible. qBittorrent is still supported as a second
backend — `TORRENT_BACKEND=qbittorrent` — kept as a migration path for anyone who already runs one,
but it is not the default and not a dependency.

**Importing uses a hardlink**, so filing a film is instant, costs no extra disk, and seeding
continues from the download path. It never moves, copies over, renames or deletes your source file
([D8](docs/decisions.md#d8--import-by-hardlink)). A season pack is the same rule applied per file: N
episodes become N hardlinks, not one enormous film.

**Television is opt-in and off by default.** Set `LIBRARY_TV` and the same pipeline does shows —
TMDB's television catalogue, releases from TPB and 1337x, and episodes hardlinked into
`tv/Show (Year)/Season NN/`. Leave it unset, which is what a fresh install does, and nothing about
the film half changes: no TV root is scanned, no TV row is pruned, and the television routes answer
503 naming the variable
([D48](docs/decisions.md#d48--television-is-additive-a-show-is-a-row-in-movies-and-the-second-library-root-is-opt-in)).
**Episodes play in Jellyfin rather than in the browser**, because curator's player serves one file
per library row and a show is not one file. That is why the TV library wants Jellyfin in a way the
film library does not.

**Playback is direct play**, with a remux for the containers a browser refuses — an `.mkv` whose
streams are already H.264 and AAC is rewrapped, not re-encoded. There is no transcoding; that is
Jellyfin's job and Jellyfin is one profile away.

## What it deliberately does not do

Each of these is a recorded decision, not a gap:

- **No unattended television.** Shows are opt-in behind `LIBRARY_TV`, and when they are on they work
  exactly like films: you search, you pick, one grab at a time. No monitoring, no season-by-season
  "what am I missing", and no fourth indexer for it — and episodes play in Jellyfin rather than in
  the browser
  ([D48](docs/decisions.md#d48--television-is-additive-a-show-is-a-row-in-movies-and-the-second-library-root-is-opt-in)).
- **No automatic grabbing.** No monitored lists, no RSS, no quality-profile scoring. You search and
  you pick ([D5](docs/decisions.md#d5--manual-search-not-automatic-grabbing)). For one person and a few dozen films,
  that is usually better than automation, and it is most of what the complexity was buying.
- **No users or roles.** One optional password over the whole app, off by default
  ([D25](docs/decisions.md#d25--authentication-is-optional-and-off-by-default)). It is a LAN
  application, the same posture as the stack it replaces.
- **No transcoding.** Remux only, and only when the browser refuses the container.
- **No private trackers.** Three public indexers, no ratio rules, no announce credentials.

## Why

Radarr and Prowlarr generalise across 500+ indexers, private trackers with ratio rules, custom
format scoring and multi-user request queues. If your actual usage is one person, a few dozen movies
and a handful of public sources, almost none of that is doing work for you — but all of it still
needs updating, configuring, backing up and keeping in sync.

curator does the same pipeline with one binary, one config, one database. On the Raspberry Pi this
was built for, thirteen services became five: jellyfin, portainer, watchtower, homepage and curator.

## Companion service

1337x sits behind Cloudflare, so curator reaches it through
[**minter**](https://github.com/DulsaraNethmin/Minter) — a small solver built for this, which
replaces both flaresolverr and byparr. YTS and TPB are plain JSON and need no help, so minter is
opt-in: `--profile 1337x`.

## Backups: copy the volume, not the database

curator keeps two named volumes — `curator-data` (the database and the key its secrets are encrypted
with) and `curator-media` (your films and downloads).

**Back up the whole `curator-data` volume, never `curator.db` on its own.** SQLite runs in WAL mode,
so recent writes live in `curator.db-wal` until a checkpoint folds them in. On a container that had
been up for one minute, `curator.db` was 4,096 bytes and `curator.db-wal` was 41,232 — ten times the
size of the database it belongs to. A backup of the `.db` alone restores a stale snapshot that opens
cleanly and answers plausibly for the wrong reason, which is the worst way for a backup to fail.

The key sits beside the database on purpose: anything that copies the volume copies both, and a
database restored without its key keeps every row and cannot read one secret back
([D28](docs/decisions.md#d28--settings-are-writable-secrets-are-encrypted-at-rest-and-write-only-across-the-api)).

```bash
docker run --rm -v curator-data:/data -v "$PWD":/backup alpine \
  tar czf /backup/curator-data.tar.gz -C /data .
```

## Updating

The Settings screen checks for a release and tells you one of three things: a button, the command to
paste, or that this is the newest release. The command is not an error state — it is the ordinary
one for anyone running without an updater:

```bash
docker compose pull && docker compose up -d
```

curator never touches the Docker socket. A container cannot replace itself, and the alternative —
mounting the socket into a process that parses untrusted HTML from public indexers — was refused
outright. The `updater` profile holds that privilege instead, scoped to curator, and it does nothing
at all until the button is pressed
([D44](docs/decisions.md#d44--curator-reads-the-version-something-else-installs-it)).

`UPDATE_CHECK` switches the version check off. It defaults on: it reads a public GitHub endpoint and
sends nothing about your install, but it is still a request a media server makes on its own.

## Development

```bash
go build ./... && go test ./...
go run ./cmd/curator                  # http://localhost:8090
```

The UI is a Next.js static export embedded with `//go:embed`, so shipping is two commands and the
order matters ([D16](docs/decisions.md#d16--the-ui-is-embedded-with-all-and-a-committed-placeholder-keeps-go-build-honest)):

```bash
npm --prefix web install              # once
npm --prefix web run build            # writes internal/web/dist/
GOOS=linux GOARCH=arm64 go build ./...
```

`LIBRARY_MOVIES` defaults to `testdata/library/movies`, a fixture mirroring a real 29-film library,
so a fresh clone does something useful immediately. `LIBRARY_TV` defaults to nothing, because empty
means television is off — point it at `testdata/library/tv` to turn it on. Copy
[`.env.example`](.env.example) to `.env` for the rest.

Start with [`CLAUDE.md`](CLAUDE.md) — it carries the conventions and the traps, several of which cost
a day to find. [`docs/architecture.md`](docs/architecture.md) is the design,
[`docs/decisions.md`](docs/decisions.md) is why it is that design, and `make status` derives where
the build actually is rather than trusting a list somebody has to remember to update.

## Licence

MIT
