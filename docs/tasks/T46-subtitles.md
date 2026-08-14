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
