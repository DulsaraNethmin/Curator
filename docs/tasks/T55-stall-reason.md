# T55 — the stall reason reaches the screen

**Owns:** one field on `internal/torrent`, `downloads.reason` in `internal/store`,
the poller's write in `internal/download`, one field in `internal/api/downloads.go`, one line of
Activity in `web/`
**Depends on:** T39 (the migration)

## Goal

Activity says *why* a download is stalled, instead of showing a word and leaving the sentence in the
log.

[T36](T36-resume-stall.md) built `stalled` and the reason both, and deliberately stopped at the log:
"carrying the reason into `GET /api/downloads` needs a column; that is phase 7's, where the store is
being touched anyway." This is that. The number is 55 because the plan reserved T43–T54 for phases
8–10 and a task found afterwards takes the next free number rather than displacing one other
documents cite.

## Do

1. **The neutral torrent gets a seventh field**, and it is the one T36 argued against having six of.
   `Reason string`, empty for a backend that does not know and empty for a torrent that is fine. The
   comment on `internal/torrent` says the type carries six fields because six are read; update it
   with why the seventh exists rather than leaving a doc comment that contradicts the struct.

   T36's reasoning was that the poller cannot know the reason and the backend can. That is unchanged
   — this only moves the sentence the backend already produces onto the value it already returns.

2. **The engine fills it from `stallReason`**, which exists (`internal/engine/engine.go`) and already
   distinguishes no peers, peers without metadata, and peers that will not send. One call site, no
   new logic, and the log line it currently feeds keeps its suppress-repeats discipline.

3. **qBittorrent fills what it honestly can.** `stalledDL` means "no peers, no progress" and that is a
   reason: say it in one short sentence. It has no peer count to add, and inventing a richer one from
   a state string would be a sentence the backend cannot support.

4. **`downloads.reason TEXT`, through T39's migration.** Nullable, no default, no index — it is read
   with the row and never queried on. `schema.sql` gains the column too, so a fresh database gets it
   from the schema and an existing one from the migration, and a test proves both paths end the same.

5. **The poller writes it with the state.** `UpdateDownloadProgress` takes it alongside progress:
   same transaction, same tick, because a reason that lags the state it explains is worse than none.
   **A torrent that starts moving clears it** — an empty reason overwrites a stale one rather than
   being skipped as "no news".

6. **`GET /api/downloads` carries `reason`**, omitted when empty. Additive; nothing else in that
   response moves.

7. **Activity shows it under the badge**, small and muted, on `stalled` rows only. The badge is the
   state, the sentence is the reason, and a row that is downloading normally gains nothing.

## Do not

- Add an index, a second column, or a `reason_at`. It is a sentence about the current state, written
  with the current state.
- Let the poller compose the reason. It sees a percentage that did not move; it cannot tell "nobody
  has this" from "nobody will send it", which is precisely the distinction worth showing.
- Make `stalled` terminal or let a reason change what anything does.
  [T36](T36-resume-stall.md) settled that: it is a description, and a peer appearing puts the row
  straight back to `downloading`.
- Show the reason on states that are not `stalled`, or keep the last one after recovery.

## Verify

Hermetic, on the two-engine harness T35 built:

- a torrent with no peers past `TORRENT_STALL_AFTER` reports `stalled` **with** a reason, in the row
  and in `GET /api/downloads`
- the reason distinguishes at least "no peers" from "peers, no metadata"
- a peer appearing clears the reason and the state in the same tick
- the qBittorrent adapter maps `stalledDL` to `stalled` with its own sentence, and `stalledUP` still
  to `completed`
- **the migration from both directions**: a database created before the column, and a fresh one, both
  serve a reason; running the migration again changes nothing
- an empty reason is omitted from the JSON rather than serialised as `""`

Then live: the dead-magnet case phase 5 recorded and never fixed — the GalaxyRG release advertising
2,508 seeders whose magnet carries only dead trackers — sits in Activity saying **why** rather than
sitting at `queued` for ever with nothing on screen to explain it.
