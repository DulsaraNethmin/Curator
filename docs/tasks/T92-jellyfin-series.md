# T92 — Jellyfin: `FindSeries`, and a Shows library

**Owns** the media-server half of [phase 11](../phase-11.md)

## What it owns

**`FindSeries` beside `FindMovie`.** `FindMovie` pins `IncludeItemTypes=Movie`, so every show
misses, falls back to `WebSearchURL`, and — because `browse.go` deliberately does not log
`ErrNotFound` — the degraded link is permanent and invisible.
[D32](../decisions.md#d32--the-jellyfin-link-is-keyed-on-the-tmdb-id-not-on-the-path)'s rules are
kept: match `ProviderIds["Tmdb"]` client-side and case-insensitively, because Jellyfin silently
ignores `Path` and `AnyProviderIdEquals`; send no `userId`, because a user-scoped library shrinks.

**A Shows library**, when and only when television is configured. `libraryName = "Movies"` is a
constant and `JellyfinSetup.LibraryPath` is single-valued; both become a pair, and the adopt flow's
`covers()` check runs per root.

## The constraint that governs this file

[D34](../decisions.md#d34--curator-provisions-a-jellyfin-it-brought-up-and-never-rewrites-one-somebody-is-already-watching):
curator provisions a Jellyfin it brought up and never rewrites one somebody is already watching.
Every `/Startup/` call re-reads `/System/Info/Public` first and refuses a server whose wizard is
complete — that guard exists because Jellyfin has none of its own, measured: `POST /Startup/User` on
a configured server returned 204 and changed the admin's password. It is not weakened here.

## Measured 2026-08-20

The Pi's Jellyfin already holds a library `Shows`, collection type `tvshows`, at `/tv`, left in place
by the phase 10 cutover. So on that box the adopt flow must classify the TV root as **covered** and
must not offer to add a second one. That is the assertion, not an assumption.

## Out of scope

`checkFilm` was already scoped to films in T88 — it proved a minted key with the first matched row,
and a show would have made it ask for a *Movie* carrying a *tv* id and report a good key as broken.

## What shipped

`FindMovie` and `FindSeries` share one `findItem` parameterised by item type;
`FindMovie`'s query, statuses and single-request behaviour are unchanged and
pinned by a test.

**`FindSeries` narrows by `years=` and, on a miss only, asks once more
unnarrowed** inside the same `lookupTimeout`. D32's 18-of-18 year measurement was
taken against films, and a show's Jellyfin `ProductionYear` is its first-air
year — carrying that narrowing across unmeasured would have reintroduced exactly
the permanent invisible miss this task exists to remove.

The Shows library is created with `collectionType=tvshows` through a
`LibraryKind` enum rather than a string, because it is the only wire difference
from Movies and **it fails silently when wrong**.

**`covers()` accepts a library's collection type as evidence for television**,
when no path comparison can work. That is the Pi's actual situation: its Jellyfin
holds `Shows` at `/tv`, which is the host's `/media/storage/media/tv`, which
curator sees as `/media/tv` through its own mount — and D32 already established
that mounts disagreeing is the normal deployment rather than the exotic one. It
is not a silent guess: the sentence names the library and its path and says what
to do if the assumption is wrong. It is deliberately **not** applied to films,
where T66 measured and pinned three cases this rule would turn from `unseen` into
`covered`. The asymmetry is safe in exactly one direction — it can only ever
remove an offer to write to somebody else's server, never add one.
