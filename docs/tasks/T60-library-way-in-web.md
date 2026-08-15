# T60 — the card opens the film, and the film leads with watching

**Owns:** `web/app/library/page.tsx`, `web/app/movie/page.tsx`, `web/components/player.tsx`,
`web/app/globals.css`, the `ScanResult` type in `web/lib/api.ts`, and `handlePlayback` in
`internal/api/stream.go`
**Depends on:** [T57](T57-library-way-in.md) (the counts this screen reports), [T59](T59-already-have.md)
(the refusal this screen anticipates)

## Goal

The Library is a list of films you can press play on.

Today no card on that screen is clickable — the only thing you can do to a film from the Library is
delete it — and on the film's own page the two ways to watch are split: `Open in Jellyfin` sits in
the hero and `▶ Play` sits below it inside `<Player>`, so they never read as a pair.

## Do

1. **The card opens the film.** The grid child becomes a `.movie-cell` wrapper so Delete is a
   *sibling* of the anchor: a `<button>` nested inside an `<a>` is invalid HTML and double-fires the
   one destructive control on the screen. The href form is `movie-card.tsx`'s —
   `/movie/?id=<tmdb_id>` — because the id rides in a query string
   ([D21](../decisions.md#d21--the-movie-page-is-movieid-because-the-ui-is-a-static-export)).

   **A row with `tmdb_id: null` stays a `<div>`.** There is no page to open: `/movie/` is addressed
   by the TMDB id and its own parser takes digits or nothing, and D6/D9 keep that id NULL rather than
   guessing one. The existing `unmatched` badge already says why, and inventing a second addressing
   mode for it would be a worse answer than a card that does not link.

   The CSS is nearly free: `globals.css` already carries `a.movie:hover` rules that match nothing on
   this screen today, so hover works the moment the root is an anchor.

2. **The hero leads with watching.** When `library.state === 'imported'`, the hero renders
   **`▶ Watch here`** and **`Open in Jellyfin`** side by side, and **does not render "Find
   releases"** — that is the ask read literally: no downloading and no checking torrents for a film
   already downloaded. The `<video>` still mounts below the hero, because what it renders once it
   starts is a video and not a button.

3. **`<Player>` takes `autoStart`.** It owns its own `▶ Play` today, which is exactly why the two
   watch actions cannot sit together. The page holds `watching`, the hero button sets it, and the
   player starts itself. Everything below `start()` — `STALL_MS`, `PICTURE_MS`, the `videoWidth > 0`
   poll, the HEAD probe, the VLC card — is measured behaviour and is not touched.

4. **The releases section explains itself.** For a film on disk, the `<h2>` stays and an `<Empty>`
   says curator already has it, names the path, and says deleting it first is how you replace it. A
   section that silently vanished would be a mystery.

5. **`handlePlayback` stops handing out a URL the stream would 404.** It resolves `libraryFolder`
   only, so a folder with no film in it answers `200` with a `stream_url` and the failure arrives
   later, inside `<video>`, where it looks like a codec problem. Its own neighbouring comment argues
   the case — *"a playback response that handed out a URL the stream would 404 is worse than a 404
   from playback"* — and then chose not to touch the disk. It already reads that directory for
   `subtitleTracks`, so this costs one `ReadDir` of a hot directory. **Amend the comment.**

6. **The scan banner reports the new counts** and the lede says what a scan now does: it still never
   writes to the disk, and it removes the *rows* for folders that hold no film while leaving those
   folders exactly where they are.

## Do not

- **Wrap the card in `<Link>`.** See 1.
- **Add an `alreadyHave` prop to `<Releases>`.** The plan called for one, and it would be dead code:
  the movie page does not render the list at all for a film on disk, and `/search`'s release-name
  mode has no library information to pass. [T59](T59-already-have.md)'s 409 is the guard that
  actually holds, and it holds for the case a fetched-once page cannot see anyway.
- **Invent `/movie/?curator_id=`** for unmatched rows. A second addressing scheme, against D20/D21,
  for a population that T57 just shrank.
- **Branch on `movie.status === 'downloading'`.** `store.StatusDownloading` is declared and never
  written; `library.state` is the only truthful source and the Go side derives it.
- Touch the fallback chain's constants or its probes.

## Verify

`make check` — the UI's whole guarantee is `next build`'s type check, so the new fields go in
`web/lib/api.ts` where `tsc` catches a screen that forgot them. There is no test runner in `web/`
and this is not the task that invents one.

Hermetic, Go side: `POST /api/movies/{id}/playback` for a folder with no feature file is **404**, not
a 200 carrying a `stream_url`.

Then live, against the embedded build (not `next dev` — cross-origin, no CORS): the Library shows one
card, clicking it opens `/movie/?id=1083381`, the hero offers `▶ Watch here` and `Open in Jellyfin`
and no "Find releases", and Watch here plays the film **in a visible tab** — Chrome throttles media
preload in a hidden one and the chain falls through to remux, which looks exactly like a codec
refusal and is not one.
