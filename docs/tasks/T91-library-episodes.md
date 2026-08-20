# T91 — `internal/library` learns episodes

**Owns** the disk half of [phase 11](../phase-11.md)
**Blocks** T93, which hardlinks what this finds

## What it owns

`ParseEpisode` (`S02E05`, `s02e05`, `2x05`, `S02E05E06` with the first number winning),
`FindEpisodes` beside `FindFeature`, the destination naming — `ShowFolder`, `SeasonFolder`,
`EpisodeName` — and `ScanShows` beside `Scan`. Plus `testdata/library/tv/` fixtures, so the parser
meets real strings the way `testdata/library/movies/` already makes it.

The layout is Jellyfin's own convention:

```
Severance (2022)/Season 01/Severance (2022) - S01E01.mkv
```

## The rules it inherits rather than re-decides

**Never guess.** An unparseable episode name is skipped and logged, exactly as `ParseFolder` treats
a folder that has drifted from `Title (Year)`.

**`destTitle` is reused, not reimplemented.** [D9](../decisions.md#d9--query-tmdb-with-the-raw-folder-title)'s
colon rule applies unchanged, and CLAUDE.md's trap holds: only ` - ` is a substitution, so
`Spider-Man` and `X-Men` must survive.

**`FindFeature` is not forked.** Its doc comment argues there is one answer to "which file is the
film" *by construction*, shared by the scanner, the importer and the stream endpoint. The 50 MiB
floor, the `sample`/`extras`/`featurettes`/`subs` skips, the hidden-file skips, the depth cap and
the no-symlink rule are the same rules, reused.

**[D33](../decisions.md#d33--a-folder-with-no-film-in-it-is-not-a-movie-the-row-goes-the-folder-stays)'s
asymmetry carries over exactly.** `Skipped{NoMedia: true}` **only** when a folder was read
successfully and holds no parseable episode, because that flag is what deletes a row. "Could not
tell" is not "no film". A failing `os.ReadDir` of the root stays fatal: an unmounted disk reading as
zero folders would prune the library.

## Out of scope

Anything that consumes it. The importer is T93.
