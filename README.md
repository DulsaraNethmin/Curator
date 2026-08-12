# curator

Self-hosted movie library automation in a single Go binary. Search, download, file, serve.

It replaces the *arr stack — radarr, sonarr, prowlarr, seerr, recyclarr, flaresolverr and byparr —
with one service, keeping Jellyfin and qBittorrent because they solve genuinely hard problems.
Thirteen containers become six.

> **Status: phase 1 of 6, in development.** Not usable yet.
> See [`docs/roadmap.md`](docs/roadmap.md).

## Why

Radarr and Prowlarr generalise across 500+ indexers, private trackers with ratio rules, custom
format scoring and multi-user request queues. If your actual usage is one person, a few dozen
movies and a handful of public sources, almost none of that is doing work for you — but all of it
still needs updating, configuring, backing up and keeping in sync.

curator does the same pipeline with one binary, one config, one database.

## How it works

```
browser → curator → TMDB          metadata
                  → indexers      find releases
                  → qBittorrent   download
                  → filesystem    hardlink into the library
                  → Jellyfin      rescan
```

You search, see ranked releases and pick one — no background monitoring, no scoring heuristics. For
a single user that is usually better than automation, and it removes most of the complexity.

Importing uses a **hardlink**, so it is instant, costs no extra disk space, and seeding continues
from the download path.

See [`docs/architecture.md`](docs/architecture.md) for the full design and diagrams.

## Companion service

1337x sits behind Cloudflare, so curator reaches it through
[**minter**](https://github.com/DulsaraNethmin/Minter) — a small solver built for this, which
replaces both flaresolverr and byparr. The other indexers are plain JSON and need no help.

## Development

```bash
go build ./... && go test ./...
go run ./cmd/curator                  # http://localhost:8090
```

`LIBRARY_MOVIES` defaults to `testdata/library/movies`, a fixture mirroring a real 29-film library,
so a fresh clone does something useful immediately.

Start with [`CLAUDE.md`](CLAUDE.md) — it carries the conventions and the traps.
Current work is broken into tasks in [`docs/tasks/`](docs/tasks/).

## Licence

MIT
