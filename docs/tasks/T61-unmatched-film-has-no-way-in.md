# T61 — a film TMDB never matched still has a way in

**Owns:** how a library film with no `tmdb_id` is addressed, and the card that opens it
**Depends on:** [T60](T60-library-way-in-web.md), which made every *matched* card clickable and
deliberately left this one out
**Status:** specified, not started

## Goal

Every film in the library can be opened, watched in the browser, and opened in Jellyfin —
including the ones TMDB could not match.

[T60](T60-library-way-in-web.md) made the Library card open the film. It only did so for a row that
has a `tmdb_id`, because `/movie/` is addressed by TMDB's id
([D21](../decisions.md#d21--the-movie-page-is-movieid-because-the-ui-is-a-static-export)) and the
page parses it with `/^\d+$/`. A row with `tmdb_id` NULL has no URL to go to, so its card stays a
`<div>` and the `unmatched` badge is the only explanation offered.

**The film itself is fine.** `/api/movies/{id}/stream`, `/remux`, `/subtitles/{name}` and
`/playback` all take **curator's `movies.id`** and never touch TMDB — the stream endpoint resolves
`library_path` and asks `library.FindFeature`. So curator can already play these films perfectly. The
only thing missing is a page addressed in a way that does not require a catalogue match.

Jellyfin is the same shape and needs its own answer: `details.jellyfin_url` is built by looking the
film up in Jellyfin **by TMDB id** ([D32](../decisions.md#d32--the-jellyfin-link-is-keyed-on-the-tmdb-id-not-on-the-path)),
so an unmatched film has no link even once it has a page. D32 already names the fallback for a miss —
a link to a Jellyfin **search** for the title, which always lands somewhere useful and never 404s.

## Why this is not urgent, and why it is not nothing

[D33](../decisions.md) shrank the population sharply. 15 of the 29 folders on the Pi were unmatched,
but most of those were **empty folders** — and those are no longer rows at all. What is left is the
real unmatched films: a folder with a video in it whose name TMDB could not resolve, which is
exactly the case [D9](../decisions.md#d9--query-tmdb-with-the-raw-folder-title) exists for and
exactly the row [D6](../decisions.md#d6--tmdb_id-is-nullable) made the column nullable to keep.

**Count it before building it.** Scan the Pi's library read-only and see how many rows come back
with a film in them and no `tmdb_id`. If it is zero, this is a design gap with no instances and the
right move is to say so in the task file and stop. If it is not zero, those are films sitting on a
disk that curator can play and offers no way to reach.

## The design question, stated and not answered here

**How is a page for a film with no TMDB id addressed?** Three shapes, none free:

1. **`/movie/?curator_id=<movies.id>`** — one more query parameter on the page that already exists.
   Cheapest, and it keeps one page. But it is a **second addressing mode** on a screen whose whole
   identity is "the film comes from TMDB" ([D20](../decisions.md#d20--the-film-comes-from-tmdb-the-search-box-only-finds-it)),
   and every fetch on that page is a TMDB call that would have to become conditional. T60's task file
   ruled it out *for T60*; that was a scope decision, not a verdict.
2. **A separate `/library/film/?id=<movies.id>`** — a page about the row rather than about the
   catalogue entry. Honest, and it has no TMDB call to make conditional: title, year, size,
   `library_path` and the player all come from `GET /api/movies/{id}`. Costs a second page and a
   second place the player is mounted.
3. **Match it properly instead** — a "search TMDB for this folder" action on the unmatched card that
   lets a human pick the right film and writes the `tmdb_id`. This makes the problem go away rather
   than routing around it, and it is the only option that also fixes the poster, the overview and the
   Jellyfin link. It is also the largest, and it is a manual-match feature nobody has asked for yet.

**Decide before writing code, and record it.** Whichever wins is a decision record, because it either
amends D21's "the page is addressed by the TMDB id" or explicitly declines to.

## Do

Deliberately unspecified until the question above is answered and the count above is taken. What is
fixed either way:

- the card for an unmatched film **opens something**
- that page can play the film — the player takes `movies.id`, which these rows have
- Jellyfin gets D32's search fallback rather than a missing button
- the `unmatched` badge stays. It is still the row that wants human attention (D6), and this task
  makes it reachable rather than making it disappear

## Do not

- **Guess a `tmdb_id`.** D9 is explicit and the reason is measured: seven of the fixture's titles are
  2026 releases where a confident-but-wrong match is entirely plausible, and an unconstrained query
  for a bare title returns one.
- **Key the Jellyfin link on the path.** D32 measured that and it does not work: Jellyfin's `Path`
  filter is silently dropped, its library location is its own bind mount, and its `Path` is the file
  where `library_path` is the folder.
- **Make the scanner record a placeholder id.** A row that claims a match it does not have is worse
  than one that admits it has none.

## Verify

- an unmatched film in a `t.TempDir()` library is reachable from the Library screen, plays through
  the existing stream endpoint, and offers a Jellyfin link that lands on a search
- a matched film's route is **unchanged** — `/movie/?id=<tmdb_id>` (D21), and no card regresses
- `next build`'s type check, which is the UI's whole guarantee

---

## Reported by

Nethmin, 2026-08-15, from the Library screen. **Half of that report was a stale process and is worth
recording so it is not re-investigated:** `localhost:8090` was still serving a `go run` binary built
**Thu Aug 13 15:42**, two days before T57–T60, so it showed the empty folders the old scanner
records and cards that were still `<div>`s. Its configuration was otherwise identical to the one
T57–T60 were verified against, and restarting it on the merged binary reproduced the verified result:
`removed: 28`, one card, clickable. **A long-running dev server does not pick up a merge** — that is
the trap, and it is not a bug in this code.
