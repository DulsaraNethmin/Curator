# T65 — the Playback screen: how do you want to watch?

**Owns:** `web/app/settings/` (a Playback group), `internal/api/jellyfin.go`, and the
`PLAYBACK_TARGET` registry entry
**Depends on:** [T63](T63-compose.md) (there has to be a profile to start),
[T64](T64-jellyfin-provisioner.md) (there has to be something to call)

## Goal

A stranger who has just run `docker compose up -d` is asked one question — **in this browser, or on
my TV?** — and if they answer TV, curator walks them from nothing to a signed-in Apple TV without
their ever opening Jellyfin's own setup wizard.

## Do

1. **Two answers, and the browser one is complete.** *In this browser* stores
   `PLAYBACK_TARGET=browser`, runs no second container, asks nothing further, and never nags again.
   Phase 8's player is a real answer and this screen must not read as though it were a step on the
   way to the real one.

2. **Choosing Jellyfin shows one line, and says why it is being pasted.** Not an apology — a
   sentence: curator cannot start containers for you, deliberately, because the only way to do that
   is to hand it root on your machine. Then:

   ```
   docker compose --profile jellyfin up -d
   ```

   with a copy button, and the note that it leaves curator running.

3. **Poll `GET /api/jellyfin/probe` while that command runs.** Cold start was measured at **17.6 s**
   from `docker run` to a `200` on `/System/Info/Public`, and a first-ever `docker compose pull` adds
   **202 MB** of layers on top. So the wait is *minutes* on a slow line, not seconds: poll patiently,
   show that it is still waiting rather than a spinner that looks stuck, and never time out into an
   error state that requires a page reload to leave.

4. **The probe reports which of three worlds it is in**, because the screen branches on it:
   unreachable; reachable with `StartupWizardCompleted: false` (offer **Set up Jellyfin**, this
   task); reachable with `true` (offer **Connect to it**, which is
   [T66](T66-adopt-jellyfin.md)).

5. **Collect a username and password in curator's UI**, and pass them straight to
   [T64](T64-jellyfin-provisioner.md)'s `Provisioner`. Say plainly that these are the credentials for
   **Jellyfin**, that they are what gets typed into the Apple TV, and that curator does not keep the
   password — it mints a key and forgets it.

6. **The library path is not a field.** It is `LIBRARY_MOVIES` as this process resolved it, shown
   read-only with a sentence saying Jellyfin will be pointed at it. That is the entire point of the
   shared volume: the number-one silent failure in self-hosted media is two services disagreeing
   about a path, and a text input is how it gets reintroduced.

7. **`jellyfin_public_url` gets a default from the browser, and the browser is the only thing that
   knows it.** curator runs in a container and **cannot learn the host's LAN IP**. The answer is
   already on screen: the page was reached over that address, so
   `window.location.hostname` + `:8096` is the default, presented in an **editable** field and
   labelled as a guess. Never write it silently — a wrong one produces an Open in Jellyfin link that
   fails only for other people's devices, which is the hardest kind of bug to be told about.

8. **Write the results through phase 7's settings machinery, not beside it.** `jellyfin_url` is
   `http://jellyfin:8096`, `jellyfin_api_key` goes in as a secret — encrypted at rest, never read
   back across the API
   ([D28](../decisions.md#d28--settings-are-writable-secrets-are-encrypted-at-rest-and-write-only-across-the-api)) —
   and `jellyfin_public_url` is what the browser supplied.

9. **Every failure degrades to instructions**, with the exact path to paste. That is a phase
   requirement rather than polish: the startup endpoints are not a documented contract, so the flow
   has to survive a Jellyfin that answers something new. "Open `http://<host>:8096`, run its wizard,
   add a Movies library at `/media/movies`, then paste an API key here" is a complete fallback and it
   must be reachable from the failure, not only from a doc.

10. **Success ends somewhere.** A "you are done — open Jellyfin on your TV and sign in as `<user>`"
    panel, with the public URL shown. This is the screen's whole purpose and it should read like an
    ending.

## Do not

- **Ask before the user has done anything.** The question belongs where the answer is needed, not as
  a modal in front of an empty library.
- **Make the browser answer feel like a decline.** Equal weight, and no second prompt later.
- **Start anything.** curator does not run `docker`, cannot, and must not appear to try. The button
  copies a command; the user runs it.
- **Store the Jellyfin password.** Used once, then only the minted key persists.
- **Show the API key back**, even truncated. D28 and D17 are explicit: a masked secret still confirms
  an existence and a length to anyone on the LAN. `configured: true` and nothing else.
- **Put `PLAYBACK_TARGET` to work.** It records which question has been answered so the screen stops
  asking. It must not gate the Play button, hide the Jellyfin link, or change what any endpoint does
  — that would be phase 8's "no prefer-direct-play toggle" reintroduced under a new name.
- **Block the rest of the app on this screen.** Search, library and downloads all work with no
  playback choice made at all.
- **Poll from the browser through a proxy endpoint that blocks.** The probe is a short-timeout call —
  `internal/jellyfin` already uses a **2 s** lookup timeout for exactly this reason — and a probe
  that hangs makes the settings page hang with it.

## Verify

Hermetic, over the API with a fake Jellyfin:

- the probe's three worlds each render the right branch, including the `true` one handing off to T66
- provisioning writes `jellyfin_url`, `jellyfin_api_key` and `jellyfin_public_url`, and
  `GET /api/settings` reports the key as `configured: true` with **no value**
- a provision failure at each of T64's steps renders instructions containing the real library path
- `PLAYBACK_TARGET=browser` makes the screen stop asking and changes nothing else — asserted by the
  Play button and the Jellyfin link behaving identically either way

Then driven for real, against the **embedded build and not `next dev`** — `:3000` against `:8090` is
cross-origin and there is no CORS header anywhere in the Go code, on purpose
([`phase-8.md`](../phase-8.md)):

- from `docker compose up -d` on a clean volume: choose Jellyfin, paste the line, watch the probe
  find it, set a password, and reach the ending panel
- **then open Jellyfin on a device that is not this laptop**, sign in with those credentials, and see
  the film. That is the phase's definition of done and this screen is the only place it can be
  observed.
- Open in Jellyfin on a film's page now lands on **that film** rather than a search — which is
  [D32](../decisions.md#d32--the-jellyfin-link-is-keyed-on-the-tmdb-id-not-on-the-path) working
  against a library curator created, and the thing the current local install cannot do because its
  Jellyfin is looking at the Pi's disk.
