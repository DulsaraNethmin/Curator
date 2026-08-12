package indexer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultMinterURL matches minter's own default and the MINTER_URL default in
// docs/architecture.md.
const DefaultMinterURL = "http://localhost:8191"

const (
	// fetchTimeoutSeconds is what minter is told to spend driving the browser.
	fetchTimeoutSeconds = 180

	// minterClientTimeout must outlast fetchTimeoutSeconds, because minter starts
	// a real browser and waits out the challenge before it replies at all. A
	// cleared non-interactive challenge measures ~9 s end to end; the ceiling is
	// for the pathological case, not the normal one.
	minterClientTimeout = 240 * time.Second
)

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
		return nil, fmt.Errorf("calling minter at %s (is it running?): %w", m.baseURL, err)
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
