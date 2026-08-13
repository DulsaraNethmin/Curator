# T36 — resume, and a stall you can see

**Owns:** `internal/engine/` — resume and stall; `internal/download/poller.go`; one state value in
`internal/torrent`, `internal/store` and `web/`
**Depends on:** T35

## Goal

Two gaps, both of which only exist because the download used to happen in somebody else's process.

**Resume.** curator restarts and every unfinished download picks up where it was, without
re-downloading a byte and without needing the swarm to hand it the metadata again.

**Stall.** A torrent nobody is seeding stops looking exactly like a torrent that has not started
yet. This is the live bug: a magnet sits at `metaDL` for ever, Activity says "queued, 0 %", and
nothing anywhere says why.

## Do

1. **Boot re-adds every `downloads` row that is not `imported`.** The table already stores the hash
   **and** the magnet — [T14](T14-downloads-store.md) put them there in phase 3 — so this needs no
   column, no query and **no migration**. This repo has never run one.
2. **Prefer the persisted metainfo over the magnet.** T35 wrote `DataDir/.curator/<HASH>.torrent`
   when the info dict arrived. With it, an add is local, instant and works with the network down.
   Without it, the magnet is the fallback and boot needs peers — which is the whole reason the file
   is written. Say which path was taken in the log line; "resumed 3 downloads" that quietly means
   "and two of them are waiting on strangers" is the kind of line that costs an evening.
3. **Rows that read `imported` are not re-added, so seeding a film ends at the next restart.** That
   is a decision, not an oversight: every re-added torrent costs peers, sockets and resident memory
   on a Pi with 8 GB, and re-adopting films curator has already filed is the one behaviour with an
   unbounded cost. Seeding policy is a feature nobody has asked for; when somebody does, it is a
   setting and not a surprise.
4. **Pay the verify exactly once, where it matters.** Resume is optimistic: on add, the engine
   reports every piece complete out of its completion database *without reading a byte* — that is
   what makes it free. A piece this process downloaded was hash-checked as it was written, so the
   only unverified claim is one **restored from disk**. So: a row that comes back as `completed` but
   not `imported` is force-verified before the importer is allowed to hardlink it, and reports
   `queued` while it runs. Measured cost: **15.7–17.8 s for 755 MB**, once per film, against
   hardlinking a corrupt file into the library and telling somebody it is a movie. Corruption is
   really caught — 1 MB damaged came back as 3016 of 3020 pieces, exactly the four affected.
5. **A fifth state: `stalled`.** Add it to `internal/torrent`, to `store`'s documented values and to
   `schema.sql`'s comment — a TEXT column with a comment, so again no migration. The engine reports
   it when a torrent has made **no progress and has no peers** for `TORRENT_STALL_AFTER` (default
   5 minutes; metadata took 3.2 s in the spike, so five minutes is not a tight rope).
6. **qBittorrent gets the same word for the same thing.** `stalledDL` means precisely "no peers, no
   progress" and currently maps to `queued`, which is where the metaDL confusion comes from on that
   backend too. It becomes `stalled`. **`stalledUP` stays `completed`** — a finished torrent with no
   leechers is not a problem, it is a Tuesday.
7. **The reason is a log line, once per torrent.** The poller logs the transition into `stalled` with
   what is actually wrong — no peers for the magnet, or peers but no bytes — using the
   suppress-repeats discipline `importer.logFailure` already has, because a five-second poll must
   not produce a five-second warning. Carrying the reason into `GET /api/downloads` needs a column;
   that is phase 7's, where the store is being touched anyway. Activity shows the state, Logs shows
   the sentence.
8. **One line of UI**, because `stalled` needs a badge like the other four. Nothing else in `web/`
   changes.

## Do not

- Add a column, an index or a migration. If a step here seems to need one, the step is wrong.
- Verify on every boot. A full re-hash of every completed film at start-up is minutes of disk on a
  Pi to re-answer a question that was already answered when the file was written.
- Let `stalled` be terminal or let it block anything. It is a *description*, not a failure: the
  torrent stays added, the poller keeps polling, and a peer appearing puts it straight back to
  `downloading`. `failed` still means what it meant.
- Re-add a torrent the engine already holds. Boot resume runs once, before the poller starts, and a
  second add of a live torrent must be a no-op rather than a reset.
- Adopt files found in `DataDir` with no row.
  [D14](../decisions.md#d14--the-importer-is-driven-by-the-pollers-torrent-list-not-by-a-completion-event)
  refuses to invent a film, and a resume path that scanned the disk instead of the table would
  invent several.

## Verify

Hermetic first, with the two-engine harness T35 built:

- a client that is stopped and rebuilt over the same `DataDir` reports its torrent complete and
  transfers **0 bytes**
- with the metainfo file deleted, the same restart falls back to the magnet and says so
- a completed-but-not-imported row is verified before its first import, and a payload corrupted
  underneath it comes back **incomplete** rather than being hardlinked into the library
- a torrent with no peers becomes `stalled` after the configured interval and `downloading` again
  the moment one appears
- `stalledDL` maps to `stalled`, `stalledUP` still maps to `completed` — added to the table-driven
  state test, which already walks every entry
- the stall warning is logged **once**, not once per tick

Then live:

- kill curator mid-download, start it again, and watch the progress carry on from where it was
  rather than from zero — with the network stack up and, separately, with it down, which is what
  the metainfo file buys
- dispatch a magnet nobody is seeding and watch Activity say `stalled` and Logs say why, inside
  five minutes
- the orphan warning that currently fires every five seconds for a hand-added *Avengers (2012)*
  is **gone on the embedded backend** — not suppressed, gone, because the engine only ever holds
  torrents curator added
