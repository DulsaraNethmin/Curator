# Phase 6 — Own the download

The phase where curator stops asking another container to download for it.

**Done when** a magnet dispatched from the UI downloads through curator's **own** engine, over a
WireGuard tunnel curator brought up itself, and hardlinks into the library — with the tunnel down
meaning **no traffic at all**, rather than traffic that leaked.

---

## The goal changed, and this file is the new one

[`roadmap.md`](roadmap.md) says phase 6 is "13 containers are 6", and
[`architecture.md`](architecture.md) draws a six-container after-state. **Do not build to those.**
curator is being rebuilt from "one binary that replaces the seven \*arr containers on my Pi" into a
product anyone runs with one `docker run`, and a container count on one particular Raspberry Pi is
not a metric for that — it is a number that happens to fall out of it.

Phase 6's metric is a different question, and a sharper one: **which process owns the socket the peer
bytes leave through?** Today the answer is qBittorrent, inside gluetun, on a Pi. After this phase it
is curator, and that is what makes a VPN something curator can *guarantee* rather than something it
can only hope somebody configured.

The stale documents are corrected in **T51**, where the whole set is cleared in one pass — the dead
`yts.mx`, `README.md`'s "phase 1 of 6", `.env.example`'s LAN addresses. Correcting them here would
mean touching them twice.

---

## Tasks

| Task | Owns | Depends on | State |
|---|---|---|---|
| **T32** spike: the engine | throwaway, deleted | — | **done**, gate passed |
| **T33** spike: the tunnel | throwaway, deleted | — | **done**, gate passed |
| [T34](tasks/T34-torrent-type.md) backend-neutral torrent | `internal/torrent/` | — | |
| [T35](tasks/T35-embedded-engine.md) the embedded engine | `internal/engine/` | T32, T34 | |
| [T36](tasks/T36-resume-stall.md) resume and stall detection | `internal/engine/`, `download/poller.go` | T35 | |
| [T37](tasks/T37-tunnel.md) the tunnel | `internal/vpn/` | T33 | |
| [T38](tasks/T38-wiring-subtraction.md) wiring and subtraction | `cmd/curator`, `internal/config`, deletions | T35, T37 | |

T32 and T33 have no task file. They were throwaway `cmd/spike` code on a branch that has been
deleted, which is what a spike is in this repo: **the measurements survive, the code does not.** The
measurements are below, because a plan file on somebody's laptop is not where this project keeps a
number it paid for.

T34 and T37 are independent of each other and can land in either order.

---

## What the spikes measured

Host: MacBook (darwin/arm64), Go 1.25.4, `anacrolix/torrent v1.61.0`,
`golang.zx2c4.com/wireguard`. Baseline commit `00e5588`. Payload throughout:
`debian-13.6.0-amd64-netinst.iso`, 755.0 MB, 3020 × 256 KiB pieces.

### T32 — the engine

| Question | Measured | Estimate it tests | Verdict |
|---|---|---|---|
| `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` | **passes** | — | **gate pass** |
| `possum` in the build graph | **absent** — `go list -deps` empty | "only `storage/possum` imports it" | confirmed |
| cgo packages in the arm64 build graph | **zero** | — | confirmed |
| arm64 binary, unstripped | 16.17 → **25.22 MB** (+9.05) | +15 MB | cheaper than feared |
| arm64 binary, `-s -w` | 11.31 → **17.62 MB** (+6.31) | ~12 MB | — |
| `go.mod` requires | 14 → **85** | 60–80 | slightly over |
| `go list -m all` | 36 → **256** | — | — |
| magnet → metadata, from peers | **3.2 s** | — | — |
| download, webseeds disabled | **107.0 s, 7.06 MB/s** | — | **gate pass** |
| where the bytes came from | **755.0 MB peers / 0.0 MB webseeds**, 100 peers seen | — | honest |
| seeding afterwards | **290.14 MB served** to qBittorrent 5.1.2, which reached 100 % | — | **gate pass** |
| re-add with the data already on disk | complete in **0.04–0.06 s**, **0 bytes re-downloaded** | — | pass |
| explicit full re-hash of 755 MB | **15.7–17.8 s (43–48 MB/s)** on SSD | — | T36's input |
| does verification catch corruption? | 1 MB corrupted → **3016/3020 pieces**, exactly the 4 damaged | — | it is real |
| peak RSS downloading 755 MB | **822 MB** (`ru_maxrss`), ~1:1 with payload | — | **T35 must cap this** |

### T33 — the tunnel

Peer: `linuxserver/wireguard` in server mode on this laptop, `AllowedIPs 0.0.0.0/0`. Client:
userspace `wireguard-go` on a gVisor netstack TUN, MTU 1420. The exit IP was this ISP — the lab
proves plumbing, not anonymity, which is the scope that was chosen.

| Step | Measured | Verdict |
|---|---|---|
| **UDP over netstack** (DNS A query to 1.1.1.1) | **pass**, 51–93 ms RTT | **gate pass** |
| **DHT bootstrap over netstack** | **51 nodes, 51 good** in 60 s | **gate pass** |
| **DHT announce** of a real infohash | **518 peers in 10 s** | **gate pass** |
| torrent client OS sockets opened | **0** — every byte had to use the tunnel | structural kill switch |
| **755 MB torrent, entirely over netstack uTP** | **262.4 s, 2.88 MB/s**, 50 peers at peak | pass |
| peak RSS, tunnelled | 838 MB | same as direct |
| TCP through this lab peer | **fails** — Docker Desktop's NAT emits invalid TCP checksums and gVisor drops them; UDP survives because its checksum is optional | lab artifact, diagnosed with tcpdump, not a property of userspace WireGuard |

| Throughput | MB/s |
|---|---|
| direct, everything on (TCP+uTP, tracker+DHT) | 7.06 |
| direct, **constrained exactly like the tunnelled run** (uTP only, DHT only) | **4.17** |
| **tunnelled**, uTP only, DHT only | **2.88** |
| **ratio, like for like** | **0.69** — gate was ≥0.50 |
| point-to-point TCP ceiling through userspace WireGuard | ~22 MB/s (176 Mbps) |

Comparing 2.88 against the unconstrained 7.06 would give 41 % and would be **wrong**: that run also
had TCP peers and a working HTTP tracker, neither of which the tunnelled run could use. 0.69 removes
the confound.

### Both gates passed, so the fallbacks are off

No bundled `qbittorrent-nox`, no gluetun sidecar. T34, T35 and T37 are on.

---

## The five findings that changed the tasks

1. **The Dockerfile must set `CGO_ENABLED=0` explicitly.** With cgo on, the engine pulls three cgo
   packages — `go-libutp`, `go-llsqlite/crawshaw`, `crawshaw/c` — so the image would get a cgo uTP
   *and a second SQLite*. Go disables cgo by itself when cross-compiling, which is why
   [CLAUDE.md](../CLAUDE.md)'s bare `GOOS=linux GOARCH=arm64 go build ./...` is unaffected and why
   this is invisible until T47. `utp_go.go` is `//go:build !cgo` and selects the pure-Go
   `anacrolix/utp` — which is also what makes T33's netstack wiring possible at all.
2. **A magnet cannot resume offline.** anacrolix persists the payload and a piece-completion
   database, but **never the info dict**. Re-adding by magnet needs a metadata round trip from the
   swarm — 3.2 s when there are peers, and forever when there are not. T35 persists the metainfo
   next to the payload; T36 is what re-adds from it at boot.
3. **Resume is optimistic.** On add, the engine reports 3020/3020 complete out of the completion
   database *without reading a byte*. That is what makes resume free, and it means "complete" is a
   claim about a database rather than about the disk until something forces a verify. T36 decides
   when curator pays the ~16 s.
4. **The engine chmods finished files to `0444`.** [`phase-4.md`](phase-4.md) lines 60–71 concluded
   the importer needs no `chmod` because qBittorrent writes `0644` as `nethmin:nethmin`. **That
   measurement does not carry over.** A hardlink is a second name for one inode, so the library copy
   is `0444` too — readable, which is all Jellyfin needs, but this is now measured rather than
   assumed, and T47 has to re-measure it again under `PUID`/`PGID`.
5. **Peak RSS tracked payload size ~1:1** — 822 MB for 755 MB. This is the one number that could
   still lose the Pi, which has 8 GB and is running Jellyfin. T35 owns capping it and proving the
   cap, and a phase that shipped without that would be shipping an OOM to hardware nobody is
   watching.

---

## The shape

```
internal/torrent/    the torrent as curator sees it — six fields, four states       T34
internal/qbit/       backend: qBittorrent's Web API v2, behaviour unchanged
internal/engine/     backend: the embedded anacrolix engine                         T35, T36
internal/vpn/        the userspace WireGuard tunnel and its dialers                 T37
```

**`internal/engine`, not `internal/torrents`.** The plan named it for what it holds; that puts
`internal/torrent` and `internal/torrents` one letter apart *and* collides with the upstream
dependency, whose own package is `torrent`. Every file in the engine would import two different
things called torrent. The engine is what the plan calls it in prose anyway.

`download.TorrentClient` stays exactly where it is and keeps its four methods. It is the seam that
makes a second backend an adapter rather than a rewrite: every test in `internal/download` runs on
fakes, so they validate the new backend the day it is written, and `internal/api` has never seen a
qBittorrent type.

---

## What must not change

- **No handler in `internal/api` changes its shape.** Phases 1–4 verified those responses against
  live services. A second download backend is not a reason to rewrite a contract.
- **`internal/qbit` keeps working.** It becomes one of two backends, not a corpse — it is the
  migration path for anyone already running the \*arr stack, and the fallback if the engine
  disappoints on real hardware. Its sunset criterion is written down in
  [D22](decisions.md#d22--the-torrent-engine-moves-inside-the-binary-and-qbittorrent-becomes-the-second-backend)
  rather than left to taste.
- **[D8](decisions.md#d8--import-by-hardlink)'s hardlink and
  [D14](decisions.md#d14--the-importer-is-driven-by-the-pollers-torrent-list-not-by-a-completion-event)'s
  state-triggered import survive untouched.** The importer still triggers on *this torrent reads
  completed and its row does not read imported*, and it still never moves, copies over or deletes a
  source file.
- **No migration.** `downloads` already stores hash **and** magnet, which is everything resume needs.
  This repo has never run a migration and phase 6 is not where that changes.
- **`CGO_ENABLED=0`.** [D4](decisions.md#d4--pure-go-sqlite) keeps its conclusion and gains a second
  rationale: pure Go is what makes `FROM scratch` multi-arch possible, not only Pi cross-compilation.
  anacrolix/torrent and wireguard-go are both pure Go, so neither breaks it.
- **The web UI stays off the tunnel.** Only the engine's dialer is bound to it, so a bad tunnel
  config can never lock you out of the app that configures the tunnel.

---

## The traps, named before anyone hits them

**The kill switch is structural, and only if you let it be.** With `DisableTCP`, `DisableUTP` and
`NoDHT` set, the client opened **zero OS sockets** — every byte had to go through the dialers it was
given. That is a much stronger guarantee than a setting somebody could turn off, and it is lost the
moment one code path falls back to `net.Dial` "just for trackers". A dead tunnel must mean a failed
dial.

**The netstack wiring that actually works**, measured, because the API is large and most of it is
wrong for this:

```
utp.NewSocketFromPacketConn(...)                        the uTP socket, over the tunnel's PacketConn
Client.AddDialer / Client.AddListener                   peer connections
Client.NewAnacrolixDhtServer(...)                       DHT over the same PacketConn
Client.AddDhtServer(torrent.AnacrolixDhtServerWrapper{...})
ClientConfig.HTTPDialContext                            webseeds
ClientConfig.TrackerDialContext                         HTTP trackers
ClientConfig.TrackerListenPacket                        UDP trackers
```

**DNS is part of the tunnel or it is a leak.** Tracker URLs carry hostnames, and a dialer that
resolves through the host resolver announces what you are downloading to your ISP before a single
encrypted byte moves. The tunnel owns a `net.Resolver` that dials its DNS server over netstack —
which is exactly the UDP path T33 measured first.

**qBittorrent's `hashes=` filter is lower-case; everything else here is upper-case.** That has been
true since T13 and does not change, but the neutral type now has to pick one. It picks upper — the
form `indexer.InfoHash` produces and the `downloads` table stores — and lower-case becomes an
internal detail of one backend's wire protocol, which is what it always was.

**A torrent with no peers looks identical to a torrent that has not started.** Live, right now, a
hand-added *Avengers (2012)* torrent with no download row makes the poller warn every five seconds,
and a magnet that never finds a peer sits at `metaDL` forever with nothing on screen to say why.
T36 makes both visible instead of silent.

---

## Configuration this phase adds

Environment only. Phase 7 is what makes these writable from the UI; keeping them in the environment
until then means every deployment that exists today keeps working and `docker run -e` stays honest.

| Variable | Default | Means |
|---|---|---|
| `TORRENT_BACKEND` | `embedded` | `embedded` or `qbittorrent` |
| `DOWNLOADS_DIR` | `./downloads` | where the embedded engine writes payloads |
| `TORRENT_MAX_CONNS` | measured in T35 | per-torrent peer cap — the RSS lever |
| `VPN_CONFIG` / `VPN_CONFIG_FILE` | unset | a wg-quick `.conf`, inline or by path |
| `VPN_REQUIRED` | `true` | with no tunnel configured, the embedded engine refuses to dispatch |

`VPN_REQUIRED=false` is the deliberate, documented escape for a laptop. It is not the default,
because "mandatory VPN" that defaults to off is a slogan rather than a guarantee
([D27](decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket)).

---

## Verification

Per commit, as ever:

```bash
npm --prefix web run build && go build ./... && go vet ./... && go test -race ./... \
  && GOOS=linux GOARCH=arm64 go build ./...
```

Then, driven for real on the laptop, against the local qBittorrent 5.1.2 container and **never the
Pi**:

- dispatch → complete → hardlink, with **equal inode and link count 2** — phase 4's proof, re-run
  against the new backend
- restart curator mid-download and confirm it **resumes without re-downloading**, from the
  persisted metainfo rather than from a swarm round trip
- **kill the tunnel mid-download** and confirm traffic stops rather than falling back — measured as
  bytes, not as a log line
- place a file in the data directory by hand and confirm it is **never imported**; the same
  guarantee [D13](decisions.md#d13--downloads-are-scoped-by-a-qbittorrent-category-with-its-own-save-path)'s
  category gave, now structural because the engine only ever holds torrents curator added
- peak RSS on a full download, against the cap T35 sets
- `TORRENT_BACKEND=qbittorrent` still dispatches, polls and imports exactly as phase 4 shipped it

---

## Out of scope

- **The cutover.** Phase 10, and the \*arr config backup (T52) comes first. Nothing on the Pi
  changes in this phase.
- **Writable settings.** Phase 7. Everything here is environment-driven, and
  [D17](decisions.md#d17--settings-is-read-only-and-the-settings-table-stays-unused) still stands
  until T39 overturns the half of it that is about configuration.
- **The Dockerfile.** Phase 9, T47 — which is where finding 1 gets paid.
- **OpenVPN, and any provider that is not WireGuard.** gluetun stays the escape hatch, and phase 7's
  Settings screen says so rather than pretending.
- **Playback.** Phase 8, which depends on nothing here and can run in parallel.
- **Seeding policy, ratio limits and scheduling.** The engine seeds while curator runs and stops
  when it stops. Anything cleverer is a feature nobody has asked for yet.
