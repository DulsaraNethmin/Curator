# T40 — settings becomes read/write

**Owns:** `internal/api/settings.go`, the settings wiring in `cmd/curator/main.go`
**Depends on:** T39

## Goal

`GET /api/settings` grows the fields a form needs and loses none of the ones five phases verified.
`PUT /api/settings` writes. No secret travels outward, in either direction, ever.

## Do

1. **`GET` is additive.** `version`, `probed`, `integrations`, `paths` and `intervals` keep their
   exact shapes — phase 5 verified them and T42's own screen still reads `integrations`. A new
   `settings` array carries one entry per registry row:

   ```json
   {
     "key": "torrent_backend",
     "group": "downloads",
     "kind": "enum",
     "options": ["embedded", "qbittorrent"],
     "secret": false,
     "value": "embedded",
     "configured": true,
     "source": "env",
     "editable": false,
     "pending": null,
     "restart_required": true
   }
   ```

2. **`value` is absent for every secret. Always.** Not masked, not truncated, not a length, not a
   prefix. `configured` is the only fact ([D17](../decisions.md#d17--settings-is-read-only-and-the-settings-table-stays-unused)
   survives phase 7 with its threat model intact — only its conclusion about the environment does
   not). A masked secret still confirms a length and an existence to anyone on the LAN, and until
   T41 is switched on there is still nobody in front of this endpoint.

3. **`source` and `editable` are how the shadow case stops being a silent failure.** A value the
   environment sets is `source: "env"` and `editable: false`, and T42 renders it read-only naming the
   variable. Without this, a stored value under a set variable is typed, saved, ignored, and
   unexplainable from the screen.

4. **`pending` is the honest half of "restart to apply".** When a stored value differs from the one
   this process is running on, report both: `value` is what is live, `pending` is what is saved
   ([D29](../decisions.md#d29--a-written-setting-applies-at-the-next-start-the-password-applies-at-once)).
   `pending` is `null` for a secret exactly as `value` is — that a secret has changed is reportable,
   what it changed to is not.

5. **`PUT /api/settings` takes a partial object and applies it in one transaction.** A key that is
   absent is left alone; a key present as `""` is cleared. Decode into `map[string]json.RawMessage`
   or pointers — never a plain struct, whose zero values arrive looking like eight instructions to
   erase.

   | Case | Answer |
   |---|---|
   | all fields valid | `200` with the same body `GET` would return |
   | any field invalid | `400`, **nothing written**, `{"error": …, "fields": {"search_timeout": "…"}}` |
   | an unknown key | `400` naming it — not ignored, because a typo that silently does nothing is the worst outcome for a settings screen |
   | a key the environment owns | `409` naming the variable. Not `400`: the value was fine, the destination was not |
   | a key that is not stored at all (`DB_PATH`) | `400`, and it is not in the registry to begin with |

   The `fields` map is new and additive; the top-level `{"error": …}` envelope is phase 1's and
   stays, so every existing client keeps working.

6. **Validation is T39's, called from here.** No parsing in the handler. The message a screen shows
   for a bad duration is the message the next start-up would have printed, because they are the same
   function.

7. **The write path is silent about values.** No debug log of the body, no error that wraps it, no
   `slog.Any("settings", body)`. Log the keys that changed and their count. The scrubber only knows
   secrets it was handed at start-up, and the one being typed is by definition not one of them.

8. **`auth_password` and `auth_enabled` are writable here but take effect immediately**, through the
   holder T41 owns. This handler writes them and calls one method; it does not implement
   authentication. Setting `auth_enabled: true` with no password ever set is a `400` — the one
   ordering mistake that locks somebody out of their own library.

9. **`cmd/curator` passes a writer in, not a store.** `WithSettings` keeps taking a plain struct
   built by main; it gains a `Writer` interface with one method. `internal/api` still knows nothing
   about where settings come from, which is the property that has kept this package testable against
   fakes since phase 1.

   Two things main owns here, because both are wiring and neither is a handler:

   - **The effective value of every setting** comes off `*config.Config`, as a map literal beside
     the `paths` and `intervals` ones that already exist. It is explicit and it is boring, and it
     cannot drift from what the process is running because it is what the process is running —
     which is why T39's registry holds no defaults to disagree with it.
   - **The indexer toggles have to be honoured by somebody**, and it is the line that builds the
     aggregator. `INDEXER_YTS`, `INDEXER_TPB` and `INDEXER_1337X` are stored settings with nobody
     reading them until `indexer.NewAggregator` is given only the enabled ones. Found while
     building T39: the phase says the indexers are configurable and no task owned the half that
     makes a toggle do anything. Default is all three on, so an unset value keeps today's
     behaviour, and disabling 1337x is also what stops minter being probed for a service nobody
     asked for.

## Do not

- Return a secret. Assert it in a test against the **raw JSON**, not against a typed struct, so a
  field added in phase 8 cannot leak one past a test that only knows today's shape.
- Restart the engine, the tunnel or the poller from a handler. D29 — and phase 6's shutdown order is
  a comment in `main` for a reason.
- Rename, remove or reshape a field in the existing `GET` response.
- Accept a write to a key the registry does not carry, or to one the environment owns.
- Apply half a form. One transaction, or nothing.
- Put the probe behind the write. `?probe=1` is unchanged and still opt-in — it calls out to real
  services and one of them can wake a browser.

## Verify

Hermetic:

- `GET` with no stored settings answers exactly what it answers today, plus the new array —
  a golden-ish assertion on the five existing top-level fields
- **no secret in the raw JSON**: set all four, `GET`, and assert each plaintext appears zero times in
  the response bytes
- `source` is `env`, `stored` and `default` in the three cases, and `editable` is false for the first
- `pending` appears only when stored and live differ, and is `null` for a secret that differs
- **partial update**: absent leaves alone, `""` clears, and the other fields are untouched — read
  them back and assert
- one invalid field → `400`, `fields` names it, and **nothing was written** (read the store back)
- an unknown key → `400` naming it; an environment-owned key → `409` naming the variable
- `auth_enabled: true` with no password → `400`
- a write logs the changed **keys** and no value — assert against the log buffer, which is right
  there and is exactly the thing that would carry the leak

Then live, on the laptop:

- `PUT` a TMDB key into a database that has none, restart, and the library matches — the first
  setting that has only ever come from `.env`
- `curl -s localhost:8090/api/settings | grep -c "$THE_KEY_YOU_JUST_SET"` → **0**
- `sqlite3 curator.db 'select value from settings where key="tmdb_api_key"'` → `enc.v1.…`
