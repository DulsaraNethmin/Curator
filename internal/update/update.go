// Package update answers one question and delegates one action: is there a
// newer curator, and if so, get something else to install it.
//
// **curator never holds the Docker socket, and that is the whole design.** A
// container cannot replace itself — the image is FROM scratch and the process
// is uid 1000 with no shell (docs/tasks/T47-image.md) — so an in-app update
// button has to end up somewhere that can talk to the daemon. Mounting
// `/var/run/docker.sock` here would do it, and it would hand a media manager
// that answers on the LAN the ability to rewrite every container on the host.
// That is a far larger blast radius than this feature is worth, and it would
// undo the reason the image was built the way it was.
//
// So the split is: curator READS a version number over plain HTTPS, and ASKS an
// updater that already has the socket to do the work (docs/decisions.md D44).
// The updater is watchtower, which is already present on the box this was
// written for and is already trusted with exactly this job.
//
// The honest consequence, written down rather than discovered: **curator cannot
// report whether the update succeeded.** Triggering one restarts the container
// this code is running in, so the last thing this process does is send the
// request. The screen says "restarting", the connection drops, and the answer
// arrives as a page that loads again on the new version.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultCheckURL is GitHub's "latest release" endpoint for this project. It is
// unauthenticated because the repository is public, and it is the releases API
// rather than the registry's tag list because a tag is a string and a release
// carries the notes a person needs in order to decide.
const DefaultCheckURL = "https://api.github.com/repos/DulsaraNethmin/curator/releases/latest"

// checkTTL bounds how often GitHub is asked. Unauthenticated callers get 60
// requests an hour per address, and a household behind one address may run more
// than one curator, so this is deliberately far below the limit: nothing about
// a new release is urgent enough to spend a rate limit on.
const checkTTL = 6 * time.Hour

const (
	checkTimeout   = 10 * time.Second
	triggerTimeout = 30 * time.Second
	maxBody        = 1 << 20
)

// ErrNoUpdater reports that no updater is configured, so curator can say a
// version exists but cannot install it. It is a sentinel because it has a
// product answer rather than a technical one: the screen shows the command to
// paste instead of a button, which is the correct outcome on every install that
// runs curator without watchtower beside it.
var ErrNoUpdater = errors.New("no updater is configured")

// Status is what the screen renders.
type Status struct {
	Current   string    `json:"current"`
	Latest    string    `json:"latest,omitempty"`
	Available bool      `json:"available"`
	URL       string    `json:"url,omitempty"`
	Notes     string    `json:"notes,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitempty"`

	// CanInstall is whether an updater answered when this was configured — the
	// difference between an "Update now" button and a line of shell to copy.
	CanInstall bool `json:"can_install"`

	// Error is why the check could not be made, in prose. A failed check is not
	// an error state for the product: curator keeps working perfectly on the
	// version it has, so this is reported beside the current version rather
	// than instead of it.
	Error string `json:"error,omitempty"`
}

// Checker asks where the newest release is and remembers the answer.
type Checker struct {
	current string
	url     string
	http    *http.Client

	mu        sync.Mutex
	cached    Status
	checkedAt time.Time
}

// NewChecker returns a Checker for the running version. An empty url uses
// DefaultCheckURL.
func NewChecker(current, url string, client *http.Client) *Checker {
	if url == "" {
		url = DefaultCheckURL
	}
	if client == nil {
		client = &http.Client{Timeout: checkTimeout}
	}
	return &Checker{current: current, url: url, http: client}
}

// Check returns the cached status, refreshing it when it has gone stale. force
// skips the cache, which is what the button beside "last checked" does.
func (c *Checker) Check(ctx context.Context, force bool) Status {
	c.mu.Lock()
	fresh := !c.checkedAt.IsZero() && time.Since(c.checkedAt) < checkTTL
	if fresh && !force {
		out := c.cached
		c.mu.Unlock()
		return out
	}
	c.mu.Unlock()

	out := Status{Current: c.current}
	latest, url, notes, err := c.fetch(ctx)
	switch {
	case err != nil:
		out.Error = err.Error()
	default:
		out.Latest = latest
		out.URL = url
		out.Notes = notes
		out.Available = Newer(c.current, latest)
	}
	out.CheckedAt = time.Now()

	c.mu.Lock()
	// A failed check does not evict a good answer: the version that was known
	// five minutes ago is still the best thing to show when GitHub is briefly
	// unreachable, and blanking the screen would read as "no update" rather
	// than "could not ask".
	if err != nil && c.cached.Latest != "" {
		out.Latest = c.cached.Latest
		out.URL = c.cached.URL
		out.Notes = c.cached.Notes
		out.Available = c.cached.Available
	}
	c.cached = out
	c.checkedAt = out.CheckedAt
	c.mu.Unlock()

	return out
}

func (c *Checker) fetch(ctx context.Context) (version, url, notes string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("accept", "application/vnd.github+json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("could not reach the release feed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		// 403 here is nearly always the unauthenticated rate limit rather than
		// a permission problem, and saying so stops somebody hunting for a
		// token they do not need.
		if resp.StatusCode == http.StatusForbidden {
			return "", "", "", errors.New("the release feed refused the request, which is usually its hourly rate limit")
		}
		// 404 is "nothing has been released yet", not "something is broken".
		// It is the honest state of a project whose pipeline pushed tags and
		// images without ever creating a release object — which this one did
		// until T80 — and it must not read as a fault on the running install.
		if resp.StatusCode == http.StatusNotFound {
			return "", "", "", errors.New("no releases have been published yet")
		}
		return "", "", "", fmt.Errorf("the release feed answered %d %s",
			resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	var out struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Body    string `json:"body"`
		Draft   bool   `json:"draft"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", "", fmt.Errorf("the release feed did not answer JSON: %w", err)
	}
	if out.Draft {
		return "", "", "", errors.New("the newest release is still a draft")
	}
	return Normalise(out.TagName), out.HTMLURL, out.Body, nil
}

// Normalise strips the leading v from a git tag.
//
// The release pipeline already does this to produce the image tag —
// `version="${tag#v}"` in .github/workflows/release.yml — so the git tag is
// v0.1.0 while the running binary reports 0.1.0. Comparing the two without this
// makes every install think it is behind, forever.
func Normalise(tag string) string {
	return strings.TrimPrefix(strings.TrimSpace(tag), "v")
}

// Newer reports whether latest is a higher version than current.
//
// Deliberately not a string compare: "0.10.0" sorts BEFORE "0.9.0" as text, so
// the tenth release of a 0.x line would look like a downgrade and the update
// would never be offered. Fields are compared numerically, left to right.
//
// Anything it cannot parse is treated as NOT newer. A version this code does
// not understand must not produce an update prompt out of nothing — the failure
// worth having here is a missed notification, not a phantom one.
func Newer(current, latest string) bool {
	cur, okC := parse(current)
	lat, okL := parse(latest)
	if !okC || !okL {
		return false
	}
	for i := 0; i < 3; i++ {
		switch {
		case lat[i] > cur[i]:
			return true
		case lat[i] < cur[i]:
			return false
		}
	}
	return false
}

// parse reads major.minor.patch, ignoring any -rc.1 or +build suffix on the
// patch field. A missing field is zero, so "0.2" is 0.2.0.
func parse(v string) ([3]int, bool) {
	var out [3]int
	v = Normalise(v)
	if v == "" {
		return out, false
	}
	if cut := strings.IndexAny(v, "-+"); cut >= 0 {
		v = v[:cut]
	}
	parts := strings.Split(v, ".")
	if len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Updater asks something that holds the Docker socket to install the new image.
//
// watchtower is the one this was built against: `--http-api-update` exposes
// /v1/update, and a request carrying the token makes it pull and recreate. The
// token is never logged and never returned to a browser (docs/decisions.md D28
// puts it in the encrypted settings with the rest of the secrets).
type Updater struct {
	url   string
	token string
	http  *http.Client
}

// NewUpdater returns an Updater, or nil when no URL is configured — which is
// the normal state for an install that has no watchtower, and is why the
// Trigger path has to answer ErrNoUpdater rather than assuming one exists.
func NewUpdater(url, token string, client *http.Client) *Updater {
	url = strings.TrimRight(strings.TrimSpace(url), "/")
	if url == "" {
		return nil
	}
	if client == nil {
		client = &http.Client{Timeout: triggerTimeout}
	}
	return &Updater{url: url, token: strings.TrimSpace(token), http: client}
}

// Configured reports whether there is anything to ask, and it is deliberately
// the ONLY question this type answers about reachability.
//
// There was a Ping here that probed the updater before offering the button, and
// it was removed for a reason worth keeping written down: watchtower's
// /v1/update is a GET, so *probing it performs it*. Against an updater with no
// token, merely opening the Settings screen would have pulled a new image and
// restarted curator — a reachability check with a side effect of restarting the
// server it is checking on behalf of. There is no safe read-only endpoint to
// substitute, so the button is offered whenever a URL is configured and a wrong
// token is reported when it is pressed, which is the only moment an action
// endpoint should ever be touched.
func (u *Updater) Configured() bool { return u != nil && u.url != "" }

// Trigger asks the updater to pull and recreate.
//
// GET rather than POST, which looks wrong for something that changes the world
// and is not ours to choose: watchtower's documented contract for /v1/update is
// a GET carrying a bearer token, and matching somebody else's API matters more
// here than matching a convention.
func (u *Updater) Trigger(ctx context.Context) error {
	if !u.Configured() {
		return ErrNoUpdater
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.url+"/v1/update", nil)
	if err != nil {
		return err
	}
	if u.token != "" {
		req.Header.Set("Authorization", "Bearer "+u.token)
	}

	resp, err := u.http.Do(req)
	if err != nil {
		// The updater restarting curator IS the success case, and it can cut
		// this connection before a response arrives. That is reported by the
		// caller as "restarting" rather than as a failure, because the request
		// was already delivered by the time the socket died.
		return fmt.Errorf("calling the updater: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))

	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		// Named precisely: the updater is reachable and refused, which is a
		// wrong token and not a wrong address. Those two have different fixes
		// and the same symptom without this.
		return errors.New("the updater refused the token")
	default:
		return fmt.Errorf("the updater answered %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
}
