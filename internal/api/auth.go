package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// One password, no usernames, no roles, in front of /api/* — and off unless
// somebody turns it on (docs/decisions.md D25).
//
// It exists because three things moved to the other side of this API while
// "LAN-only, so no authentication" stayed written down: DELETE /api/movies/{id}
// removes files from disk (D19), GET /api/logs serves the process log to any
// browser on the network (D18), and from phase 7 PUT /api/settings accepts a
// WireGuard private key (D28).
//
// **It is not protection against something reading the network.** There is no
// TLS, so the password crosses the LAN in clear on every Basic-auth request.
// What it raises the bar against is a browser on the network — a housemate, a
// guest, a device that should not be deleting films — and the screen that sets
// the password says exactly that.

const (
	// cookieName is what the browser gets after POST /api/auth/login. curl
	// needs no cookie: Authorization: Basic works on every protected route.
	cookieName = "curator_session"

	// sessionLifetime is how long one signed cookie lasts. Long, because it is
	// the browser's only credential and there is no refresh token to invent;
	// changing the password is what ends a session early, and it ends every
	// session at once.
	sessionLifetime = 30 * 24 * time.Hour

	// failureDelay is what one wrong password costs. It is held under the same
	// mutex every comparison takes, so the process answers at most one wrong
	// password per second however many connections ask at once.
	failureDelay = time.Second

	// ticketParam is the query parameter a player carries a ticket in.
	//
	// It is `ticket` and not the obvious `t` because T44's remux endpoint takes
	// a seek offset that wanted the same short name; the first draft of both had
	// `t` (docs/phase-8.md, "?t= was going to mean two things").
	ticketParam = "ticket"

	// ticketMessage prefixes what a ticket signs, and is the whole of its domain
	// separation from a session.
	//
	// A session signs bare digits; a ticket signs "ticket\n" + path + "\n" +
	// expiry under the same key. The two messages can never coincide, so a
	// cookie value cannot be replayed as a ticket and a ticket cannot be pasted
	// in as a cookie — asserted in the tests rather than argued here.
	ticketMessage = "ticket\n"
)

// SessionLabel is what cmd/curator derives the session key under.
//
// It lives here rather than at the call site because it names a use of the
// secret and this package is that use: the label domain-separates the cookie's
// key from the one that encrypts settings, and the two agreeing is what makes
// every existing cookie verify after a restart. Changing it logs everybody out,
// which is the whole reason it is a constant somebody has to come here to edit.
const SessionLabel = "curator session v1"

// The three paths that decide what is protected. Everything under /api/ is,
// with these two exceptions and no others.
const (
	apiPrefix = "/api/"

	// authStatusPath is open because the UI has to know which screen to draw
	// before it has a cookie.
	authStatusPath = "/api/auth"

	// authLoginPath is open, or nobody could ever log in.
	authLoginPath = "/api/auth/login"
)

// Credential is the authentication state as cmd/curator resolved it.
//
// There are TWO comparison paths and not one, and that falls out of the
// precedence rule rather than being a choice: AUTH_PASSWORD in the environment
// is compared in clear and is never hashed or stored, while a password saved
// from the settings screen is bcrypt by the time it reaches the table. At most
// one of the two fields is ever set, because internal/settings has already
// decided which source won (docs/phase-7.md, the precedence rule).
//
// This package receives it and never reads it from anywhere: the store, the
// environment and the decision between them all stay in cmd/curator, exactly as
// they do for SettingState.
type Credential struct {
	// Enabled is auth_enabled, resolved. False is the default and the state
	// every existing install is in (docs/decisions.md D25).
	Enabled bool

	// Password is AUTH_PASSWORD, verbatim out of the environment. It is
	// compared in constant time and never hashed — hashing it would only mean
	// bcrypt's salt changing the cookie signature on every restart, which is
	// how a session that is meant to survive one would die.
	Password string

	// Hash is the stored bcrypt hash, as cmd/curator's settingsWriter wrote it.
	Hash string
}

// CredentialSource reads the live authentication settings. It is called at
// start-up and again on every write that touches one of them, which is the
// whole of what makes the password immediate (docs/decisions.md D29).
type CredentialSource func(ctx context.Context) (Credential, error)

// credential is a Credential with its signing key already derived, so that the
// hot path — every request, once authentication is on — does no key derivation
// and no allocation beyond the comparison itself.
type credential struct {
	Credential

	// key signs this password's sessions: the process's session key mixed with
	// the credential itself.
	//
	// **Mixing the credential in is what makes a password change end every
	// session**, for free and with nothing to evict — the old cookies no longer
	// verify because they were signed with a key that no longer exists. It
	// reads like an accident otherwise, so: it is deliberate, and it is the
	// reason there is no session table (docs/decisions.md D25).
	key []byte
}

// Auth is the live password, the middleware in front of /api/*, and the two
// endpoints that are deliberately not behind it.
//
// It satisfies Access, so a settings write applies to the next request rather
// than the next restart.
type Auth struct {
	source CredentialSource
	log    *slog.Logger

	// key is a subkey of the secret internal/secret owns, never that key
	// itself: this type can sign a cookie and cannot decrypt a WireGuard
	// config.
	key []byte

	// live is replaced whole by Reload and read on every request. An atomic
	// pointer rather than a mutex because reads outnumber writes by every
	// request a household makes to every password it sets.
	live atomic.Pointer[credential]

	// attempt serialises every password comparison in the process. See verify.
	attempt   sync.Mutex
	failDelay time.Duration
}

// NewAuth builds the holder. sessionKey is the key sessions are signed with,
// derived from the secret beside the database; source is required and is the
// only way this type learns what the password is.
//
// An empty sessionKey is a supported state and costs exactly one thing: a
// random key is generated instead, so sessions do not survive a restart. It
// happens only when the database was restored without its key file, which is
// already a state the settings screen reports and asks to be repaired.
func NewAuth(sessionKey []byte, source CredentialSource, log *slog.Logger) *Auth {
	if log == nil {
		log = slog.Default()
	}

	key := sessionKey
	if len(key) == 0 {
		key = make([]byte, sha256.Size)
		// Since Go 1.24 crypto/rand.Read is documented never to return an
		// error; it panics rather than handing back short randomness.
		_, _ = rand.Read(key)
	}

	a := &Auth{source: source, log: log, key: key, failDelay: failureDelay}
	a.live.Store(&credential{})
	return a
}

// Reload re-reads the stored authentication settings and installs them for the
// next request. It is Access, called by the settings handler after a write that
// touched auth_enabled or auth_password.
func (a *Auth) Reload(ctx context.Context) error {
	next, err := a.source(ctx)
	if err != nil {
		return err
	}
	a.install(next)
	return nil
}

// install derives the signing key for a credential and publishes it.
func (a *Auth) install(next Credential) {
	live := &credential{Credential: next}

	switch {
	case next.Hash != "":
		live.key = a.mix([]byte(next.Hash))
	case next.Password != "":
		live.key = a.mix([]byte(next.Password))
	case next.Enabled:
		// A switch that is on with nothing to satisfy it. The API cannot
		// produce this state — a write enabling authentication without a
		// password is refused before it lands — so it means a row edited by
		// hand, or AUTH_ENABLED=true set in the environment with the password
		// forgotten.
		//
		// It is NOT enforced, and that is deliberate: enforcing it would answer
		// 401 to every request including the screen that fixes it, for a
		// mistake that is a typo. Nobody unauthenticated can cause it either,
		// because with authentication already on the write that would clear the
		// password is itself behind the password.
		live.Enabled = false
		a.log.Warn("auth_enabled is true and there is no password to log in with: " +
			"authentication is NOT being enforced. Set auth_password, or AUTH_PASSWORD in the environment")
	}

	a.live.Store(live)
}

// mix derives the key one credential's sessions are signed with.
func (a *Auth) mix(credential []byte) []byte {
	mac := hmac.New(sha256.New, a.key)
	mac.Write(credential)
	return mac.Sum(nil)
}

// Register mounts the two endpoints that are open by design.
func (a *Auth) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth", a.handleStatus)
	mux.HandleFunc("POST /api/auth/login", a.handleLogin)
}

// Middleware puts the password in front of /api/*, and nothing else.
//
// It wraps the whole mux rather than each route, which is what makes a
// protected route answer 401 whether or not it exists: the check runs before
// routing, so an unknown path under /api/ cannot be told apart from a real one
// by anybody without the password.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		live := a.live.Load()
		if !live.Enabled || openPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if reason := a.check(r, live); reason != "" {
			a.deny(w, r, reason)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// openPath reports the paths the password is deliberately not in front of.
//
// A path is matched exactly rather than by prefix, so nothing dressed up as
// /api/auth/../movies is open — that is not equal to either constant, so it is
// protected, and the mux answers a non-canonical path with a redirect to the
// canonical one, which is then protected in its turn.
func openPath(path string) bool {
	// Everything outside /api/: the static UI, which is markup with no data in
	// it — every fact it displays comes from an endpoint that IS protected —
	// and GET /healthz, because a healthcheck carrying a credential is a
	// credential in a `docker inspect`.
	if !strings.HasPrefix(path, apiPrefix) {
		return true
	}
	return path == authStatusPath || path == authLoginPath
}

// noCredential is the request a browser makes before it has ever logged in. It
// is a 401 like any other and it is the one that is not logged.
const noCredential = "no credential"

// check reports why a request may not proceed, or "" when it may.
//
// A cookie is tried first and a header second, because a browser sends the
// first and curl sends the second — and a request carrying both is a curl
// invocation someone pasted a cookie into, where the header is what they meant.
func (a *Auth) check(r *http.Request, live *credential) string {
	presented := false
	if cookie, err := r.Cookie(cookieName); err == nil {
		if live.valid(cookie.Value, time.Now()) {
			return ""
		}
		presented = true
	}

	// Any username. There are no users, so there is nothing for one to name,
	// and rejecting a username would be a second thing to get wrong for no
	// second guarantee.
	if _, password, ok := r.BasicAuth(); ok {
		if a.verify(live, password) {
			return ""
		}
		return "wrong password"
	}

	// The ticket is third, after both of the credentials that are not in a URL.
	// A browser holding a cookie never reaches this line, which is the point:
	// the ticket exists for VLC, mpv and a television, and a credential in a
	// query string is not what something with a cookie jar should be using
	// (docs/decisions.md D31).
	if ticket := r.URL.Query().Get(ticketParam); ticket != "" {
		if live.validTicket(ticket, r.URL.Path, time.Now()) {
			return ""
		}
		// Its own reason, so the log says which credential failed. It is
		// logged, unlike no credential at all: an expired ticket is a URL
		// somebody pasted into a player and is worth being able to see.
		return "the ticket is expired, is for another path, or was signed with a password that has since changed"
	}

	if presented {
		return "the session has expired, or was signed with a password that has since changed"
	}
	return noCredential
}

// verify compares one presented password, serialised across the whole process.
//
// Every comparison goes through this mutex, not only the login endpoint's.
// bcrypt costs real milliseconds on a Pi, which is the point of bcrypt and is
// also a denial-of-service lever if it can be hit in parallel — and the
// cheapest place to hit it would be Basic auth against any protected route,
// not the login form. Per-IP counters are the alternative and they are worse
// here: a LAN behind one NAT is one IP, and locking out the household because
// a script had a bad day is a self-inflicted outage.
//
// A correct password does not sleep. A wrong one holds the lock for failDelay,
// so twenty connections asking at once take twenty delays rather than one.
func (a *Auth) verify(live *credential, password string) bool {
	a.attempt.Lock()
	defer a.attempt.Unlock()

	if live.matches(password) {
		return true
	}
	time.Sleep(a.failDelay)
	return false
}

// matches compares against whichever of the two credentials this one carries.
func (c *credential) matches(password string) bool {
	switch {
	case c.Hash != "":
		return bcrypt.CompareHashAndPassword([]byte(c.Hash), []byte(password)) == nil
	case c.Password != "":
		return subtle.ConstantTimeCompare([]byte(c.Password), []byte(password)) == 1
	default:
		return false
	}
}

// session is the cookie value for a session expiring at unix.
//
// `<expiry>.<HMAC-SHA256 of that expiry>`, keyed by the session key mixed with
// the password. No session table, no map to evict, and it survives the restart
// D29 makes routine — you are not logged out every time you change a setting.
func (c *credential) session(unix int64) string {
	stamp := strconv.FormatInt(unix, 10)
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(stamp))
	return stamp + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// valid reports whether value is a session this credential signed and that has
// not run out.
//
// The whole cookie is re-derived from the expiry it claims and compared, so a
// tampered expiry fails on the signature and a non-canonical one ("0123" for
// 123) fails on the re-serialisation. There is nothing here that trusts the
// value before checking it.
func (c *credential) valid(value string, now time.Time) bool {
	stamp, _, ok := strings.Cut(value, ".")
	if !ok {
		return false
	}
	unix, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || !now.Before(time.Unix(unix, 0)) {
		return false
	}
	return hmac.Equal([]byte(c.session(unix)), []byte(value))
}

// ticket is the credential a player carries in a URL: the same shape as a
// session, over a message that names the path it is for.
//
// `<expiry>.<HMAC-SHA256 of "ticket\n" + path + "\n" + expiry>`, keyed by the
// session key mixed with the password — so all three of the properties D31
// claims fall out of the construction rather than being built. It is valid for
// one path, because the path is in the signed message. It expires, because the
// expiry is. And changing the password invalidates every outstanding one, with
// nothing to evict, because c.key no longer exists.
//
// base64url without padding, so the value needs no escaping in a query string.
func (c *credential) ticket(path string, unix int64) string {
	stamp := strconv.FormatInt(unix, 10)
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(ticketMessage + path + "\n" + stamp))
	return stamp + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// validTicket reports whether value is a ticket this credential signed for this
// path and that has not run out.
//
// Re-derived and compared whole, exactly as valid does it, so a tampered expiry
// fails on the signature and a non-canonical one fails on the re-serialisation.
func (c *credential) validTicket(value, path string, now time.Time) bool {
	stamp, _, ok := strings.Cut(value, ".")
	if !ok {
		return false
	}
	unix, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || !now.Before(time.Unix(unix, 0)) {
		return false
	}
	return hmac.Equal([]byte(c.ticket(path, unix)), []byte(value))
}

// Ticket mints a bearer credential for one URL path, and satisfies Tickets.
//
// ok is false when there is no password to be a credential for, which is the
// default and is how POST /api/movies/{id}/playback knows to answer with a
// plain URL rather than a meaningless token. It is also false in the state
// install refuses to enforce — authentication switched on with nothing to log
// in with — because a ticket for a door that is not locked is a token that
// would keep working after somebody locked it.
//
// The returned time is the one that was signed, truncated to the second, so
// what a caller reports as expires_at is exactly when it stops working.
func (a *Auth) Ticket(path string, ttl time.Duration) (string, time.Time, bool) {
	live := a.live.Load()
	if !live.Enabled {
		return "", time.Time{}, false
	}
	expires := time.Now().Add(ttl).Truncate(time.Second)
	return live.ticket(path, expires.Unix()), expires.UTC(), true
}

// Enforcing reports whether a password is actually being demanded right now.
//
// It is the LIVE answer and not the setting, and the difference is the whole
// reason this is a method rather than a caller reading auth_enabled. install
// refuses to enforce a switch that is on with nothing to satisfy it — a row
// edited by hand, or AUTH_ENABLED=true with the password forgotten — precisely
// so the screen that fixes it stays reachable. A caller gating a secret on the
// stored setting would withhold it in a state where anybody on the LAN can
// still read everything else.
//
// Its one caller is the VPN endpoint, which withholds the exit address unless
// something is in front of it (docs/decisions.md D18).
func (a *Auth) Enforcing() bool { return a.live.Load().Enabled }

// authBody is what GET /api/auth answers, and what a successful login answers.
//
// Two booleans, because the UI's question is which screen to draw: `required`
// says whether there is a password at all, and `authenticated` says whether
// this browser would get past it.
type authBody struct {
	Required      bool `json:"required"`
	Authenticated bool `json:"authenticated"`
}

// handleStatus answers whether this request needs a password and whether it
// already has one. Open, or the login screen could never be drawn.
func (a *Auth) handleStatus(w http.ResponseWriter, r *http.Request) {
	live := a.live.Load()
	body := authBody{Required: live.Enabled, Authenticated: true}
	if live.Enabled {
		body.Authenticated = a.check(r, live) == ""
	}
	a.respond(w, http.StatusOK, body)
}

// handleLogin takes the password and hands back a signed cookie.
func (a *Auth) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		// The decoder's own error is deliberately dropped rather than wrapped:
		// it quotes the body it failed on, and the body is a password.
		a.respond(w, http.StatusBadRequest, map[string]string{"error": `body is {"password": "..."}`})
		return
	}

	live := a.live.Load()
	if !live.Enabled {
		// There is nothing to log in to, and everything is open in this state
		// anyway. Answering 401 would send a browser that raced a change to a
		// login form it could never leave.
		a.respond(w, http.StatusOK, authBody{Required: false, Authenticated: true})
		return
	}
	if !a.verify(live, body.Password) {
		a.failed(r, "wrong password")
		// The same answer a wrong password for a user that does not exist would
		// get, because there are no users.
		a.respond(w, http.StatusUnauthorized, map[string]string{"error": "wrong password"})
		return
	}

	expiry := time.Now().Add(sessionLifetime)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    live.session(expiry.Unix()),
		Path:     "/",
		Expires:  expiry,
		MaxAge:   int(sessionLifetime.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Deliberately NOT Secure. There is no TLS, and a Secure cookie over
		// plain HTTP is a cookie the browser silently never sends — which looks
		// exactly like a login that does not work.
	})
	a.respond(w, http.StatusOK, authBody{Required: true, Authenticated: true})
}

// deny answers a request that did not get past the password.
func (a *Auth) deny(w http.ResponseWriter, r *http.Request, reason string) {
	if reason != noCredential {
		a.failed(r, reason)
	}

	// One message for every reason and every path. A wrong password and an
	// unknown route answer identically, so nobody without the password can map
	// the API by watching which 401 they get.
	a.respond(w, http.StatusUnauthorized, map[string]string{
		"error": "authentication is required: log in, or send the password with Authorization: Basic",
	})
}

// failed logs an attempt that did not work, and never the attempt itself.
//
// A log tail readable over HTTP is exactly where a password mistyped into a
// username field would end up (docs/decisions.md D18) — so the remote address
// and the reason, and nothing that came out of the request body or the
// Authorization header. With authentication on, /api/logs is finally behind
// something, which is what D18 said it was missing.
//
// A request that presented nothing is not logged: that is every request a
// browser makes before it logs in, and recording those would fill the buffer
// the Logs screen serves with the ordinary operation of the login page.
//
// The address is the socket's, never X-Forwarded-For: this line is evidence,
// and a header is written by whoever is being recorded.
func (a *Auth) failed(r *http.Request, reason string) {
	a.log.Warn("auth failed", "remote", r.RemoteAddr, "path", r.URL.Path, "reason", reason)
}

func (a *Auth) respond(w http.ResponseWriter, status int, body any) {
	if err := writeJSON(w, status, body); err != nil {
		a.log.Error("write response", "err", err)
	}
}
