package indexer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// decodeFetchTarget asserts the request is a well-formed POST /fetch and returns
// the URL it asked minter to render.
func decodeFetchTarget(t *testing.T, r *http.Request) string {
	t.Helper()
	if r.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", r.Method)
	}
	if r.URL.Path != "/fetch" {
		t.Errorf("path = %s, want /fetch", r.URL.Path)
	}
	if ct := r.Header.Get("content-type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	var req struct {
		URL     string `json:"url"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode request body %q: %v", body, err)
	}
	if req.Timeout != fetchTimeoutSeconds {
		t.Errorf("timeout = %d, want %d", req.Timeout, fetchTimeoutSeconds)
	}
	return req.URL
}

func writeFetchResponse(t *testing.T, w http.ResponseWriter, html string) {
	t.Helper()
	w.Header().Set("content-type", "application/json")
	if err := json.NewEncoder(w).Encode(Page{
		Solved:    true,
		HTML:      html,
		FinalURL:  "https://1337x.to/",
		UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:151.0) Gecko/20100101 Firefox/151.0",
		ElapsedMS: 8879,
	}); err != nil {
		t.Fatalf("encode fetch response: %v", err)
	}
}

func TestMinterFetch(t *testing.T) {
	var target string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target = decodeFetchTarget(t, r)
		writeFetchResponse(t, w, "<html><body>hello</body></html>")
	}))
	defer srv.Close()

	page, err := NewMinter(srv.URL).Fetch(context.Background(), "https://1337x.to/search/Interstellar/1/")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if target != "https://1337x.to/search/Interstellar/1/" {
		t.Errorf("minter was asked for %q", target)
	}
	if !page.Solved {
		t.Error("Solved was not decoded")
	}
	if page.HTML != "<html><body>hello</body></html>" {
		t.Errorf("HTML = %q", page.HTML)
	}
	if page.ElapsedMS != 8879 {
		t.Errorf("ElapsedMS = %d", page.ElapsedMS)
	}
	if !strings.Contains(page.UserAgent, "Firefox/151.0") {
		t.Errorf("UserAgent = %q", page.UserAgent)
	}
}

func TestMinterTrailingSlashInBaseURL(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		writeFetchResponse(t, w, "<html></html>")
	}))
	defer srv.Close()

	if _, err := NewMinter(srv.URL+"/").Fetch(context.Background(), "https://1337x.to/"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if path != "/fetch" {
		t.Errorf("path = %q, want /fetch — a configured trailing slash must not double up", path)
	}
}

func TestMinterFetchErrors(t *testing.T) {
	t.Run("non-200 carries minter's message", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "  browser did not start\n")
		}))
		defer srv.Close()

		_, err := NewMinter(srv.URL).Fetch(context.Background(), "https://1337x.to/")
		if err == nil {
			t.Fatal("want an error for a 500")
		}
		if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "browser did not start") {
			t.Errorf("error = %v, want it to carry the status and minter's body", err)
		}
	})

	t.Run("garbage body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "not json")
		}))
		defer srv.Close()

		if _, err := NewMinter(srv.URL).Fetch(context.Background(), "https://1337x.to/"); err == nil {
			t.Fatal("want an error for an undecodable body")
		}
	})

	t.Run("minter not running", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := srv.URL
		srv.Close() // nothing is listening now

		_, err := NewMinter(url).Fetch(context.Background(), "https://1337x.to/")
		if err == nil {
			t.Fatal("want an error when minter is unreachable")
		}
		// The hint matters: a dead minter is the most common failure and the
		// message should say which address was tried.
		if !strings.Contains(err.Error(), "is it running") || !strings.Contains(err.Error(), url) {
			t.Errorf("error = %v, want it to name the address and hint that minter may be down", err)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeFetchResponse(t, w, "<html></html>")
		}))
		defer srv.Close()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := NewMinter(srv.URL).Fetch(ctx, "https://1337x.to/"); err == nil {
			t.Fatal("want an error for a cancelled context")
		}
	})
}

func TestNewMinterDefaultsURL(t *testing.T) {
	if got := NewMinter("").baseURL; got != DefaultMinterURL {
		t.Errorf("baseURL = %q, want %q", got, DefaultMinterURL)
	}
}
