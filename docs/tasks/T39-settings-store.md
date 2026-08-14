# T39 — the settings store

**Owns:** `internal/settings/` — `registry.go`, `resolve.go`, `secret.go` and their tests;
`internal/store/settings.go`; `internal/store/migrate.go`; a second constructor on
`internal/config`
**Depends on:** —

## Goal

A setting can be written down, read back, and resolved against the environment — and a secret
written down is ciphertext on disk. Nothing above this task learns that a value could have come from
a database.

No handler, no screen and no behaviour change. This is the layer T40, T41 and T42 stand on, and it
is a commit of its own so that a bisect lands on "the store" rather than on "settings".

## Do

1. **The registry is one table, in code.** Every setting is one entry: stored key, environment
   variable, kind, secret or not, validator, default, and which group the screen puts it in. The
   catalogue is in [phase-7.md](../phase-7.md#the-settings-catalogue) and this is the only place it
   exists twice — a test asserts the two agree in count, so a setting added to one and not the other
   is a failing test rather than a field nobody can save.

   **The stored key is the lower-case environment variable**: `TMDB_API_KEY` ↔ `tmdb_api_key`. Not a
   convention to remember — assert it, for every entry, in a test. Three settings have no variable
   today (`INDEXER_YTS`, `INDEXER_TPB`, `INDEXER_1337X`) and get one invented rather than an
   exception carved out.

2. **`internal/store/settings.go` handles opaque strings.** `Setting(ctx, key)`,
   `Settings(ctx) map[string]string`, `SetSettings(ctx, map[string]string) error` — one transaction,
   all or nothing — and `DeleteSetting`. It does not know what a secret is, what a duration is, or
   which key is which. `internal/store`'s package comment says it knows about rows and nothing else;
   keep that true.

3. **Resolution is a function, and it reports its source.**

   ```
   environment variable, if set   →   stored setting, if present   →   default
   ```

   `Resolve(stored map[string]string) (values map[string]string, sources map[string]Source)`, where
   `Source` is `env`, `stored` or `default`. The source is not a nicety: T42 renders a shadowed field
   read-only, and without it somebody types a TMDB key into a screen that silently ignores it because
   `.env` sets one. Test all three, and test the shadow case explicitly.

4. **`config` gains a second constructor and keeps its first.** `config.Bootstrap()` reads the
   environment-only settings — `DB_PATH`, `PORT`, `LOG_LEVEL`, `LOG_BUFFER_LINES`, `SECRET_KEY`,
   `SECRET_KEY_FILE` — because they are needed *before* there is a database to read the rest from.
   `config.Load(stored)` then produces the same `*Config` every caller already takes, with the same
   parsers and the same error messages. `config.Load(nil)` is today's behaviour exactly, which is
   what keeps the existing tests meaningful rather than rewritten.

5. **Validate on the way in, with the parsers `Load` already uses.** A duration, an int with its
   range, a bool, a URL, an enum, and `vpn.ParseConfig` for the tunnel's `.conf`. The error message
   is the one start-up would have produced — `TORRENT_BACKEND "qbit": want "embedded" or
   "qbittorrent"` — because a settings screen that accepts a value the next boot rejects is a
   settings screen that bricks a container.

   That means `internal/settings` imports `internal/vpn` for one function. Cheaper than a second
   parser that disagrees with the real one about which fields are required.

6. **Secrets are encrypted; the password is hashed.** AES-256-GCM, a fresh nonce per value, the
   setting's key as additional authenticated data, written as `enc.v1.<base64(nonce||ciphertext)>`.
   The AAD is what stops a ciphertext being moved from one row to another inside the database.

   `auth_password` is bcrypt and never decryptable, because curator only ever compares it. A VPN key
   has to be *used*, so it has to be readable; a password does not, so it must not be. That
   distinction is the design, not an implementation detail — say it in the code.

7. **The key is a file beside the database**, `curator.key`, mode `0600`, written with
   `O_CREATE|O_EXCL` so two processes racing cannot both think they made it. `SECRET_KEY` inline
   overrides it — the same pair of shapes `VPN_CONFIG`/`VPN_CONFIG_FILE` already has, for the same
   reason: a provider hands you a file and a container hands you a variable.

   **Generate only when there is nothing to lose.** If any `enc.v1.` value is present and the key is
   absent, do not generate: start, log it loudly, and report those fields as unreadable. GCM's tag
   makes a wrong key a detectable failure rather than plausible garbage, so this is a real branch and
   not a hope. A silent re-key turns "I restored the database without the key file" from recoverable
   into invisible.

8. **The first migration.** `internal/store/migrate.go`: an ordered list of steps, each idempotent
   and each **checked against the live schema** rather than against a stored version number, run
   after `schema.sql` on every start. Step one is T55's `ALTER TABLE downloads ADD COLUMN reason
   TEXT`, guarded by `pragma_table_info`. A version number is a second source of truth about a shape
   the database can already be asked for.

9. **The scrub list is built from the resolved configuration.** `logs.NewBuffer` is handed the
   secrets in use — environment and stored together — and gains the two [T37](T37-tunnel.md) asked
   for and never got: the tunnel's `PrivateKey` and `PresharedKey`, parsed out of `vpn_config`. No
   known path logs them; phase 7 is where secrets start arriving in request bodies, which is when
   defence in depth stops being theoretical.

   This inverts the start-up order in `cmd/curator`: bootstrap, open the database, read and decrypt,
   *then* build the logger and resolve. `store.Open` returns its errors and logs nothing, which is
   what makes that legal — a test that opens a store with no logger configured keeps it that way.

## Do not

- Put encryption, validation or the registry in `internal/store`. It owns rows.
- Cache the decrypted secrets anywhere but the `*Config` that already holds them. There is exactly
  one copy of `TMDB_API_KEY` in this process today; do not make it two.
- Add a `settings` row for `DB_PATH`, `PORT`, `LOG_LEVEL`, `LOG_BUFFER_LINES` or the secret key.
  Anything needed to reach the settings screen is not settable from the settings screen.
- Version the schema with a number. The database knows its own shape; ask it.
- Log a value being written, at any level, in any error — including one wrapping a validation
  failure. Name the field, never the value.
- Change `config.Config`'s fields or any existing default. Every caller keeps compiling and every
  existing config test keeps meaning what it meant.

## Verify

Hermetic, all of it:

- **round trip** every kind — text, int, bool, duration, url, path, enum, multiline secret — and
  read each back through `Resolve`
- **the four secrets are ciphertext in the table.** Query `settings` directly in the test and assert
  the plaintext appears in no value. Not "assert it starts with `enc.`" — assert the secret is
  absent, so an encoding that silently degrades to plaintext fails
- **the codec**: a wrong key fails; a ciphertext moved between two keys fails on the AAD; the same
  plaintext encrypted twice gives two different ciphertexts, which is the nonce doing its job
- **the key file**: created `0600`, not regenerated when it exists, and **not generated at all** when
  ciphertext is present without it — that case reports unreadable fields and starts
- **precedence**: env beats stored beats default, with the reported source matching in all three, and
  a stored value under a set variable reported `env`
- **partial update**: an absent key is left alone, `""` clears, and one invalid field rejects the
  whole write with nothing partially applied — assert the other seven fields are unchanged
- **validation** produces the message `Load` produces, asserted against each other rather than
  against a literal, so the two cannot drift
- **the registry matches the catalogue** in `phase-7.md`, by count, and every entry's stored key is
  its variable lower-cased
- **the migration, twice**: a fresh database and one created before `downloads.reason` existed both
  end with the column; a third run changes nothing; `PRAGMA table_info` is the assertion
- `config.Load(nil)` behaves exactly as `config.Load()` did — the phase 1–6 config tests, unchanged
