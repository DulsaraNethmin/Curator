package indexer

import (
	"context"
	"encoding/json"
	"errors"
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
	// Asserted rather than remembered. minter binds IPv4 only, so "localhost"
	// resolves to ::1 first and the connection fails — the default was
	// http://localhost:8191 until T49, disagreeing with internal/config's,
	// CLAUDE.md's and the compose file's, and it is the kind of default nobody
	// notices because everything real passes MINTER_URL in.
	if strings.Contains(DefaultMinterURL, "localhost") {
		t.Errorf("DefaultMinterURL = %q: minter binds IPv4 only, so this must be the literal 127.0.0.1", DefaultMinterURL)
	}
}

// TestMinterProbe covers the three worlds the Indexers screen draws, and the
// one that used to be indistinguishable from a healthy minter.
func TestMinterProbe(t *testing.T) {
	// The measured body, byte for byte, from minter sha-adc1d6a on 2026-08-15.
	const healthy = `{"ok":true,"user_agent":"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:151.0) Gecko/20100101 Firefox/151.0","detail":{}}`

	t.Run("healthy", func(t *testing.T) {
		var path string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			w.Header().Set("content-type", "application/json")
			_, _ = io.WriteString(w, healthy)
		}))
		defer srv.Close()

		health, err := NewMinter(srv.URL).Probe(context.Background())
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if path != healthPath {
			t.Errorf("path = %q, want %q", path, healthPath)
		}
		if !health.OK {
			t.Error("OK was not decoded")
		}
		if !strings.Contains(health.UserAgent, "Firefox/151.0") {
			t.Errorf("UserAgent = %q, want minter's patched Firefox — it is the field that proves this is minter", health.UserAgent)
		}
	})

	t.Run("answering but not ready", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"ok":false,"user_agent":"","detail":{"browser":"starting"}}`)
		}))
		defer srv.Close()

		health, err := NewMinter(srv.URL).Probe(context.Background())
		// Not an error: something answered, and telling the user to run the
		// compose command again is the wrong instruction for a service that is
		// already there. The caller decides what ok=false means.
		if err != nil {
			t.Fatalf("Probe: %v, want a clean read reporting OK=false", err)
		}
		if health.OK {
			t.Error("OK = true for a minter that said false")
		}
	})

	t.Run("something else on the port", func(t *testing.T) {
		// Something is listening and has no /health. This is the case the
		// pre-T49 probe passed: it hit the ROOT and accepted any answer at all,
		// so anything on the port read as a healthy minter — including minter's
		// own root, which answers 307 rather than 200.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer srv.Close()

		_, err := NewMinter(srv.URL).Probe(context.Background())
		if err == nil {
			t.Fatal("want an error when /health is not there")
		}
		if errors.Is(err, ErrUnreachable) {
			t.Errorf("error = %v, want NOT ErrUnreachable — something IS listening, and "+
				"running the compose command again is the wrong instruction", err)
		}
		if !strings.Contains(err.Error(), "404") {
			t.Errorf("error = %v, want it to carry the status", err)
		}
	})

	t.Run("nothing listening", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := srv.URL
		srv.Close()

		_, err := NewMinter(url).Probe(context.Background())
		if err == nil {
			t.Fatal("want an error when minter is unreachable")
		}
		if !errors.Is(err, ErrUnreachable) {
			t.Errorf("error = %v, want ErrUnreachable — it is what turns a 1337x "+
				"search into an indexer reporting itself unconfigured", err)
		}
		if !strings.Contains(err.Error(), url) {
			t.Errorf("error = %v, want it to name the address that was tried", err)
		}
	})

	t.Run("undecodable body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "not json")
		}))
		defer srv.Close()

		if _, err := NewMinter(srv.URL).Probe(context.Background()); err == nil {
			t.Fatal("want an error for an undecodable body")
		}
	})

	t.Run("the context bounds it, not the 240 s client", func(t *testing.T) {
		// The whole reason Probe takes a context and does not lean on m.http's
		// own timeout: that one is sized for a browser clearing a Cloudflare
		// challenge, and a settings screen must not hang for four minutes.
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-release
		}))
		defer srv.Close()
		defer close(release)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := NewMinter(srv.URL).Probe(ctx); err == nil {
			t.Fatal("want an error for a cancelled context")
		}
	})
}

// TestMinterFetchUnreachableIsAttributable is the search half of the same fact.
// A 1337x search that fails because minter is not up has to be separable from
// one that fails because Cloudflare won, all the way up to the aggregator.
func TestMinterFetchUnreachableIsAttributable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	_, err := NewMinter(url).Fetch(context.Background(), "https://1337x.to/")
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("error = %v, want ErrUnreachable", err)
	}

	// And a minter that IS up and simply failed must not claim to be missing —
	// otherwise the screen offers a compose command for a running container.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "browser did not start")
	}))
	defer up.Close()

	_, err = NewMinter(up.URL).Fetch(context.Background(), "https://1337x.to/")
	if errors.Is(err, ErrUnreachable) {
		t.Errorf("error = %v, want NOT ErrUnreachable for a minter that answered 500", err)
	}
}
