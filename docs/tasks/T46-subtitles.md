# T46 — the subtitle sidecar

**Owns:** `internal/importer/importer.go`, `GET /api/movies/{id}/subtitles/{name}` in
`internal/api/stream.go`
**Depends on:** T43

## Goal

Subtitles that came with a download end up beside the film in the library and show up as a track in
the player. This fixes a live gap rather than adding a feature: the subtitles for Backrooms have been
sitting in the downloads folder since phase 4, because the importer hardlinks the feature and nothing
else.

## Do

1. **Link the sidecars beside the feature, in the same import.** `.srt`, `.ass`, `.ssa`, `.sub`,
   `.vtt` — found beside the feature file, or one level down in a `Subs`/`Subtitles` folder, which is
   where release groups actually put them (and which `library.FindFeature` already knows to *skip*
   when it is looking for video).

2. **Name them off the feature, keeping the language suffix.** `Title (Year).en.srt` beside
   `Title (Year).mkv`. That is the convention Jellyfin, Plex and VLC all already read, so this one
   rename makes the file work in three players that are not curator. A sidecar with no recognisable
   language keeps its stem and no suffix.

3. **Same rules as the feature, for the same reasons.** `library.Link` — hardlink, never move, never
   copy over, never delete the source
   ([D8](../decisions.md#d8--import-by-hardlink)) — and `library.AssertInside` on the destination.
   A `.srt` inside a release folder is a filename a stranger chose, and it is going into a path.

4. **Sidecars never fail an import.** A subtitle that cannot be linked is a `Warn` and the film is
   still imported. The feature is the point; the subtitle is a courtesy, and an import that failed
   because of a bad `.srt` would be the worst trade in the codebase.

5. **Serve them converted, from the same containment rule as the stream.**
   `GET /api/movies/{id}/subtitles/{name}` — the name matched against the sidecars actually found in
   that film's folder, never joined onto a path from the request.

6. **SubRip becomes WebVTT on the way out, not on the way in.** `<track>` takes WebVTT and nothing
   else. Measured: `mime.TypeByExtension(".vtt")` is `""` even on this Mac with Apache's table
   loaded, and `.srt` is `application/x-subrip`, which no browser renders. So: `text/vtt` set
   explicitly, and the conversion is a `WEBVTT` header, `,` → `.` in the timestamps, and dropping
   the cue numbers.

   **On serve, so the file on disk stays the one the release shipped** — which is what VLC and
   Jellyfin want, and which is the whole reason for step 2's naming.

   `.ass`/`.ssa` are **not** converted. They are a styled format and turning them into WebVTT means
   throwing the styling away or reimplementing it. They are linked, so Jellyfin and VLC get them, and
   the player does not offer them as a track.

7. **The player lists what exists.** `POST .../playback` grows a `subtitles` array — label, language,
   URL — and the `<video>` gets a `<track>` per entry. Empty is the ordinary case and draws nothing.

## Do not

- **Fetch subtitles from the internet.** No OpenSubtitles, no scraping, no "download subtitles"
  button. It is a different feature with an API key, a rate limit and a matching problem of its own.
- **Burn them in.** That is a transcode ([D24](../decisions.md#d24--playback-remuxes-and-never-transcodes)).
- **Extract embedded subtitle tracks from the MKV.** Most `.mkv` releases carry them inside, and
  pulling them out means running ffmpeg at import time for every film — a new dependency on an
  optional binary, in the one path that must keep working when it is absent. The remux carries them
  through as streams; a `<track>` for them is not what this task is.
- **Let a sidecar rename collide with the feature.** `Title (Year).srt` and `Title (Year).mkv` are
  fine; two sidecars that normalise to the same name are not, and the second is skipped with a
  warning rather than overwriting the first.
- **Trust `{name}` from the URL.** Match it against the list found on disk; never `filepath.Join` it
  onto anything.

## Verify

Hermetic, over a `t.TempDir()`:

- a release folder with `Movie.mkv`, `Movie.en.srt` and `Subs/2_English.srt` imports the feature and
  both sidecars, named off the feature
- equal inode and link count 2 for a sidecar, exactly as phase 4 proved for the feature — it is the
  same `library.Link` and the test says so
- a sidecar that cannot be linked (unwritable destination) leaves the **film imported** and logs a
  warning
- two sidecars normalising to one name: the first wins, the second warns
- a sidecar path that would escape the library is refused
- `GET .../subtitles/{name}` for a name not in that folder → `404`; a name with `..` in it → `404`,
  and the test asserts nothing outside the folder was ever opened
- an `.srt` with `00:00:01,000 --> 00:00:02,000` and a cue number comes out as `WEBVTT`, `.`
  timestamps, no cue number, `Content-Type: text/vtt`
- an `.ass` is linked and is **not** in the `subtitles` array
- a UTF-8 BOM at the front of an `.srt` does not end up in the `WEBVTT` header line — the one
  encoding detail that actually breaks players

Then live:

- re-import, or hand-link, the Backrooms subtitles that have been outstanding since phase 4, and
  watch the track appear in the player and in Jellyfin

---

## What the real files actually said

**Built 2026-08-15.** Everything above holds. Four things came out of meeting the real release
rather than a fixture, and two of them changed the code.

### The fixture in "Verify" collides, and the collision rule is right

`Movie.mkv`, `Movie.en.srt` and `Subs/2_English.srt` cannot produce two sidecars: both name the same
language, the rule is "named off the feature", and both therefore want
`Title (Year).en.srt`. **The naming is what earns this task its keep** — `2_English.srt` is
associated with nothing until it is renamed, and that rename is the entire reason Jellyfin, Plex and
VLC pick it up — so the collision is the correct outcome and the fixture is the thing that is wrong.
The tests split it in two: `Movie.en.srt` + `Subs/2_French.srt` for "both land", and the file's own
fixture verbatim for "first wins, second warns".

### `hi` is both a flag and a language, and the real release ships both readings

YTS's Backrooms release contains `Subs/SDH.eng.HI.srt`. `hi` is the hearing-impaired marker there —
and it is also Hindi's ISO 639-1 code, which is how a Hindi subtitle is named everywhere. A table
cannot settle this; the token in front of it can. `hi` is a **flag when a language precedes it**
(skipping any flags in between, so `Movie.en.sdh.hi` works) and **the language otherwise**. It
normalises to `sdh`, which is the spelling Jellyfin acts on.

That is also why the flags are kept at all rather than dropped to a bare language: a film with
foreign dialogue ships `English.srt` *and* `English.forced.srt`, and a rule carrying only the
language would collide them and throw one away.

### The language table is a closed set, and that is what makes the destination safe

A sidecar's library name is the **feature's** stem, plus an ISO code out of curator's table, plus a
flag out of another, plus a known extension. **No part of the filename a release group chose ever
reaches the path.** `AssertInside` stays, because it is what keeps that a property rather than a
habit — but the honest statement of the defence is the closed table, and the test asserts the
property directly: a sidecar named `x..%2F..%2Fetc%2Fpasswd.en.srt` lands as `Title (Year).en.srt`
and nothing appears outside the library.

Its stated cost: `no`, `it` and `id` are also English words, so a sidecar whose name happens to
*end* in one gets a wrong suffix. That mislabels a track in a menu. It never loses a file and never
moves one, because the suffix is the only thing derived from it.

### Measured, against the release that has been sitting in downloads since phase 4

The real importer, run once against the real content path. All five landed, every inode equal to its
source and every link count 2:

```
Backrooms.2026.1080p.WEBRip.x264.AAC5.1-[YTS.GG - YTS.BZ].srt  →  Backrooms (2026).srt
Subs/English.srt                                               →  Backrooms (2026).en.srt
Subs/SDH.eng.HI.srt                                            →  Backrooms (2026).en.sdh.srt
Subs/Latin American.spa.srt                                    →  Backrooms (2026).es.srt
Subs/Saudi Arabia.ara.srt                                      →  Backrooms (2026).ar.srt
```

No two collide, which is the only property that decides whether all five survive.

**In a visible Chrome 151, against the embedded build**: five `<track>` elements, `text/vtt`, and
**Chrome's own WebVTT parser accepted 1,826 cues** out of the converted `.en.sdh.srt` — the whole
file, not a prefix. The caption rendered over the film at 30:06 of 1:50:28 on direct play. 119,695
bytes on disk became 111,680 served, which is the cue numbers going away. Not one line in
`/api/logs` from the subtitle path across the entire session.

**Subtitles survive the fallback to remux**, observed rather than designed: the tracks come off the
playback response and not out of the container, so the same five are attached when `<video src>`
re-points at `/remux`.

**VLC 3.0.23 on this Mac auto-detected all five from the renamed files** — `autodetected subtitle:
Backrooms (2026).srt with priority 4`, the four language-suffixed ones at priority 3, all loaded as
SPU streams. That is the "works in three players that are not curator" claim, measured in one of the
three.

### What could not be verified, and why

**Jellyfin has not seen these files and cannot.** Jellyfin's library is the Pi's
`/media/storage/media/movies`; curator's local library is this laptop's `~/curator-local/movies`, and
the two are different disks. Checked read-only: **the Pi's library holds zero sidecar subtitles**, so
there is nothing there to compare against either. Putting a file on the Pi is phase 10. VLC above is
the substitute measurement, and it is the same convention.
