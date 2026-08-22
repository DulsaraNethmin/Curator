# T107 — Activity says how fast, how long, and can stop it

**Owns** `torrent.Torrent`'s two new fields and its sixth state, the engine's rate sampler and
per-torrent pause, qBittorrent's stop/start, `download.Active` and the live join, two API routes,
and the Activity screen · **decides**
[D55](../decisions.md#d55--a-pause-is-a-state-a-hold-is-a-reason) and
[D56](../decisions.md#d56--speed-and-eta-are-read-never-recorded-and-eta-has-one-definition) ·
**lifts** the constraint `internal/qbit` and `download.TorrentClient` both recorded · **bound by**
[D19](../decisions.md#d19--curator-deletes-only-what-it-created),
[D27](../decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket) and
[D47](../decisions.md#d47--every-torrent-network-operation-is-tunnel-bound-or-disabled)

## What it owns

The screen before this had a 6px bar and no number on it, and its own lede said *"Nothing here can
pause, resume or delete a torrent — that stays qBittorrent's business."* That sentence was stale
twice over already: the default backend has been curator's own since D22, and D19 added
delete-via-movie.

```
downloading   42% · 3.4 GB of 8.0 GB        Pause   Remove
              4.0 MB/s · 20 min left
paused        42% · 3.4 GB of 8.0 GB        Resume  Remove
imported      100%
```

Two lines, deliberately: how much above how fast, so a paused or finished row loses the second line
entirely rather than reflowing.

## The two decisions, in a sentence each

**D55 — a pause is a state; a hold is a reason.** They run the identical three anacrolix calls, and
the discriminator is what the screen does next: *a control needs a machine-readable state, a policy
needs a sentence*. The row has to draw a **Resume button**, and a button cannot be drawn on prose.

**D56 — speed and ETA are read, never recorded.** `Poller.Tick` writes a row only when something
moved; a rate column would change every tick and defeat that permanently. The ETA is computed above
both backends, because qBittorrent has an `eta` and the engine does not, and decoding theirs would
make "minutes left" depend on `TORRENT_BACKEND`.

## MEASURED — do not re-derive

Against a **qBittorrent stub over real HTTP**, through the real `internal/qbit` client, the real
join, and the real API. The stub is a 5.x: it answers `torrents/stop` and 404s `torrents/pause`.

```
GET /api/downloads, backend UP
  AAAA1111 downloading  rate 4194304   size 8589934592   eta 1187
  FFFF6666 imported     rate —         size —            eta —        <- the stub has no such torrent

GET /api/downloads, backend DOWN (QBIT_URL=http://127.0.0.1:9)
  both rows present, and NOT ONE live key on either — the JSON is byte-identical
  to the shape phase 3 shipped
```

`eta 1187` is curator's own arithmetic: 8 GiB × (1 − 0.42) ÷ 4 MiB/s ≈ 1187 s. **The stub sends
`"eta": 999999` on the wire and it is ignored**, which is D56's second half demonstrated rather than
asserted.

The status codes, over HTTP:

```
POST /pause  unknown hash        404  {"error":"curator has no download with that hash"}
POST /pause  imported row        409  {"error":"that download is already in the library, so
                                       there is nothing running to stop"}
POST /pause  backend unreachable 502  {"error":"curator could not reach the torrent client,
                                       so the download was left as it was"}
POST /resume backend unreachable 502  same
             → and the row is STILL `downloading` afterwards: a stop that did not
               happen must not leave a `paused` row with a Resume button on it
POST /pause  reachable           200  {"state":"paused"}   then the poll keeps it paused
POST /resume reachable           200  {"state":"queued"}   then the poll moves it to downloading
```

The engine's arithmetic, against an injected clock (`internal/engine/rate_test.go`):

```
5 MB / 5 s   → 1 MB/s          the interval is measured, not assumed
10 MB / 2 s  → 5 MB/s
1 MB / 40 ms → carries 1 MB/s  below rateFloor; naively it would read 25 MB/s
count falls  → 0 B/s           a re-hash is not a negative download
at 29 s      → the rate holds, and `stalled` has not fired
at 31 s      → 0 B/s, and `stalled` still has not fired
at 5 m       → stalled          the two signals are independent and in that order
```

## TRAPS found the hard way

- **The five-minute lie.** A paused torrent gains no bytes, so the stall detector is correct that
  nothing is arriving and wrong about why — after `DefaultStallAfter` the row would say *"no peers
  are connected"* about a download curator stopped on request. That is T78's defect from a third
  direction. Guard: `describe` skips the stall branch when `isPaused`.
- **Pause and resume must delete the hash's `mark`.** `mark.since` is when the count last *moved*,
  and a pause is hours of it not moving — so without the delete, the first observation after a
  resume reports `stalled` immediately. `TestResumingDoesNotInheritTheStallClockFromThePause` pauses
  for two hours and requires the row not to be stalled on the first look.
- **`Release()` would un-pause everything.** It loops every torrent and allows it, so one shared set
  would make any tunnel blip silently restart every download somebody deliberately stopped, with
  nothing on screen saying so. Guard: the paused set is separate and `Release` skips it.
- **A pause that lived only in the backend would not survive a reboot.** `Service.Resume` re-adds
  every non-imported row by magnet at boot and the engine rebuilds from disk with no memory of a
  preference. The row is the record, and the re-pause happens *after* the add — a paused download
  the client does not hold at all cannot be resumed later.
- **qBittorrent 5.0 renamed pause to stop.** `state.go` has documented the matching rename of the
  *state* names since phase 3. Both spellings are sent, newest first, and `notFound` reads `do()`'s
  own rendering of a 404 — a coupling pinned by `TestNotFoundMatchesWhatDoActuallyWrites`, because
  if either side drifts the 4.x fallback dies silently.
- **A paused row kept reporting a rate.** The engine zeroes it; the qBittorrent path trusted the
  backend. Found with a stub that kept claiming 4 MiB/s after a stop, and fixed in the join above
  both — "paused · 4.1 MB/s" is a nonsense line whose appearance must not depend on which backend
  is running.

## Remove deletes the film, not the torrent

There is no per-download delete and deliberately no second destructive path. Remove calls the same
`DELETE /api/movies/{id}` the Library screen does, which takes the torrent, the partial files, the
download rows, the movie row and the library folder in D19's order.

On this screen the row is named by a **release**, so the confirmation names the film:

```
Remove Deadpool (2016)?
This stops the download and deletes the film — not just this release — and cannot be undone.
  · the torrent Deadpool.2016.1080p.BluRay.x265 stops and is removed, with whatever
    it has downloaded so far
```

For a show it reuses the Library's own words, *"and every season under it"*, because one season's
row deletes all of them. That confirmation is why the screen reads the library once on mount —
`store.Download` carries no title, no size and no media type — and it pays for itself twice, because
the table gains the film's name beside the release name, which it never had.

## Still outstanding

- **No real torrent was measured.** The rate against a moving download, the ETA against the actual
  time to completion, and the six-minute observation proving the stall detector stays quiet on a
  paused torrent are all **unrun**. The laptop's only NordLynx endpoint (`187.15.102.104`) was held
  by a long-running instance with 4.6 hours of uptime, and bringing a second tunnel up would make it
  flap — CLAUDE.md's own warning. Sending un-tunnelled torrent traffic instead would break D47. What
  stands in for it: the engine's sampler against an injected clock, its pause tests against a real
  anacrolix client holding a real seeded torrent, and the HTTP surface against a stub.
- **`torrents/stop` was never sent to a real qBittorrent.** The fallback is pinned by a stub that
  404s the modern name; which path a real 5.1.2 takes is unmeasured. The Pi's qBittorrent is dead
  and staying dead (D43), so this needs a container somebody stands up on purpose.
- **`bytes_freed` is 0 for a download in flight**, because `Deletion` reads it from
  `movie.SizeBytes`, which the importer writes after the fact. The dialog does not print it for such
  a row, but nothing yet reports what the partial actually freed.

## Verification

```bash
# the stub, and curator pointed at it
python3 qbstub.py 8123
QBIT_URL=http://127.0.0.1:8123 QBIT_USER=u QBIT_PASS=p DOWNLOAD_POLL_INTERVAL=3s <curator>

curl -s :8097/api/downloads | jq -r '.[] | [.state, .download_rate, .size_bytes, .eta_seconds] | @tsv'
curl -s -w '%{http_code}\n' -XPOST :8097/api/downloads/$H/pause
curl -s -w '%{http_code}\n' -XPOST :8097/api/downloads/$H/resume

# and with the backend gone — every row must still list, without the keys
QBIT_URL=http://127.0.0.1:9 <curator>
curl -s :8097/api/downloads | jq 'length'
```
