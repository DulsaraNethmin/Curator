package qbit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// The endpoints are spelled out rather than built from the package's own
// constants: a test that reuses client.go's paths would agree with a typo in
// them.
const (
	loginPath = "/api/v2/auth/login"
	addPath   = "/api/v2/torrents/add"
	infoPath  = "/api/v2/torrents/info"
)

const (
	testUser     = "nethmin"
	testPass     = "correct-horse"
	testCategory = "curator"

	// The same torrent as internal/indexer's fixtures, in the two cases curator
	// has to reconcile: indexer.InfoHash returns upper, qBittorrent reports lower.
	infoHashUpper = "89599BF4DC369A3A8ECA26411C5CCF922D78B486"
	infoHashLower = "89599bf4dc369a3a8eca26411c5ccf922d78b486"

	// A magnet with the ampersands that make form encoding matter: concatenated
	// into a body by hand, its &dn= and &tr= would arrive as separate form fields.
	testMagnet = "magnet:?xt=urn:btih:" + infoHashUpper +
		"&dn=Interstellar+%282014%29+%5B1080p%5D&tr=udp%3A%2F%2Fopen.demonii.com%3A1337%2Fannounce"
)

// infoBody is one torrents/info entry as qBittorrent 5.1.2 sends it: the hash in
// lower-case hex, progress as a fraction rather than a percentage, and paths in
// qBittorrent's own namespace — /downloads is its mount of the Pi's
// /media/storage/media/downloads, so these strings do not exist on the machine
// running this test, which is exactly the point.
const infoBody = `[
  {
    "hash": "` + infoHashLower + `",
    "name": "Interstellar (2014) [1080p] [BluRay] [x264]",
    "state": "stalledUP",
    "progress": 1,
    "size": 2147483648,
    "content_path": "/downloads/complete/curator/Interstellar (2014) [1080p] [BluRay] [x264]",
    "category": "curator",
    "save_path": "/downloads/complete/curator/",
    "ratio": 1.37,
    "dlspeed": 0
  }
]`

// recorded is one request the stub saw.
type recorded struct {
	method      string
	path        string
	query       url.Values
	form        url.Values
	contentType string
	sid         string
}

// qbitStub is an httptest.Server standing in for qBittorrent. It serves the login
// endpoint itself — the 200-carrying-"Fails." behaviour is part of what is being
// tested — and hands everything else to the handler a test supplies.
type qbitStub struct {
	client *Client

	mu       sync.Mutex
	logins   int
	requests []recorded

	// Knobs. Set before the first call; nothing is running yet.
	password    string
	loginStatus int
	loginBody   string
	noCookie    bool
}

func newStub(t *testing.T, handler http.HandlerFunc) *qbitStub {
	t.Helper()

	stub := &qbitStub{password: testPass}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form for %s: %v", r.URL.Path, err)
		}
		got := recorded{
			method:      r.Method,
			path:        r.URL.Path,
			query:       r.URL.Query(),
			form:        r.PostForm,
			contentType: r.Header.Get("Content-Type"),
		}
		if cookie, err := r.Cookie(sessionCookie); err == nil {
			got.sid = cookie.Value
		}
		stub.mu.Lock()
		stub.requests = append(stub.requests, got)
		stub.mu.Unlock()

		if r.URL.Path == loginPath {
			stub.serveLogin(w, r)
			return
		}
		if handler == nil {
			t.Errorf("unexpected request to %s", r.URL.Path)
			http.Error(w, "no handler", http.StatusNotImplemented)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	stub.client = New(srv.URL, testUser, testPass, srv.Client())
	return stub
}

func (s *qbitStub) serveLogin(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.logins++
	attempt := s.logins
	status, body, noCookie, password := s.loginStatus, s.loginBody, s.noCookie, s.password
	s.mu.Unlock()

	if status != 0 || body != "" {
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
		return
	}

	// A wrong username or password is a 200 carrying "Fails.", not a 401. This is
	// qBittorrent's own behaviour and the reason login reads the body at all.
	if r.PostFormValue("username") != testUser || r.PostFormValue("password") != password {
		_, _ = io.WriteString(w, bodyFails)
		return
	}
	if !noCookie {
		// A fresh value per login, so a test can prove a retry carried the *new*
		// session rather than the one that had just been refused.
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: fmt.Sprintf("session-%d", attempt), Path: "/"})
	}
	_, _ = io.WriteString(w, bodyOK)
}

func (s *qbitStub) requestsFor(path string) []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []recorded
	for _, r := range s.requests {
		if r.path == path {
			out = append(out, r)
		}
	}
	return out
}

func (s *qbitStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *qbitStub) loginCount() int { return len(s.requestsFor(loginPath)) }

// serveJSON answers every request with the same body.
func serveJSON(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
}

func serveStatus(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// TestLoginSendsFormValuesAndLaterCallsCarryTheSession is the shape of every
// other test: credentials go in the body as form values, and the SID cookie the
// login answers with is what authenticates everything after it.
func TestLoginSendsFormValuesAndLaterCallsCarryTheSession(t *testing.T) {
	stub := newStub(t, serveJSON(infoBody))

	if _, err := stub.client.Torrents(context.Background(), testCategory); err != nil {
		t.Fatalf("Torrents: %v", err)
	}

	logins := stub.requestsFor(loginPath)
	if len(logins) != 1 {
		t.Fatalf("logins = %d, want 1", len(logins))
	}
	if logins[0].method != http.MethodPost {
		t.Errorf("login method = %s, want POST", logins[0].method)
	}
	if got := logins[0].form.Get("username"); got != testUser {
		t.Errorf("login form username = %q, want %q", got, testUser)
	}
	if got := logins[0].form.Get("password"); got != testPass {
		t.Errorf("login form password = %q, want %q", got, testPass)
	}
	// The credentials belong in the body. In the query string they would land in
	// qBittorrent's access log and in any *url.Error we ever printed.
	if logins[0].query.Get("username") != "" || logins[0].query.Get("password") != "" {
		t.Errorf("credentials were sent in the query string: %v", logins[0].query)
	}

	info := stub.requestsFor(infoPath)
	if len(info) != 1 {
		t.Fatalf("torrents/info requests = %d, want 1", len(info))
	}
	if info[0].sid != "session-1" {
		t.Errorf("torrents/info carried SID %q, want the cookie login issued", info[0].sid)
	}
}

// TestSessionIsReusedAcrossCalls pins the rule that logging in before every
// request is waste: the session lasts, and three calls cost one login.
func TestSessionIsReusedAcrossCalls(t *testing.T) {
	stub := newStub(t, serveJSON(infoBody))

	for i := range 3 {
		if _, err := stub.client.Torrents(context.Background(), testCategory); err != nil {
			t.Fatalf("Torrents %d: %v", i, err)
		}
	}
	if got := stub.loginCount(); got != 1 {
		t.Errorf("logins = %d for three calls, want 1", got)
	}
}

// TestForbiddenReAuthenticatesOnceAndRetries is the expired-session path: one
// 403, one fresh login, one retry, and the retry's answer is what the caller gets.
func TestForbiddenReAuthenticatesOnceAndRetries(t *testing.T) {
	var calls atomic.Int64
	stub := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		serveJSON(infoBody)(w, r)
	})

	torrents, err := stub.client.Torrents(context.Background(), testCategory)
	if err != nil {
		t.Fatalf("Torrents: %v", err)
	}
	if len(torrents) != 1 {
		t.Fatalf("torrents = %d, want the retry's 1", len(torrents))
	}
	if got := stub.loginCount(); got != 2 {
		t.Errorf("logins = %d, want 2 — one at the start and one after the 403", got)
	}

	info := stub.requestsFor(infoPath)
	if len(info) != 2 {
		t.Fatalf("torrents/info requests = %d, want 2", len(info))
	}
	// The retry must carry the session the second login issued. Retrying with the
	// cookie that was just refused would 403 again and look like bad credentials.
	if info[0].sid != "session-1" || info[1].sid != "session-2" {
		t.Errorf("SIDs = %q then %q, want session-1 then session-2", info[0].sid, info[1].sid)
	}
}

// TestSecondForbiddenIsReportedNotRetried is the wrong-password path, which
// arrives as the same status code as the expired session above. It must stop
// after one retry and say what it looks like.
func TestSecondForbiddenIsReportedNotRetried(t *testing.T) {
	stub := newStub(t, serveStatus(http.StatusForbidden, "Forbidden"))

	_, err := stub.client.Torrents(context.Background(), testCategory)
	if err == nil {
		t.Fatal("Torrents succeeded against a qBittorrent answering 403 to everything")
	}
	if !errors.Is(err, ErrAuth) {
		t.Errorf("error is not ErrAuth, so a caller cannot tell it from qBittorrent being down: %v", err)
	}
	for _, want := range []string{"qbittorrent", "credentials", "session", "403"} {
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if got := stub.loginCount(); got != 2 {
		t.Errorf("logins = %d, want exactly 2: one retry, never a loop", got)
	}
	if got := len(stub.requestsFor(infoPath)); got != 2 {
		t.Errorf("torrents/info requests = %d, want exactly 2", got)
	}
}

// TestLoginRejectedReportsCredentials covers qBittorrent's 200 that means no:
// wrong credentials come back "200 Fails.", and a client that only looked at the
// status would carry on and blame the next request.
func TestLoginRejectedReportsCredentials(t *testing.T) {
	stub := newStub(t, serveJSON(infoBody))
	stub.password = "not-the-password"

	_, err := stub.client.Torrents(context.Background(), testCategory)
	if err == nil {
		t.Fatal("Torrents succeeded with a password qBittorrent rejected")
	}
	if !errors.Is(err, ErrAuth) {
		t.Errorf("error is not ErrAuth: %v", err)
	}
	if !strings.Contains(err.Error(), bodyFails) {
		t.Errorf("error %q does not quote qBittorrent's %q", err, bodyFails)
	}
	if got := stub.loginCount(); got != 1 {
		t.Errorf("logins = %d, want 1: a rejected password is not retried", got)
	}
	if got := len(stub.requestsFor(infoPath)); got != 0 {
		t.Errorf("torrents/info was called %d times after a failed login", got)
	}
}

// TestLoginWithoutSessionCookieIsAnError covers the otherwise baffling case of a
// login that succeeds and authenticates nothing — a proxy stripping Set-Cookie
// looks identical to bad credentials from the next request onwards.
func TestLoginWithoutSessionCookieIsAnError(t *testing.T) {
	stub := newStub(t, serveJSON(infoBody))
	stub.noCookie = true

	_, err := stub.client.Torrents(context.Background(), testCategory)
	if err == nil {
		t.Fatal("Torrents succeeded although the login set no session cookie")
	}
	if !strings.Contains(err.Error(), sessionCookie) {
		t.Errorf("error %q does not name the missing %s cookie", err, sessionCookie)
	}
}

// TestLoginBannedIPIsNotRetried covers the 403 qBittorrent answers on the login
// endpoint itself once an IP has failed too often. Retrying is the one thing that
// extends the ban.
func TestLoginBannedIPIsNotRetried(t *testing.T) {
	stub := newStub(t, serveJSON(infoBody))
	stub.loginStatus = http.StatusForbidden
	stub.loginBody = "Your IP address has been banned after too many failed authentication attempts."

	_, err := stub.client.Torrents(context.Background(), testCategory)
	if err == nil {
		t.Fatal("Torrents succeeded against a login answering 403")
	}
	if !errors.Is(err, ErrAuth) {
		t.Errorf("error is not ErrAuth: %v", err)
	}
	if !strings.Contains(err.Error(), "banned") {
		t.Errorf("error %q does not pass on qBittorrent's explanation", err)
	}
	if got := stub.loginCount(); got != 1 {
		t.Errorf("logins = %d, want 1", got)
	}
}

// TestAddMagnetPostsUrlsCategoryAndTags proves the request qBittorrent actually
// receives: form-encoded, with the magnet intact. A magnet concatenated into a
// body by hand would arrive as three fields, because it is full of ampersands.
func TestAddMagnetPostsUrlsCategoryAndTags(t *testing.T) {
	stub := newStub(t, serveStatus(http.StatusOK, bodyOK))

	if err := stub.client.AddMagnet(context.Background(), testMagnet, testCategory); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}

	adds := stub.requestsFor(addPath)
	if len(adds) != 1 {
		t.Fatalf("torrents/add requests = %d, want 1", len(adds))
	}
	if adds[0].method != http.MethodPost {
		t.Errorf("method = %s, want POST", adds[0].method)
	}
	if !strings.HasPrefix(adds[0].contentType, "application/x-www-form-urlencoded") {
		t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", adds[0].contentType)
	}
	if got := adds[0].form.Get("urls"); got != testMagnet {
		t.Errorf("urls = %q, want the magnet verbatim %q", got, testMagnet)
	}
	// Category and tag both, on the one request: the category sets the save path
	// and scopes every later poll, the tag makes ownership visible in the Web UI
	// (docs/decisions.md D13).
	if got := adds[0].form.Get("category"); got != testCategory {
		t.Errorf("category = %q, want %q", got, testCategory)
	}
	if got := adds[0].form.Get("tags"); got != testCategory {
		t.Errorf("tags = %q, want %q", got, testCategory)
	}
}

// TestAddMagnetReportsFails covers the only 200 body from this endpoint that
// means anything: "Fails." is qBittorrent saying it accepted nothing.
func TestAddMagnetReportsFails(t *testing.T) {
	stub := newStub(t, serveStatus(http.StatusOK, bodyFails))

	err := stub.client.AddMagnet(context.Background(), testMagnet, testCategory)
	if err == nil {
		t.Fatal("AddMagnet returned nil for a 200 Fails.")
	}
	if !strings.Contains(err.Error(), bodyFails) {
		t.Errorf("error %q does not quote qBittorrent's answer", err)
	}
}

// TestAddMagnetRejectsAnEmptyMagnet fails before opening a connection: an empty
// magnet is a caller bug, and qBittorrent would answer "Ok." to it.
func TestAddMagnetRejectsAnEmptyMagnet(t *testing.T) {
	stub := newStub(t, serveStatus(http.StatusOK, bodyOK))

	if err := stub.client.AddMagnet(context.Background(), "   ", testCategory); err == nil {
		t.Fatal("AddMagnet accepted an empty magnet")
	}
	if got := stub.count(); got != 0 {
		t.Errorf("%d requests were sent for an empty magnet, want 0", got)
	}
}

// TestTorrentsFiltersByCategoryAndDecodesTheFields is the main parse.
func TestTorrentsFiltersByCategoryAndDecodesTheFields(t *testing.T) {
	stub := newStub(t, serveJSON(infoBody))

	torrents, err := stub.client.Torrents(context.Background(), testCategory)
	if err != nil {
		t.Fatalf("Torrents: %v", err)
	}
	if len(torrents) != 1 {
		t.Fatalf("torrents = %d, want 1", len(torrents))
	}

	info := stub.requestsFor(infoPath)
	if got := info[0].query.Get("category"); got != testCategory {
		t.Errorf("category filter = %q, want %q — an unfiltered poll would see the *arr stack's torrents", got, testCategory)
	}

	// What comes out is the neutral type, not the wire one: the hash has changed
	// case, `stalledUP` has become curator's `completed`, and the two fields
	// nothing reads — size and save_path — are decoded by nobody.
	got := torrents[0]
	want := torrent.Torrent{
		Hash:        infoHashUpper,
		Name:        "Interstellar (2014) [1080p] [BluRay] [x264]",
		State:       torrent.StateCompleted,
		Progress:    1,
		ContentPath: "/downloads/complete/curator/Interstellar (2014) [1080p] [BluRay] [x264]",
		Category:    testCategory,
	}
	if got != want {
		t.Errorf("torrent =\n%+v\nwant\n%+v", got, want)
	}
}

// TestTorrentsDecodesTheWireShapeVerbatim is the other half: qBittorrent's own
// state survives inside this package, so a vocabulary change stays visible in a
// log line rather than being flattened at the JSON boundary.
func TestTorrentsDecodesTheWireShapeVerbatim(t *testing.T) {
	stub := newStub(t, serveJSON(infoBody))

	wire, err := stub.client.torrents(context.Background(), nil)
	if err != nil {
		t.Fatalf("torrents: %v", err)
	}
	if len(wire) != 1 {
		t.Fatalf("torrents = %d, want 1", len(wire))
	}
	if wire[0].State != "stalledUP" {
		t.Errorf("State = %q, want qBittorrent's own %q", wire[0].State, "stalledUP")
	}
	if wire[0].Hash != infoHashLower {
		t.Errorf("Hash = %q, want the wire's lower-case %q", wire[0].Hash, infoHashLower)
	}
}

// T55: a stalled torrent arrives with the sentence attached, through the whole
// path rather than through the mapping alone. The `stalledUP` fixture above
// asserts the other half by struct equality — a completed torrent's Reason is
// "" and a field appearing there would fail that comparison.
func TestTorrentsCarryTheStallReason(t *testing.T) {
	stub := newStub(t, serveJSON(`[
	  {
	    "hash": "`+infoHashLower+`",
	    "name": "Interstellar (2014) [1080p]",
	    "state": "stalledDL",
	    "progress": 0,
	    "content_path": "",
	    "category": "curator"
	  }
	]`))

	torrents, err := stub.client.Torrents(context.Background(), testCategory)
	if err != nil {
		t.Fatalf("Torrents: %v", err)
	}
	if len(torrents) != 1 {
		t.Fatalf("torrents = %d, want 1", len(torrents))
	}
	if torrents[0].State != torrent.StateStalled {
		t.Fatalf("State = %q, want stalled", torrents[0].State)
	}
	if torrents[0].Reason == "" {
		t.Error("Reason is empty on a stalledDL torrent; Activity would show a badge and no explanation")
	}
}

// TestTorrentsEmptyIsNilAndNil — no torrents in the category is a normal answer.
func TestTorrentsEmptyIsNilAndNil(t *testing.T) {
	stub := newStub(t, serveJSON(`[]`))

	torrents, err := stub.client.Torrents(context.Background(), testCategory)
	if err != nil {
		t.Fatalf("Torrents: %v", err)
	}
	if torrents != nil {
		t.Errorf("torrents = %#v, want nil", torrents)
	}
}

// TestTorrentsWithoutACategoryOmitsTheParameter guards a trap in qBittorrent's
// API: `category=` present but empty means "torrents with no category at all",
// which is a different question and a wrong answer.
func TestTorrentsWithoutACategoryOmitsTheParameter(t *testing.T) {
	stub := newStub(t, serveJSON(infoBody))

	if _, err := stub.client.Torrents(context.Background(), "   "); err != nil {
		t.Fatalf("Torrents: %v", err)
	}
	info := stub.requestsFor(infoPath)
	if len(info) != 1 {
		t.Fatalf("torrents/info requests = %d, want 1", len(info))
	}
	if _, ok := info[0].query["category"]; ok {
		t.Errorf("sent category=%q; an empty category parameter asks qBittorrent for uncategorised torrents", info[0].query.Get("category"))
	}
}

// TestTorrentByHashAcceptsAnUpperCaseInfoHash is the case trap: the hash comes
// from indexer.InfoHash in upper-case, qBittorrent knows it in lower-case, and a
// raw comparison would report every torrent we just added as missing.
//
// Both directions are asserted here, because this is the seam: lower-case goes
// out on the wire, upper-case comes back to curator.
func TestTorrentByHashAcceptsAnUpperCaseInfoHash(t *testing.T) {
	stub := newStub(t, serveJSON(infoBody))

	found, err := stub.client.TorrentByHash(context.Background(), infoHashUpper)
	if err != nil {
		t.Fatalf("TorrentByHash: %v", err)
	}
	if found == nil {
		t.Fatal("TorrentByHash found nothing for the hash qBittorrent is reporting")
	}
	if found.Hash != infoHashUpper {
		t.Errorf("Hash = %q, want curator's upper-case %q", found.Hash, infoHashUpper)
	}
	if got := stub.requestsFor(infoPath)[0].query.Get("hashes"); got != infoHashLower {
		t.Errorf("hashes filter = %q, want lower-case %q", got, infoHashLower)
	}
}

// TestTorrentByHashAbsentIsNilAndNil covers both ways a torrent can be missing:
// qBittorrent says so, or qBittorrent ignores the filter and answers with
// somebody else's torrent. The second is why the hash is checked again here.
func TestTorrentByHashAbsentIsNilAndNil(t *testing.T) {
	otherHash := "1111111111111111111111111111111111111111"

	for name, body := range map[string]string{
		"empty list":     `[]`,
		"filter ignored": strings.Replace(infoBody, infoHashLower, otherHash, 1),
	} {
		t.Run(name, func(t *testing.T) {
			stub := newStub(t, serveJSON(body))

			found, err := stub.client.TorrentByHash(context.Background(), infoHashUpper)
			if err != nil {
				t.Fatalf("TorrentByHash: %v", err)
			}
			if found != nil {
				t.Errorf("TorrentByHash = %+v, want nil: absence is a normal answer, and this one is not ours", found)
			}
		})
	}
}

// TestTorrentByHashRejectsAnEmptyHash — an empty hash would ask qBittorrent for
// everything and confirm any add at all.
func TestTorrentByHashRejectsAnEmptyHash(t *testing.T) {
	stub := newStub(t, serveJSON(infoBody))

	if _, err := stub.client.TorrentByHash(context.Background(), "  "); err == nil {
		t.Fatal("TorrentByHash accepted an empty hash")
	}
	if got := stub.count(); got != 0 {
		t.Errorf("%d requests were sent for an empty hash, want 0", got)
	}
}

// TestServerErrorNamesQBittorrent — when the far end is unwell the error has to
// say which far end, because curator talks to five services.
func TestServerErrorNamesQBittorrent(t *testing.T) {
	stub := newStub(t, serveStatus(http.StatusInternalServerError, "<html><body><h1>500 Internal Server Error</h1></body></html>"))

	_, err := stub.client.Torrents(context.Background(), testCategory)
	if err == nil {
		t.Fatal("Torrents returned nil error for a 500")
	}
	for _, want := range []string{"qbittorrent", "500"} {
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	// A 5xx is not an auth problem; a caller retrying it must not be told the
	// password is wrong.
	if errors.Is(err, ErrAuth) {
		t.Errorf("a 500 was reported as ErrAuth: %v", err)
	}
}

// TestNonJSONBodyNamesQBittorrent — a reverse proxy in front of qBittorrent
// answers 200 with an HTML holding page, which decodes into no torrents at all if
// nobody checks.
func TestNonJSONBodyNamesQBittorrent(t *testing.T) {
	stub := newStub(t, serveJSON(`<html><body>qBittorrent is starting…</body></html>`))

	_, err := stub.client.Torrents(context.Background(), testCategory)
	if err == nil {
		t.Fatal("Torrents returned nil error for an HTML body")
	}
	for _, want := range []string{"qbittorrent", "json"} {
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestNewLeavesTheCallersHTTPClientAlone — curator hands one *http.Client to
// TMDB, the indexers and this. Attaching a jar to it would clobber any jar the
// caller set and would race with the goroutines already using it.
func TestNewLeavesTheCallersHTTPClientAlone(t *testing.T) {
	shared := &http.Client{Timeout: time.Second}

	client := New("", testUser, testPass, shared)

	if shared.Jar != nil {
		t.Error("New attached a cookie jar to the caller's http.Client")
	}
	if client.http == shared {
		t.Error("New kept the caller's http.Client rather than a copy")
	}
	if client.http.Jar == nil {
		t.Error("the client has no cookie jar, so the session cookie would be dropped after login")
	}
	if client.http.Timeout != time.Second {
		t.Errorf("Timeout = %v, want the caller's %v to survive the copy", client.http.Timeout, time.Second)
	}
	if client.baseURL != DefaultBaseURL {
		t.Errorf("baseURL = %q, want the default %q for an empty base", client.baseURL, DefaultBaseURL)
	}
}

// TestConcurrentCallsShareOneLogin runs under -race and pins the epoch check: a
// burst of callers meeting an unauthenticated client costs one login between
// them, not one each.
func TestConcurrentCallsShareOneLogin(t *testing.T) {
	stub := newStub(t, serveJSON(infoBody))

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := stub.client.Torrents(context.Background(), testCategory); err != nil {
				t.Errorf("Torrents: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := stub.loginCount(); got != 1 {
		t.Errorf("logins = %d, want 1 for eight concurrent callers", got)
	}
}

// liveDefaultURL is the Pi from a laptop on the LAN. qBittorrent shares gluetun's
// network namespace and has no ports of its own, so this is gluetun's address
// (docs/phase-3.md, measured 2026-08-12).
const liveDefaultURL = "http://192.168.1.26:8080"

// TestLiveListCuratorCategory is the one test that talks to the real qBittorrent.
// Every other test proves we parse a stub correctly; this proves the requests we
// build are ones qBittorrent accepts, and that the credentials work.
//
// It is **read-only**: it lists our own category and adds nothing. The *arr stack
// shares this instance until phase 6, and a test that added or removed a torrent
// would be operating on a live service.
//
// It skips whenever QBIT_USER or QBIT_PASS is unset, so a fresh clone and a CI box
// without secrets both stay green:
//
//	go test -short ./internal/qbit                                  # never touches the network
//	QBIT_USER=nethmin QBIT_PASS=… go test -run Live ./internal/qbit  # the live check
func TestLiveListCuratorCategory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the live qBittorrent check in -short mode")
	}
	user := strings.TrimSpace(os.Getenv("QBIT_USER"))
	pass := os.Getenv("QBIT_PASS")
	if user == "" || pass == "" {
		t.Skip("no QBIT_USER/QBIT_PASS in the environment; skipping the live qBittorrent check")
	}
	base := strings.TrimSpace(os.Getenv("QBIT_URL"))
	if base == "" {
		base = liveDefaultURL
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	torrents, err := New(base, user, pass, nil).Torrents(ctx, testCategory)
	switch {
	case errors.Is(err, ErrAuth):
		// Rejected credentials are a real fault, not an absent instance.
		t.Fatalf("qBittorrent at %s rejected the credentials: %v", base, err)
	case err != nil:
		t.Skipf("live qBittorrent at %s unreachable: %v", base, err)
	}

	for _, got := range torrents {
		if got.Category != testCategory {
			t.Errorf("torrent %q has category %q; the filter is what keeps us off the *arr stack's torrents", got.Name, got.Category)
		}
		if got.Hash != torrent.NormalizeHash(got.Hash) {
			t.Errorf("hash %q is not in curator's case", got.Hash)
		}
	}
	t.Logf("live: %d torrent(s) in category %q at %s", len(torrents), testCategory, base)
}

// The guard that matters. The *arr stack shares this qBittorrent until phase 6,
// so a hash belonging to radarr must not be deletable through this client even
// if something hands it one — and the check is here, at the lowest level,
// rather than only in the caller (docs/decisions.md D19).
func TestDeleteTorrentRefusesAnotherCategory(t *testing.T) {
	var deleteCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, pathLogin):
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "s"})
			fmt.Fprint(w, bodyOK)
		case strings.HasSuffix(r.URL.Path, pathTorrentsInfo):
			fmt.Fprint(w, `[{"hash":"aaaa","name":"someone else's","category":"radarr"}]`)
		case strings.HasSuffix(r.URL.Path, pathTorrentsDelete):
			deleteCalled = true
			fmt.Fprint(w, bodyOK)
		}
	}))
	defer srv.Close()

	err := New(srv.URL, "u", "p", srv.Client()).DeleteTorrent(context.Background(), "AAAA", "curator", true)
	if !errors.Is(err, torrent.ErrWrongCategory) {
		t.Fatalf("err = %v, want ErrWrongCategory", err)
	}
	if deleteCalled {
		t.Error("torrents/delete was called for a torrent in category radarr")
	}
}

func TestDeleteTorrentRemovesOurOwn(t *testing.T) {
	var gotHashes, gotDeleteFiles string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, pathLogin):
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "s"})
			fmt.Fprint(w, bodyOK)
		case strings.HasSuffix(r.URL.Path, pathTorrentsInfo):
			fmt.Fprint(w, `[{"hash":"aaaa","name":"ours","category":"curator"}]`)
		case strings.HasSuffix(r.URL.Path, pathTorrentsDelete):
			_ = r.ParseForm()
			gotHashes, gotDeleteFiles = r.FormValue("hashes"), r.FormValue("deleteFiles")
			fmt.Fprint(w, bodyOK)
		}
	}))
	defer srv.Close()

	if err := New(srv.URL, "u", "p", srv.Client()).DeleteTorrent(context.Background(), "AAAA", "curator", true); err != nil {
		t.Fatalf("DeleteTorrent: %v", err)
	}
	if gotHashes != "aaaa" {
		t.Errorf("hashes = %q, want the normalised lower-case hash", gotHashes)
	}
	if gotDeleteFiles != "true" {
		t.Errorf("deleteFiles = %q, want true — the disk has to actually be freed", gotDeleteFiles)
	}
}

// Delete gets retried after a partial failure, so a retry that fails because
// the work was already done is a retry nobody can complete.
func TestDeleteTorrentOnAMissingTorrentIsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, pathLogin):
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "s"})
			fmt.Fprint(w, bodyOK)
		case strings.HasSuffix(r.URL.Path, pathTorrentsInfo):
			fmt.Fprint(w, `[]`)
		default:
			t.Errorf("unexpected call to %s for a torrent that is not there", r.URL.Path)
		}
	}))
	defer srv.Close()

	if err := New(srv.URL, "u", "p", srv.Client()).DeleteTorrent(context.Background(), "AAAA", "curator", true); err != nil {
		t.Errorf("DeleteTorrent on a missing torrent: %v, want success", err)
	}
}

func TestDeleteTorrentRejectsAnEmptyHash(t *testing.T) {
	if err := New("http://127.0.0.1:9", "u", "p", nil).DeleteTorrent(context.Background(), "  ", "curator", true); err == nil {
		t.Error("an empty hash was accepted")
	}
}

// TestExternalAddressReadsTheServerState is the honest floor for a torrent
// client curator does not route: libtorrent's own idea of how it looks from the
// swarm, which is the only thing about an external client's exit that the Web
// API exposes at all.
//
// The body is shaped as qBittorrent 5.1.2 sends it (measured 2026-08-13).
func TestExternalAddressReadsTheServerState(t *testing.T) {
	stub := newStub(t, serveJSON(`{"server_state":{"last_external_address_v4":"187.14.240.8",
		"last_external_address_v6":"","connection_status":"connected","dht_nodes":365}}`))

	got, err := stub.client.ExternalAddress(context.Background())
	if err != nil {
		t.Fatalf("ExternalAddress: %v", err)
	}
	if got != "187.14.240.8" {
		t.Errorf("ExternalAddress = %q, want the v4 address", got)
	}
}

// A client that has never talked to a swarm has never learned its address. That
// is an empty string and not an error — the caller decides what to do with
// "unknown", and it is not the same answer as "unprotected".
func TestExternalAddressUnknownIsEmpty(t *testing.T) {
	stub := newStub(t, serveJSON(`{"server_state":{"last_external_address_v4":"","last_external_address_v6":""}}`))

	got, err := stub.client.ExternalAddress(context.Background())
	if err != nil {
		t.Fatalf("ExternalAddress: %v", err)
	}
	if got != "" {
		t.Errorf("ExternalAddress = %q, want empty for a client that has not learned one", got)
	}
}
