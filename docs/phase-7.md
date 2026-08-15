# Phase 7 — Settings that write

The phase where the `settings` table [T2](tasks/T2-store.md) created and nothing has ever read
finally has a use.

**Done when** the backend, the tunnel, the indexers and Jellyfin are all configurable from the UI;
no secret is ever sent back to a browser; every secret at rest is encrypted; and one password can
gate the whole API.

---

## What phase 6 put on this desk

[D17](decisions.md#d17--settings-is-read-only-and-the-settings-table-stays-unused) held for two
phases on one argument: configuration comes from the environment, so there is nothing to write, and
a screen that cannot write cannot leak. Phase 6 broke the first half of that.

**curator now needs a WireGuard private key.** Not a key it checks — a key it *uses*, on every
boot, to bring up the tunnel every peer byte leaves through. Today it arrives in `VPN_CONFIG` or a
file path, which is fine for a laptop with a `.env` and impossible for phase 9's `docker run` on
somebody else's machine: the whole point of that phase is that a stranger runs one command and
configures the rest in a browser, and the first thing they have to configure is the tunnel. A textarea is what [T37](tasks/T37-tunnel.md) step 1 already promised.

So D17's *threat model* survives intact and its *conclusion about the environment* does not. No
secret is ever sent to a browser — not masked, not truncated, not hashed — and the write direction
is the one that opens ([D28](decisions.md#d28--settings-are-writable-secrets-are-encrypted-at-rest-and-write-only-across-the-api)).
Which is precisely why the same phase adds the password: a page that accepts a WireGuard key and can
change where the importer writes is not a page to leave open to the LAN by default, even though it
still will be by default ([D25](decisions.md#d25--authentication-is-optional-and-off-by-default)).

Phase 6 also left a promise. [T36](tasks/T36-resume-stall.md) step 7 deferred the *reason* a torrent
is stalled to "phase 7, where the store is being touched anyway". It is here, as T55.

---

## Tasks

| Task | Owns | Depends on | State |
|---|---|---|---|
| [T39](tasks/T39-settings-store.md) the settings store | `internal/settings/`, `internal/store/settings.go` | — | specified |
| [T40](tasks/T40-settings-api.md) settings becomes read/write | `internal/api/settings.go`, `cmd/curator` | T39 | specified |
| [T41](tasks/T41-auth.md) optional authentication | `internal/api/auth.go`, `cmd/curator` | T39 | specified |
| [T42](tasks/T42-settings-screens.md) the Settings screens | `web/app/settings/`, `web/lib/api.ts` | T40, T41 | specified |
| [T55](tasks/T55-stall-reason.md) the stall reason reaches the screen | `internal/torrent`, `store`, `download`, `api`, `web` | T39 | specified |

T41 depends on T39 rather than on nothing, as the plan had it: the password lives in the same store
and is signed with the same key, and building a second home for one credential would be the only
part of this phase worth regretting.

**T55 is numbered 55 because the plan reserved T43–T54 for phases 8–10.** A task discovered after
the plan takes the next free number rather than displacing one that other documents already cite.

---

## The shape

```
internal/settings/          the registry, the resolver and the scrub list         T39
internal/secret/            AES-256-GCM, and the key file beside the database     T39
internal/store/settings.go  read and write a key/value table                      T39
internal/store/migrate.go   the first migration, and the rule it sets             T39
internal/api/settings.go    GET grows fields; PUT is new                          T40
internal/api/auth.go        the middleware, the login endpoint, the cookie        T41
web/app/settings/           the form, the sections and the login screen           T42
```

**The codec is its own package, not a file in `internal/settings`.** It turned
out that nothing above `cmd/curator` needs it: main decrypts what the store hands
back and passes plaintext into the resolver, so `internal/settings` never holds a
key and `internal/config` never touches crypto at all. A leaf that imports only
the standard library is also the one part of this phase worth being able to read
in one sitting.

**`internal/settings`, not `internal/store/settings.go` alone.** The plan gave T39 one file in the
store. Storage is the smallest part of it: what phase 7 actually needs is a *registry* — every
setting's key, its environment variable, its kind, whether it is a secret, how it is validated —
plus the rule that resolves the three sources into one value. `internal/store`'s own doc comment
says it "knows about rows and nothing else, and never reads a disk or calls an API"; a package that
owns validation is not that package. The table access stays in `internal/store/settings.go`, where
it belongs, and handles opaque strings.

**The registry deliberately holds no defaults.** The default of `SEARCH_TIMEOUT` is a documented
constant in `internal/config`, next to the reasoning for its value, and a copy in the registry would
be a second answer that drifts. What a screen shows as the current value is what this process
actually resolved, read off `*config.Config` — which cannot disagree with the running program,
because it *is* the running program. `source` says whether that came from the environment, the
store, or the default.

`internal/config` keeps its shape, its job and its invariant — it imports nothing of curator's, so
the parsers cannot pick up a dependency on the things they configure — and gains a second
constructor plus four exported parsers. `internal/settings` validates a write by calling those, so
the message a screen shows is the message the next start-up would print, asserted by a test that
compares the two rather than either against a literal. Nothing downstream of `Load` learns that a
value could have come from a database.

---

## Precedence: the environment wins, the store fills the rest

```
environment variable, if set   →   stored setting, if present   →   default
```

Every existing deployment keeps working, `docker run -e` stays honest, and — the part that is worth
more than either — **the environment is the recovery path.** Lock yourself out with a password you
mistyped and `-e AUTH_ENABLED=false` gets you back in. Point `DOWNLOADS_DIR` at a disk that no longer
exists and one `-e` starts the process that lets you fix it. That falls out of the precedence rule
rather than needing a rescue mode, which is the argument for this order rather than the other one.

It has one sharp edge and the UI has to carry it: **a stored value that the environment shadows is
invisibly ignored.** Type a TMDB key into the settings screen on a machine whose `.env` already sets
`TMDB_API_KEY`, restart, and nothing changes — with no way to tell from the screen why. So every
setting reports its **source** (`env`, `stored`, `default`) and a shadowed field renders read-only,
saying which variable owns it. A settings page that silently drops what you typed is worse than one
that refuses it.

### Every stored setting has exactly one environment variable

The stored key is the lower-case form of the variable: `TMDB_API_KEY` ↔ `tmdb_api_key`. One row per
setting in one registry, so the two ways of configuring curator cannot drift into two vocabularies,
and the three settings that have no variable today get one invented rather than an exception carved
out.

### What cannot be set from the settings screen, and why

`DB_PATH`, `PORT`, `LOG_LEVEL`, `LOG_BUFFER_LINES`, `SECRET_KEY`/`SECRET_KEY_FILE`.

The rule is not a list, it is a sentence: **anything needed in order to reach the settings screen is
not settable from the settings screen.** `DB_PATH` says where the settings live, so it cannot live
there. `PORT` is the address you are asking from. The secret key decrypts the rest, and a key
readable from the store it protects is not a key. `LOG_LEVEL` and the buffer size are read before
the database is open, because the logger has to exist before anything can report failing to open it.

---

## A written setting applies at the next start; the password applies at once

Saving writes to the database. It does **not** rebuild the running process
([D29](decisions.md#d29--a-written-setting-applies-at-the-next-start-the-password-applies-at-once)).

Live-applying is not a bigger feature, it is a different phase. The tunnel and the engine are exactly
the two things phase 6 learned are order-sensitive to tear down — `cancel → wg.Wait → client.Close`,
and the engine strictly before the tunnel or the uTP read loop complains into the log — and swapping
a backend at runtime means rebuilding the dispatcher, the poller and the importer's client under
live requests. Phase 7 would become a lifecycle rewrite wearing a settings screen.

So the screen says so, per field, and shows what is **pending**: the value in the database, next to
the value this process is using, whenever they differ. An honest "restart to apply" beats a
half-applied configuration nobody can diff.

**The password is the exception, and it has to be.** A password that takes effect at the next
restart means saving one leaves you unprotected until then, which is the opposite of what was asked
for. It is read per request, through one atomic holder that the write path updates, so enabling
authentication takes effect on the next request and changing it ends every existing session.

---

## Secrets at rest

Four values in the store are secrets — `tmdb_api_key`, `qbit_pass`, `jellyfin_api_key`, and
`vpn_config`, which carries a private key and a preshared key inside a config file. They are
**encrypted**: AES-256-GCM, a fresh nonce per value, the setting's own key as additional
authenticated data so a ciphertext cannot be moved from one row to another, written as
`enc.v1.<base64>`.

`auth_password` is **hashed**, not encrypted, and the difference is the whole design: curator must be
able to *use* a VPN key, so it must be able to decrypt it; it only ever needs to *compare* a
password, so it must not be able to read it back. bcrypt, and `golang.org/x/crypto` is already in
the module graph via `wireguard-go`, so it costs no new module.

**The key is a file beside the database** — `curator.key`, mode `0600`, generated on first write —
or `SECRET_KEY` inline, the same pair of shapes `VPN_CONFIG`/`VPN_CONFIG_FILE` already uses.

**Be exact about what this buys, because the obvious reading is too generous.** The key file sits in
the same volume as the database, so anything that copies the volume copies both. What encryption
defends against is narrower and real: a `curator.db` pasted into an issue for help, a backup script
that globs `*.db`, a future handler that returns a settings row by accident, and anyone who reads
the file without owning the process. It is not protection against someone who has the machine — they
can read the key, and they can read the process's memory, where the plaintext is anyway.

The failure that matters is a database restored without its key. GCM's tag makes a wrong key a
detectable failure rather than garbage, so the field reports itself **unreadable** and asks to be
re-entered; the process starts, and the integration behaves as unconfigured. A new key is generated
**only** when there is no ciphertext to fail against — silently re-keying over unreadable secrets
would turn a recoverable mistake into a silent one.

---

## The settings catalogue

Every row is stored, has one environment variable, and takes effect at the next start unless noted.

| Group | Key / variable | Kind | |
|---|---|---|---|
| Library | `library_movies` · `LIBRARY_MOVIES` | path | |
| | `tmdb_api_key` · `TMDB_API_KEY` | **secret** | |
| Downloads | `torrent_backend` · `TORRENT_BACKEND` | enum | `embedded` \| `qbittorrent` |
| | `downloads_dir` · `DOWNLOADS_DIR` | path | embedded only |
| | `torrent_max_conns` · `TORRENT_MAX_CONNS` | int | 1–500 |
| | `torrent_port` · `TORRENT_PORT` | int | 0–65535 |
| | `torrent_stall_after` · `TORRENT_STALL_AFTER` | duration | |
| | `download_poll_interval` · `DOWNLOAD_POLL_INTERVAL` | duration | |
| qBittorrent | `qbit_url` · `QBIT_URL` | url | |
| | `qbit_user` · `QBIT_USER` | text | |
| | `qbit_pass` · `QBIT_PASS` | **secret** | |
| | `qbit_category` · `QBIT_CATEGORY` | text | |
| | `downloads_path` · `DOWNLOADS_PATH` | path | empty is meaningful |
| | `qbit_downloads_path` · `QBIT_DOWNLOADS_PATH` | path | |
| VPN | `vpn_config` · `VPN_CONFIG` | **secret**, multiline | a wg-quick `.conf` |
| | `vpn_required` · `VPN_REQUIRED` | bool | default `true` |
| | `vpn_ip_check_url` · `VPN_IP_CHECK_URL` | url | |
| Indexers | `indexer_yts` · `INDEXER_YTS` | bool | **new** |
| | `indexer_tpb` · `INDEXER_TPB` | bool | **new** |
| | `indexer_1337x` · `INDEXER_1337X` | bool | **new**; default `false` since T49 — the other two need nothing, this one needs a container |
| | `minter_url` · `MINTER_URL` | url | |
| | `search_timeout` · `SEARCH_TIMEOUT` | duration | |
| | `search_cache_ttl` · `SEARCH_CACHE_TTL` | duration | |
| Playback | `ffmpeg_path` · `FFMPEG_PATH` | path | **phase 8**; empty = look on `PATH`, not found = remux off |
| | `playback_target` · `PLAYBACK_TARGET` | enum | **phase 9**; `browser` \| `jellyfin`, empty = not yet asked |
| Jellyfin | `jellyfin_url` · `JELLYFIN_URL` | url | |
| | `jellyfin_api_key` · `JELLYFIN_API_KEY` | **secret** | |
| | `jellyfin_public_url` · `JELLYFIN_PUBLIC_URL` | url | **phase 8**; empty = use `jellyfin_url` |
| Access | `auth_enabled` · `AUTH_ENABLED` | bool | default `false`; **applies at once** |
| | `auth_password` · `AUTH_PASSWORD` | **write-only, hashed** | applies at once |

**Playback was not in this table when phase 7 shipped**, and the plan's "done when" said it should
be. Playback settings did not exist until phase 8 built the thing they configure, and inventing a
"prefer direct play" toggle then would have been a row nothing reads. The registry is what phase 7
owed phase 8, and the debt came due exactly as promised: `ffmpeg_path` arrived with
[T44](tasks/T44-remux.md) as **one row above**, not a screen. There is still no "prefer direct play"
toggle, for the original reason — direct play is always tried first and the browser decides.

---

## The first migration, and the rule it sets

Five phases have shipped with no migration, on a real argument: `schema.sql` is all `IF NOT EXISTS`,
so applying it every start is a no-op, and every column phases 3–6 needed already existed. T55 needs
one that does not — `downloads.reason` — and `CREATE TABLE IF NOT EXISTS` silently does nothing to a
table that is already there.

So `internal/store/migrate.go` arrives: an ordered list of steps, each **idempotent and checked
against the live schema** rather than against a version number, run after `schema.sql` on every
start. `ADD COLUMN` guarded by `pragma_table_info`. Thirty lines.

The reason for doing it now, rather than in phase 9 where it becomes unavoidable: **from phase 9
there are databases this repo has never seen.** "We have never needed a migration" is a statement
about a project with one user, and it stops being true the week the image is public. Better to
introduce the mechanism with one nullable column that a test can prove twice — once on a fresh
database, once on a database created before the column existed — than to introduce it under
pressure with something that matters.

---

## What must not change

- **No secret reaches a browser.** D17's actual argument, unweakened: not masked, not truncated, not
  its length, not a prefix. `configured: true` is the whole of what a screen may know.
- **`GET /api/settings` grows fields and changes none.** `version`, `probed`, `integrations`,
  `paths` and `intervals` keep their shapes — phase 5 verified them and T42's own screen reads them.
- **Authentication is off by default.** curator on a LAN behaves exactly as it does today, for
  everybody who does not go looking for the switch.
- **The environment keeps winning.** Anything else and a container's `-e` becomes a suggestion.
- **The engine, the tunnel and the poller are not restarted from a request.** D29.
- **`internal/store` still knows about rows and nothing else.** No encryption in it, no validation in
  it, no registry in it.
- **Nothing on the Pi.** Phase 10, after T52.

---

## The traps, named before anyone hits them

**The log buffer's scrub list is built at start-up, and it is short by one.**
`logs.NewBuffer(lines, TMDBAPIKey, QBitPass, JellyfinAPIKey)` — [T37](tasks/T37-tunnel.md) said to
hand it the tunnel's keys too and it was never done. No known path logs them, so this is defence in
depth rather than a leak, but phase 7 is where secrets start arriving in **request bodies** and the
gap stops being theoretical. The list is built from the *resolved* configuration, environment and
stored together, and it gains the private key and the preshared key parsed out of `vpn_config`.

**A write path must never log its own body.** Not at debug, not in an error wrapping the request.
The scrubber is a second line of defence and it only knows values it was handed at start-up — the
key you are typing right now is by definition not one of them.

**Start-up order inverts.** Today: read the environment, build the logger, open the database. From
T39: read the environment-only settings, open the database, read and decrypt the stored ones, then
build the logger with the full secret list and resolve the rest of the configuration. `store.Open`
logs nothing and returns its errors, which is what makes that order legal — check it stays that way.

**`""` and absent are different, and JSON hides it.** A settings write is a partial update: a key
that is absent is left alone, and a key present as `""` is *cleared*. Decode into a map or into
pointer fields, never into a plain struct where the zero value of every field arrives looking like
an instruction to erase it.

**Validate with the same parsers `config.Load` uses, at write time.** A duration that does not parse,
a `torrent_backend` that is neither value, a `.conf` with no `PrivateKey` — all of these are already
errors at start-up, and discovering them at the *next* start is how a settings screen bricks a
container. `vpn.ParseConfig` exists and is exactly the validator the VPN textarea needs.

**A settings write is one transaction.** A form with eight changed fields either applies all eight or
none. Half a configuration is not a state anything downstream reasons about.

**bcrypt costs real milliseconds on a Pi**, which is the point of it, and it is enough to make the
login endpoint a denial-of-service lever if it can be hit in parallel. Failed attempts serialise
through one mutex with a fixed delay, so the endpoint answers at most one wrong password per second
however many connections ask at once.

**There is no TLS, and a password over plain HTTP is a password in clear on the LAN.** Say it in the
UI where the password is set, not only in a decision record. This raises the bar against a browser
on the network; it is not protection against something reading the network.

---

## Configuration this phase adds

Environment only — the settings that describe where settings live cannot themselves be settings.

| Variable | Default | Means |
|---|---|---|
| `SECRET_KEY` | unset | the encryption key, base64, inline |
| `SECRET_KEY_FILE` | `<dir of DB_PATH>/curator.key` | the same key as a file; generated `0600` on first write |
| `AUTH_ENABLED` | unset | overrides the stored value — the lockout escape hatch |
| `AUTH_PASSWORD` | unset | ditto, and compared without ever being hashed or stored |

---

## Verification

Per commit, as ever:

```bash
make check      # npm export, go build, go vet, go test -race, arm64 cross-compile
```

Hermetic, and most of this phase is:

- **round trip**: write every kind — text, int, bool, duration, url, path, enum, multiline secret —
  read every one back, and confirm the four secrets are ciphertext in the table and never plaintext
- **the codec**: a wrong key fails to decrypt rather than returning garbage; a ciphertext moved
  between two rows fails, because the key name is the additional data
- **precedence**: env beats stored beats default, and the reported `source` matches, for all three
- **the shadow case**: a stored value under a set environment variable is reported `env` and the
  field is not editable
- **partial update**: absent leaves alone, `""` clears, one invalid field rejects the whole write and
  names the field
- **validation**: a bad duration, an unknown backend and a `.conf` with no `PrivateKey` are all 400
  at write time, with the same message `config.Load` would have produced at the next start
- **no secret in any response**: assert against the raw JSON of `GET /api/settings`, so a future
  field cannot leak one past a typed test
- **auth off** (the default): every route behaves exactly as it does today
- **auth on**: `/api/*` answers 401 without a cookie; `/healthz`, the login endpoint and the static
  UI do not; a correct password sets a cookie and the same request then passes; a wrong one is 401,
  delayed, and logged without the attempt in it
- **the lockout escape**: `AUTH_ENABLED=false` in the environment beats a stored `true`
- **changing the password ends existing sessions** — the old cookie is 401 on the next request
- **the migration, twice**: on a fresh database, and on one created before `downloads.reason`
  existed; running it twice more changes nothing
- **T55**: a torrent with no peers reports `stalled` *with* the reason in `GET /api/downloads`, and
  the reason clears when it starts moving

Then driven for real on the laptop, against the running binary:

- configure a TMDB key **from the UI on a database with none**, restart, and confirm the library
  matches — the first setting that has only ever come from `.env`
- paste the NordLynx `.conf` into the textarea on a process started with no `VPN_CONFIG` at all,
  restart, and confirm `vpn tunnel up` and an exit address that differs from the host's
- confirm the stored `vpn_config` is unreadable in `sqlite3 curator.db 'select * from settings'`
- turn authentication on from the UI, confirm the browser is asked to log in on the next request
  **without a restart**, and confirm `curl` with `Authorization: Basic` still works
- `curl -s localhost:8090/api/settings | grep -c <the key you just typed>` → **0**

---

## Out of scope

- **Live reconfiguration.** [D29](decisions.md#d29--a-written-setting-applies-at-the-next-start-the-password-applies-at-once).
  A restart endpoint is phase 9's to consider, where a container has a supervisor that would restart
  the process rather than leave it exited.
- **Users, roles, sessions in the database, OAuth, TLS.** One password, one cookie, off by default
  ([D25](decisions.md#d25--authentication-is-optional-and-off-by-default)). Anything more is a
  product decision nobody has asked for and a surface nobody has to maintain.
- **Playback settings.** Phase 8 builds the thing they configure.
- **Fetching minter when 1337x is enabled.** Phase 9, T49 — the toggle this phase stores is what it
  hangs off, and the Docker-socket cost it carries is
  [D23](decisions.md)'s to record, not this phase's.
- **A first-run wizard.** T51. What phase 7 owes it is that every value it would ask for is
  writable, which is this phase's whole content.
- **Secrets in an external manager** — Vault, Docker secrets, a KMS. `SECRET_KEY` inline is the seam
  that makes any of them a one-line integration later.
