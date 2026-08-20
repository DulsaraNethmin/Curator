# Phase 11 — Television

The phase every earlier one deferred, and the only one that reopens something the roadmap listed
under *Deliberately out of scope*. It is allowed to, because
[D6](decisions.md#d6--tmdb_id-is-nullable) put `media_type TEXT NOT NULL DEFAULT 'movie'` into the
schema in phase 1 **on purpose** — *"so TV is additive later"* — and nothing has ever written
anything but `'movie'` to it. This is later.

**Done when** you can search a show, pick a release, and watch the episodes on the television —
having done nothing to the film half except make it say which media type it means.

---

## The scope, which is deliberately narrower than Sonarr's

> Search a show on TMDB, see ranked releases, pick one, download it through the tunnel, hardlink the
> episodes into a TV library, tell Jellyfin. **One deliberate grab at a time, exactly like a film.**

[D5](decisions.md#d5--manual-search-not-automatic-grabbing) is **extended to a second media type,
not reversed**. So: no monitoring, no scheduler, no RSS, no quality profiles, nothing grabbed
unattended, and no season-by-season "what am I missing" tracking. Both **season packs and single
episodes** import. **YTS is skipped for TV searches** rather than queried and discarded, because it
is `/list_movies.json` and has no TV surface at all.

Television is **opt-in and off by default**. An unset `LIBRARY_TV` means no Shows tab, no TV rails,
and the TV routes answering 503 naming the variable — the same posture `QBIT_USER` and
`JELLYFIN_URL` already have. That is what keeps the product honest for anyone who wants exactly what
the README promised before this phase.

## Out of scope, said here so it is not discovered from a 404

- **Playing an episode in the browser.** `GET /api/movies/{id}/stream` serves one file per row and a
  show is not one file. Episodes play in Jellyfin, which is what the Shows library is for. This
  needs no new guard — `stream.go`'s `AssertInside(s.libraryRoot, …)` already refuses a row under
  the TV root by construction, and that refusal is correct rather than a gap to close.
- **Monitoring, and everything that hangs off it.** See D5, above.
- **A fourth indexer.** EZTV has said *"TV, a later phase"* in `architecture.md` since phase 2 and
  is still later: TPB's TV categories and 1337x's keyword search are what this phase uses.

---

## The reading this is built on — **measured 2026-08-20**

| | |
|---|---|
| `apibay.org/q.php?q=severance&cat=205,208` | **works** — 100 rows, season packs *and* single episodes. `Severance - Season 1 - Mp4 x264 AC3 1080p` 844 seeders; `Severance S02E05 … WEB-DL` 381 seeders. 205/208 are TPB's TV categories. |
| Pi `/media/storage/media/tv` | exists, **empty**, `1000:1000`, sibling of `movies` and `downloads` on one filesystem — so [D8](decisions.md#d8--import-by-hardlink)'s hardlink works there unchanged |
| Pi Jellyfin `/Library/VirtualFolders` | already holds **`Shows` · `tvshows` · `/tv`**, left in place by the phase 10 cutover. curator does not need to create it on that box. |
| `SearchMovie` in the tree | **29 references outside tests across 12 files, 201 inside** — and two different methods share the name, `tmdb.Client`'s and `indexer.Indexer`'s |

---

## Tasks

| | | |
|---|---|---|
| [T88](tasks/T88-two-media-types.md) | store and config learn there are two media types | **done** |
| **T89** | `internal/tmdb` grows the TV half | |
| **T90** | the indexer seam generalises, and YTS declines TV | |
| **T91** | `internal/library` learns episodes | |
| **T92** | Jellyfin: `FindSeries`, and a Shows library | |
| **T93** | the importer files a whole season | |
| **T94** | the API grows a parallel tree | |
| **T95** | the UI | |
| **T96** | the documents | |

`make status` derives this table's task numbers from the file above rather than from a list somebody
has to remember to update. Where the two disagree, `make status` is right.

## The shape of the work

T88 lands alone, because everything imports it. T89, T90, T91 and T92 are genuinely independent —
four packages, four worktrees, no shared files — which is the one place in this repo where
parallelism pays, and `CLAUDE.md` says why the ceiling is nearer 2x than Nx. T93 needs T91, T94 needs
the four, T95 needs T94's route shapes, and T96 is coupled prose and therefore one lane and one mind.

**One worktree per agent is not tidiness.** `web/scripts/embed.mjs` does `rm -rf` on
`internal/web/dist` and rebuilds it in place, while `//go:embed all:dist` is a compile-time
directive — so a second `make check` in one tree walks a directory being deleted under it. See
`CLAUDE.md`.

---

## The two traps that decided the design

**A movie scan would have deleted every TV row.** `prune`'s switch puts `case outside` — computed as
`AssertInside(s.libraryRoot, row.LibraryPath) != nil` — *before* `case recorded[key]`, and
`MoviesOnDisk` had no media filter. A show row is not merely unfound by a movie scan; it is
affirmatively deleted, with a log line reading *"its library_path is outside LIBRARY_MOVIES, so it
can never be served"*, taking its downloads with it via the foreign key. The first movie scan after
the first TV import would have emptied the TV library.

**A show would have quietly taken a film's identity.** `MoviesMissingMetadata` selects
`WHERE tmdb_id IS NULL`, and a show's `tmdb_id` is NULL by construction — so every show lands on the
matching pass's work list on every scan, gets looked up against TMDB's `/search/movie`, and is
written back with `SetTMDBMetadata`, which overwrites unconditionally by design. For **Fargo,
Watchmen, Hannibal, Westworld, Dune and Snowpiercer** that lookup *succeeds*.

Both are [D48](decisions.md#d48--television-is-additive-a-show-is-a-row-in-movies-and-the-second-library-root-is-opt-in),
along with the third — `ScannedMovie.MediaType` defaulting to `movie` while `UpsertMovieByPath`
rewrites the column from it on every pass.

## Verification

Per commit, in every worktree: `make check`. Then, end to end, **in this order** — the two that lose
data first, before anything is built on top of them:

1. A movie scan does not touch a show row, and a show scan does not touch a film. Both directions.
2. A `ScannedMovie` with no media type is refused rather than written as a film.
3. A show named *Fargo* with a NULL TMDB id survives a scan without acquiring the 1996 film's poster.
4. `LIBRARY_TV=./testdata/library/tv`, `POST /api/scan` twice — idempotent, movie rows untouched.
5. `GET /api/search?title=Severance&media=tv` returns ranked releases, with YTS reported **not
   applicable** rather than failed or empty, and TPB answering from `cat=205,208`.
6. A real season pack, from the UI, through the tunnel, into `Season NN/` — with `stat` showing
   **link count 2 on each episode** and the source untouched. D8's proof, per file.
7. A single episode into a show that already has that season, proving `library.Link`'s
   same-inode-is-success path rather than an overwrite.
8. Jellyfin's `Shows` library picks the episodes up from the refresh curator already sends.

The Pi is a separate decision. Nothing in this phase deploys.
