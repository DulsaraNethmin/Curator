# T50 — the first run

**Owns:** a first-run route in `web/app/`, and whatever `internal/api/settings.go` has to answer to
drive it
**Depends on:** [T47](T47-image.md), [T63](T63-compose.md)

## Goal

A stranger whose container has just started for the first time is not shown an empty library. They
are shown the three or four things curator needs, in order, with the reason for each — and then the
empty library, which by then makes sense.

**This is T51's other half, and it is numbered 50 rather than 51.** `phase-7.md:384` says *"A
first-run wizard. T51"*, and `phase-6.md:24` says T51 corrects the stale documents. Both cannot be
T51. [`phase-9.md`](../phase-9.md) resolves it: T51 keeps the documents, because phase 6 deliberately
left them stale *in exchange for that promise*, and the wizard takes **T50** — a number the plan
reserved for this phase and never gave a meaning to, so it displaces nothing.

## Do

1. **Detect a first run from the settings, not from a flag.** No TMDB key configured and no films
   scanned is a first run; a `first_run_complete` row is a second source of truth that goes wrong
   when someone clears the database and keeps the volume. Phase 7's `GET /api/settings` already
   reports `configured` per setting and every setting's **source**, which is enough.

2. **Ask for what actually blocks the product, in the order it blocks it.**
   - **TMDB key** — without it nothing resolves, and it is free. Link straight to the page that
     issues one.
   - **Where your films are** — `LIBRARY_MOVIES`, already `/media/movies` in the bundle, so this is a
     confirmation rather than a question. Offer the scan.
   - **A VPN config** — because `VPN_REQUIRED` is `true` by default and dispatch is refused without
     one ([D27](../decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket)). Say that
     plainly: it is not a bug and it is not optional-by-accident.
   - **How you want to watch** — hand off to [T65](T65-playback-screen.md)'s Playback screen rather
     than duplicating it.

3. **Every step is skippable and the app works without it.** curator's existing posture is that an
   unset key disables a feature rather than failing start-up
   ([D15](../decisions.md#d15--the-jellyfin-refresh-is-best-effort-and-its-key-is-optional)), and a
   wizard that insists is a wizard people close.

4. **Say what is set by the environment and cannot be typed here.** D28's precedence means the
   environment beats the store, and phase 7 already built a screen that refuses to let you type into
   a field it would ignore. The first run must inherit that rather than reinvent it, or someone in a
   compose deployment types a TMDB key into a box that silently does nothing.

5. **Offer the password once, and be honest about it.** Authentication is off by default
   ([D25](../decisions.md#d25--authentication-is-optional-and-off-by-default)) and that stays true;
   this is the one moment where offering it is not nagging. The sentence D25 requires — **there is no
   TLS, so the password crosses the LAN in clear** — belongs here as much as on the settings screen,
   because this is where the belief is formed.

6. **Ending on a scan is the right ending.** The last thing a first run should do is find the user's
   films and show them.

## Do not

- **Block the app behind it.** Every screen must be reachable with the wizard dismissed. It is an
  offer, not a gate.
- **Add a settings row to remember it.** Point 1.
- **Re-ask on every restart**, which is what a badly derived "is this a first run" does the moment
  someone runs with `-e` and no store.
- **Collect anything curator does not need**, and do not ask for a Jellyfin URL here — T65 owns that
  question and asking it twice with two different defaults is how they disagree.
- **Duplicate the Settings screen.** Phase 7 built the fields, the validation and the source
  reporting. This is an ordering and a set of sentences over the same API, not a second editor.
- **Write `PLAYBACK_TARGET` on behalf of the user.** It records an answer they gave.

## Verify

- a container with an empty volume lands on the first run; the same container restarted after
  completing it does not
- a container started with `-e TMDB_API_KEY=…` skips that step and **says** the environment set it
- every step skipped: the app is fully usable, the library scans, search works, dispatch is refused
  with the VPN sentence
- turning the password on inside the wizard leaves the session logged in rather than immediately
  locked out ([D29](../decisions.md#d29--a-written-setting-applies-at-the-next-start-the-password-applies-at-once):
  the password applies at once)
- clearing the database but keeping the volume produces a first run again, which is the behaviour a
  stored flag would have got wrong
