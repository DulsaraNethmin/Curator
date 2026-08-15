# T51 — the documents say what the product is

**Owns:** `README.md`, `.env.example`, and the stale passages in `docs/`
**Depends on:** [T47](T47-image.md), [T63](T63-compose.md) — the quickstart has to be a command that
works, not one that will

## Goal

Somebody who finds this repository can tell what it is and run it. Right now they cannot: the README
says **"Status: phase 1 of 6, in development"**, **"Thirteen containers become six"**, and lists
qBittorrent as a dependency — which stopped being true in phase 6, when the torrent engine moved
inside the binary.

**T51 is this, and not the first-run wizard.** `phase-7.md:384` claims the number for a wizard;
`phase-6.md:24` claims it for the documents, and phase 6 *deliberately left them stale in exchange* —
*"Correcting them here would mean touching them twice."* Renumbering would strand a promise made in a
shipped phase's document, so the wizard moved to [T50](T50-first-run.md) instead, and correcting
`phase-7.md:384` is one of the lines this task owns.

## Do

1. **The README's quickstart is the product.** For a phase whose promise is one command, the first
   thing on the page is that command and what it gets you:

   ```
   curl -fsSL <compose.yaml> -o compose.yaml && docker compose up -d
   ```

   Then: open `http://<host>:8090`, add a TMDB key, point it at your films. Then the sentence about
   watching on a TV, and the one profile line
   ([T63](T63-compose.md), [T65](T65-playback-screen.md)).

2. **Fix the four factual wrongs**, each of which is a specific string rather than a vibe:
   - *"phase 1 of 6"* — there are ten phases and this is the ninth.
   - *"Thirteen containers become six"* — that was the Pi-specific goal phase 6 replaced.
     `phase-6.md:13` says why: *a container count on one particular Raspberry Pi is not a metric*.
     The product is one container, plus Jellyfin if you want it.
   - **qBittorrent as a dependency** — it is now the *second* backend, kept as a migration path and a
     fallback ([D22](../decisions.md#d22--the-torrent-engine-moves-inside-the-binary-and-qbittorrent-becomes-the-second-backend)).
     The default is curator's own engine, over a tunnel curator owns.
   - the **how it works** diagram, which still routes downloads through qBittorrent and says nothing
     about the VPN — the one property phase 6 exists for.

3. **Say what curator does *not* do, in the README.** No television, no automatic grabbing, no users,
   no transcoding. Everything in that list is a deliberate decision with a record behind it, and
   stating them up front is what stops the first three issues anyone files.

4. **`.env.example` is a laptop's file, and the container has a different one.** It currently carries
   `192.168.1.26` addresses — this Pi's — as though they were defaults, and `LIBRARY_MOVIES` pointing
   at `testdata/`. Split honestly: what a **developer** sets to run from source, and what the
   **compose file** sets, which is nearly nothing because phase 7 made the rest writable from the
   browser.

5. **The dead `yts.mx`.** `yts.mx` is NXDOMAIN; YTS is
   `https://movies-api.accel.li/api/v2` ([D12](../decisions.md#d12--yts-is-reached-at-movies-apiaccelli-not-ytsmx)),
   and `yts.rs` / `yts.hn` resolve and look plausible while being clone sites running a
   re-implemented API. Anywhere a document still says otherwise.

6. **`phase-7.md:384`**, which now points at the wrong task number. One line.

7. **`architecture.md` and `roadmap.md` draw a six-container after-state.** `phase-6.md:13` names
   them both as wrong and defers the fix here. They do not need rewriting from scratch — they need to
   stop describing an abandoned goal as the target.

8. **Backing up means the volume, and the volume means the WAL.** Wherever the README tells someone
   how to keep their data: `curator.db-wal` was measured at **951 KB and two days newer than
   `curator.db`**, so copying the `.db` alone restores a stale snapshot that produces plausible
   answers for the wrong reason. And
   [D28](../decisions.md#d28--settings-are-writable-secrets-are-encrypted-at-rest-and-write-only-across-the-api)
   is explicit that the secret key sits beside the database — **anything that copies the volume copies
   both**, which is the point and also the warning.

## Do not

- **Rewrite the phase documents.** `phase-1.md` through `phase-8.md` are records of what was decided
  and measured at the time, and a record edited to match today is not a record. The exception is a
  line that misdirects the *next* reader — a wrong task number, a URL that no longer resolves — which
  is what this task is for.
- **Touch `docs/decisions.md`'s existing entries.** Decisions are amended by later decisions and
  cross-linked, never edited.
- **Promise anything unbuilt.** No television, no Chromecast, no "coming soon". The README describes
  what the tagged image does.
- **Delete CLAUDE.md's traps as stale.** The title-parsing trap, the pure-Go SQLite rule, minter's
  IPv4-only binding and the "do not touch the Pi" rule are all still live.
- **Leave the quickstart aspirational.** If [T47](T47-image.md) and [T48](T48-release-pipeline.md)
  have not published an image yet, this task waits. A README whose first command fails on a pull is
  worse than the stale one it replaced.

## Verify

- **Run the quickstart, on a machine that has never run this**, exactly as written, from a clean
  Docker. That is the only verification that counts and everything else here is secondary to it.
- `grep -rn "phase 1 of 6\|Thirteen containers\|yts\.mx" README.md docs/ .env.example` returns
  nothing outside a historical passage that is explicitly marked as one
- every link in the README resolves, including the anchors into `decisions.md`
- `make status` still derives the same phase table — this task edits prose, and prose is exactly what
  it is designed not to read
- a reader who has never seen the repository can say, from the README alone, what curator does, what
  it deliberately does not, and what it needs to run
