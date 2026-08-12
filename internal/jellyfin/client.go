// Package jellyfin asks Jellyfin to rescan its library, and does nothing else.
//
// The one on the Pi is 10.10.7 at 192.168.1.26:8096, reached as
// http://jellyfin:8096 from inside Docker. There is no API key yet, and an
// unset key disables the refresh rather than failing startup
// (docs/decisions.md D15).
//
// The narrowness is the design. There are no item queries, no per-item
// refreshes, no user or session endpoints and no playback control, for the same
// reason internal/qbit cannot delete or pause a torrent: a method that does not
// exist cannot be called by mistake against a media server the household is
// watching. If a later phase needs more, it can add exactly what it needs.
//
// RefreshLibrary returns an error, deliberately. The guarantee that a refresh
// can never fail an import or a poll tick is implemented one layer up, at
// internal/importer's seam, where swallowing it is a considered decision made
// once. A client that hid its own failures could not be tested, and its live
// test could not fail on a bad status.
package jellyfin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is where Jellyfin listens when curator runs beside it. On the
// Pi it is 192.168.1.26:8096 from a laptop and http://jellyfin:8096 inside
// Docker; this default is for running on a laptop, which is where it gets typed
// most.
const DefaultBaseURL = "http://127.0.0.1:8096"

// requestTimeout bounds the one request this package makes. Jellyfin answers a
// refresh immediately — it queues the scan rather than performing it — so this
// is a ceiling for a wedged instance, not a budget.
const requestTimeout = 15 * time.Second

// pathLibraryRefresh queues a scan of every library. Jellyfin answers 204 and
// does the work in the background.
const pathLibraryRefresh = "/Library/Refresh"

// tokenHeader is how Jellyfin takes an API key. It can also be passed as an
// api_key query parameter, which this package deliberately does not use: a
// query string ends up in *url.Error messages, in access logs and in any proxy
// in between, and a header does not.
const tokenHeader = "X-Emby-Token"

// ErrUnauthorized reports that Jellyfin refused the API key rather than the
// request.
//
// It is separated from every other failure because the two want opposite
// handling: an unreachable Jellyfin is worth retrying on the next tick, and a
// revoked key is worth reporting once and not hammering. Test with errors.Is.
var ErrUnauthorized = errors.New("jellyfin rejected the API key")

// Client talks to one Jellyfin. The zero value is not usable; call New.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New returns a client for the Jellyfin at baseURL, empty meaning
// DefaultBaseURL. The base is a parameter so an httptest.Server can stand in,
// which is how every test here runs.
//
// httpClient is injected so callers share one connection pool; a nil one gets a
// default with a timeout, since http.DefaultClient has none and would hang
// forever.
func New(baseURL, apiKey string, httpClient *http.Client) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	return &Client{
		// Trailing slash trimmed here rather than at every use: pathLibraryRefresh
		// carries its own leading "/", and "…8096//Library/Refresh" is a 404 from
		// a server that would otherwise have worked.
		baseURL: strings.TrimSuffix(strings.TrimSpace(baseURL), "/"),
		apiKey:  apiKey,
		http:    httpClient,
	}
}

// RefreshLibrary asks Jellyfin to rescan every library.
//
// It returns as soon as the scan is queued — 204 No Content — not when the scan
// finishes, so a caller learns that Jellyfin accepted the request and nothing
// more. That is all an importer needs: the file is already hardlinked into the
// library and Jellyfin would find it on its own schedule regardless.
func (c *Client) RefreshLibrary(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	endpoint := c.baseURL + pathLibraryRefresh
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return fmt.Errorf("jellyfin refresh: build request: %w", err)
	}
	req.Header.Set(tokenHeader, c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("jellyfin refresh: calling jellyfin at %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	// Capped, then drained: the success case has no body at all, and the failure
	// case behind an unwell reverse proxy is a whole HTML error page that has no
	// business in an error string. Draining the rest keeps the connection
	// reusable.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	_, _ = io.Copy(io.Discard, resp.Body)

	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		// 204 is what 10.10.7 sends. 200 is accepted because older and
		// differently-proxied versions send it, and the distinction carries no
		// information we act on.
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		// Both mean the token rather than the request: 401 for a key Jellyfin
		// does not recognise, 403 for one whose user is not permitted to trigger
		// a scan. Neither is fixed by trying again.
		return fmt.Errorf("jellyfin refresh: jellyfin answered %d %s (%s): %w",
			resp.StatusCode, http.StatusText(resp.StatusCode), snippet(body), ErrUnauthorized)
	default:
		return fmt.Errorf("jellyfin refresh: jellyfin answered %d %s: %s",
			resp.StatusCode, http.StatusText(resp.StatusCode), snippet(body))
	}
}

// snippet renders the front of a body for an error message, capped and
// flattened to one line.
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
