# T7 — Test fixture

**Owns:** `testdata/library/`
**Depends on:** nothing
**Status:** done — recorded so its provenance is not lost

## Goal

A stand-in library that the scanner can be tested against without SSH in the loop.

## What exists

`testdata/library/movies/` holds **29 empty directories using the exact folder names from the real
library** on the Pi, captured from `ls /media/storage/media/movies`. Real names matter: they are what
the parser has to survive, and a hand-invented fixture would not contain the cases that break it.

**8 of the 29 contain ` - `**, where a colon was replaced because it is illegal in filenames:

```
Avengers - Infinity War (2018)
Captain America - The First Avenger (2011)
Predator - Badlands (2025)
Spider-Man - Across the Spider-Verse (2023)
Spider-Man - Into the Spider-Verse (2018)
Spider-Man - No Way Home (2021)
Tom Clancy's Jack Ryan - Ghost War (2026)
X-Men Origins - Wolverine (2009)
```

The three Spider-Man titles and `X-Men Origins - Wolverine` are the important ones: they contain a
**real** hyphen and a **substituted** one in the same string, which is what makes a naive
`-` → `:` replacement wrong.

Also present: an apostrophe (`Tom Clancy's`), an ampersand (`Deadpool & Wolverine`), a two-character
title (`F1 (2025)`), and seven 2026 releases.

Four dummy files exercise the size logic:

| Path | Size | Purpose |
|---|---|---|
| `Interstellar (2014)/Interstellar (2014).mkv` | 4096 B | the feature — should win |
| `Interstellar (2014)/sample.mkv` | 512 B | a smaller video, must not win |
| `Interstellar (2014)/poster.jpg` | 128 B | not a video, must be ignored |
| `Spider-Man - No Way Home (2021)/movie.mp4` | 2048 B | a different extension |

The other 27 folders are empty, so "no video files" is covered too.

## Refreshing it

```bash
ssh pi 'ls /media/storage/media/movies' | while IFS= read -r n; do
  mkdir -p "testdata/library/movies/$n"
done
```

Keep the dummy files, or the size assertions in T3 stop meaning anything.
