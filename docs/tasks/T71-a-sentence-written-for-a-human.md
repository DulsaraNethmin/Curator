# T71 — every refusal answers with a sentence somebody wrote

**Owns:** the five 409/422 answers that put a `fmt.Errorf` chain on the wire — `failDelete`
(`internal/api/movies_delete.go`), `failImport` (`internal/api/imports.go`), `provisionFailure`
(`internal/api/jellyfin.go`) — and the three typed errors that carry the facts those sentences need
**Depends on:** nothing. It is the debt [T70](T70-a-title-that-is-true.md) named in its own *Do not* —
*"writing those three sentences is a server task with its own measurements"* — and
[D39](../decisions.md#d39--a-failure-title-must-be-true-of-every-situation-its-status-covers-or-there-is-no-title)
recorded as its one honest edge

## Goal

No refusal shows a human a Go error chain. Every 409 and 422 answers with prose written for the
person reading it, and the wrapping chain stays intact for Go.

## It is five, not three

D39 and T70 both say three. `internal/api/api.go:508` is `s.respond(w, status, map[string]string{"error": err.Error()})`
— the whole flattened chain, every `%w` prefix any package added, straight onto the wire. Counting
every distinct 409 and 422 sentence and reconstructing each one from its format strings:

| | sentences | written for a human | leaking a chain |
|---|---|---|---|
| 409 | 10 | 7 | **3** |
| 422 | 3 | 1 | **2** |
| | **13** | **8** | **5** |

The three at 409 are the ones D39 named. **The two at 422 are new**, and one of them is a single
`+` in the middle of an otherwise correct handler:

1. `internal/api/movies_delete.go:55` — the worst. Three packages' prefixes, and the same 40 hex
   characters twice **in two different cases**: `download.TorrentHash` is upper-case
   (`internal/torrent/torrent.go:63-64`) and `wire` is lower-cased on the way in
   (`internal/qbit/client.go:509-511`), so it reads as two torrents. It also has a second variant
   nobody has written down — `internal/engine/engine.go:445` wraps the same sentinel with `engine: `
   where qbit wraps it with `qbit torrents/delete: `, so the sentence differs by backend.
2. `internal/api/imports.go:60` — **at least eight distinct strings from one `s.fail`.**
   `library.ErrBadTitle` has six wrap sites (`internal/library/link.go:132, 151, 160, 167, 170, 192`)
   and `library.ErrNoVideo` has two (`link.go:215, 225`), all funnelled through
   `internal/importer/importer.go:95`'s `import %s: `. Leaks a path on the server's download disk.
3. `internal/api/jellyfin.go:513` — `body.Error = "…" + body.Error`. A good sentence with the raw
   chain **concatenated onto the end of it**. Ten lines away, `internal/api/jellyfin.go:1041` does
   the same job with `=` instead of `+` and is clean.
4. `internal/api/jellyfin.go:476` — `jellyfin provision: ` plus a tail that restates the wrapper:
   `internal/jellyfin/provision.go:269` writes *"is already set up"* and then appends
   `ErrAlreadyConfigured`, whose text is *"jellyfin has already completed its startup wizard"*. One
   sentinel wrapped in a paraphrase of itself.
5. `internal/api/imports.go:55` — `import <hash>: ` only. The mildest, and the sentinel behind it is
   the one complete honest human sentence in the whole failure path.

## The design question T70 posed has a third answer, and both its horns are false

D39 left the choice as: carry a sentence separately from the chain (a field on the envelope), or
stop wrapping (and lose what the log says). **Neither is what this needs, and the second premise does
not hold at all.**

- **Nothing logs these.** `internal/api/api.go:505` gates the only log on
  `status >= http.StatusInternalServerError`. Every 409 and every 422 in this codebase is written to
  the client and **never written to a log at all**. There is no log line to protect. The asymmetry
  runs the wrong way: the response is the verbose channel and the log is the silent one.
- **The one chain that is logged already has its prefix as a field.**
  `internal/importer/importer.go:302` is `im.log.Warn("import failed", "hash", hash, "name", name, "err", err)`
  — so `importer.go:95`'s `import %s: ` duplicates an attribute that is already there, plus a `name`
  the string does not have. (`logFailure` de-duplicates on the rendered message and is keyed by hash,
  `importer.go:291-301`, so stripping the prefix does not weaken the de-dup either.)
- **No envelope field is needed.** D39 refused a `code`/`kind` on scope and was right; this task does
  not want one either, and nothing here branches on which refusal it got.

The answer is the one already in this package: **the handler that identified the refusal writes the
sentence.** `failMatch` (`internal/api/movies_match.go:211-229`) has done exactly this since T67 —
`internal/store/movies.go:272` wraps every match failure with `match movie %d to tmdb %d: ` and
`failMatch` discards all of it, matching with `errors.Is` and building a fresh `errors.New`. Its
three 409s are three of the seven clean ones. `internal/api/imports.go:47-49` does it once too, for
`store.ErrNotFound`, in the very handler that leaks four lines below.

**The chain is not deleted and no package stops wrapping.** `CLAUDE.md:112-114` mandates
`fmt.Errorf("scan %s: %w", path, err)`, and it stays exactly as it is. What changes is that
`err.Error()` stops being the thing the browser reads.

## Where a fact would be lost, it travels in a typed error

Three of the five sentences need a value that only exists deep in the chain, and a handler that
cannot reach it can only write a vaguer sentence. The repo has the pattern already: `stepError`
(`internal/api/jellyfin.go:447-455`) is a struct field travelling with an error and recovered by
`errors.As`, and `unreachable` (`internal/jellyfin/provision.go:884-889`) attaches a sentinel
**without letting it print**. Three small types, each `Unwrap()`ing to the sentinel that already
exists, so every `errors.Is` in the repo and in the tests keeps answering:

- `torrent.WrongCategory{Hash, Actual, Required}` — `"radarr"` is the single actionable word in that
  409 and there is no other way to reach it. It belongs in `internal/torrent` for the reason
  `ErrWrongCategory` already does (`torrent.go:54-57`): both backends wrap it, and an API layer
  reaching into one particular client is the coupling that package removes. **It also collapses the
  two backend variants into one sentence**, which is the qbit/engine defect above.
- `download.NotCompleted{State}` — `"stalled"` and `"downloading"` are different situations and the
  distinction is the whole value of that answer.
- `library.BadTitle{Title, Reason}` — six sites with six already-written reasons. The reason moves
  into a field; nothing is reworded.

**The sentence must never depend on the typed error being present.** Every handler writes a true
sentence from the sentinel alone and the typed error only adds a clause. This is not defensive
programming for its own sake: `internal/api/imports_test.go:64-89` and
`internal/api/movies_delete_test.go:67` construct their cases as bare `fmt.Errorf("import x: %w", …)`
with no typed error in sight, so the fallback branch is the one seven existing tests already
exercise.

## Do

1. **Three typed errors**, each with `Unwrap() error` returning its existing sentinel, constructed at
   the sites that have the facts: `internal/qbit/client.go:449` and `internal/engine/engine.go:445`;
   `internal/download/service.go:343`; `internal/library/link.go:132, 151, 160, 167, 170, 192`. The
   sentinels stay exported and stay the thing every `errors.Is` matches.

2. **`failDelete` and `failImport` write their sentences**, in `failMatch`'s shape — `errors.As` for
   the clause, a sentence that is true without it. **Split `imports.go:56`'s shared case in two**:
   `ErrNoVideo` and `ErrBadTitle` reaching one banner as one sentence is the 422 half of the defect
   T70 found, and leaving them joined would fix the prefix and keep the lie.

3. **`provisionFailure` assigns, never appends.** `jellyfin.go:513`'s `+ body.Error` becomes `=`,
   matching `jellyfin.go:1041`. For `ErrAlreadyConfigured`, use the sentence the probe route already
   wrote for the identical condition — `jellyfin.go:221` — rather than a ninth phrasing of it.

4. **Drop the restatement at `internal/jellyfin/provision.go:269`.** *"is already set up"* and the
   sentinel it appends say the same thing. It is a log/500 string after this task and still should
   not say it twice.

5. **Do not touch `internal/importer/importer.go:95`.** It is measured redundant with the log's own
   `"hash"` attribute, and it is also the only prefix that survives into a 500 where the chain is the
   right answer. Removing it is a change with no defect behind it once the handlers stop rendering it.

## Do not

- **Add a `code`, `kind` or `slug` to the error envelope.** D39 refused it on scope and the reason
  holds: nothing branches on which 409 it got, and `jellyfinFailureBody.Adopt` remains the precedent
  for the day something does.
- **Stop wrapping in `internal/download`, `internal/qbit`, `internal/engine` or `internal/library`.**
  The chains are correct Go and `CLAUDE.md:112-114` requires them. They were never the defect; the
  defect is one line rendering them at a human.
- **Change `s.fail`.** It has one job and it does it. A `s.fail(w, status, human, logged)` overload
  would put the decision back in the helper, and the five handlers are where the sentinel is already
  known.
- **Log the 4xx.** It is tempting once you notice nothing does, and it is a separate argument with a
  separate cost — `failFields` (`internal/api/settings.go:511-512`) deliberately never logs because
  its messages can quote a rejected value. Do not settle that here.
- **Reword the eight sentences that are already written for a human**, or touch `web/`. This task
  changes what the server says, and `<Failure>` renders the server's sentence unchanged (D39).

## Verify

`make check`.

Hermetic, and this is the part `make check` actually proves — the existing tests assert the status
and that `{"error": …}` is non-empty, so they pass either way and are not the guard:

- **`internal/api/movies_delete_test.go`** — a `torrent.WrongCategory` answers 409 and the body
  contains `radarr`; a bare `fmt.Errorf("delete: %w", torrent.ErrWrongCategory)` answers 409 with the
  fallback sentence; **neither body contains `qbit`, `engine`, `torrents/delete`, `delete movie` or
  the info hash.**
- **`internal/api/imports_test.go`** — `library.BadTitle{Title: "Dune", Reason: …}` answers 422 with
  a body naming `Dune` and its reason; `library.ErrNoVideo` answers 422 with a **different** sentence
  from `ErrBadTitle`'s, which is the T70 finding made executable; `download.NotCompleted{State: "stalled"}`
  answers 409 naming `stalled`. **No body contains `import ` followed by the hash, `find feature in`,
  or `destination folder`.**
- **`internal/api/jellyfin_test.go`** — the `ErrBadCredentials` 422 body is exactly the written
  sentence and does **not** contain `AuthenticateByName` or `jellyfin provision`; the
  `ErrAlreadyConfigured` 409 body does not contain `jellyfin provision` and does not say
  "already" twice.
- **`internal/library`, `internal/qbit`, `internal/engine`, `internal/download`** — `errors.Is`
  against every sentinel still answers through the new types. This is the one that would break
  silently and take the API's status codes with it.

Then live, on a scratch instance with `DB_PATH` set, because the repo's `./curator.db` is the 8090
instance's live database:

- a `Title (Year)` folder holding one sparse file scans as a real imported row, and
  `POST /api/downloads` for it answers **409** with `curator already has this film` — unchanged, and
  the control that proves the clean seven were not disturbed.
- `POST /api/jellyfin/provision` against a throwaway Jellyfin that has already completed its wizard
  answers **409** with the probe's sentence and `"adopt": true`.

The delete 409 needs a qBittorrent holding a foreign-category torrent, which is not conjurable on a
shared daemon — T70 recorded the same caveat. The hermetic test is the honest proof for that one, and
it is a better one than a live click: it pins the absence of five specific substrings.
