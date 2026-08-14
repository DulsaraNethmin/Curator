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
