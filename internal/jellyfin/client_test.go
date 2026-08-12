package jellyfin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The happy path, and the assertions that pin the request itself: a client that
// posted to the wrong path or sent the key the wrong way would still see a 204
// from a permissive server.
func TestRefreshLibraryPostsTheRightRequest(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotToken  string
		gotQuery  string
		gotURL    string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotToken = r.Method, r.URL.Path, r.Header.Get(tokenHeader)
		gotQuery, gotURL = r.URL.RawQuery, r.URL.String()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := New(srv.URL, "s3cr3t", srv.Client()).RefreshLibrary(context.Background()); err != nil {
		t.Fatalf("RefreshLibrary: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotPath != pathLibraryRefresh {
		t.Errorf("path = %q, want %q", gotPath, pathLibraryRefresh)
	}
	if gotToken != "s3cr3t" {
		t.Errorf("%s = %q, want the API key", tokenHeader, gotToken)
	}
	// The key must not be reachable from a log line or a *url.Error, which is
	// why it goes in a header and not in api_key=.
	if gotQuery != "" || strings.Contains(gotURL, "s3cr3t") {
		t.Errorf("the API key leaked into the URL: query=%q url=%q", gotQuery, gotURL)
	}
}

// 204 is what 10.10.7 sends; 200 arrives from older versions and from some
// reverse proxies, and the distinction carries nothing we act on.
func TestRefreshLibraryAcceptsBoth204And200(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusOK} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		if err := New(srv.URL, "k", srv.Client()).RefreshLibrary(context.Background()); err != nil {
			t.Errorf("status %d: %v", status, err)
		}
		srv.Close()
	}
}

// The distinction the sentinel exists for. A revoked key is a configuration
// fault worth reporting once; an unwell Jellyfin is worth retrying next tick.
func TestRefreshLibraryReportsARejectedKeyAsErrUnauthorized(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		err := New(srv.URL, "wrong", srv.Client()).RefreshLibrary(context.Background())
		if !errors.Is(err, ErrUnauthorized) {
			t.Errorf("status %d: err = %v, want ErrUnauthorized", status, err)
		}
		srv.Close()
	}
}

func TestRefreshLibraryReportsAServerErrorAsSomethingElse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := New(srv.URL, "k", srv.Client()).RefreshLibrary(context.Background())
	if err == nil {
		t.Fatal("a 500 reported success")
	}
	if errors.Is(err, ErrUnauthorized) {
		t.Error("a 500 was reported as ErrUnauthorized; the sentinel would then mean nothing")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %q, want it to name the status", err)
	}
}

// A reverse proxy having a bad day answers with a whole HTML page, and none of
// it belongs in an error string.
func TestRefreshLibraryCapsAnHTMLErrorPage(t *testing.T) {
	page := "<html>\n<head><title>502 Bad Gateway</title></head>\n<body>" +
		strings.Repeat("x", 4000) + "</body>\n</html>"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	err := New(srv.URL, "k", srv.Client()).RefreshLibrary(context.Background())
	if err == nil {
		t.Fatal("a 502 reported success")
	}
	if len(err.Error()) > 512 {
		t.Errorf("error is %d bytes; the page was pasted in whole", len(err.Error()))
	}
	if strings.Contains(err.Error(), "\n") {
		t.Error("the error spans multiple lines")
	}
}

// A trailing slash is easy to type into JELLYFIN_URL and a doubled slash is a
// 404 from a server that would otherwise have worked.
func TestNewTrimsATrailingSlash(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := New(srv.URL+"/", "k", srv.Client()).RefreshLibrary(context.Background()); err != nil {
		t.Fatalf("RefreshLibrary: %v", err)
	}
	if gotPath != pathLibraryRefresh {
		t.Errorf("path = %q, want %q — a doubled slash is a 404", gotPath, pathLibraryRefresh)
	}
}

func TestNewDefaults(t *testing.T) {
	c := New("", "k", nil)
	if c.baseURL != DefaultBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, DefaultBaseURL)
	}
	// http.DefaultClient has no timeout and would hang for ever on a wedged
	// Jellyfin, so a nil client must not become that one.
	if c.http == http.DefaultClient {
		t.Fatal("a nil http.Client became http.DefaultClient, which has no timeout")
	}
	if c.http.Timeout <= 0 {
		t.Errorf("timeout = %v, want a positive one", c.http.Timeout)
	}
}

func TestRefreshLibraryReportsAnUnreachableJellyfin(t *testing.T) {
	// Port 9 is discard; nothing listens.
	const dead = "http://127.0.0.1:9"

	err := New(dead, "k", &http.Client{Timeout: 2 * time.Second}).RefreshLibrary(context.Background())
	if err == nil {
		t.Fatal("an unreachable Jellyfin reported success")
	}
	if !strings.Contains(err.Error(), dead) {
		t.Errorf("err = %q, want it to name the URL that could not be reached", err)
	}
	if errors.Is(err, ErrUnauthorized) {
		t.Error("an unreachable host was reported as a rejected key")
	}
}

func TestRefreshLibraryHonoursACancelledContext(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer func() { close(release); srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := New(srv.URL, "k", srv.Client()).RefreshLibrary(ctx); err == nil {
		t.Fatal("a cancelled context still refreshed")
	}
}
