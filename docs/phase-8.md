# Phase 8 — Watch it here

The phase where the library stops being a list of files and becomes something you can press play on.

**Done when** Play works on a film's page for the files curator actually has; a container the browser
refuses falls back to a remux rather than a spinner; a player that is not a browser can be handed a
URL; and Open in Jellyfin lands on *that film* rather than on Jellyfin's home screen.

---

## What the rest of the build put on this desk

**Phase 8 depends on nothing in 6 or 7 and has been unblocked since day one** — it needs the library,
which has existed since phase 1. What the intervening phases changed is not what it builds but what
it must be careful about, and there are exactly two things.

**T41 put a password in front of `/api/*`.** A `<video src>` is a same-origin subresource and carries
the session cookie without being asked; VLC, mpv, an Apple TV and a phone on the sofa do not.
"Show the stream URL for VLC" is in this phase's own fallback path, so the question *who may read a
film* has to be answered before the handler is written, not after
([D31](decisions.md#d31--the-stream-is-behind-the-same-password-and-a-ticket-is-how-a-player-carries-it)).

**Phase 9 ships `FROM scratch`.** That is not this phase's commit, but it decides one line of it —
see the Content-Type trap below, which is a bug that would work perfectly on a laptop and mislabel
every file in the image.

Everything else is a gift. `http.ServeContent` is already in this repo doing exactly this job
(`internal/web/web.go:152`), `library.FindFeature` already knows which file in a folder is the film,
and the movie screen already holds the id the stream URL needs.

---

## Tasks

| Task | Owns | Depends on | State |
|---|---|---|---|
| [T43](tasks/T43-stream.md) the stream endpoint | `internal/api/stream.go`, `internal/library/contain.go` | — | **built** |
| [T44](tasks/T44-remux.md) remux | `internal/remux/`, the ffmpeg build | T43 | **built** — 2.2 MB |
| [T45](tasks/T45-player.md) the player and the Jellyfin link | `web/app/movie/`, `internal/jellyfin` | T43, T44 | **built** |
| [T46](tasks/T46-subtitles.md) the subtitle sidecar | `internal/importer`, `internal/api/stream.go`, `internal/library/subtitles.go` | T43 | **built** |

T46 depends on T43 rather than on nothing, as the plan had it: linking a `.srt` into the library is
half of it, and the half that matters is serving it to a `<track>` element, which is the stream
endpoint's business and needs its containment rule.

---

## The shape

```
internal/library/contain.go   AssertInside — moved out of the importer, now that two callers need it   T43
internal/api/stream.go        GET .../stream, GET .../remux, POST .../playback                          T43
internal/api/auth.go          the ticket: a third credential beside the cookie and the header           T43
internal/remux/               find ffmpeg, run it, kill it when the request goes away                   T44
web/app/movie/page.tsx        the player, the fallback chain, the two links                             T45
internal/jellyfin/client.go   one read-only query, and the narrowness rule amended on purpose           T45
internal/importer/importer.go the sidecars beside the feature                                           T46
```

**`internal/api/stream.go`, not a new package.** There is no logic here to isolate: the endpoint is a
lookup, a containment check, a file open and `http.ServeContent`. `internal/remux` *is* its own
package, because a subprocess with a lifetime is a thing with invariants — and because the ffmpeg
question has its own fallback and might not ship at all.

**`AssertInside` moves to `internal/library` before anything uses it, in the T34 discipline: no
behaviour change, its own hunk, the importer's tests unchanged.** It is `assertInsideLibrary` today,
an unexported method on `*Importer` reading `im.moviesRoot` (`importer.go:236`). Copying it into the
API is how two answers to one question start drifting, and `internal/library` is where the root's
meaning already lives — it is the package that turns a title into a folder name and refuses one that
would escape.

---

## Who may read a film

**The stream is behind the same password as everything else under `/api/`, with no exemption**, and a
**ticket** is how a player that cannot carry a cookie carries the password instead
([D31](decisions.md#d31--the-stream-is-behind-the-same-password-and-a-ticket-is-how-a-player-carries-it)).

Exempting the route was the alternative that is cheapest to write and impossible to defend: with
authentication on, `GET /api/movies` is protected, so an install that hides the *titles* of its films
while serving the *films* to anyone on the network is not a posture, it is an oversight.

```
POST /api/movies/{id}/playback     → the URLs this film can be played with
GET  /api/movies/{id}/stream       → the file, cookie or Basic or ?ticket=
GET  /api/movies/{id}/remux        → the same file through ffmpeg, same three credentials
```

A ticket is the session cookie's own machinery pointed at a URL: `<expiry>.<HMAC>`, signed with the
key that already signs cookies — the process's session key mixed with the credential itself. Three
properties fall out of that rather than being built:

- **it is for one path**, because the path is in the signed message, so a ticket for one film is not
  a ticket for the library;
- **it expires**, in 12 hours — comfortably longer than a film and much shorter than a cookie's 30
  days;
- **changing the password kills every outstanding one**, with nothing to evict, exactly as it already
  kills every session.

**A ticket is a bearer credential in a URL, and that is its whole cost.** It lands in shell history,
in VLC's recent-files list and in any proxy's access log. So it is minted only when asked for, it is
never what the browser's own `<video>` uses — that is same-origin and has a cookie — and the response
separates the two: `stream_url` for the page, `external_url` for the thing you paste.

**With authentication off, which is the default, none of this exists.** `POST .../playback` answers
with the plain URLs and no ticket, so the UI has one code path and no branch on auth state.

Putting the password in the URL instead — `http://x:hunter2@curator:8090/api/movies/7/stream`, which
VLC accepts today with no code at all — is the alternative that lost. It works, it will keep working
because it is a property of Basic auth and not something curator can block, and anyone who prefers it
is not being stopped. It is not what the *button* hands out: that URL is the whole install rather
than one film, it never expires, and it goes in the same recent-files list.

---

## No codec probe

**The browser is the codec authority.** Attempt direct play; on failure, attempt the remux; on
failure, show the URL for VLC. curator never asks a file what is inside it in order to decide what
to do with it.

> **Amended by T45, which drove this against a real Chrome 151.** This section originally said the
> **`error` event** was the authority and that it "is never wrong". The rule below survives intact —
> it came out of T45 stronger than it went in — but that *mechanism* is wrong and the wrong version
> is dangerous, because it is the one a player gets built on. What the browser actually does is in
> the traps section under "An `error` event is not the only way a source fails, and two of the three
> ways are silent". In short: an unplayable file often fires **no error at all**, and
> `canPlayType` was measured lying in **both** directions. The authority is whether a **picture**
> appears, and that is what [T45](tasks/T45-player.md) waits for.

This is a rule and not an optimisation, because the probe is the obvious next helpful thing and it is
worth naming what it costs. It would mean running ffprobe on every play (or caching an answer that
goes stale when a file is replaced); it would mean maintaining a codec-support matrix per browser and
per version, which is a table that is wrong the week it is written; and it would let curator's
*opinion* about playability disagree with the browser sitting in front of the user, which is the one
disagreement there is no way to resolve. The `error` event answers the actual question — can *this*
browser play *this* file — for free, and no table curator could keep can answer it.

**T45 measured the probe being wrong in both directions, which is the argument made twice over.**
`canPlayType('video/x-matroska')` answers `"maybe"`, and `canPlayType('video/x-msvideo')` answers
`""` — a flat no — for an AVI that **Chrome then played**. A probe that trusted the first would
remux a file that plays; a probe that trusted the second would refuse one that works. The browser
disagreeing with its own advertised capabilities is exactly the disagreement there is no way to
resolve, and it is why nothing here asks.

The traps it comes with are in the traps section: an `error` event does not say *why*, a 401 looks
exactly like an unsupported codec — and an unplayable file frequently fires no `error` at all.

---

## Remux, and never transcode

[D24](decisions.md#d24--playback-remuxes-and-never-transcodes). Remux rewrites the container and
copies the streams (`-c copy`); a transcode re-encodes the video. The first is a few percent of one
core and is lossless. The second is more CPU than a Pi 5 has for 1080p in real time, and it is
quality thrown away from a file that was downloaded at a chosen quality half an hour earlier.

**What remux fixes is the container, and that is most of what actually breaks.** YTS ships `.mp4`
that plays everywhere; the `.mkv` releases are where Play fails, and an H.264 + AAC stream inside an
MKV is playable by every browser the moment it is inside a fragmented MP4 instead.

**What remux cannot fix stays unplayable, and the answer is the VLC link rather than a bigger
ffmpeg.** HEVC that a browser will not decode, DTS or TrueHD audio, VC-1 — a container change moves
those bytes without making them decodable. That is the honest edge of this feature and the UI says so
in a sentence, because "Play" that silently does nothing is the failure this phase exists to remove.

**ffmpeg is an external binary, not a linked library.** `CGO_ENABLED=0` and
[D4](decisions.md#d4--pure-go-sqlite) are untouched. It is **optional**: absent ffmpeg means direct
play only, reported in the UI, and never a start-up failure — the same posture as an unset Jellyfin
key ([D15](decisions.md#d15--the-jellyfin-refresh-is-best-effort-and-its-key-is-optional)).

Build it minimal — matroska and mov/mp4 demuxers, the mp4 muxer, no encoders at all, no filters, no
network protocols. **Budget 20 MB; reconsider past 40 MB, and the fallback is shipping direct play
alone**, which already covers everything YTS produces.

---

## The remux endpoint is not the stream endpoint, and the difference is seeking

This is the one genuinely hard thing in the phase and it should be understood before T44 starts.

`GET .../stream` is `http.ServeContent` over an `*os.File`: a known size, byte ranges, `206`, `416`,
`HEAD`, conditional requests, all free and all correct.

`GET .../remux` is a subprocess writing into the response. It has **no length and no byte-range
semantics** — you cannot answer `Range: bytes=500000000-` from a pipe without having produced the
first 500 MB. So:

- the output is a **fragmented** MP4 (`-movflags frag_keyframe+empty_moov+default_base_moof`), so it
  plays before it is finished rather than needing a `moov` atom that only exists at the end;
- the response is `200` with no `Content-Length`, and `Accept-Ranges: none` is set explicitly so a
  player stops asking;
- **seeking is a parameter, not a header.** `?start=<seconds>` re-runs ffmpeg with `-ss`, and the
  player re-points `<video src>` when the user seeks outside what is buffered. That is a contract
  between T44 and T45 and it is the reason they are separate tasks with a stated seam.

Two invariants that a subprocess brings and a file does not: **it must die with the request** —
`exec.CommandContext`, and the process group killed, or a closed tab leaves ffmpeg reading a
755 MB file forever — and **there is a cap on how many run at once**, because each is a disk read
and a pipe, and the household is three people, not thirty.

---

## The settings this phase adds

Phase 7 promised the registry would make this one entry rather than a screen
([`phase-7.md`](phase-7.md), "Playback is not in this table"). It is two.

| Group | Key / variable | Kind | |
|---|---|---|---|
| Playback | `ffmpeg_path` · `FFMPEG_PATH` | path | empty = look on `PATH`; not found = remux off |
| Jellyfin | `jellyfin_public_url` · `JELLYFIN_PUBLIC_URL` | url | empty = use `jellyfin_url` |

**There is no "prefer direct play" toggle**, for the reason phase 7 gave for having no playback
settings at all: it would be a row nothing reads. Direct play is always tried first and the browser
decides; the only switch anybody needs is whether remux exists, and that is `ffmpeg_path`.

**`jellyfin_public_url` is not redundant, and the reason is the whole of T45's link.**
`jellyfin_url` is what *curator* uses to reach Jellyfin — inside Docker that is
`http://jellyfin:8096`, a name that resolves on the container network and nowhere else. The Open in
Jellyfin link is clicked by a *browser*, which needs `http://192.168.1.26:8096`. One URL cannot be
both, and a link to a hostname the browser cannot resolve is the kind of bug that gets called
"Jellyfin is down".

---

## What must not change

- **The library is read, never written.** This phase opens files read-only. `internal/importer`
  stays the only thing in curator that creates a file inside `LIBRARY_MOVIES` — T46 adds sidecars
  *through the importer*, not through the player.
- **No transcode, ever.** [D24](decisions.md#d24--playback-remuxes-and-never-transcodes).
- **No codec probe.** The rule above.
- **Authentication off by default still means everything works**
  ([D25](decisions.md#d25--authentication-is-optional-and-off-by-default)). A LAN install gets a
  Play button and never sees a ticket.
- **`internal/jellyfin` gains exactly one read-only query and still cannot write.** Its doc comment
  makes narrowness a design rather than an accident — "a method that does not exist cannot be called
  by mistake against a media server the household is watching". T45 amends it by one lookup and
  keeps the rule, because the household is watching that Jellyfin during the whole of phase 8.
- **The engine, the tunnel and the poller are not touched.** Nothing here is downstream of phase 6.
- **`internal/web` keeps serving the embedded UI and nothing else.** It reads an `embed.FS`; the
  stream reads a disk. Sharing a handler between the two would put a filesystem path into the
  package whose whole job is that there isn't one.

---

## The traps, named before anyone hits them

**Go does not know what a `.mkv` is, and on a laptop it looks like it does.** Measured: Go's builtin
MIME table (`mime/type.go:53-70`) contains **no video extension at all** — `.mkv`, `.mp4`, `.avi` and
`.m4v` are absent. On this Mac `mime.TypeByExtension(".mkv")` answers `video/x-matroska`, and it does
so by reading `/etc/apache2/mime.types`. **Phase 9's image is `FROM scratch`**: none of the four
files Go looks in exists, every answer becomes `""`, and `http.ServeContent` falls back to sniffing
the first 512 bytes. Sniffing an MKV gives **`video/webm`** — measured — because Matroska and WebM
share the EBML header. So curator would confidently tell every browser that a 12 GB MKV is a WebM,
in the image and not on the laptop it was written on.

The rule: **the stream endpoint sets Content-Type from a table in curator**, four extensions long,
and never calls `mime.TypeByExtension` and never lets sniffing decide. A test asserts `.mkv` answers
`video/x-matroska` and specifically **not** `video/webm`.

**The id in the stream URL is not the id in the address bar.** The movie screen is `/movie/?id=…`
where that id is **TMDB's** ([D21](decisions.md#d21--the-movie-page-is-movieid-because-the-ui-is-a-static-export));
`/api/movies/{id}/stream` takes **curator's** `movies.id`. The page already has both — the detail
body carries `library.movie_id` for exactly the films that are on disk — so this is one line to get
right and a 404-on-a-film-you-can-see to get wrong.

**`library_path` is a folder, not a file.** The importer stores the directory on purpose: it is the
scanner's identity key, and a row holding the `.mkv` path is a row no future scan would match
(`importer.go:153`). The endpoint turns it into a file with **`library.FindFeature`, the same picker
the importer used to put it there** — which brings the 50 MiB floor and the `sample`/`extras`/`subs`
skip with it, so a folder with a 6 MB `sample.mkv` beside the film cannot stream the sample. Two
different answers to "which file is the film" is a bug that would only appear on the folders where it
matters most.

**The containment check defends a row, not a URL — say so and do not oversell it.** The id is parsed
with `strconv.ParseInt`, so nothing traverses in from the request. What `AssertInside` catches is a
`library_path` that points outside `LIBRARY_MOVIES`: a row written when the variable pointed
somewhere else, a database restored beside a different library, a row edited by hand. That is a real
failure and it is worth a 404 and a warning line; it is not the path-traversal defence it looks like.

**An `error` event is not the only way a source fails, and two of the three ways are silent.**
Measured by T45 in a real, visible Chrome 151 — the tab matters, because Chrome throttles media in a
hidden one and an automated tab is hidden by default, which is what made an earlier reading of this
inconclusive.

1. **It can fire `error`.** The case this phase was designed around, and the least common of the
   three in practice.
2. **It can hang.** `loadstart`, then `stalled`, then nothing: no error, `networkState` 2,
   `readyState` 0, indefinitely.
3. **It can report success and play nothing.** Handed an MKV whose video is MPEG-4 Part 2 — or
   ProRes, or FFV1, all three measured — Chrome fires `loadedmetadata`, `canplay` and `playing`,
   reports `readyState` 4, and advances `currentTime` for ever, while `videoWidth` stays `0` and not
   one frame is decoded. It plays the audio track and silently drops the video. **Nothing in the
   event stream ever says so.** A player that waits for `error` waits for ever, and the user gets a
   black rectangle making noise.

So the test for a working source is a **picture** — `videoWidth > 0` — and not any event. The
converse trap is just as real and cost a live regression: `loadedmetadata` also fires with
`videoWidth` still `0` on a file that plays perfectly, filling it in a moment later. Deciding at
either edge is wrong in one direction or the other, so T45 polls for a picture and gives a source
that has announced metadata a **three-second** grace to produce one, under an absolute stall cap.

**Probing the same URL a `<video>` is still streaming can queue behind it.** curator speaks HTTP/1.1
with no TLS and no h2. Measured: the `HEAD` below took **20 seconds** instead of 4 ms, because it
was ordered behind the element's own open `GET` on the same connection, and the whole fallback chain
waited on it. The player aborts the failed source — `removeAttribute('src')` then `load()` — before
it probes, which is also the only thing that stops a rejected film downloading in the background
while the next one plays.

**An `error` event does not say why, and a 401 looks exactly like a bad codec.** `MediaError.code`
is `4` (`SRC_NOT_SUPPORTED`) for an unplayable container *and* for a response the element could not
load at all. A UI that treats every `4` as "codec" will answer an expired session by silently
remuxing, failing again, and offering VLC. So the player checks the URL is actually answering before
it concludes anything about codecs — one `HEAD`, whose status tells the difference the event cannot.

**`?t=` was going to mean two things.** The ticket and the remux seek offset both want the obvious
short name. They are `?ticket=` and `?start=`, and the collision is recorded here because the first
draft of both had `t`.

**ffmpeg talks constantly on stderr.** Frame counters, bitrate, time — several lines a second. Into
the log buffer at info level, that is `/api/logs` filled with progress output and every real line
pushed out of the tail ([D18](decisions.md#d18--the-log-tail-is-readable-without-authentication-so-it-is-redacted-at-the-source)
made that buffer a product surface). stderr is captured to a bounded tail and logged **only when the
process exits non-zero**, which is the one time anybody wants it.

**`<track>` wants WebVTT and the disk has SubRip.** Measured: `mime.TypeByExtension(".vtt")` is `""`
even here, and `.srt` is `application/x-subrip`, which no browser will render as a text track. The
conversion is small — a `WEBVTT` header and `,` → `.` in the timestamps — and it happens **on serve**,
not on import, so the file on disk stays the `.srt` that VLC and Jellyfin both already want.

> **Built by T46, and it holds.** Chrome 151's own WebVTT parser accepted all 1,826 cues of a real
> converted `.srt`, and VLC auto-detected all five renamed sidecars off the disk. Two things the
> conversion needed that this paragraph does not say. The cue numbers have to go, and a cue number is
> only recognisable as *digits alone immediately in front of a timing line* — a line of dialogue that
> is a bare number is not one, and deleting it loses a line silently. And the timing line is rewritten
> **strictly** rather than substituted: SubRip has no specification and files in the wild vary in the
> spacing around the arrow and the width of the milliseconds field, while WebVTT does have one and a
> browser drops a cue it cannot parse without reporting anything.

**A sidecar's library name is built from a closed table, and that is the containment argument.**
T46's destination is the *feature's* stem plus an ISO 639-1 code out of curator's own language table
plus a flag out of another plus a known extension — so no part of the filename a release group chose
reaches the path at all. `library.AssertInside` stays on that destination, but as the guard that
keeps the property true rather than as the thing the safety rests on. The same distinction the
containment check above already insists on: **be exact about what it buys.**

**A remux carries every audio track and the browser takes the first.** `-c copy` copies all streams;
`<video>` gives no way to choose. A film whose first track is a commentary or a foreign dub will play
with the wrong audio and nothing in the UI will explain it. Not solved in this phase — named, because
the fix is a track selector in the remux URL and it is not free.

---

## Verification

Per commit, as ever:

```bash
make check      # npm export, go build, go vet, go test -race, arm64 cross-compile
```

Hermetic, over a `t.TempDir()` library:

- **ranges**: `Range: bytes=100-199` → `206`, exactly 100 bytes, the right `Content-Range`; an
  unsatisfiable range → `416`; `HEAD` answers the length with no body, because players probe with it
- **Content-Type** comes from curator's table for all four extensions, and `.mkv` is
  `video/x-matroska` rather than the `video/webm` sniffing would produce
- **the feature picker**: a folder whose only video is 6 MB is a `404`, not a stream of the sample;
  a folder with two features streams the larger and logs the other
- **the misses**: a row with `library_path` NULL, a folder that has gone missing, a folder with no
  video, and an id that is not a number — four distinct answers, none of them a `500`
- **containment**: a row pointing outside `LIBRARY_MOVIES` is a `404` and a warning, asserted against
  the log rather than only the status
- **auth on**: no credential → `401`; cookie → `200`; `Authorization: Basic` → `200`; a ticket for
  *this* path → `200`; a ticket for **another film's** path → `401`; an expired ticket → `401`; a
  ticket minted before a password change → `401` after it
- **auth off**: `POST .../playback` mints nothing and returns the plain URLs
- **the remux subprocess**, against a fake ffmpeg on `PATH` that writes known bytes and then blocks:
  cancelling the request kills it and the test observes the process gone; a non-zero exit ends the
  response and logs the captured stderr; the N+1th concurrent request is refused rather than queued
- **srt → vtt**: `00:00:01,000 --> 00:00:02,000` becomes `WEBVTT` and `.` timestamps, served as
  `text/vtt`
- **no ffmpeg configured**: `POST .../playback` omits `remux_url` entirely rather than returning one
  that 503s

Then driven for real, on the laptop, against the embedded build — **not `next dev`**, see below:

- press Play on an imported film and watch it play; **seek, and confirm in the Network tab that the
  browser issued range requests** rather than pulling the whole file. That is the difference between
  streaming and downloading and it is invisible from the UI.
- an `.mkv` the browser refuses: confirm the fallback chain runs — `error`, then remux, then playing
  — and that it does not spin
- turn authentication on, copy the external URL, paste it into VLC and confirm it plays; **then
  change the password and confirm the same URL stops working**, which is the one property that
  distinguishes the ticket from a password in a URL
- Open in Jellyfin lands on the film's own page, against the real 10.10.7 at `192.168.1.26:8096`.
  **Read-only, and nothing on the Pi changes** — phase 10, after T52.
- the subtitles that have been sitting in the downloads folder since phase 4 appear as a track

**The documented dev workflow cannot verify any of this in a browser.** `next dev` on `:3000` against
the API on `:8090` is cross-origin, and there is no CORS header anywhere in the Go code. Verify
against `npm --prefix web run build` then `go build` — the binary embeds the export
([D16](decisions.md#d16--the-ui-is-embedded-with-all-and-a-committed-placeholder-keeps-go-build-honest)),
and a binary built without that step says so in the log and serves the API perfectly, which is easy
to miss if you are only curling. **This is not to be "fixed" with a permissive CORS block**: the API
now carries a session cookie, so that is a CSRF surface and it would need a decision record of its
own, not a header added in passing.

---

## Out of scope

- **Transcoding.** [D24](decisions.md#d24--playback-remuxes-and-never-transcodes). The fallback for a
  codec the browser cannot decode is the VLC link, which is one click and always works.
- **Subtitle burn-in.** It is a transcode wearing a different name.
- **Playback position, resume, and watched state.** "Play" invites all three and none of them is a
  playback feature — they are per-user library state, they need a users table this product has
  decided not to have ([D25](decisions.md#d25--authentication-is-optional-and-off-by-default)), and
  Jellyfin is already what the household uses for them. That is the argument for the Jellyfin link
  being in this phase rather than being made redundant by it.
- **Streaming a file that is still downloading.** Only `imported` rows have a `library_path`, and a
  partial file has no valid header to seek in.
- **Chromecast, AirPlay, and a remote-control API.** Different product.
- **Audio-track and subtitle-track selection in the player.** Named as a trap above; the fix is a
  parameter on the remux URL and a control in the player, and neither is what "Play works" means.
- **Anything on the Pi.** Phase 10, after T52 backs up the \*arr configs.
