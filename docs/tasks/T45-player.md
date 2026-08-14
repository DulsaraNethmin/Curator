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

3. **The fallback chain, in order:**

   ```
   direct play  →  (fails)  →  remux, if remux_url exists  →  (fails)  →  the VLC card
   ```

   Nothing here asks what is inside the file (`phase-8.md`, "No codec probe"). The browser is the
   authority and it answers by failing.

   **"Fails" is not "fires `error`", and this task is where that was measured.** It was written as
   the error event and it is not: two of the three ways a source fails are silent, including the
   common one, where Chrome reports `playing` at `readyState` 4 and decodes nothing. See
   "What a real browser actually did" below and `phase-8.md`'s traps.

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

### What the real 10.10.7 answered

Run read-only against `192.168.1.26:8096` on 2026-08-14, before the client was written. **The shape
sketched above does not exist**, which is the reason it was sketched rather than assumed.

- **`Path=` is silently ignored.** `GET /Items?Recursive=true&IncludeItemTypes=Movie&Fields=Path`
  works with only `X-Emby-Token` and no `userId`, and answers all 18 movies. Adding
  `Path=/movies/Gone Girl (2014)/Gone Girl (2014) WEBDL-1080p.mkv` answers **all 18**; so does
  `Path=/nowhere/at/all.mkv`. It is not a filter that fails, it is a parameter the server drops.
  Controls that prove the endpoint filters perfectly well: `years=2014` → 1, `searchTerm=Pulp` → 1.
- **`AnyProviderIdEquals=tmdb.210577` is silently ignored too**, in both casings, with and without
  `userId`. So is `hasTmdbId=true`. There is no server-side identity filter to be had here.
- **The path Jellyfin knows is not the path curator stored, twice over.** Jellyfin's movies library
  location is `/movies` — its own bind mount — while curator stores
  `/media/storage/media/movies/Title (Year)`. And Jellyfin's `Path` is the **file**
  (`…/Gone Girl (2014)/Gone Girl (2014) WEBDL-1080p.mkv`) where `library_path` is the **folder**.
  A string match is wrong on both axes, and it stays wrong across any container boundary, which is
  the ordinary deployment rather than the exotic one.
- **Every item carries `ProviderIds.Tmdb`**, and the movie page already has the TMDB id in its
  address bar. That is an exact key, and it is immune to all three problems above *and* to the
  ` - ` → `:` title trap — Jellyfin says `Spider-Man: No Way Home`, the folder on disk says
  `Spider-Man - No Way Home`.
- **`years=` is a real filter and it is safe to narrow with.** All 18 of Jellyfin's `ProductionYear`
  values equal TMDB's own `release_date` year, checked film by film against the live TMDB API.
  Both sides get the year from TMDB, so they agree by construction rather than by luck.
- **Cost, measured.** The whole library with `Fields=Path,ProviderIds` is 21,386 bytes in 74 ms for
  18 films — linear, so a thousand-film library is about 1.2 MB on a page load. Narrowed to
  `years=<year>&Fields=ProviderIds&EnableImages=false` it is **542 bytes in 5.5 ms**, and it stays
  that size however big the library gets.
- **A TMDB id is not unique.** `Iron Man` is in this library twice under Tmdb `1726` — once at
  `/movies/Iron Man (2008)/` and once at `/media/downloads/complete/`. Either lands on that film,
  so the first match wins and nothing here tries to be cleverer about it.
- **The link is `#/details?id=<itemId>&serverId=<serverId>`**, which is not guesswork: it is the
  string `main.jellyfin.bundle.js` builds for an item of type `Movie`. Items carry their own
  `ServerId` (`004aee638b7c45e38a6e510d3d485829`), so it comes back with the answer.
- **Query without `userId`.** With one the total drops 18 → 16: a user sees a subset, and the link
  is being built for whoever is holding the laptop, not for the API key's owner.

So the answer to "which items query answers the item id for this path" is **none, and the path is
the wrong key anyway**. The query this task builds is the TMDB id, narrowed by year
([D32](../decisions.md#d32--the-jellyfin-link-is-keyed-on-the-tmdb-id-not-on-the-path)).

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

## What a real browser actually did

Driven in a **visible, focused** Chrome 151 against the embedded build. The visibility matters and
is the first finding: an automated tab is `hidden` by default, Chrome throttles media preload in a
hidden tab, and the "20 s of nothing from an `.mkv`" recorded before this task was that throttle
rather than a codec refusal. In a visible tab the same file plays.

**Chrome plays every container curator serves.** `.mkv` with H.264+AAC direct-plays (1924×1040, 384
frames); so does `.avi` with H.264+MP3. So the *container* fallback this phase was designed around
is dormant on Chrome — the remux earns its place for other browsers (Safari has no Matroska at all)
and the codec cases below, not for MKV-on-Chrome.

**`canPlayType` is wrong in both directions**, which is [the no-codec-probe rule](../phase-8.md)
paying for itself twice: `video/x-matroska` → `"maybe"`, and `video/x-msvideo` → `""`, a flat no,
for the AVI Chrome then played.

**The three ways a source fails, two of them silent**, are written up in `phase-8.md`'s traps. What
they cost here was three live bugs in a player that had already passed every unit test:

1. The first version treated `loadedmetadata` as proof of life. An MKV whose video is MPEG-4 Part 2
   reports metadata, `canplay`, `playing` and `readyState` 4 while decoding **nothing**, so the film
   sat there as a black rectangle with sound and the chain never ran.
2. The fix — "metadata without dimensions is a refusal" — then fired on a **good** file, because
   `loadedmetadata` arrives with `videoWidth` still 0 and fills it in a moment later. That remuxed
   films that play. Both edges are wrong; only a picture, polled, with a grace, is right.
3. Probing the URL the element was still streaming took **20 s** instead of 4 ms, queued behind that
   stream on HTTP/1.1. The player now aborts the failed source before probing.

**The chain, measured end to end** on an MKV holding MPEG-4 Part 2 (Xvid), which Chrome refuses:

```
   6ms  POST /playback → 200
  10ms  direct play
3288ms  HEAD /stream → 200      the bytes are fine, so it is the codec
3290ms  remux
6549ms  HEAD /remux → 200       a remux cannot fix a codec
6551ms  VLC CARD
```

With no ffmpeg, `remux_url` is absent and the same file reaches the VLC card at 3276 ms with no
remux attempted. A film Chrome *can* play stays on direct play indefinitely — held past the stall
cap, zero probes, zero remux attempts.

**Range requests, read off the wire** through a logging proxy rather than inferred:

| | `Range` | answer |
|---|---|---|
| initial | `bytes=0-` | `206 bytes 0-241659311/241659312` |
| index probe | `bytes=241631232-241659311` | `206`, 28,080 bytes — the MKV tail |
| after seeking to 540 s | `bytes=229933056-241631231` | `206`, 11,698,176 bytes |

Streaming and not downloading: the seek pulled 11 MB of a 230 MB file.

**A browser has now played a remux** — the outstanding item this task inherited. Chrome decoded
`/api/movies/1/remux?start=120` live: 1924×1040, 285 frames, `duration` growing as fragments
arrived, which is what an fMP4 of unknown length looks like. That growing duration is also why this
task did **not** implement re-pointing `<video src>` on a seek: the native control bar has no seek
affordance to trigger it with, so the seam T44 left is still open and still unneeded.

**The ticket, through its whole life, in VLC 3.0.23:**

- minted only with a password, `expires_at` 12 h out, `stream_url` still relative and ticket-free
- its own path `200`; the **remux** path `401`; another film's path `401`
- VLC played it — `206 Partial Content`, `V_MPEG4/ISO/ASP` through `avcodec`, `A_MPEG/L3` through
  `mpg123`, decoding the very file Chrome had refused
- after `PUT /api/settings` changed the password **with no restart**: the same URL `401`, the old
  password `401`, the new one `200`, and VLC on the same URL `401 Unauthorized`, zero decoders

**A session dying under an open page** shows the login state and stops, rather than blaming the
codec: `HEAD /remux → 401` at 3800 ms, login form at 3818 ms, and no VLC card.

**Open in Jellyfin lands on the film**, against the real 10.10.7, read-only: the deep link opens
Backrooms (2026)'s own page, and the search fallback opens Jellyfin's search with the box pre-filled
and the film found.

### Not verified, and why

**Direct play failing and the remux then succeeding has not been seen in one run.** It needs a
browser that refuses a *container* while accepting the codecs inside it, and Chrome 151 refuses none
of curator's four. Safari is that browser — it has no Matroska — but driving it needs Accessibility
and Screen Recording permissions this machine has not granted, and `osascript` against Safari timed
out on a consent prompt. Both halves are proven separately: the chain advances on a real failure,
and Chrome decodes the remux endpoint's output. **The composition is the gap.** The cheapest way to
close it is a human pressing Play once in Safari.

### Fixtures these used, kept

- `~/curator-local/remux-check/` — H.264+AAC MKV, 230 MB. Direct-plays. Pre-existing.
- `~/curator-local/fallback-check/` — H.264+MP3 **AVI**, 144 MB. Also direct-plays, which is the
  finding.
- `~/curator-local/nocodec-check/` — MPEG-4 Part 2 + MP3 MKV, 255 MB. **Chrome refuses this one**,
  silently, and it is what drives the whole chain. Above `FindFeature`'s 50 MiB floor on purpose.
