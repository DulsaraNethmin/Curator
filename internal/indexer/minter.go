package indexer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultMinterURL is the literal IPv4 address, not "localhost": minter binds
// IPv4 only, so localhost resolves to ::1 first and the connection fails while
// 127.0.0.1 works. Inside Docker the service name "minter" resolves correctly
// and compose.yaml's 1337x profile is what puts it there.
//
// It matches internal/config's defaultMinterURL, and that agreement is the
// point — a second spelling here is a default nothing outside this package
// would ever be running on.
const DefaultMinterURL = "http://127.0.0.1:8191"

const (
	// fetchTimeoutSeconds is what minter is told to spend driving the browser.
	fetchTimeoutSeconds = 180

	// minterClientTimeout must outlast fetchTimeoutSeconds, because minter starts
	// a real browser and waits out the challenge before it replies at all. A
	// cleared non-interactive challenge measures ~9 s end to end; the ceiling is
	// for the pathological case, not the normal one.
	minterClientTimeout = 240 * time.Second

	// healthPath is minter's own health endpoint, and it is the probe rather than
	// the root for a measured reason: the root answers 307, so a probe that
	// accepts "anything answered" also accepts a redirect from something that is
	// not minter at all. /health answers 200 with {"ok":…}, which is a fact worth
	// asserting.
	healthPath = "/health"
)

// ErrUnreachable reports that nothing answered at minter's address — the
// container is not up, the URL is wrong, or the network is.
//
// It is a sentinel because it is the one minter failure with a *product* answer
// rather than a technical one: minter lives behind compose's 1337x profile
// (docs/decisions.md D34, docs/phase-9.md), so "nothing answered" almost always
// means the pasted `docker compose --profile 1337x up -d` has not been run. The
// aggregator turns it into an indexer reporting itself unconfigured instead of
// reporting no results, which is the whole of T49.
var ErrUnreachable = errors.New("minter could not be reached")

// Page is minter's POST /fetch response: a page already rendered by a real
// browser, with any Cloudflare challenge cleared.
type Page struct {
	Solved    bool   `json:"solved"`
	HTML      string `json:"html"`
	FinalURL  string `json:"final_url"`
	UserAgent string `json:"user_agent"`
	ElapsedMS int    `json:"elapsed_ms"`
}

// Minter is a client for the minter service, which gets past Cloudflare.
//
// Every page comes back through the browser. We do not mint a cf_clearance cookie
// and replay it over plain HTTP: Cloudflare binds the cookie to the exit IP, the
// User-Agent *and* the TLS fingerprint, and no off-the-shelf uTLS profile
// reproduces minter's patched Firefox 151 — JA3, JA4 and the HTTP/2 SETTINGS all
// differ, and a replayed cookie gets a 403 indistinguishable from having none.
// This was measured; see docs/decisions.md D2 before trying to be clever here.
type Minter struct {
	baseURL string
	http    *http.Client
}

// NewMinter returns a client for the minter at baseURL, or DefaultMinterURL if
// baseURL is empty.
func NewMinter(baseURL string) *Minter {
	if baseURL == "" {
		baseURL = DefaultMinterURL
	}
	return &Minter{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: minterClientTimeout},
	}
}

// URL is where this client looks for minter, after the default has been applied
// and any trailing slash trimmed.
//
// It exists so a probe can name the address it failed to reach without the
// caller keeping a second copy of the string it passed in — which is how a
// screen ends up telling somebody to check a URL other than the one that was
// actually used.
func (m *Minter) URL() string { return m.baseURL }

// Health is what GET /health answers.
//
// Measured against minter sha-adc1d6a on 2026-08-15: 200 with
// {"ok":true,"user_agent":"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:151.0)
// Gecko/20100101 Firefox/151.0","detail":{}}. The user agent is reported
// because it is the one field that proves this is minter's patched Firefox
// rather than something else answering on the port — D2's whole argument is
// that no other fingerprint substitutes for it.
type Health struct {
	OK        bool
	UserAgent string
}

// Probe asks minter whether it is there, and it must stay cheap: the Settings
// screen calls it on a loop while a container starts.
//
// It deliberately does NOT go through Fetch. A probe that rendered a page would
// wake a real Firefox to answer "are you up", and it would do it every time
// somebody left the Settings tab open.
//
// /health is far cheaper than that, but it is NOT free, and this comment used
// to claim it answered "in milliseconds". Measured on the Pi, read out of
// minter's own HEALTHCHECK log: two consecutive healthy checks took 8.61 s and
// 6.73 s, both returning {"ok":true}. minter serves /health from the process
// that drives the browser and waits on the same lock, so it answers when the
// browser is free rather than on demand. A caller has to budget for that — see
// minterProbeTimeout in internal/api/indexers.go, which was 5 s on the strength
// of the sentence above and sat below minter's healthy floor.
//
// The context bounds it. m.http's own timeout is 240 s, sized for a browser
// clearing a challenge, so a probe that relied on it would hang a screen for
// four minutes against an address nothing is listening on.
func (m *Minter) Probe(ctx context.Context) (Health, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.baseURL+healthPath, nil)
	if err != nil {
		return Health{}, fmt.Errorf("build minter health request for %s: %w", m.baseURL, err)
	}
	req.Header.Set("accept", "application/json")

	resp, err := m.http.Do(req)
	if err != nil {
		// unreachable{} carries ErrUnreachable AND the transport error, so a
		// caller that ran out of time can still see context.DeadlineExceeded in
		// here — but only if it looks for it FIRST. Test the deadline before
		// ErrUnreachable, or a minter that is merely busy reads as one that was
		// never started (internal/api/indexers.go's handleMinterProbe).
		return Health{}, unreachable{fmt.Errorf("calling minter at %s: %w", m.baseURL, err)}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxHealthBody))
	_, _ = io.Copy(io.Discard, resp.Body)
	if err != nil {
		return Health{}, fmt.Errorf("read minter health from %s: %w", m.baseURL, err)
	}
	if resp.StatusCode != http.StatusOK {
		// Not ErrUnreachable: something IS listening, and telling the user to
		// run the compose command again is the wrong instruction for a service
		// that is answering. The root's own 307 lands here, which is exactly the
		// case a "did anything answer" probe used to pass.
		return Health{}, fmt.Errorf("minter at %s answered %d %s to %s: %s",
			m.baseURL, resp.StatusCode, http.StatusText(resp.StatusCode), healthPath,
			strings.TrimSpace(string(raw)))
	}

	var out struct {
		OK        bool   `json:"ok"`
		UserAgent string `json:"user_agent"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Health{}, fmt.Errorf("decode minter health from %s: %w", m.baseURL, err)
	}
	return Health{OK: out.OK, UserAgent: out.UserAgent}, nil
}

// maxHealthBody caps what a probe will read. /health is 119 bytes; the cap is
// for whatever is on the port when it is not minter.
const maxHealthBody = 8 << 10

// unreachable carries ErrUnreachable alongside the transport error rather than
// instead of it, so that both errors.Is(err, ErrUnreachable) and
// errors.Is(err, context.DeadlineExceeded) answer truthfully. It is the same
// shape internal/jellyfin uses, for the same reason.
type unreachable struct{ err error }

func (u unreachable) Error() string   { return u.err.Error() }
func (u unreachable) Unwrap() []error { return []error{u.err, ErrUnreachable} }

// Fetch renders target through minter's browser and returns the resulting page.
func (m *Minter) Fetch(ctx context.Context, target string) (*Page, error) {
	// A map of string and int cannot fail to marshal.
	body, _ := json.Marshal(map[string]any{"url": target, "timeout": fetchTimeoutSeconds})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/fetch", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build minter request for %s: %w", target, err)
	}
	req.Header.Set("content-type", "application/json")

	resp, err := m.http.Do(req)
	if err != nil {
		// ErrUnreachable rather than a bare wrap, so that a 1337x search failing
		// because minter is not up is distinguishable from one failing because
		// Cloudflare won: the first is an indexer reporting itself unconfigured
		// with a command to run, the second is a real failure. Both used to
		// arrive here as the same opaque string.
		return nil, unreachable{fmt.Errorf("calling minter at %s (is it running?): %w", m.baseURL, err)}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read minter response for %s: %w", target, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("minter returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out Page
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode minter response for %s: %w", target, err)
	}
	return &out, nil
}
