# T35 — the embedded engine

**Owns:** `internal/engine/` — `engine.go`, `state.go`, `metainfo.go` and their tests
**Depends on:** T32 (gate passed), T34

## Goal

`internal/engine` is a `download.TorrentClient` that downloads the torrent itself, in this process.
The interface does not change; the four methods do the same four things; every test in
`internal/download` already runs on fakes, so those tests validate this adapter the day it is
written.

The dependency this adds is `github.com/anacrolix/torrent v1.61.0` and the 71 modules behind it. It
belongs in this commit and nowhere earlier — the same rule `golang.org/x/sync` followed in T11.

## Do

1. `New(cfg Config) (*Engine, error)`, where `Config` carries `DataDir`, `Category`, `MaxConns`, a
   `Logger`, and a **nilable `Network`** — the dialers and packet conns the client is built on.
   Nil means the host's own stack, which is what the tests use and what `VPN_REQUIRED=false` gives.
   T37 supplies the real one. The engine does not import `internal/vpn`; it takes an interface,
   because a package that owns a socket should not also own a VPN.
2. **`CGO_ENABLED=0` is not optional.** With cgo on, this dependency pulls `go-libutp`,
   `go-llsqlite/crawshaw` and `crawshaw/c` — a C uTP *and a second SQLite* beside
   [D4](../decisions.md#d4--pure-go-sqlite)'s. Go turns cgo off by itself when cross-compiling, so
   `GOOS=linux GOARCH=arm64 go build ./...` will not catch a regression here. T47's Dockerfile is
   where it gets set; a comment in this package is where somebody reads why.
3. **Produce curator's four states directly** — do not port `qbit.MapState`'s 23-entry map, which
   translates a vocabulary this backend does not have:

   | Engine condition | State |
   |---|---|
   | no info dict yet (metadata still being fetched) | `queued` |
   | info known, bytes missing | `downloading` |
   | every piece present | `completed` |
   | — | `failed` is **not reachable here**, and that is worth a comment |

   `failed` exists in the table because qBittorrent has `error` and `missingFiles`. This engine has
   no equivalent: a torrent it cannot make progress on is not failed, it is *stalled*, which is
   T36's word and T36's job. Inventing a `failed` here would tell somebody their download died when
   what actually happened is that nobody is seeding it.
4. **`ContentPath` is a path curator can open**, `filepath.Join(DataDir, info.Name)` — the same
   convention qBittorrent's `content_path` follows: the file for a single-file torrent, the folder
   for a multi-file one, which is exactly what `library.FindFeature` already handles. There is no
   second namespace and therefore nothing to translate, which is what T38 gets to delete.
5. **`Category` is structural, not a guard.** This engine only ever holds torrents curator added, so
   `Torrents(ctx, category)` cannot return somebody else's and `DeleteTorrent`'s category check
   cannot fail. Return the configured category on every torrent and keep the parameter honoured —
   the interface is shared with a backend where it is a real filter
   ([D13](../decisions.md#d13--downloads-are-scoped-by-a-qbittorrent-category-with-its-own-save-path),
   now enforced by exclusive ownership instead of by a string).
6. **Persist the metainfo when it arrives.** anacrolix persists the payload and a piece-completion
   database but **never the info dict**, so a magnet cannot resume without a swarm round trip
   (3.2 s measured with peers, forever without). One goroutine per torrent waits on `GotInfo` and
   writes `DataDir/.curator/<UPPER-HASH>.torrent`. Writing is this task's; reading it at boot is
   T36's. Write to a temp file and rename, because a half-written metainfo that survives a crash is
   worse than none.
7. **Cap the memory, and prove the cap.** Peak RSS tracked payload size ~1:1 in the spike — 822 MB
   for 755 MB — on a Pi that has 8 GB and is also running Jellyfin. The levers, in the order worth
   trying: `ClientConfig.MaxUnverifiedBytes`, `EstablishedConnsPerTorrent` /
   `Torrent.SetMaxEstablishedConns` (`TORRENT_MAX_CONNS`), and the storage implementation the client
   is given. **Measure before and after on a real payload** and put both numbers in the commit
   message and in [`phase-6.md`](../phase-6.md). Target: peak RSS bounded by something that is not
   the size of the file. If no lever gets a 755 MB download under ~400 MB, **say so with the
   number** — a measured disappointment is a result, and shipping this silently is how a Pi gets
   OOM-killed at 3 a.m. with nobody watching.
8. `Close(ctx)` stops the client and is called from `main`'s shutdown path, so seeding ends with the
   process and no goroutine outlives `run()` — the rule the poller and the search cache already
   follow.

## Do not

- Start this engine when `TORRENT_BACKEND=qbittorrent`. An unused torrent client is sockets, threads
  and a data directory nobody asked for.
- Add a delete path that reaches outside `DataDir`. `DeleteTorrent` drops the torrent and removes
  **its own** files, containment-checked the way `importer.assertInsideLibrary` checks the library
  root. [D19](../decisions.md#d19--deleting-a-movie-removes-the-file-and-asks-qbittorrent-to-remove-its-own)
  narrowed deletion to "only ever a file curator created itself"; that guarantee gets stronger here,
  not weaker, because now curator did create it.
- Import a thing from `internal/vpn`, `internal/download` or `internal/store`. This package's
  dependencies are `internal/torrent`, the engine, and the stdlib.
- Adopt a torrent that is already in `DataDir` but has no row. The poller reports orphans and never
  adopts them, and an engine that re-added everything it found on disk would fabricate exactly the
  films D14 refuses to invent.
- Port qBittorrent's state map, its hash-case rules, or its "the add proves nothing" dance. None of
  those are properties of downloading; they are properties of talking to qBittorrent over HTTP.

## Verify

**Hermetic, and no network.** Two engines on `127.0.0.1` with DHT off: one seeds a payload generated
in `t.TempDir()`, the other is given its metainfo and address and downloads it. That covers the
whole adapter without a swarm, a tracker, or a public torrent:

- `Torrents` is empty before an add and holds one torrent after
- the states walk `queued` → `downloading` → `completed`, and `Progress` is 0..1 rather than a
  percentage — the same contract `qbit.Torrent` promised, because the poller believes it
- `ContentPath` is openable and `library.FindFeature` finds the payload in it
- `TorrentByHash` finds an upper-case hash and returns `(nil, nil)` for one that is absent —
  absence is a normal answer, not an error
- `DeleteTorrent` with a mismatched category refuses and **leaves the files on disk**; with the right
  one it removes them; a torrent that is already gone is success, not an error
- the metainfo file appears under `.curator/`, is a valid metainfo, and is written by rename
- a magnet with no info hash is rejected before anything is created

Then one live run on the laptop, because a hermetic test cannot prove a swarm:

- a real magnet completes, and the file lands where `ContentPath` said it would
- peak RSS, against step 7's budget
- restart with the payload on disk: the torrent is complete in well under a second and **0 bytes**
  are re-downloaded
- the finished files are `0444`, which retires [`phase-4.md`](../phase-4.md)'s permissions
  measurement and is why the importer's hardlink still works unchanged
