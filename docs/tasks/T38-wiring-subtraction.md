# T38 — wiring, and the subtraction

**Owns:** `cmd/curator/main.go`, `internal/config/`, the `Add` collapse in `internal/download`,
deletions in `internal/importer`
**Depends on:** T35, T37

## Goal

Choose a backend at start-up, put the tunnel under it, and then delete what the old arrangement
required and the new one does not. The satisfying part.

## Do

1. **`TORRENT_BACKEND`**, `embedded` or `qbittorrent`, default `embedded`. `qbittorrent` keeps
   phase 3 and 4 exactly as they shipped and needs `QBIT_USER`/`QBIT_PASS`; `embedded` needs a
   `DOWNLOADS_DIR` and, unless `VPN_REQUIRED=false`, a tunnel. An unknown value is a start-up error
   naming both, not a silent fall-back to either.
2. **Wire the tunnel under the engine, not under the process.** `main` builds `*vpn.Tunnel`, hands
   it to `engine.New` as its `Network`, and hands nothing to anybody else. The web server, TMDB, the
   indexers, minter and Jellyfin all keep the host's stack, which is what stops a bad tunnel config
   locking you out of the screen that fixes it.
3. **Close in the right order on shutdown**: HTTP server, then poller (it already dies with `ctx`),
   then engine, then tunnel. An engine closed after its network is an engine writing into a device
   that is gone.
4. **The settings view learns three facts** and no secrets: which backend is running, whether a
   tunnel is configured and handshaking, and **whether the tunnelled exit differs from the host's —
   as a boolean, not an address**. `GET /api/settings` is unauthenticated on the LAN
   ([D17](../decisions.md#d17--settings-is-read-only-and-the-settings-table-stays-unused)), and
   `configured: true` has always been the honest amount to say.
5. **Collapse add-then-confirm into one call.** `TorrentClient.AddMagnet` becomes
   `Add(ctx, magnet, hash, category) (torrent.Torrent, error)`. The engine's add *is* authoritative
   and returns the torrent. qBittorrent's cannot — measured in phase 3: `torrents/add` answers
   `200 Ok.` for a magnet it ignored and `Fails.` for one it already holds, and never returns a hash
   — so the add-then-look-up dance moves **inside that adapter**, which is the only place that
   knows why it exists. `Dispatch` loses its two-error reasoning and reads like what it does.

   **Outstanding.** Everything else in this task is built; this is not. It is a pure refactor —
   nothing works differently after it — and it touches the interface, both backends, dispatch and
   every fake in `internal/download`'s tests, so it is a commit of its own rather than a rushed
   passenger on this one. The two-error reasoning in `Dispatch` is correct today and its comments
   say why.
6. **The importer stops knowing about deployment paths.** `Paths`, `translate` and `relativeTo`
   leave `internal/importer`. The neutral `Torrent.ContentPath` becomes, by contract, **a path
   curator can open**: the engine has one namespace so there is nothing to translate, and the
   qBittorrent adapter — which is the thing with two namespaces — translates its own before handing
   the torrent over. [D13](../decisions.md#d13--downloads-are-scoped-by-a-qbittorrent-category-with-its-own-save-path)
   put the translation in the importer because there was one backend and the importer was the only
   place that knew both sides. With two backends that is no longer true, and the code moves to where
   the knowledge now lives.
7. **`ErrClient` stops being called qBittorrent.** It exists so the API can answer 502 for a
   dependency's failure and keep 500 for ours; with two backends, the dependency has a name that is
   configuration, not a constant.

## The two things the plan said to delete that survive, and why

The plan's T38 says to delete the `DOWNLOADS_PATH`/`QBIT_DOWNLOADS_PATH` pair and qBittorrent's
~110-line cookie-session layer. Both belong to the qBittorrent adapter, and
[D22](../decisions.md#d22--the-torrent-engine-moves-inside-the-binary-and-qbittorrent-becomes-the-second-backend)
**keeps that adapter on purpose** — it is the migration path for anyone already running the \*arr
stack and the fallback if the engine disappoints on real hardware. Deleting the session layer would
delete the backend; deleting the path pair would leave it able to report paths curator cannot open.

So they survive, scoped to one backend and unreachable from the other, and D22 already records the
criterion that removes all of it at once: **if the embedded engine runs the Pi for a full phase with
no fallback needed, the adapter goes, and this goes with it.** A deletion with a written trigger is
worth more than a deletion done early and reverted under pressure.

## Do not

- Start the engine, open the tunnel, or create `DOWNLOADS_DIR` when the backend is `qbittorrent`.
- Default `TORRENT_BACKEND` by sniffing whether `QBIT_USER` happens to be set. Configuration that
  guesses is configuration nobody can predict; the variable is one line in a `.env` and this is the
  phase where it changes.
- Change any response body in `internal/api`. A new 503 case for "no VPN" is a new *reason*, not a
  new shape — the envelope is the one phases 1–5 verified.
- Touch the Pi. Phase 10, after the \*arr configs are backed up (T52).

## Verify

The whole phase, end to end, on the laptop, with the local qBittorrent 5.1.2 container available for
the other backend:

```bash
npm --prefix web run build && go build ./... && go vet ./... && go test -race ./... \
  && GOOS=linux GOARCH=arm64 go build ./...
```

- **dispatch → complete → hardlink**, with equal inode and link count 2. Phase 4's proof, re-run
  against a backend phase 4 never saw.
- **restart mid-download** and confirm resume without re-downloading (T36), including with the
  network down.
- **kill the tunnel mid-download** and confirm transfer stops rather than falling back — bytes, not
  log lines.
- **place a file in `DOWNLOADS_DIR` by hand** and confirm it is never imported. The guarantee D13's
  category gave, now structural.
- `TORRENT_BACKEND=qbittorrent` with the same database: dispatch, poll and import behave exactly as
  phase 4 shipped them, and the exit-IP guard **refuses**, because that container has no VPN and its
  `last_external_address_v4` is this host's.
- an unknown `TORRENT_BACKEND` fails at start-up with a message naming both valid values.
- `go test -race ./...` with no environment set at all still passes — every one of these variables
  has a default or a documented unconfigured state.
