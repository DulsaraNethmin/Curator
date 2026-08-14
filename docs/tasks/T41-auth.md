# T41 — optional authentication

**Owns:** `internal/api/auth.go` and its tests, the middleware mount in `cmd/curator/main.go`
**Depends on:** T39

## Goal

One password, off by default, in front of `/api/*`. It exists because
`DELETE /api/movies/{id}` removes files from disk
([D19](../decisions.md#d19--deleting-a-movie-removes-the-file-and-asks-qbittorrent-to-remove-its-own)),
`/api/logs` is plaintext by a design that assumed nobody untrusted could reach it
([D18](../decisions.md#d18--the-log-tail-is-readable-without-authentication-so-it-is-redacted-at-the-source)),
and from this phase `PUT /api/settings` accepts a WireGuard private key.

**Off by default** ([D25](../decisions.md#d25--authentication-is-optional-and-off-by-default)).
Nothing changes for anyone who does not go looking for the switch.

## Do

1. **One password. No users, no roles, no registration.** There is one household and one library.
   A username field would be a second thing to forget for no second guarantee.

2. **bcrypt.** `golang.org/x/crypto` is already in the module graph via `wireguard-go`, so this costs
   no new module — only a move from indirect to direct. Default cost; measure it on the Pi at
   phase 10 and write the number down rather than tuning it against a laptop.

3. **Two ways to present it, because both are already how curator is used.**

   | | |
   |---|---|
   | `Authorization: Basic` | any username, the password. Every verification block in `docs/` is `curl`; an API that cannot be curled would make the next phase's evidence harder to gather than the phase |
   | `Cookie: curator_session` | what the browser gets after `POST /api/auth/login` |

4. **The cookie is signed, not stored.** HMAC-SHA256 over an expiry, keyed by the secret T39 owns
   mixed with the current password hash. No session table, no map to evict, and it survives the
   restart that D29 makes routine — you are not logged out every time you change a setting.

   **Mixing in the password hash is what makes a password change end every session**, for free and
   without state. Say so in the comment; it reads like an accident otherwise.

   `HttpOnly`, `SameSite=Lax`, `Path=/`, 30 days. **Not `Secure`** — there is no TLS, and a `Secure`
   cookie over plain HTTP is a cookie the browser silently never sends.

5. **What is protected, and what is deliberately not.**

   | | |
   |---|---|
   | `/api/*` | protected, all of it |
   | `GET /healthz` | open — a container healthcheck has no credentials, and it answers "is the process up" and nothing else |
   | `POST /api/auth/login` | open, or nobody can ever log in |
   | `GET /api/auth` | open: `{"required": true, "authenticated": false}`. The UI has to know which screen to draw before it has a cookie |
   | the static UI at `/` | open. It is markup with no data in it; every fact it displays comes from an endpoint that is protected. Gating it would only mean maintaining a second login path for a bundle anyone can read in the public image anyway |

6. **The password is read per request, through an atomic holder.** Enabling authentication applies to
   the next request, not the next restart — a password that takes effect in an hour is not a password
   ([D29](../decisions.md#d29--a-written-setting-applies-at-the-next-start-the-password-applies-at-once)
   makes this the one exception, and it is the reason there is an exception at all). T40's write path
   calls one method on the holder; nothing else in the process mutates it.

7. **Failed attempts serialise.** One mutex, a fixed delay, so the endpoint answers at most one wrong
   password per second no matter how many connections ask at once. bcrypt costing real milliseconds
   on a Pi is the point of bcrypt and it is also a denial-of-service lever if it can be hit in
   parallel; serialising fixes both at once. Per-IP counters are the alternative and they are worse
   here: a LAN behind one NAT is one IP, and locking out the household because a script had a bad
   day is a self-inflicted outage.

8. **The escape hatch already exists and costs nothing.** `AUTH_ENABLED=false` in the environment
   beats a stored `true`, and `AUTH_PASSWORD` in the environment beats a stored hash — because
   T39's precedence rule says the environment wins, everywhere, for everything. There is no rescue
   mode to build, and the recovery instruction is one `-e`. Write it in the UI next to the switch,
   not only here.

9. **Log every failure, name no attempt.** `auth failed` with the remote address, never the password
   tried — a log tail readable over HTTP is exactly where a mistyped-into-the-username-field password
   would end up ([D18](../decisions.md#d18--the-log-tail-is-readable-without-authentication-so-it-is-redacted-at-the-source)).
   Note that with authentication on, `/api/logs` is finally behind something, which is what D18 said
   it was missing.

## Do not

- Turn it on by default, or nag about it being off. LAN-only is a supported posture and it is the one
  every existing install has.
- Claim it is protection against the network. There is no TLS: the password crosses the LAN in clear
  on every Basic-auth request. It raises the bar against a browser on the network — a housemate, a
  guest, a device that should not be deleting films — and the UI says exactly that where the password
  is set.
- Store a session server-side, invent a refresh token, or add a second credential for scripts. One
  password, one signature.
- Protect `/healthz`. A healthcheck with a credential in it is a credential in a `docker inspect`.
- Gate the static UI, or serve a different bundle when authentication is on.
- Let a wrong password and an unknown route answer differently in a way that maps the API. A
  protected route with no credential is `401` whether or not it exists.

## Verify

Hermetic, and all of it can be:

- **off by default**: with nothing configured, every route behaves exactly as it does today. Run the
  existing `internal/api` suite through the middleware to prove it, rather than asserting it once
- **on**: `/api/movies` is `401` with no cookie and no header; `/healthz`, `POST /api/auth/login`,
  `GET /api/auth` and `/` are not
- correct password → `200` and a `Set-Cookie`; the same request with that cookie → `200`
- correct password via `Authorization: Basic`, any username → `200`, no cookie needed
- wrong password → `401`, and the response is not distinguishable from a wrong password for a user
  that does not exist, because there are no users
- **changing the password invalidates the old cookie** — the signature no longer verifies, and the
  next request is `401` without anything having been evicted
- an expired cookie is `401`; a cookie with a tampered expiry is `401` and the tamper is what the
  HMAC catches
- `AUTH_ENABLED=false` in the environment beats a stored `true`; `AUTH_PASSWORD` beats a stored hash
- `auth_enabled: true` with no password is refused by T40 before it can lock anybody out
- concurrent wrong passwords serialise: twenty at once take twenty delays, not one
- the log buffer holds `auth failed` and **not** the attempted password — assert against the buffer's
  contents, which is the place the leak would actually appear

Then live, on the laptop:

- turn it on from the UI, watch the browser be asked to log in **without a restart**
- log in, restart curator, and confirm the session survives — the cookie is signed, not stored
- `curl -u :thepassword localhost:8090/api/movies` works, and the same without `-u` is `401`

## What it shipped, beyond the sketch above

**The holder has two comparison paths, and the cookie needed an answer for both.** D25 signs the
session with "the current password hash", which a stored `auth_password` has and an `AUTH_PASSWORD`
in the environment does not — it is compared in clear and is never hashed or stored. Hashing it on
the way in would have been the obvious fix and is the wrong one: bcrypt salts, so the hash of the
same environment variable differs on every start, and every restart would end every session — the
one thing step 4 says a signed cookie exists to avoid. So the signing key is mixed from **whichever
credential this process holds**: the stored bcrypt string, or the environment's plaintext. Both are
deterministic across a restart and both change when the password changes, which is the only property
the design actually needs. `api.Credential` carries the two as separate fields so the holder never
has to guess whether a string is a hash, and `cmd/curator`'s `authCredential` — which can see
`Resolution.Sources` — is what decides which one is set.

**The session key is a subkey, via a new `secret.Codec.Derive(label)`.** T39's key decrypts a
WireGuard config; the authentication holder has no business being able to do that, so it gets
`HMAC-SHA256(key, "curator session v1")` and the key itself never leaves `internal/secret`. Derive
is deterministic on purpose and a test says so — that is what makes a cookie survive a restart with
no session table. A database restored without its key file has no codec, so the holder generates a
random key: logging in still works, and the honest cost is that sessions then last as long as the
process.

**`auth_enabled: true` with no password is NOT enforced.** T40 refuses the write that would create
it, so it means a row edited by hand or an `-e AUTH_ENABLED=true` with the password forgotten.
Enforcing it would answer 401 to every request including the screen that fixes it, for what is a
typo; and nobody unauthenticated can cause it, because with authentication already on, the write
that would clear the password is itself behind the password. It logs a warning naming both ways to
fix it, and `GET /api/auth` reports `required: false` so the UI does not draw a login form nothing
can satisfy.

**Every password comparison serialises, not only the login endpoint's.** Step 7 names the login
endpoint, but the cheaper place to hit the same bcrypt is `Authorization: Basic` against any
protected route — an attacker would not use the form. One mutex covers both, held across the
comparison and across the delay, so a correct password never sleeps and twenty wrong ones take
twenty delays. The cost of the choice, stated: one `curl -u` request pays one bcrypt, and two
concurrent ones are serial.

**A request that presented nothing is not logged.** "Log every failure" would otherwise mean logging
every request a browser makes before it logs in — several per page load, into the ring buffer
`/api/logs` serves. A rejected credential (wrong password, expired or re-signed cookie) is logged
with the remote address and a reason; an absent one is answered and not recorded.

**No `WWW-Authenticate` header on the 401.** It would make browsers raise the native Basic dialog
that D25 rejects for being impossible to log out of, and it buys nothing: `curl -u` sends the header
preemptively rather than waiting for a challenge, which is what the live check confirmed.

**No logout endpoint.** T42's `web/lib/api.ts` names `authStatus` and `login` and nothing else, and
a cookie with `MaxAge` and no server state has nothing to invalidate that clearing it does not do.

**Measured live**, on 8097 against a scratch database, with the UI still unbuilt (T42):

- `PUT /api/settings {"auth_password": …, "auth_enabled": "true"}` → the **next** request to
  `/api/movies` is 401. No restart. `/healthz` and `/` stay 200, and `/api/nonsense` is 401 exactly
  as `/api/movies` is
- `curl -u :password` and `curl -u anyone:password` are both 200; no `-u` is 401
- logged in, killed the process, started it again: the same cookie jar is still 200 — the cookie is
  signed, not stored
- changed the password: the old cookie **and** the old password are 401 on the next request, with
  nothing evicted anywhere
- `AUTH_ENABLED=false` in the environment beat the stored `true` with the stored row untouched;
  `AUTH_PASSWORD` beat the stored hash, and the screen was then refused (409) from writing one
- the table holds `$2a$10$…`; `grep -c` of the password in `GET /api/settings` is 0, and of `$2a$`
  is also 0 — the hash is not a value a screen may see either
- the log tail holds two `auth failed` lines with `remote` and a reason, and `grep -c` of either
  password attempted is 0
