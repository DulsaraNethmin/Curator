# T44 — remux

**Owns:** `internal/remux/`, `GET /api/movies/{id}/remux`, the ffmpeg build and its size
**Depends on:** T43

## Goal

An `.mkv` the browser refuses plays anyway, by arriving as a fragmented MP4 with its streams copied
and nothing re-encoded. This is the fallback the `error` event triggers, and it is the difference
between a library where half the films play and one where nearly all of them do.

**Remux, and never transcode**
([D24](../decisions.md#d24--playback-remuxes-and-never-transcodes)). `-c copy`, always.

## Do

1. **Find ffmpeg, and treat missing as normal.** `ffmpeg_path` · `FFMPEG_PATH`, empty meaning look on
   `PATH`. Not found is **not** a start-up failure: remux is off, `POST .../playback` omits
   `remux_url`, and the UI says direct play only — the same posture as an unset Jellyfin key
   ([D15](../decisions.md#d15--the-jellyfin-refresh-is-best-effort-and-its-key-is-optional)).
   Probe once at start-up and log which binary was found, because "why is there no fallback" is
   otherwise a question with no evidence.

2. **The invocation, and every flag in it is load-bearing:**

   ```
   ffmpeg -nostdin -loglevel warning [-ss <start>] -i <file> \
          -c copy -f mp4 -movflags frag_keyframe+empty_moov+default_base_moof pipe:1
   ```

   `-nostdin` or ffmpeg reads the server's stdin. `frag_keyframe+empty_moov` is what makes the output
   playable *before it is finished* — an ordinary MP4 puts its `moov` atom at the end, which a stream
   never reaches. `-c copy` is the whole decision: no `-c:v`, no `-crf`, no `-preset`, no filter.

3. **The response is not `ServeContent` and must not pretend to be.** No length, `200` not `206`, and
   **`Accept-Ranges: none` set explicitly** so a player stops asking for ranges nothing can answer.
   `Content-Type: video/mp4`.

4. **Seeking is `?start=<seconds>`, not a `Range` header.** You cannot answer `bytes=500000000-` from
   a pipe without producing the first 500 MB. `-ss` before `-i` seeks by keyframe and is effectively
   instant on a copy. The player re-points `<video src>` when the user seeks outside the buffer, and
   that seam is T45's half of this task.

   `?start=`, not `?t=` — `?ticket=` already owns `t` (`phase-8.md`, the traps).

5. **The process dies with the request.** `exec.CommandContext` with the request's context, and
   `Cancel` set to kill the **process group** (`Setpgid`, then `syscall.Kill(-pid, SIGKILL)`) —
   ffmpeg spawns nothing today, but a process that outlives its reader is a file handle and a disk
   read that nobody will ever collect. A closed tab is the ordinary case here, not the exceptional
   one.

6. **Cap the concurrent remuxes** — a buffered channel, default 3, refuse with `503` and a
   `Retry-After` rather than queueing. The household is three people. Queueing would turn "the film
   is slow to start" into "the film never starts", which is worse and harder to explain.

7. **stderr is captured to a bounded tail and logged only on a non-zero exit.** ffmpeg writes several
   lines a second at default verbosity; into the ring buffer `/api/logs` serves
   ([D18](../decisions.md#d18--the-log-tail-is-readable-without-authentication-so-it-is-redacted-at-the-source))
   that is every real log line pushed out by frame counters. `-loglevel warning` cuts most of it;
   the tail is for the one time it matters, which is when it failed.

8. **A failure mid-stream cannot become an error page.** The headers went out with the first byte.
   Log it, end the response, and let the browser's `error` event do what it already does — which is
   exactly the fallback chain's next step.

9. **Build ffmpeg minimal, and record the number.** `--disable-everything` then enable: the
   `matroska`, `mov`/`mp4` and `avi` demuxers, the `mp4` muxer, the `pipe` and `file` protocols, the
   bitstream filters an MKV→MP4 copy needs (`h264_mp4toannexb`'s inverse — `aac_adtstoasc` and
   friends; let the build error tell you which). **No encoders. No filters. No network protocols.**

   **Budget 20 MB; reconsider past 40 MB.** Past that, the fallback in the plan stands: ship direct
   play alone, which already covers everything YTS produces. Write the measured size into
   `docs/progress.md` either way — it is the number phase 9's image budget is built on.

## Do not

- **Transcode.** Not "just for HEVC", not "only when the browser cannot decode it", not behind a
  setting. D24 is the record and the reason is arithmetic: a Pi 5 does not do 1080p in software in
  real time, and re-encoding throws away quality that was chosen deliberately half an hour earlier.
- **Probe the file to decide whether to remux.** The browser already failed; that *is* the decision
  (`phase-8.md`, "No codec probe").
- **Write the remux to disk.** A cache of transcoded films is a disk-usage feature with an eviction
  policy, and it is not this. The remux is free enough to redo.
- **Queue when the cap is hit.** `503` and say so.
- **Let ffmpeg pick the container by extension.** `-f mp4` explicitly; `pipe:1` has no extension to
  guess from and the failure is confusing.
- **Return a `remux_url` that answers `503` because ffmpeg is absent.** Omit the field. A URL that
  exists and never works is worse than one that does not exist.

## Verify

Hermetic, with a **fake ffmpeg** — a shell script on `PATH` written by the test that emits known
bytes, then blocks:

- the handler streams what the fake wrote, with `Accept-Ranges: none` and no `Content-Length`
- **cancelling the request kills it**: the test cancels, then observes the process is gone
  (`syscall.Kill(pid, 0)`), which is the invariant, not the log line
- a non-zero exit ends the response and the captured stderr reaches the log **once**
- the N+1th concurrent request is `503` with `Retry-After`, and a slot freeing lets the next through
- `?start=90` reaches the fake as `-ss 90`, and `?start=` with rubbish in it is `400` rather than a
  flag pasted into an argv
- **no ffmpeg on `PATH` and no `ffmpeg_path`**: `POST .../playback` has no `remux_url`, the route
  answers `404`, and start-up logged it once
- the argv is asserted as a whole, so a `-c:v libx264` can never be added without a test failing.
  That is the D24 guard in code rather than in prose

Then live, with a real ffmpeg and a real `.mkv`:

- an MKV that Chrome or Safari refuses direct plays through `/remux`
- `?start=` mid-film starts there, and the player's seek does not restart from zero
- close the tab and confirm with `ps` that no ffmpeg is left behind — the check that most obviously
  is not hermetic-able against the real binary
- `ls -l` the built ffmpeg and record the size

## The risk, stated up front

**This task can fail and the phase still ships.** If the minimal build lands past 40 MB, or the
bitstream filter set for MKV→MP4 turns out to need most of libavcodec, the answer is direct play plus
the VLC link and phase 8 is still worth having: T43 and T45 stand alone, and the `error` event's
fallback chain simply has one fewer step. Do not solve a 60 MB ffmpeg by transcoding fewer formats or
by shipping the general-purpose static — the first is D24 and the second is four times the binary
curator ships today.
