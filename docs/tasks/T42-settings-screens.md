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
