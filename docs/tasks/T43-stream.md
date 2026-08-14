# T43 — the stream endpoint

**Owns:** `internal/api/stream.go` and its tests, `internal/library/contain.go`, the ticket in
`internal/api/auth.go`, the mount in `cmd/curator/main.go`
**Depends on:** nothing — the library has existed since phase 1

## Goal

`GET /api/movies/{id}/stream` hands a film in the library to `http.ServeContent`, so a browser can
play it and seek in it without downloading it. Everything else in phase 8 hangs off this: the remux
is the same endpoint through ffmpeg, the player is a `<video>` pointed at it, and the subtitle is a
sidecar served beside it.

## Do

1. **Move the containment check into `internal/library`, first, as its own hunk with no behaviour
   change.** `assertInsideLibrary` is an unexported method on `*Importer` (`importer.go:236`) and it
   is about to have a second caller. It becomes `library.AssertInside(root, path string) error`; the
   importer calls it and its tests do not change. The T34 discipline, for the T34 reason: one
   question with two answers is how the two drift.

2. **The route takes curator's `movies.id`, and the lookup is `store.GetMovie`.** Not the TMDB id —
   the movie screen's URL carries that one
   ([D21](../decisions.md#d21--the-movie-page-is-movieid-because-the-ui-is-a-static-export)) and the
   detail body already carries `library.movie_id` beside it for exactly the films this can serve.

3. **`library_path` is a folder; `library.FindFeature` turns it into a file.** The same picker the
   importer used to put the file there (`importer.go:132`), with its 50 MiB floor and its
   `sample`/`extras`/`subs` skip. Do not write a second one, and do not just take the first `.mkv`:
   a folder with a 6 MB `sample.mkv` in it is exactly the case both rules exist for.

   No cache. It is one `ReadDir` of a folder with two or three entries, bounded by
   `DefaultMaxDepth`, and a cached answer would go stale the moment a file is replaced.

4. **`AssertInside` before the open**, against `LIBRARY_MOVIES`. Be exact about what that buys: the
   id is `strconv.ParseInt`, so nothing traverses in from the request. What this catches is a *row*
   pointing outside the library — written when the variable pointed elsewhere, restored beside a
   different library, or edited by hand. `404` to the caller, `Warn` to the log with both paths,
   because it is the operator's problem and not the browser's.

5. **`http.ServeContent` with the `*os.File`.** Ranges, `206`, `416`, `HEAD` and conditional requests
   all come free and all come correct — it is the same call `internal/web/web.go:152` already makes.
   Pass `info.ModTime()` so `If-Modified-Since` works; pass the *file* name, not the folder's.

6. **Set `Content-Type` from a table in this package. Never `mime.TypeByExtension`, never sniffing.**

   Measured: Go's builtin MIME table (`mime/type.go:53-70`) has **no video extension in it at all**.
   On this Mac `.mkv` answers `video/x-matroska` only because Go read `/etc/apache2/mime.types`;
   phase 9's image is `FROM scratch`, where none of the four files Go looks in exists, every answer
   is `""`, and `ServeContent` sniffs instead. **Sniffing an MKV gives `video/webm`** — measured —
   because Matroska and WebM share the EBML magic. The bug would work perfectly on this laptop and
   mislabel every file in the image.

   | | |
   |---|---|
   | `.mkv` | `video/x-matroska` |
   | `.mp4` | `video/mp4` |
   | `.m4v` | `video/x-m4v` |
   | `.avi` | `video/x-msvideo` |

7. **The ticket, in `internal/api/auth.go`, as a third credential beside the cookie and the header**
   ([D31](../decisions.md#d31--the-stream-is-behind-the-same-password-and-a-ticket-is-how-a-player-carries-it)).

   `?ticket=<expiry>.<base64url HMAC>`, signed with the same per-credential key the cookie uses
   (`Auth.mix`), over the message `"ticket\n" + path + "\n" + expiry`. The `ticket\n` prefix is what
   domain-separates it from a session, whose message is bare digits, so neither can ever be replayed
   as the other. 12 hours: longer than a film, far shorter than a cookie's 30 days.

   It goes in `Auth.check`, so there stays **one** place that decides whether a request may proceed,
   and it is checked *after* the cookie and the header — a browser that has both should use the one
   that is not in a URL.

8. **`POST /api/movies/{id}/playback` mints it, and only when asked.** It is the UI's one question —
   "how do I play this film" — and one round trip:

   ```json
   { "stream_url":   "/api/movies/7/stream",
     "external_url": "http://192.168.1.26:8090/api/movies/7/stream?ticket=1755…",
     "expires_at":   "2026-08-15T04:39:00Z" }
   ```

   `stream_url` is relative and has no ticket in it: that is what the page's own `<video>` uses, and
   same-origin means it carries the cookie by itself. `external_url` is absolute — VLC needs a host —
   built from the request's `Host`, because the browser asking is on the network that will play it.

   **With authentication off, `external_url` is the same URL without a ticket and `expires_at` is
   absent.** Nothing is minted, and the UI still has one code path.

   `remux_url` is T44's field and is absent until it exists.

9. **Mount it, and keep the route set honest.** `RegisterStream`, beside `RegisterMovieDelete` rather
   than inside `Register`, for the same reason: phase 1's route set keeps its shape and a deployment
   could omit playback entirely.

## Do not

- **Exempt the stream from the password.** An install that protects the list of titles and serves the
  films to anyone on the network is not a posture. D31.
- **Put the ticket in `stream_url`.** The browser has a cookie. A credential in a URL that did not
  need one ends up in the DOM, in error messages, and in whatever the user pastes when reporting a
  bug.
- **Probe the file.** No ffprobe, no codec sniffing, no `playable: true` in any response. The
  browser's `error` event decides, and it decides for free (`phase-8.md`, "No codec probe").
- **Read a partial download.** Only `imported` rows have a `library_path`. Serving a file that is
  still being written gives a header that does not describe the bytes behind it.
- **Write anything.** This endpoint opens files read-only and creates nothing, including no
  thumbnail, no index and no cache file inside the library.
- **Reimplement ranges.** `ServeContent` is correct, including the parts — multipart ranges, `416`
  with `Content-Range: bytes */size` — that a hand-rolled version gets wrong and no test here would
  have caught.
- **Log the ticket.** It is a credential. The path is fine; the query string is not.

## Verify

Hermetic, over a `t.TempDir()` library and a fake store:

- `Range: bytes=100-199` → `206`, exactly those 100 bytes, the right `Content-Range`; a range past
  the end → `416`; `HEAD` → the length and no body
- `Content-Type` for all four extensions, from the table — and `.mkv` asserted to be
  **`video/x-matroska` and not `video/webm`**, which is the mislabel this exists to prevent
- a folder whose only video is 6 MB → `404`, not a stream of the sample
- a folder with two qualifying videos streams the larger, and logs that it passed one over
- `library_path` NULL → `404`; folder missing → `404`; no video in it → `404`; id not a number →
  `400`. Four answers, no `500`
- a `library_path` outside `LIBRARY_MOVIES` → `404`, and the warning is asserted **in the log
  buffer**, because the log is where that failure is actually diagnosed
- `library.AssertInside` keeps every case `assertInsideLibrary` had — the importer's existing tests
  are the proof, unchanged
- **auth on**: no credential → `401`; cookie → `200`; `Authorization: Basic` → `200`; a ticket for
  this path → `200`; **a ticket minted for another film's path → `401`**; an expired ticket → `401`;
  a ticket minted before a password change → `401` after it
- **auth off**: `POST .../playback` returns no `expires_at`, and the URL it returns works
- a session cookie's value used as a `ticket` → `401`, and a ticket used as a cookie → `401`. The
  domain separation, asserted rather than argued

Then live, against the embedded build on 8097 (**not `next dev`** — cross-origin, no CORS):

- `curl -r 0-99 -s -o /dev/null -w '%{http_code} %{size_download}\n'` against a real film → `206 100`
- `curl -sI` shows `Accept-Ranges: bytes`, the real `Content-Length`, and the right `Content-Type`
  for an `.mkv`
- with authentication on: the external URL plays in **VLC**; change the password and the same URL
  stops working

## Notes for whoever writes this

`internal/api` declares its dependencies as interfaces it owns (`Store`, `Scanner`, `Browser`) and
attaches the optional ones with `With…` — follow that. The minting side is one method, so the
interface is one method:

```go
// Tickets mints a bearer credential for one URL path. Nil when authentication
// is off, which is the default and is why it is optional.
type Tickets interface {
    Ticket(path string, ttl time.Duration) (value string, expires time.Time, ok bool)
}
```

`*Auth` satisfies it, `cmd/curator` passes it, and `ok` is false when there is no password — which is
how `POST .../playback` knows to answer with a plain URL rather than a meaningless token.
