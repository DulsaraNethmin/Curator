# T42 — the Settings screens

**Owns:** `web/app/settings/page.tsx`, `web/components/settings/`, `web/components/gate.tsx`,
`web/lib/api.ts`
**Depends on:** T40, T41

## Goal

Everything in the catalogue is editable from a browser, a secret is never rendered because it is
never sent, and a password can be turned on from the same screen that warns you what it does and
does not protect.

## Do

1. **The screen keeps its read-only half and grows a writable one above it.** The integrations table,
   the probe button, `paths` and `intervals` are what phase 5 built and what "what is this process
   actually running" is answered by; they stay, below the form. The form is *what will be running
   next time*, and the distinction is the screen's whole job now
   ([D29](../decisions.md#d29--a-written-setting-applies-at-the-next-start-the-password-applies-at-once)).

2. **One section per group, one Save per section.** A section submits only its own changed fields,
   which is exactly the partial update T40 accepts and keeps the all-or-nothing transaction the size
   of one visible thing. A form that saves eight sections at once has to explain which of them failed.

3. **Render by `kind`, not by a switch nobody updates.** `text`, `int`, `bool`, `duration`, `url`,
   `path`, `enum` (a `<select>` from `options`), `multiline`. A setting added to T39's registry in
   phase 8 appears here with no change to this file — that is the point of the registry, and it is
   how playback's settings arrive as one entry rather than a screen.

4. **A secret is an empty input, always.** Its label says `configured` or `not set`; its placeholder
   says leaving it blank keeps what is there; and clearing is an explicit control that submits `""`,
   never an empty box that erases on save. Nothing is pre-filled, because nothing was sent — there is
   no masked value to render and there must not be a placeholder that looks like one.

5. **A field the environment owns is disabled and says which variable owns it.** `source: "env"` →
   read-only, with "set by `TMDB_API_KEY` in the environment" under it. This is the shadow case, and
   it is the difference between a screen that ignores what you typed and a screen that told you it
   would.

6. **Pending changes get a banner and a per-field badge.** When any `pending` is non-null: "Saved.
   Restart curator for these to take effect", listing the keys. Per field, the live value and the
   saved one, side by side. For a secret, the badge alone — that it changed is reportable, what it
   changed to is not.

7. **Validation errors land under their fields.** T40 answers `400` with a `fields` map; render each
   message where it belongs and leave the form's values alone so nothing typed is lost. A `409` is
   the environment case and renders as the same sentence as the disabled state, because it is the
   same fact arriving late.

8. **The VPN field is a monospace textarea with a wg-quick skeleton as its placeholder**, and a
   sentence saying the file is what the provider hands you and it is stored encrypted. This is the
   field [T37](T37-tunnel.md) step 1 promised and the one that makes `docker run` with no `.env`
   possible.

9. **The Access section warns before it switches.** Password, confirm, and: there is no TLS, so this
   raises the bar against a browser on the LAN and does not protect against something reading the
   network; and the recovery, in monospace — `AUTH_ENABLED=false` in the environment beats what is
   stored. Somebody who locks themselves out will not be reading `docs/`.

10. **Login is a component in the layout, not a route.** `<Gate>` asks `GET /api/auth` once; when
    authentication is required and this browser is not authenticated it renders the login form in
    place of the page. `api.ts`'s `request()` gains one module-level `onUnauthorized` hook so a `401`
    mid-session flips the same state.

    A route would need a redirect, a `?next=` parameter and a way back — and
    [D21](../decisions.md#d21--the-movie-page-is-movieid-because-the-ui-is-a-static-export) is the
    standing evidence that routes cost more than components in a static export. A component means a
    deep link like `/movie/?id=299536` asks for a password and then *is that page*, with no
    round trip to lose the query string.

11. **`web/lib/api.ts` gains four calls and their types**: `settings()` extended, `updateSettings`,
    `authStatus`, `login`. `credentials: 'include'` on every request, or the cookie never travels
    when the UI is served from a different origin in development.

## Do not

- Render a secret, a masked secret, a length, or a placeholder shaped like one.
- Pre-fill a secret input, or clear one on an empty save.
- Offer `DB_PATH`, `PORT`, `LOG_LEVEL` or the secret key. They are not in the registry and the screen
  is generated from it; if one appears, T39 is wrong, not this.
- Poll `/api/settings` on a timer. It is a form, not a dashboard.
- Probe automatically on load. Unchanged from phase 5: one of those calls can wake a browser.
- Add a settings state manager, a context provider or a form library. Five sections of controlled
  inputs is `useState`.
- Turn `typescript.ignoreBuildErrors` on. Next 16's checker found a real bug on phase 5's first
  build; it stays honest.

## Verify

`npm --prefix web run build` type-checks clean, and then **driven in a browser**, because this is the
half of the phase a test genuinely cannot cover — phase 5's "not verified: how it looks" is the
standing lesson:

- every kind renders, edits and saves; a bad duration shows its message under its own field and
  nothing else moves
- a secret shows `configured`, saves a new value, and the reloaded page still shows only `configured`
- **the network panel carries no secret in any response** — the check that matters, made with the
  tool that would find it
- a field the environment owns is disabled and names the variable
- pending: save something, see the banner and the two values, restart, see them agree
- the VPN textarea accepts the real NordLynx `.conf`, and a `.conf` with `PrivateKey` deleted is
  rejected inline with the parser's own message
- turn authentication on: the next navigation asks for a password **without a restart**; a wrong one
  says so; a right one lands on the page that was asked for
- a deep link to `/movie/?id=299536` while logged out asks for a password and then shows that film,
  with the query string intact
- log in, restart curator, reload: still logged in
- light and dark both legible, including the disabled fields and the pending badges

## What it shipped, beyond the sketch above

**`<Gate>` holds the pending render by not mounting it.** Step 10 asked for a component and left
open how it keeps the page it is standing in front of. The answer is the cheapest one available: a
locked gate returns the login form *instead of* `children`, so the page is never mounted, never
fetches into a 401, and never navigates — the URL in the bar is still the one the deep link asked
for. React unmounts the subtree on the way in and mounts it fresh on the way out, so every screen
re-fetches after a login with no cache to invalidate and no generation counter to bump. A `key`
bump was written and then deleted: it was a no-op, because a component that was removed from the
tree remounts anyway.

**`request()` excludes the login endpoint from `onUnauthorized`.** Its 401 is "wrong password",
which belongs under the password box on a form that is already showing; routing it through the gate
resets the form somebody is mid-way through typing into. Found by driving it — the first version
cleared the box on every wrong attempt.

**`settingBody` gained `file_env`, which is a change to [T40](T40-settings-api.md)'s file.** `env`
alone renders "set by `VPN_CONFIG`" — and `VPN_CONFIG_FILE` is the variable that is actually set,
in the developer's own `.env` and in the live run this was verified against. The screen would have
named a variable that is not set and told somebody to unset it. `envOwner()` already names both in
the 409 that arrives late, and step 7 says the disabled state and the 409 are the same sentence, so
the payload had to be able to say it. Additive, `omitempty`, asserted both ways in
`internal/api/settings_test.go`.

**An empty box means different things for a secret and for everything else, and both are right.**
For a normal setting the box shows the value, so emptying it is the ordinary way to remove one and
it sends `""`. For a secret there is nothing in the box to empty — so an empty one is *untouched*,
a typed-then-deleted key is untouched, and clearing is an explicit control that disables the input
and says "will be cleared when this section is saved" with an undo. Without that split, opening the
page and pressing Save would erase every secret in the section.

**The input shows the SAVED value, not the running one.** `pending ?? value`, with the running value
named in words underneath. Snapping back to what the process is using would make every save look
like it had been discarded, which is the failure that would make D29's "restart to apply" unusable.

**A bool sends `"true"`/`"false"` and never `""`.** `VPN_REQUIRED` defaults to `true`, so unticking
it has to *store* `false`; clearing it would restore the default and turn the switch back on. That
is also why a checkbox and a select get the explicit clear control and a text box does not — they
have no empty state to type, so clearing is the only way back to a default.

**The label is the environment variable.** Not a humanised key: `.env`, the docs, the validation
messages and the "set by …" sentence all name the variable, and a prettier second name for it would
be the second vocabulary this phase exists to prevent. It also means a setting added in phase 8
needs no label written anywhere.

**Every kind is a text input except the four that cannot be.** `type="number"` turns anything
unparseable into `""`, which this form reads as a clear — so a typo would silently erase a setting.
`type="url"` raises a native tooltip that preempts the server's message, which is the message the
next start-up would print and the one worth reading. Only `password`, `bool`, `enum` and
`multiline` get their own control.

**Verified live on 8097**, against a scratch database, driven in a browser:

- every kind rendered and saved; `TORRENT_STALL_AFTER: "banana"` answered
  `TORRENT_STALL_AFTER "banana": not a duration (want e.g. "30s", "1h")` under its own field, with
  the good `DOWNLOAD_POLL_INTERVAL` in the same section kept and nothing written
- a `.conf` with `PrivateKey` deleted was refused inline with `vpn config: [Interface] PrivateKey is
  missing` — the parser's own sentence, and proof the 20-character redaction floor leaves the
  explanation intact
- a secret saved, the reloaded page showed only `configured`, and the box stayed empty
- `grep -c` of the typed TMDB key and of the pasted WireGuard private key in `GET /api/settings` and
  in the log tail: **0** for all four. The table held `enc.v1.…` for both and `$2a$10$…` for the
  password
- `VPN_CONFIG_FILE` set: the field was disabled and read "set in the environment by `VPN_CONFIG`
  (or `VPN_CONFIG_FILE`)" — the case `file_env` exists for
- `TORRENT_MAX_CONNS` drew an empty box, not `0`
- pending: saved, saw the banner and both values, restarted, saw them agree and the banner gone
- turned authentication on from the form; the **next navigation** asked for a password with no
  restart. A wrong one said "wrong password" without resetting the form; the right one landed on
  `/movie/?id=299536` with the query string intact
- logged in, killed and restarted curator, reloaded: still logged in
- light and dark both legible, disabled fields and pending badges included

**Not verified: the real NordLynx `.conf` through the textarea.** It was driven with a structurally
identical wg-quick file carrying random keys, because pasting the live private key into a throwaway
database is a copy of a credential that buys nothing — `vpn.ParseConfig` is the same function that
already reads the real file on every start.
