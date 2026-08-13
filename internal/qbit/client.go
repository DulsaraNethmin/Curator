// Package qbit is a client for qBittorrent's Web API v2 — the one on the Pi is
// 5.1.2, sharing gluetun's network namespace at http://gluetun:8080.
//
// It can log in, add a magnet, read torrents back, and delete a torrent that is
// in a category it was told to require. It deliberately cannot pause, resume or
// reprioritise anything: the *arr stack shares this qBittorrent until phase 6
// (docs/phase-3.md), and a method that does not exist cannot be called by
// mistake at three in the morning.
//
// Delete was added for D19 and is the one destructive call here, so it carries
// its own guard rather than trusting its caller: it looks the torrent up first
// and refuses unless the category matches what the caller demanded. A hash
// belonging to radarr cannot be removed through this client even if something
// hands it one.
//
// Three things about this API are worth knowing before reading the code.
//
// Authentication is a session cookie, not a header. POST /api/v2/auth/login takes
// the username and password as form values and answers with an SID cookie that
// every later call carries; the cookie expires, and an expired one is reported
// exactly as a wrong password is — 403. So the client logs in once, and on a 403
// re-authenticates exactly once and retries. A second 403 is a real failure and
// is reported as one rather than retried in a loop.
//
// torrents/add answers "200 Ok." whether or not it accepted the magnet, and never
// returns a hash. AddMagnet therefore promises nothing beyond "qBittorrent
// received this"; confirming the add is the caller's job, via TorrentByHash on
// the hash taken from the magnet itself.
//
// Hashes on the wire are lower-case, because that is how qBittorrent reports
// them and what its `hashes=` filter demands, while indexer.InfoHash and the
// downloads table use upper-case. That difference stops at this package's edge:
// wireHash keeps the lower-case form for the API and torrent.NormalizeHash puts
// the upper-case one on everything that leaves.
//
// What leaves is a torrent.Torrent — the six fields curator reads, with the
// state already translated into curator's own vocabulary. This package is one
// of two backends behind download.TorrentClient (docs/decisions.md D22), and none of
// qBittorrent's vocabulary crosses that boundary.
package qbit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// DefaultBaseURL is where qBittorrent listens when curator runs beside it. In
// Docker it is http://gluetun:8080 — qBittorrent has no ports of its own, because
// it shares gluetun's network namespace — and from a laptop on the LAN it is
// http://192.168.1.26:8080 (measured 2026-08-12, docs/phase-3.md).
const DefaultBaseURL = "http://127.0.0.1:8080"

// requestTimeout bounds one HTTP round trip. qBittorrent is on the LAN and
// answers in milliseconds; this ceiling exists so that a wedged instance fails a
// poll tick rather than parking the poller forever.
const requestTimeout = 15 * time.Second

// The API root and the three endpoints this client uses. Nothing that mutates a
// torrent beyond adding one is listed, on purpose — see the package comment.
const (
	apiPrefix          = "/api/v2"
	pathLogin          = "auth/login"
	pathTorrentsAdd    = "torrents/add"
	pathTorrentsInfo   = "torrents/info"
	pathTorrentsDelete = "torrents/delete"
)

// sessionCookie is the cookie qBittorrent issues on login and expects back.
const sessionCookie = "SID"

// The plain-text bodies qBittorrent answers with. Both endpoints say "Ok." and
// mean quite different things by it: from auth/login it is a real success, while
// from torrents/add it means only that the request was parsed.
const (
	bodyOK    = "Ok."
	bodyFails = "Fails."
)

// ErrAuth reports that qBittorrent refused who we are rather than what we asked
// for. It is a configuration fault — a wrong password, or an account the Web UI
// will not admit — and it is separated from every other failure because callers
// must not treat it as "qBittorrent is down" and retry it. Test with errors.Is.
var ErrAuth = errors.New("qbittorrent refused the credentials")

// info is one entry of GET /api/v2/torrents/info, as qBittorrent sends it. It
// is unexported because it is a wire format: what this package hands out is a
// torrent.Torrent, and the two are deliberately not the same shape.
//
// Only what curator reads is decoded; qBittorrent sends around forty more
// fields. `size` and `save_path` used to be decoded here and were read by
// nothing for three phases, so they are gone — a field nobody reads is a field
// that can be wrong indefinitely without anyone noticing.
type info struct {
	// Hash is lower-case hex, normalised by wireHash on the way in.
	Hash string `json:"hash"`

	// Name is qBittorrent's display name, which for a magnet is the `dn` until
	// metadata arrives and the real torrent name replaces it.
	Name string `json:"name"`

	// State is qBittorrent's own vocabulary ("stalledUP", "metaDL", …), kept
	// verbatim so a vocabulary change stays visible in a log line. mapState
	// turns it into curator's on the way out.
	State string `json:"state"`

	// Progress is 0..1, not a percentage.
	Progress float64 `json:"progress"`

	// ContentPath is where the payload lives *in qBittorrent's namespace*:
	// /downloads/complete/curator/… , because its mount is host
	// /media/storage/media/downloads → container /downloads. That path need not
	// exist on the machine curator runs on. Phase 3 stores what qBittorrent said
	// and translates nothing; the translation belongs in phase 4, where the
	// hardlink is made (docs/decisions.md D13).
	ContentPath string `json:"content_path"`

	// Category is what scopes a torrent to us: everything curator adds is in
	// `curator`, and the *arr stack's torrents are in `radarr` and `sonarr`.
	Category string `json:"category"`
}

// neutral is the translation out of qBittorrent's world and into curator's:
// the hash changes case, the state changes vocabulary, and nothing else moves.
func (i info) neutral() torrent.Torrent {
	return torrent.Torrent{
		Hash:        torrent.NormalizeHash(i.Hash),
		Name:        i.Name,
		State:       mapState(i.State),
		Progress:    i.Progress,
		ContentPath: i.ContentPath,
		Category:    i.Category,
	}
}

// Client talks to one qBittorrent. It is safe for concurrent use; the zero value
// is not usable, call New.
type Client struct {
	baseURL  string
	username string
	password string
	http     *http.Client

	// mu guards epoch and serialises logins, so that N callers meeting an expired
	// session cost one login between them rather than N.
	mu sync.Mutex

	// epoch counts successful logins: 0 means "never logged in", and a caller that
	// took a 403 while holding epoch e knows its session is stale only if epoch is
	// still e. Without it, two concurrent 403s would each log in, and the second
	// login would invalidate the session the first had just started using.
	epoch uint64
}

// New returns a client for the qBittorrent at baseURL, empty meaning
// DefaultBaseURL. The base is a parameter so an httptest.Server can stand in,
// which is how every test here runs.
//
// httpClient is injected so callers share one connection pool; a nil one gets a
// default with a timeout, since http.DefaultClient has none and would hang
// forever.
func New(baseURL, username, password string, httpClient *http.Client) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}

	// The caller's client is shallow-copied rather than modified. http.Client has
	// only exported fields and no internal state, so the copy shares the caller's
	// Transport — and its connection pool — while getting a cookie jar of its own.
	// Assigning the jar to the caller's client instead would clobber any jar they
	// had set and would race with the goroutines already using it, since curator
	// hands the same client to TMDB, the indexers and this.
	session := *httpClient
	// cookiejar.New only ever returns a nil error for nil options; the second
	// return value exists for options this package does not pass. The jar keys
	// cookies by host and handles a bare IP as a host-only cookie, which matters:
	// the Pi is reached at 192.168.1.26, not by name.
	jar, _ := cookiejar.New(nil)
	session.Jar = jar

	return &Client{
		baseURL:  strings.TrimSuffix(strings.TrimSpace(baseURL), "/"),
		username: username,
		password: password,
		http:     &session,
	}
}

// AddMagnet adds one magnet under a category, applying the same name as a tag on
// the same request (docs/decisions.md D13: the category sets the save path and
// scopes every later poll, the tag makes ownership visible in the Web UI). An
// empty category adds neither, which leaves the torrent in qBittorrent's default
// save path — callers in this project always pass one.
//
// A nil error means qBittorrent received the request. It does not mean the
// torrent was added. torrents/add answers "200 Ok." both for a magnet it took and
// for one it silently ignored, and it never returns a hash, so there is nothing
// here to report and nothing to trust. The caller confirms the add by taking the
// info hash from the magnet (indexer.InfoHash) and looking it up with
// TorrentByHash — the same class of lie as minter's 200-carrying-a-failure, which
// this repo has been bitten by before.
//
// Adding a magnet qBittorrent already has is not an error: it is idempotent, and
// re-dispatching a release after a restart should converge rather than fail.
func (c *Client) AddMagnet(ctx context.Context, magnet, category string) error {
	if strings.TrimSpace(magnet) == "" {
		return errors.New("qbit torrents/add: empty magnet")
	}

	form := url.Values{}
	form.Set("urls", magnet)
	if category = strings.TrimSpace(category); category != "" {
		form.Set("category", category)
		form.Set("tags", category)
	}

	// Form-encoded, though the API reference shows multipart/form-data for this
	// endpoint: qBittorrent parses urlencoded posts on every endpoint alike, and
	// this is one body format fewer to build. If adds ever start being ignored,
	// suspect this line first — the endpoint answers "Ok." either way.
	body, err := c.do(ctx, http.MethodPost, pathTorrentsAdd, nil, form)
	if err != nil {
		return err
	}

	// "Fails." is the one 200 body that means qBittorrent accepted nothing. Every
	// other body, including an empty one, is treated as received — because
	// "received" is the most a 200 from this endpoint ever proves.
	if strings.EqualFold(strings.TrimSpace(string(body)), bodyFails) {
		return fmt.Errorf("qbit torrents/add: qBittorrent answered %q, having accepted no magnet", bodyFails)
	}
	return nil
}

// Torrents lists the torrents in a category — one request per call, however many
// torrents are in flight, which is what makes a poll tick cost a single round
// trip.
//
// An empty category lists *every* torrent, the *arr stack's included, because
// qBittorrent reads a present-but-empty category parameter as "torrents with no
// category at all" and the parameter is therefore omitted rather than sent empty.
// Callers that mean ours pass "curator".
//
// No torrents is (nil, nil): an empty category is a normal answer, not a failure.
func (c *Client) Torrents(ctx context.Context, category string) ([]torrent.Torrent, error) {
	query := url.Values{}
	if category = strings.TrimSpace(category); category != "" {
		query.Set("category", category)
	}

	found, err := c.torrents(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}

	torrents := make([]torrent.Torrent, 0, len(found))
	for _, i := range found {
		torrents = append(torrents, i.neutral())
	}
	return torrents, nil
}

// TorrentByHash looks one torrent up by info hash, in either case: the hash is
// normalised on the way in, because qBittorrent reports lower-case hex and
// indexer.InfoHash returns upper-case, and comparing them raw finds nothing
// forever.
//
// A nil Torrent with a nil error means qBittorrent does not have it. Absence is
// the normal answer to "did the add work?", not an error.
//
// The lookup is not scoped to a category: the hash is the identity, and a release
// the *arr stack downloaded first will be found by it, carrying Category "radarr".
// Callers that need ownership as well as existence must check Torrent.Category.
func (c *Client) TorrentByHash(ctx context.Context, hash string) (*torrent.Torrent, error) {
	wire := wireHash(hash)
	if wire == "" {
		return nil, errors.New("qbit torrents/info: look up a torrent by an empty hash")
	}

	// hashes= is qBittorrent's own filter, so the usual answer is a list of one.
	found, err := c.torrents(ctx, url.Values{"hashes": {wire}})
	if err != nil {
		return nil, err
	}
	for i := range found {
		// Checked again on our side rather than trusting the filter: if a future
		// qBittorrent ignored an unknown parameter instead of applying it, the
		// reply would be every torrent on the instance and the first of them is
		// emphatically not ours.
		if strings.EqualFold(found[i].Hash, wire) {
			neutral := found[i].neutral()
			return &neutral, nil
		}
	}
	return nil, nil
}

// DeleteTorrent removes a torrent, and its downloaded files when deleteFiles is
// true. See docs/decisions.md D19.
//
// requireCategory is not optional in practice and should never be empty: the
// torrent is looked up first and the delete is refused unless its category
// matches. That check lives here, at the lowest level, rather than only in the
// caller, because this is the one call in this package that destroys something.
//
// A torrent that is already gone is success, not an error. Delete is the kind of
// operation that gets retried after a partial failure, and a retry that fails
// because the work was already done is a retry nobody can complete.
func (c *Client) DeleteTorrent(ctx context.Context, hash, requireCategory string, deleteFiles bool) error {
	wire := wireHash(hash)
	if wire == "" {
		return errors.New("qbit torrents/delete: delete a torrent by an empty hash")
	}

	existing, err := c.TorrentByHash(ctx, wire)
	if err != nil {
		return err
	}
	if existing == nil {
		return nil // already gone; nothing to do and nothing to report
	}
	if requireCategory != "" && !strings.EqualFold(existing.Category, requireCategory) {
		return fmt.Errorf("qbit torrents/delete: %s is in category %q, not %q: %w",
			wire, existing.Category, requireCategory, torrent.ErrWrongCategory)
	}

	form := url.Values{}
	form.Set("hashes", wire)
	form.Set("deleteFiles", strconv.FormatBool(deleteFiles))

	if _, err := c.do(ctx, http.MethodPost, pathTorrentsDelete, nil, form); err != nil {
		return err
	}
	return nil
}

// wireHash puts an info hash in the case qBittorrent's API uses: lower, which is
// what it reports and what its `hashes=` filter matches on.
//
// It is unexported because it is a fact about this wire protocol and about
// nothing else. Curator's own form is upper-case — indexer.InfoHash produces it
// and the downloads table is keyed on it — and torrent.NormalizeHash is the one
// exported normaliser. Two exported functions with the same name doing opposite
// things is a trap this package no longer sets.
func wireHash(hash string) string {
	return strings.ToLower(strings.TrimSpace(hash))
}

// torrents performs one torrents/info call and decodes it. It returns the wire
// shape; the callers translate, because TorrentByHash still has to compare
// against qBittorrent's own lower-case hash first.
func (c *Client) torrents(ctx context.Context, query url.Values) ([]info, error) {
	body, err := c.do(ctx, http.MethodGet, pathTorrentsInfo, query, nil)
	if err != nil {
		return nil, err
	}

	var torrents []info
	if err := json.Unmarshal(body, &torrents); err != nil {
		return nil, fmt.Errorf("qbit torrents/info: qBittorrent's reply is not the JSON array we expect (%s): %w", snippet(body), err)
	}
	for i := range torrents {
		torrents[i].Hash = wireHash(torrents[i].Hash)
	}
	return torrents, nil
}

// do performs an authenticated request, logging in first if this client never
// has, and re-authenticating exactly once on a 403 before retrying.
//
// One retry, not a loop: an expired session and a wrong password are the same
// status code, and spending one login is the only way to tell them apart. If the
// answer is 403 again with a session qBittorrent has just issued, the session was
// never the problem.
func (c *Client) do(ctx context.Context, method, path string, query, form url.Values) ([]byte, error) {
	epoch, err := c.session(ctx)
	if err != nil {
		return nil, err
	}

	reply, err := c.send(ctx, method, path, query, form)
	if err != nil {
		return nil, err
	}

	if reply.status == http.StatusForbidden {
		if err := c.refresh(ctx, epoch); err != nil {
			return nil, fmt.Errorf("qbit %s: qBittorrent answered 403, and logging in again: %w", path, err)
		}
		if reply, err = c.send(ctx, method, path, query, form); err != nil {
			return nil, err
		}
		if reply.status == http.StatusForbidden {
			return nil, fmt.Errorf("qbit %s: qBittorrent answered 403 twice, the second time carrying a session it had just issued to %q — so this is the credentials or that account's Web UI permissions, not an expired session: %w", path, c.username, ErrAuth)
		}
	}

	if reply.status != http.StatusOK {
		return nil, fmt.Errorf("qbit %s: qBittorrent answered %d %s: %s", path, reply.status, http.StatusText(reply.status), snippet(reply.body))
	}
	return reply.body, nil
}

// session returns the epoch of the session the caller is about to use, logging in
// if this client never has. Logging in before every request would be a round trip
// per call to prove a session that lasts an hour is still there.
func (c *Client) session(ctx context.Context) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.epoch == 0 {
		if err := c.login(ctx); err != nil {
			return 0, err
		}
		c.epoch++
	}
	return c.epoch, nil
}

// refresh logs in again, unless another goroutine has already replaced the
// session the caller used. That check is what keeps a burst of concurrent 403s to
// one login: without it, each caller would log in, and each login would hand the
// others a session that was stale by the time they retried.
func (c *Client) refresh(ctx context.Context, used uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.epoch != used {
		return nil
	}
	if err := c.login(ctx); err != nil {
		return err
	}
	c.epoch++
	return nil
}

// login exchanges the username and password for an SID cookie, which the jar then
// carries on every later request. Callers hold c.mu.
func (c *Client) login(ctx context.Context) error {
	form := url.Values{}
	form.Set("username", c.username)
	form.Set("password", c.password)

	reply, err := c.send(ctx, http.MethodPost, pathLogin, nil, form)
	if err != nil {
		return err
	}

	switch reply.status {
	case http.StatusOK:
	case http.StatusForbidden:
		// qBittorrent bans the *client IP* after enough failed logins and says so
		// with a 403 on the login endpoint itself. Retrying is the one thing that
		// makes that worse, so it is reported and not retried.
		return fmt.Errorf("qbit login as %q: qBittorrent refused the login with 403, which is what it answers once an IP is banned for repeated failures (%s): %w", c.username, snippet(reply.body), ErrAuth)
	default:
		return fmt.Errorf("qbit login as %q: qBittorrent answered %d %s: %s", c.username, reply.status, http.StatusText(reply.status), snippet(reply.body))
	}

	// The 200 that lies: a wrong username or password comes back 200 with the body
	// "Fails.", not 401. Reading the body is the only way to tell a login from a
	// rejection.
	if !strings.EqualFold(strings.TrimSpace(string(reply.body)), bodyOK) {
		return fmt.Errorf("qbit login as %q: qBittorrent answered 200 %s, which is its reply to a username or password it does not accept: %w", c.username, snippet(reply.body), ErrAuth)
	}

	for _, cookie := range reply.cookies {
		if cookie.Name == sessionCookie && cookie.Value != "" {
			return nil
		}
	}
	// "Ok." with no cookie would leave every later call 403ing at a client that
	// believes it is logged in. Said plainly here, it costs one line to diagnose;
	// left alone it looks exactly like bad credentials.
	return fmt.Errorf("qbit login as %q: qBittorrent answered %q but set no %s cookie, so nothing after this could be authenticated", c.username, bodyOK, sessionCookie)
}

// reply is one HTTP answer, read to the end so the connection can be reused.
type reply struct {
	status  int
	body    []byte
	cookies []*http.Cookie
}

// send performs exactly one request. It does not authenticate, retry, or look at
// the status: do and login own those, and login must not recurse through do.
func (c *Client) send(ctx context.Context, method, path string, query, form url.Values) (*reply, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	endpoint := c.baseURL + apiPrefix + "/" + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	var body io.Reader
	if form != nil {
		// The password rides in the body, not the query, so it stays out of
		// *url.Error's message, out of access logs and out of any proxy in between.
		body = strings.NewReader(form.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("qbit %s: build request: %w", path, err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Accept", "application/json")
	// No Referer or Origin header is sent. qBittorrent's CSRF check rejects one
	// that disagrees with its own host but permits a request carrying neither, and
	// guessing the host it thinks it has is how that check starts failing. If a
	// login ever 403s with credentials known to be right, a Referer of c.baseURL
	// is the first thing to try.

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qbit %s: calling qBittorrent at %s: %w", path, c.baseURL, err)
	}
	defer resp.Body.Close()

	// Read whole, not capped: torrents/info is a JSON array whose size is set by
	// how many torrents we own, and a truncated body would surface as a baffling
	// decode error instead of an honest one.
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("qbit %s: read qBittorrent's reply: %w", path, err)
	}
	return &reply{status: resp.StatusCode, body: raw, cookies: resp.Cookies()}, nil
}

// snippet renders the front of a body for an error message. It is capped and
// flattened to one line because a qBittorrent behind a proxy that is unwell
// answers with an HTML error page, and none of that belongs in an error string.
func snippet(body []byte) string {
	flat := strings.Join(strings.Fields(string(body)), " ")
	if flat == "" {
		return "no body"
	}
	if len(flat) > 256 {
		flat = flat[:256] + "…"
	}
	return strconv.Quote(flat)
}
