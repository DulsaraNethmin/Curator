package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/DulsaraNethmin/curator/internal/logs"
)

// The password every test here logs in with, and a distinctive wrong one. The
// second is a sentinel: if it ever reaches the log buffer, the endpoint is
// writing down what somebody typed.
const (
	authPassword = "correct horse battery staple"
	wrongGuess   = "WRONG-GUESS-4c81ba"
)

// authKey stands in for the subkey cmd/curator derives from the file beside the
// database. Fixed, so a test can build two holders and assert a cookie crosses
// a restart.
var authKey = []byte("a fixed session key, 32 bytes...")

// hashed is bcrypt at MIN cost.
//
// Production uses DefaultCost and D25 defers measuring it on the Pi to phase
// 10; a suite paying ~60 ms per comparison is a suite nobody runs, and what
// these tests assert is the comparison happening at all, not what it costs.
func hashed(t *testing.T, password string) string {
	t.Helper()
	out, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	return string(out)
}

// authFixture is the handler cmd/curator builds — API routes, /healthz, the
// static UI at "/", the two open endpoints, and the middleware in front of all
// of it — plus the same mux without the middleware, so "off changes nothing"
// can be asserted against the thing it must not change.
type authFixture struct {
	guarded http.Handler
	bare    http.Handler
	auth    *Auth
	logs    *logs.Buffer

	credential Credential
}

func newAuthFixture(t *testing.T, credential Credential) *authFixture {
	t.Helper()

	// NO secrets handed to the buffer, deliberately. In production the resolved
	// password is in the scrub list, so a leak would be redacted on the way in;
	// here nothing is, which is what makes "the attempt is not in the log"
	// prove the handler never wrote it rather than prove the scrubber caught it.
	buffer := logs.NewBuffer(200)
	log := slog.New(buffer.Handler(slog.NewTextHandler(io.Discard, nil)))

	f := &authFixture{logs: buffer, credential: credential}
	f.auth = NewAuth(authKey, func(context.Context) (Credential, error) { return f.credential, nil }, log)

	// One millisecond instead of one second. The serialisation is what is being
	// tested, not the constant.
	f.auth.failDelay = time.Millisecond
	if err := f.auth.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	mux := http.NewServeMux()
	set := fullSettings()
	New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, log).
		WithSettings(set).
		Register(mux)
	New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, log).
		WithSettings(set).
		RegisterSettings(mux)

	// main's two, stubbed: the healthcheck and the embedded UI.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>the UI</html>"))
	})
	f.auth.Register(mux)

	f.bare, f.guarded = mux, f.auth.Middleware(mux)
	return f
}

// setCredential is a settings write landing: the store now says something else,
// and Reload installs it for the next request.
func (f *authFixture) setCredential(t *testing.T, credential Credential) {
	t.Helper()
	f.credential = credential
	if err := f.auth.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
}

// logged flattens the buffer into one string — message and attributes, which is
// where a value would actually end up.
func (f *authFixture) logged() string {
	entries, _, _ := f.logs.Since(0, 200)
	var out strings.Builder
	for _, entry := range entries {
		out.WriteString(entry.Msg)
		for key, value := range entry.Attrs {
			out.WriteString(" " + key + "=" + value)
		}
		out.WriteString("\n")
	}
	return out.String()
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func get(target string) *http.Request { return httptest.NewRequest(http.MethodGet, target, nil) }

func postLogin(password string) *http.Request {
	body := strings.NewReader(`{"password":` + strconv.Quote(password) + `}`)
	return httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
}

// basic is what every verification block in docs/ looks like: curl -u :password.
func basic(target, password string) *http.Request {
	req := get(target)
	req.SetBasicAuth("", password)
	return req
}

func session(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range (&http.Response{Header: rec.Header()}).Cookies() {
		if cookie.Name == cookieName {
			return cookie
		}
	}
	t.Fatalf("no %s cookie in %v", cookieName, rec.Header())
	return nil
}

func withCookie(target string, cookie *http.Cookie) *http.Request {
	req := get(target)
	req.AddCookie(cookie)
	return req
}

func decodeAuth(t *testing.T, rec *httptest.ResponseRecorder) authBody {
	t.Helper()
	var body authBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v — body was %s", err, rec.Body)
	}
	return body
}

// The default, and the state every install that exists is in: with nothing
// configured, every route answers exactly what it answers without the
// middleware in front of it (docs/decisions.md D25).
//
// Asserted by running real requests through both handlers and comparing, rather
// than by asserting once that a flag is false — the flag is not the promise.
func TestAuthOffChangesNothing(t *testing.T) {
	f := newAuthFixture(t, Credential{})

	for _, build := range []func() *http.Request{
		func() *http.Request { return get("/api/movies") },
		func() *http.Request { return get("/api/movies/1") },
		func() *http.Request { return get("/api/movies/not-a-number") },
		func() *http.Request { return get("/api/settings") },
		func() *http.Request { return get("/api/auth") },
		func() *http.Request { return get("/api/nothing-here") },
		func() *http.Request { return get("/healthz") },
		func() *http.Request { return get("/") },
		func() *http.Request { return postLogin(authPassword) },
	} {
		bare := serve(f.bare, build())
		guarded := serve(f.guarded, build())

		if bare.Code != guarded.Code || bare.Body.String() != guarded.Body.String() {
			t.Errorf("%s: authentication off changed the answer\n  without: %d %s  with: %d %s",
				build().URL.Path, bare.Code, bare.Body, guarded.Code, guarded.Body)
		}
	}
}

// On: /api/* is 401, and the four things that are deliberately not.
func TestAuthOnProtectsTheAPIAndNothingElse(t *testing.T) {
	f := newAuthFixture(t, Credential{Enabled: true, Hash: hashed(t, authPassword)})

	for _, path := range []string{"/api/movies", "/api/movies/1", "/api/settings", "/api/logs"} {
		if rec := serve(f.guarded, get(path)); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want 401", path, rec.Code)
		}
	}

	// The healthcheck has no credential to carry — one in a container's
	// healthcheck is a credential in a `docker inspect` — and the static UI is
	// markup whose every fact comes from an endpoint that IS protected.
	for _, path := range []string{"/healthz", "/", "/movie/", "/api/auth"} {
		if rec := serve(f.guarded, get(path)); rec.Code == http.StatusUnauthorized {
			t.Errorf("GET %s = 401, and it is open by design", path)
		}
	}
	if rec := serve(f.guarded, postLogin(authPassword)); rec.Code != http.StatusOK {
		t.Errorf("POST /api/auth/login = %d with the right password, want 200 — "+
			"if it needed a cookie nobody could ever log in", rec.Code)
	}
}

// A protected route with no credential answers 401 whether or not it exists, so
// nobody without the password can map the API by watching which one they get.
func TestAuthDoesNotSayWhichRoutesExist(t *testing.T) {
	f := newAuthFixture(t, Credential{Enabled: true, Hash: hashed(t, authPassword)})

	real := serve(f.guarded, get("/api/movies"))
	invented := serve(f.guarded, get("/api/there-is-no-such-thing"))
	wrong := serve(f.guarded, basic("/api/movies", wrongGuess))

	if real.Code != invented.Code || real.Body.String() != invented.Body.String() {
		t.Errorf("a real route and an invented one answer differently:\n  %d %s\n  %d %s",
			real.Code, real.Body, invented.Code, invented.Body)
	}
	if real.Body.String() != wrong.Body.String() {
		t.Errorf("a wrong password and a missing one answer differently:\n  %s\n  %s", real.Body, wrong.Body)
	}
	if !strings.Contains(real.Body.String(), `"error"`) {
		t.Errorf("401 = %s, want phase 1's error envelope", real.Body)
	}
}

// The browser's path: a password in, a signed cookie out, and the same request
// with that cookie passes.
func TestAuthLoginSetsASessionThatWorks(t *testing.T) {
	f := newAuthFixture(t, Credential{Enabled: true, Hash: hashed(t, authPassword)})

	rec := serve(f.guarded, postLogin(authPassword))
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d, want 200 — body %s", rec.Code, rec.Body)
	}
	if body := decodeAuth(t, rec); !body.Required || !body.Authenticated {
		t.Errorf("login answered %+v, want both true", body)
	}

	cookie := session(t, rec)
	if got := serve(f.guarded, withCookie("/api/movies", cookie)); got.Code != http.StatusOK {
		t.Errorf("with the cookie = %d, want 200 — body %s", got.Code, got.Body)
	}

	// Not Secure, and that is deliberate: there is no TLS, and a Secure cookie
	// over plain HTTP is one the browser silently never sends.
	if cookie.Secure {
		t.Error("the session cookie is Secure: over plain HTTP the browser would never send it")
	}
	if !cookie.HttpOnly {
		t.Error("the session cookie is readable from JavaScript")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Errorf("Path = %q, want /", cookie.Path)
	}
}

// curl's path. Every verification block in docs/ is a curl, so an API that
// could not be curled would make the next phase's evidence harder to gather
// than the phase.
func TestAuthBasicWorksWithAnyUsername(t *testing.T) {
	f := newAuthFixture(t, Credential{Enabled: true, Hash: hashed(t, authPassword)})

	for _, user := range []string{"", "nethmin", "anything at all"} {
		req := get("/api/movies")
		req.SetBasicAuth(user, authPassword)

		rec := serve(f.guarded, req)
		if rec.Code != http.StatusOK {
			t.Errorf("Basic as %q = %d, want 200 — there are no users to name", user, rec.Code)
		}
		if len((&http.Response{Header: rec.Header()}).Cookies()) != 0 {
			t.Errorf("Basic as %q was handed a cookie: a script does not need a session", user)
		}
	}

	if rec := serve(f.guarded, basic("/api/movies", wrongGuess)); rec.Code != http.StatusUnauthorized {
		t.Errorf("a wrong password = %d, want 401", rec.Code)
	}
}

// AUTH_PASSWORD in the environment is compared in clear and is never hashed —
// hashing it would mean bcrypt's salt changing the cookie signature on every
// restart, which is how a session meant to survive one would die instead.
func TestAuthComparesAnEnvironmentPasswordInClear(t *testing.T) {
	f := newAuthFixture(t, Credential{Enabled: true, Password: authPassword})

	if rec := serve(f.guarded, basic("/api/movies", authPassword)); rec.Code != http.StatusOK {
		t.Errorf("the environment password = %d, want 200", rec.Code)
	}
	if rec := serve(f.guarded, basic("/api/movies", wrongGuess)); rec.Code != http.StatusUnauthorized {
		t.Errorf("a wrong password = %d, want 401", rec.Code)
	}

	// The hash of it is not it. This fails if anybody ever "helpfully" hashes
	// the environment value on the way in.
	if rec := serve(f.guarded, basic("/api/movies", hashed(t, authPassword))); rec.Code != http.StatusUnauthorized {
		t.Errorf("a bcrypt hash of the password = %d, want 401", rec.Code)
	}
}

// Changing the password ends every session, with nothing evicted: the old
// cookie was signed with a key mixed from the old credential, and that key no
// longer exists (docs/decisions.md D25).
func TestAuthChangingThePasswordEndsEverySession(t *testing.T) {
	f := newAuthFixture(t, Credential{Enabled: true, Hash: hashed(t, authPassword)})

	cookie := session(t, serve(f.guarded, postLogin(authPassword)))
	if rec := serve(f.guarded, withCookie("/api/movies", cookie)); rec.Code != http.StatusOK {
		t.Fatalf("the fresh cookie = %d, want 200", rec.Code)
	}

	f.setCredential(t, Credential{Enabled: true, Hash: hashed(t, "something else entirely")})

	if rec := serve(f.guarded, withCookie("/api/movies", cookie)); rec.Code != http.StatusUnauthorized {
		t.Errorf("the old cookie = %d after a password change, want 401", rec.Code)
	}
	if rec := serve(f.guarded, postLogin("something else entirely")); rec.Code != http.StatusOK {
		t.Errorf("the new password = %d, want 200", rec.Code)
	}

	// Even the same password again ends the old sessions, because bcrypt salts
	// and the stored hash is therefore a different string. That is the safe
	// direction for a re-typed password to fail in.
	f.setCredential(t, Credential{Enabled: true, Hash: hashed(t, authPassword)})
	if rec := serve(f.guarded, withCookie("/api/movies", cookie)); rec.Code != http.StatusUnauthorized {
		t.Errorf("the old cookie = %d after the password was written again, want 401", rec.Code)
	}
}

// The cookie is signed, not stored, so it survives the restart D29 makes
// routine — you are not logged out every time you change a setting.
func TestAuthSessionSurvivesARestart(t *testing.T) {
	credential := Credential{Enabled: true, Hash: hashed(t, authPassword)}
	source := func(context.Context) (Credential, error) { return credential, nil }

	before := NewAuth(authKey, source, quiet())
	if err := before.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	cookie := before.live.Load().session(time.Now().Add(sessionLifetime).Unix())

	// A second process, same key file, same stored hash, no shared memory.
	after := NewAuth(authKey, source, quiet())
	if err := after.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !after.live.Load().valid(cookie, time.Now()) {
		t.Error("the session did not survive a restart: it is signed, so there is nothing for a restart to lose")
	}

	// A different key file — a database restored without its own — cannot
	// verify it, which is the honest cost of having no session table.
	elsewhere := NewAuth([]byte("a different key, also 32 bytes.."), source, quiet())
	if err := elsewhere.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if elsewhere.live.Load().valid(cookie, time.Now()) {
		t.Error("a cookie signed under one secret verified under another")
	}
}

// An expired cookie is 401, a tampered expiry is 401, and the HMAC is what
// catches the second.
func TestAuthRejectsAnExpiredOrTamperedSession(t *testing.T) {
	f := newAuthFixture(t, Credential{Enabled: true, Hash: hashed(t, authPassword)})
	live := f.auth.live.Load()

	valid := live.session(time.Now().Add(time.Hour).Unix())
	_, mac, _ := strings.Cut(valid, ".")

	for name, value := range map[string]string{
		"expired":          live.session(time.Now().Add(-time.Minute).Unix()),
		"tampered expiry":  strconv.FormatInt(time.Now().Add(100*time.Hour).Unix(), 10) + "." + mac,
		"no signature":     strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
		"not a cookie":     "nonsense",
		"empty":            "",
		"forged signature": strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + ".AAAAAAAA",
	} {
		rec := serve(f.guarded, withCookie("/api/movies", &http.Cookie{Name: cookieName, Value: value}))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("a %s cookie = %d, want 401", name, rec.Code)
		}
	}

	// And the one that must still work, so the table above is not passing for
	// the wrong reason.
	if rec := serve(f.guarded, withCookie("/api/movies", &http.Cookie{Name: cookieName, Value: valid})); rec.Code != http.StatusOK {
		t.Errorf("the valid cookie = %d, want 200", rec.Code)
	}
}

// Every failure is logged, and no attempt is: a log tail readable over HTTP is
// exactly where a password mistyped into a username field would end up (D18).
func TestAuthLogsFailuresAndNeverTheAttempt(t *testing.T) {
	f := newAuthFixture(t, Credential{Enabled: true, Hash: hashed(t, authPassword)})

	serve(f.guarded, postLogin(wrongGuess))
	serve(f.guarded, basic("/api/movies", wrongGuess))
	serve(f.guarded, postLogin(authPassword)) // the correct one, which is also never logged

	logged := f.logged()
	if strings.Count(logged, "auth failed") != 2 {
		t.Errorf("want two failures logged, got:\n%s", logged)
	}
	if !strings.Contains(logged, "192.0.2.1") { // httptest's RemoteAddr
		t.Errorf("the failure did not name where it came from:\n%s", logged)
	}
	if strings.Contains(logged, wrongGuess) {
		t.Errorf("the attempted password is in the log:\n%s", logged)
	}
	if strings.Contains(logged, authPassword) {
		t.Errorf("the real password is in the log:\n%s", logged)
	}
}

// A request that presented nothing is not a failed attempt — it is every
// request a browser makes before it logs in, and logging those would fill the
// buffer the Logs screen serves with the ordinary operation of the login page.
func TestAuthDoesNotLogARequestThatPresentedNothing(t *testing.T) {
	f := newAuthFixture(t, Credential{Enabled: true, Hash: hashed(t, authPassword)})

	for i := 0; i < 5; i++ {
		serve(f.guarded, get("/api/movies"))
	}
	if logged := f.logged(); strings.Contains(logged, "auth failed") {
		t.Errorf("a browser that has not logged in yet filled the log:\n%s", logged)
	}
}

// bcrypt costing real milliseconds is the point of bcrypt, and it is also a
// denial-of-service lever if it can be hit in parallel. Twenty wrong passwords
// at once take twenty delays, not one.
func TestAuthFailedAttemptsSerialise(t *testing.T) {
	f := newAuthFixture(t, Credential{Enabled: true, Hash: hashed(t, authPassword)})

	const attempts = 20
	const delay = 5 * time.Millisecond
	f.auth.failDelay = delay

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Through the middleware, not the login endpoint: Basic auth on any
			// protected route is the cheaper place to hit the same bcrypt.
			serve(f.guarded, basic("/api/movies", wrongGuess))
		}()
	}
	wg.Wait()

	if elapsed := time.Since(start); elapsed < attempts*delay {
		t.Errorf("%d wrong passwords took %v, want at least %v — they ran in parallel",
			attempts, elapsed, attempts*delay)
	}
}

// The UI has to know which screen to draw before it has a cookie, so this one
// is open and answers both halves of the question.
func TestAuthStatusSaysWhichScreenToDraw(t *testing.T) {
	off := newAuthFixture(t, Credential{})
	if body := decodeAuth(t, serve(off.guarded, get("/api/auth"))); body.Required || !body.Authenticated {
		t.Errorf("with authentication off = %+v, want required false and authenticated true", body)
	}

	on := newAuthFixture(t, Credential{Enabled: true, Hash: hashed(t, authPassword)})
	if body := decodeAuth(t, serve(on.guarded, get("/api/auth"))); !body.Required || body.Authenticated {
		t.Errorf("with no cookie = %+v, want required true and authenticated false", body)
	}

	cookie := session(t, serve(on.guarded, postLogin(authPassword)))
	if body := decodeAuth(t, serve(on.guarded, withCookie("/api/auth", cookie))); !body.Required || !body.Authenticated {
		t.Errorf("with the cookie = %+v, want both true", body)
	}
}

// A switch that is on with nothing to satisfy it is a lockout, and the API
// cannot produce it — T40 refuses the write. A row edited by hand or a
// forgotten -e can, so it is reported loudly and not enforced: enforcing it
// would answer 401 to the screen that fixes it.
func TestAuthEnabledWithNoPasswordIsNotEnforced(t *testing.T) {
	f := newAuthFixture(t, Credential{Enabled: true})

	if rec := serve(f.guarded, get("/api/movies")); rec.Code != http.StatusOK {
		t.Errorf("GET /api/movies = %d with a password nobody can supply, want 200", rec.Code)
	}
	if body := decodeAuth(t, serve(f.guarded, get("/api/auth"))); body.Required {
		t.Error("required is true with no password: the UI would draw a login form nothing can satisfy")
	}
	if logged := f.logged(); !strings.Contains(logged, "auth_enabled") {
		t.Errorf("nothing said why authentication is not on:\n%s", logged)
	}
}

// The login endpoint takes {"password": "..."} and nothing else, and its
// rejection never quotes what it could not read.
func TestAuthLoginRejectsABodyItCannotRead(t *testing.T) {
	f := newAuthFixture(t, Credential{Enabled: true, Hash: hashed(t, authPassword)})

	rec := serve(f.guarded, httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`"`+wrongGuess)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if strings.Contains(rec.Body.String(), wrongGuess) {
		t.Errorf("the rejection echoed the body, which is a password: %s", rec.Body)
	}
}
