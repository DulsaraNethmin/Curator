# T45 — the player, and the Jellyfin link

**Owns:** `web/app/movie/page.tsx`, `web/lib/api.ts`, `internal/jellyfin/client.go`,
`internal/api/browse.go`'s detail body
**Depends on:** T43, T44

## Goal

Press Play on a film's page and watch it. When the browser cannot, fall back without a spinner and
without a dead end. And when you would rather watch it in Jellyfin, one link opens **that film**
rather than Jellyfin's home screen.

The two are complementary and the plan said so from the start. Jellyfin has the apps, the TV client
and the watched state; curator has the film you just downloaded and the page you are already on.

## Do

1. **Play appears only for `library.state === "imported"`.** The detail body already carries it, and
   `library.movie_id` beside it — which is the id the stream URL needs, and **not** the TMDB id in
   the address bar ([D21](../decisions.md#d21--the-movie-page-is-movieid-because-the-ui-is-a-static-export)).

2. **One call, then one `<video>`.** `POST /api/movies/{id}/playback` returns the URLs; the element
   gets `stream_url`, which is relative and same-origin and carries the session cookie by itself.

3. **The fallback chain, in order, driven by the `error` event:**

   ```
   direct play  →  (error)  →  remux, if remux_url exists  →  (error)  →  the VLC card
   ```

   Nothing here asks what is inside the file (`phase-8.md`, "No codec probe"). The browser is the
   authority and it answers by failing.

4. **Tell a codec failure from a transport failure before concluding anything.** `MediaError.code`
   is `4` for an unplayable container *and* for a response that could not be loaded at all — an
   expired session, a `404`, a process that has gone away. So on `error`, issue one `HEAD` against
   the URL:

   | `HEAD` says | It means | Do |
   |---|---|---|
   | `200` | the bytes are there and the browser will not decode them | next step in the chain |
   | `401` | the session went away while the page stayed open | show the login state, do not remux |
   | `404`, `5xx` | the film or the process is the problem | say which, do not remux |

   Without this, an expired cookie silently remuxes, fails again, and offers VLC for a film that
   would have played fine after logging back in.

5. **The VLC card is a real fallback, not an apology.** `external_url`, a copy button, and one
   sentence: this URL contains a key that works for this film for 12 hours and stops working if the
   password changes. When authentication is off there is no key and the sentence says that instead.

6. **Open in Jellyfin, and the id lookup behind it.** `internal/jellyfin` gains **one read-only
   query** and keeps its narrowness rule — its doc comment makes that a design, not an accident, and
   the household is watching that Jellyfin throughout this phase.

   **Establish the query against the real 10.10.7 before writing the client**, read-only, and write
   the answer into the task file. The question is "which items query answers *the item id for this
   path*" — Jellyfin's `/Items` with `Recursive=true` and `Fields=Path`, filtered on the path curator
   stored, is the shape to start from. It is not assumed here because an API shape nobody has run is
   not a fact.

   **The fallback if no query answers it cleanly:** link to a Jellyfin search for the title. It
   always lands somewhere useful, and it is better than a link that 404s.

7. **`jellyfin_public_url` is what the link uses.** `jellyfin_url` is what *curator* uses — inside
   Docker that is `http://jellyfin:8096`, a name that resolves on the container network and nowhere
   a browser can follow. Empty falls back to `jellyfin_url`, which is right on a laptop and wrong in
   the image, which is exactly why the setting exists.

8. **No Jellyfin configured means no link.** Not a disabled button, not a tooltip explaining what
   you are missing — the same rule the rest of the UI follows for an unconfigured integration.

## Do not

- **Build a custom control bar.** `<video controls>` gives play, seek, volume, fullscreen, picture-in-picture
  and captions, keyboard-accessible, in every browser, for free. A custom skin is a week of work and
  a permanent accessibility bug.
- **Autoplay.** A film starting on its own when a page loads is a jump scare with sound.
- **Poll `POST .../playback` on a timer to refresh the ticket.** The browser's own playback does not
  use a ticket at all; the external URL is a thing someone copied, and re-minting behind them does
  not reach the player they pasted it into.
- **Show playback position, resume, or watched state.** Out of scope for the phase, and the reason is
  in `phase-8.md`: they are per-user library state, not playback, and they need a users table this
  product has decided not to have.
- **Widen `internal/jellyfin` beyond the one lookup.** No playback control, no sessions, no user
  queries, no refresh-this-item.
- **Fetch `external_url` from the page.** It is for the clipboard. Fetching it would put a bearer
  credential into the browser's network log for no gain.

## Verify

Hermetic, in the Go tests where the seam is Go:

- the detail body carries what the page needs for a film on disk, and carries nothing new for one
  that is not
- the Jellyfin lookup against an `httptest.Server`: an item found, an item not found (fall back to
  search), a `401` from a revoked key (`ErrUnauthorized`, already distinguished), and an unreachable
  server — none of which may fail the page
- `jellyfin_public_url` empty falls back to `jellyfin_url`, and a set one wins

The UI's own checks are live, because a `<video>` element is not something a unit test observes:

- press Play on an imported film in the **embedded build** (`npm --prefix web run build`, then
  `go build` — `next dev` is cross-origin and there is no CORS header anywhere in the Go code)
- **seek, and watch the Network tab issue range requests.** That is the difference between streaming
  and downloading, and it is invisible from the UI
- an `.mkv` the browser refuses: the chain runs to the remux and plays; with ffmpeg absent, it runs
  to the VLC card instead
- with authentication on, let a session expire, then press Play: the login state appears, and **no**
  remux is attempted
- copy the external URL into VLC and play it; change the password and confirm it stops
- Open in Jellyfin lands on the film's own page in the real 10.10.7 at `192.168.1.26:8096`.
  **Read-only. Nothing on the Pi changes** — that is phase 10, after T52.
