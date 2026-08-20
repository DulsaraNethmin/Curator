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
