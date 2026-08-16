# T70 — the failure banner stops titling the two statuses it cannot read

**Owns:** `title()` in `web/components/states.tsx`, and the `ApiError` comment in `web/lib/api.ts`
that documents what each status means
**Depends on:** nothing. It is the debt [T69](T69-correct-a-wrong-match.md) named in its own *Do not*
— *"fixing the title is a task of its own and is not smuggled in here"* — and
[D38](../decisions.md#d38--a-wrong-match-is-corrected-by-overwriting-it-never-by-clearing-it-first)
recorded as its one honest edge

## Goal

`<Failure>` never prints a title that is false. Where the status it was handed does not identify a
situation, it prints no title at all and lets the server's sentence carry the banner.

## The bug is live, and D38 recorded it as latent

D38 and `web/lib/api.ts` both say the mis-titling is latent, *"because the picker renders the
server's sentence inline rather than through `<Failure>`"*. The picker is not the only caller that
can provoke one, and that reasoning was never checked against the screens that are not the picker.
Three `<Failure>` banners can carry a 409 or a 422, and the titling is wrong at two of them without
any race at all:

- `web/app/activity/page.tsx:101` — the import path, and the **422** is wrong here on the screen the
  words were written for. `failImport` (`internal/api/imports.go:57-60`) maps both
  `library.ErrNoVideo` and `library.ErrBadTitle` onto 422, and *"Nothing to import"* is false for the
  second. The 409 on this same banner is the one case the old title described correctly.
- `web/app/library/page.tsx:139` — a delete refused by `torrent.ErrWrongCategory`
  (`internal/api/movies_delete.go:55`), titled *"Not finished yet"*. Ordinary rather than exotic on
  the qBittorrent backend, which is exactly where the *arr stack's torrents are.
- `web/components/releases.tsx:118` — a dispatch refused by `alreadyHave`
  (`internal/api/downloads.go:146`), titled *"Not finished yet"*. **Measured: this one needs a
  race.** `/movie/` hides the release list for a film already on disk (`web/app/movie/page.tsx:238`)
  and its own comment is careful that the server refusal is the guard and the notice is *"the
  explanation, not the guard"* — so reaching it means the film became imported while the page sat
  open.

Wrong in three places, ordinary in two of them, and never latent in any.

## The count was five and it is ten, which is the actual finding

`grep -rn "StatusConflict" internal/` is eleven sites carrying ten distinct sentences across eight
routes — not the five `api.ts` claims. The five it lists are the `download`/`movies` family; it
misses `ErrTMDBIDTaken` on the match routes it was written beside, the settings write, and three
Jellyfin refusals.

**The number is not a documentation defect, it is the argument.** A title keyed on a status is a
second vocabulary that has to be kept in step with the Go one — the thing `Download.reason` refuses
by name in the same file (*"nothing here maps a code onto a string"*) — and this one fell out of step
five refusals ago without a single test or type failing. Nothing makes the sixth cheaper to notice.

422 has the same disease one screen further in, and worse: `failImport`
(`internal/api/imports.go:57-60`) maps **two** sentinels onto one status, so `library.ErrNoVideo` and
`library.ErrBadTitle` arrive at the same banner. "Nothing to import" is false for the second — there
is something to import and its title is what cannot be a folder name.

## A caller cannot supply the missing title either

The obvious repair is a `title` prop, so the screen that knows what the user was doing says it. It
does not work, and the reason is worth writing down before somebody tries it again: **every error
state in this UI holds more than one status.** `web/app/library/page.tsx` has one `error`, set by
`load()`, `rescan()` and `remove()` alike, so any fixed word above it is wrong for two of the three.
The same is true of `importError`, which a 500 or a 502 reaches as readily as the 422.

So the title cannot come from the status and cannot come from the caller. It comes from neither, and
the sentence the server already wrote carries the banner — which is what
`web/components/match-picker.tsx:175-179` has rendered all along.

## Do

1. **`title()` returns `string | null`, and `<Failure>` tests the null before drawing the
   `<strong>`.** Write that test by hand and do not expect the compiler to demand it —
   **measured: `<strong>{heading}</strong>` with `heading: string | null` typechecks clean**, because
   `ReactNode` includes `null`. `npx tsc --noEmit` exits 0 on it. It renders an *empty* `<strong>`,
   and `.banner strong` is `display: block` with a `margin-bottom`, so the visible symptom is a stray
   gap above the message rather than an error anybody gets told about.

2. **409 and 422 return null explicitly, in a shared case, and are NOT deleted.** A deleted case
   falls through to `default` and titles them `Failed (409)` — the status number dressed as a
   diagnosis, which is a worse claim than none.

   Together with 1 those are **two ways to implement this wrongly, and both compile**. `web/` has no
   test runner and `tsc` refuses neither, so the explicit `heading !== null`, the explicit `return
   null`, and the comments on both are the entire guard. That is the honest state of it.

3. **Keep every other title**, and record the rule that keeps them: *a title may only say something
   true of every situation its status covers*. 503 is always an unconfigured integration and 502 is
   always a dependency that is down, so those categories cover many situations without lying about
   one. That is the test to apply to the next status, not "does it have one meaning".

4. **Correct the `ApiError` comment.** It says five, it says latent, and both are false. It is the
   only place in the repo that enumerates what a 409 means, so a wrong count there is what the next
   session will build on.

## Do not

- **Add a `code`, `kind` or `slug` to the error envelope.** It is the durable fix and it is not this
  task: `s.fail` (`internal/api/api.go:508`) is the chokepoint for six of the eleven, and the other
  five write `StatusConflict` inline through three different envelopes. It is a server-side scheme
  across seven sites, and it buys a title that the server's sentence is already carrying.
- **Add a `title` prop to `<Failure>`.** See above — no error state in this UI holds one status, so
  the prop would be wrong wherever it was used and the guess would survive wherever it was not.
- **Reword the server's sentences.** Three of the ten do leak a Go error chain at a human
  (`import <hash>: …`, and `delete movie 7: removing torrent …: the torrent client: qbit
  torrents/delete: …`), and a title above them is exactly the paper that has been hiding it. Removing
  the paper is this task; writing those three sentences is a server task with its own measurements.
- **Touch the twelve inline renderers.** `web/components/settings/section.tsx`, `first-run.tsx`,
  `gate.tsx` and `settings/playback.tsx` hand-roll `<div className="banner error">` with their own
  titles, and those titles are written per situation and are already true. Folding them into
  `<Failure>` is a refactor with no defect behind it.
- **Change any Go file.** The status codes are right; 409 and 422 are the correct answers to all
  fourteen refusals. What was wrong was the browser claiming to know which.

## Verify

`make check`. **It does not prove this change and neither does `tsc`** — see *Do* 1 and 2, where both
wrong implementations are measured compiling. `make check` is here to prove the change broke nothing
else, which is a different claim and the only one available: there is no test file anywhere under
`web/`.

By reading, then, since nothing in this repo executes a React tree:

- `title()` returns null for 409 and 422 and a string for 0, 410, 502, 503, 404 and the default
- `<Failure>` renders no `<strong>` for a null, and the banner is still `role="alert"` with the
  message intact — `.banner strong` is `display: block` (`web/app/globals.css:234`), so a missing
  title costs a line and no layout

Then live, on a scratch instance running the new binary, with real refusals rather than mocked ones.
A `Title (Year)` folder holding one sparse file scans as a real imported row, which is all either
refusal needs:

- `POST /api/downloads` for that film answers **409** *"curator already has this film, at … — delete
  it first to replace it"*. `alreadyHave` runs before the dispatcher, so a bogus release id is
  enough and no indexer is asked anything.
- `POST /api/movies/{id}/match` on that row answers **409** *"this film is already matched to a TMDB
  film; correcting that match is a PUT, not a POST"*.

And in a browser, on the rendered DOM, because that is the only place the change is visible.
`<Failure>` at a 404 still reads `<strong>Not found</strong>` followed by the sentence. At a 409 the
`<strong>` is **absent entirely** — `querySelectorAll('strong').length === 0`, not an empty one — and
the banner is the server's sentence and the retry button alone.

Reaching that 409 needs one honest caveat written down rather than glossed: the two ordinary routes
to it need a qBittorrent holding a foreign-category torrent, which is not a thing to conjure on a
shared daemon. So the 409 was curator's own, fetched live from `POST /api/downloads`, and only the
*click* that delivered it was substituted — the response, the `request()` path and the `ApiError`
are all the real ones. It proves the rendering, which is what changed; it does not re-prove the
server, which the two `curl`s above do.

And on the artefact itself, which is what actually ships: `internal/web/dist/` must no longer contain
`Not finished yet` or `Nothing to import`, and must still contain `A dependency is down`,
`Not configured` and `That search has aged out` — the two dropped and three of the five kept.
