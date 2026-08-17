# T72 — a dependency's failure answers with a sentence, and the chain goes to the log

**Owns:** the twelve 502/503 answers that put a `fmt.Errorf` chain on the wire — `failDelete`
(`internal/api/movies_delete.go`), `failImport` (`internal/api/imports.go`), `failTMDB` and the
discover rails (`internal/api/browse.go`), `failDispatch` (`internal/api/downloads.go`),
`provisionFailure` and `adoptFailure` (`internal/api/jellyfin.go`) — plus `failCause`/`logCause` and
one typed error, `download.Unprotected`
**Depends on:** nothing. It is the debt [T71](T71-a-sentence-written-for-a-human.md) named in its own
*Do not* — it fixed the 409s and 422s and left the 5xx where it found them — under the rule
[D40](../decisions.md#d40--a-refusals-sentence-is-written-at-the-boundary-that-answers-it) set

## Goal

No dependency failure shows a human a Go error chain, and no chain is lost doing it. The person
reading gets a sentence; the operator reading `/api/logs` gets everything the sentence dropped.

## It is twelve, not two

The handoff measured two strings, both from `POST /api/jellyfin/provision`. They are real and
correctly transcribed, and they are two samples from one route in one package. Counting every site
that decides a 502 or a 503 — `internal/api/` contains **no numeric status literals**, every site
goes through an `http.Status*` constant, so the constant grep is exhaustive:

| | 502 | 503 | total |
|---|---|---|---|
| distinct sites | 10 | 15 | **25** |
| raw wrapped chain | 8 | 4 | **12** |
| hand-written sentence | 2 | 7 | 9 |
| bare-sentinel passthrough | 0 | 3 | 3 |
| mixed — one arm each way | 0 | 1 | 1 |

Six of the ten 502 sites and nine of the fifteen 503 sites are **outside `jellyfin.go`**. The leak is
not a Jellyfin problem; it is delete, import, dispatch, both TMDB arms and the VPN guard as well.

**And then there were thirteen.** `internal/api/browse.go:177` set `rows[i].Error = errs[i].Error()`
on a failed discover rail inside a **200**, so no audit keyed on a status constant could find it. It
was measured by accident, on a live instance with a key TMDB refuses, while checking the 502 above.

## The design question T71 answered does not transfer, and its answer inverts

T71's argument turned on `internal/api/api.go:505`: the only log is gated on `status >= 500`, so a
409 was written to the client and to nobody else, and there was no log line to protect. **At 502 and
503 that gate is open.** Measured, on the three failure families:

- **The `fail` sites already log the chain.** Passing a written sentence to `fail` would have put the
  sentence in *both* channels and destroyed the chain — the exact opposite of T71's situation.
- **The seven Jellyfin sites log nothing at all.** `provisionFailure` and `adoptFailure` answer
  through `respond` (`jellyfin.go:343`, `:785`), not `fail`. They were 5xx written to **nobody**: the
  chain sat in a body where it could not be read, and in no log.
- **So the two channels are the fix.** `failCause(w, status, sentence, cause)` writes the sentence and
  logs the cause; `logCause(status, cause)` does the log half for the handlers that build their own
  body. `fail` is untouched, which keeps T71's *Do not* — the overload it refused was refused because
  the second channel was dead at 409, and it is not dead here.

`internal/api/stream.go:299-301` has done exactly this since T44 — `log.Warn` with ffmpeg's stderr,
then a written sentence to the caller — and `stream_test.go:945-966` pins both halves. It is the
precedent, not an invention.

## Where a fact would be lost, it travels in a typed error

One sentence needed a value only the chain had. `internal/vpn`'s guard sentences are already the
actionable half — they name `VPN_CONFIG`, or `VPN_REQUIRED=false`, or the exit address that matched
curator's own — and the only thing wrong with that 503 was `dispatch <releaseID>: ` and a second
sentinel in front of them. So `download.Unprotected{Reason}` carries the guard's own words, in T71's
shape: `Error()` is the reason alone, `ErrUnprotected` is reached through `Unwrap` and never prints.

Its `Unwrap` returns **both** errors, in `unreachable`'s multi-error form, because the wrap it
replaces was `%w: %w` and a guard that timed out must keep answering
`errors.Is(err, context.DeadlineExceeded)`.

## Do

1. **`failCause` and `logCause` in `api.go`,** both gated at 500 exactly as `fail` is. Do not change
   `fail`.
2. **The five `fail` classifiers write sentences** and hand the chain to `failCause`. `failDispatch`'s
   shared 503 arm **splits in two**: `ErrUnconfigured` arrives bare and its own text names the two
   variables that are the whole remedy, so it keeps passing through; `ErrUnprotected` gets the typed
   error and a sentence.
3. **`provisionFailure` and `adoptFailure` split** into a pure `…Outcome` that classifies and a method
   that logs, so a branch cannot be added later that forgets the log. Every branch assigns
   `body.Error`; the `step.err.Error()` seed is deleted.
4. **The sentence never repeats `Step`.** The screen renders *"Setting up Jellyfin failed at the
   library"* (`web/components/settings/playback.tsx:457`), so a sentence naming the step says it twice.
5. **One `tmdbSentence`,** used by both `failTMDB` and the discover rail. The rail logs at **Warn**
   and not through `logCause`, because its envelope is 200 and a gate on the status would drop it.

## Do not

- **Reword the nine 502/503 sentences that are already written**, or touch the eleven fixed strings
  in the table above. T71 left its eight clean ones alone for the same reason.
- **Route `stream.go:284`'s remux-cap 503 through `fail` or `failCause`.** It answers through
  `respond` deliberately, and `stream_test.go:889-908` asserts **no entry at ERROR** — the cap doing
  its job is not a server fault, and `api.go:505` would log it as one.
- **Add a `code`, `kind` or `slug` to the envelope.** Refused by D39 and again by D40, and nothing has
  changed: nothing branches on which 502 it got, and `jellyfinFailureBody.Adopt` is still the
  precedent for the day something does.
- **Stop wrapping in `internal/jellyfin`, `internal/tmdb`, `internal/qbit`, `internal/engine`,
  `internal/download` or `internal/vpn`.** `CLAUDE.md:112-114` requires those chains and they are now
  the log's content. They were never the defect.
- **Log the 4xx.** D40 left them alone and the reason is unchanged — `failFields`
  (`internal/api/settings.go:507-515`) never logs because its messages can quote a rejected value.
  `logCause`'s gate is what lets the Jellyfin handlers, which answer 409 and 422 as well, gain logging
  without settling that.
- **Touch the 500s.** They put a chain on the wire too, and there the chain is arguably the right
  answer. It is a separate argument and it does not belong in this commit.

## Verify

`make check`.

Hermetic, and this is the part `make check` proves. The wrong implementation is invisible rather than
broken — every one of these sites passed the whole suite before the rewrite — so what is asserted is
**an absence in the body and a presence in the log**, which is the pair T71 did not have to make:

- **`internal/api/movies_delete_test.go`** — a full delete chain answers 502 saying the film survived;
  the body contains none of `delete movie`, `removing torrent`, `qbit`, `engine:`, `torrents/delete`,
  `calling qBittorrent at`, the info hash, `connection refused`; **and the log contains the endpoint,
  the transport error and the hash**. `the torrent client` is deliberately not an absence: it is
  `ErrClient`'s text and also the plain English name of the thing, and the written sentence says it.
- **`internal/api/imports_test.go`** — the same, plus the sentence says trying again will work.
- **`internal/api/browse_test.go`** — the two TMDB 502s answer with **different** sentences, or the
  `ErrUnauthorized` arm is decoration; a failed discover rail is a **200** whose row carries the
  sentence and whose chain is in the log.
- **`internal/api/downloads_test.go`** — the VPN 503 **keeps `internal/vpn`'s sentence whole** while
  losing `dispatch yts-1234` and `refusing to dispatch`; a bare `ErrUnprotected` still answers 503 with
  the fallback.
- **`internal/api/jellyfin_test.go`** — all four 502/503 branches lose `jellyfin provision`,
  `jellyfin find movie`, both upstream paths and `snippet`'s quoted third-party body, keep
  `Instructions` and `Step`, and **reach the log**; and a 409 still reaches **no** log.
- **`internal/download/service_test.go`** — `errors.Is` answers for `ErrUnprotected` *and* for the
  guard's own cause through the wrap.

Every one of these was verified by reverting the handler and watching it fail — the Jellyfin four
against `git checkout HEAD -- internal/api/jellyfin.go`, which fails on both the leak assertions and
the log assertions.

Then live, on a scratch instance with `DB_PATH` set and `PORT=8095`, because the repo's
`./curator.db` is the 8090 instance's live database:

- `POST /api/jellyfin/provision` with `JELLYFIN_URL` **unset** — the bundle default `http://jellyfin:8096`
  does not resolve — answers **503** with the written sentence while the log carries the whole chain.
  Note `JELLYFIN_URL` must be unset rather than pointed somewhere dead: set in the environment it
  shadows the setting and the 409 fires first.
- `GET /api/tmdb/search?query=dune` with a 32-character `TMDB_API_KEY` TMDB refuses answers **502**;
  `GET /api/tmdb/discover` answers **200** with both rails carrying the sentence and both chains in
  the log.

The torrent-client 502s need a qBittorrent that has stopped answering, which is not conjurable on a
shared daemon — T70 and T71 recorded the same caveat. The hermetic tests are the honest proof there,
and they are a better one: they pin eight specific substrings out of the body and three into the log.
