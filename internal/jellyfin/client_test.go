package jellyfin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// --- the one lookup ---------------------------------------------------------

// itemsBody is a cut-down /Items response in the exact shape 10.10.7 sends,
// including the capitalised "Tmdb" provider key.
const itemsBody = `{"Items":[
 {"Id":"aaa","ServerId":"srv","Name":"Pulp Fiction","ProviderIds":{"Tmdb":"680","Imdb":"tt0110912"}},
 {"Id":"bbb","ServerId":"srv","Name":"Gone Girl","ProviderIds":{"Tmdb":"210577","Imdb":"tt2267998"}}
],"TotalRecordCount":2}`

// The happy path, and the assertions that pin the request. A client that sent
// the wrong path or dropped Recursive would still be handed this body by a
// permissive fake, so the request is checked rather than only the answer.
func TestFindMovieAsksForTheRightThingAndMatchesOnTMDBID(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(itemsBody))
	}))
	defer srv.Close()

	item, err := New(srv.URL, "s3cr3t", srv.Client()).FindMovie(context.Background(), 210577, 2014)
	if err != nil {
		t.Fatalf("FindMovie: %v", err)
	}
	if item.ID != "bbb" || item.ServerID != "srv" {
		t.Errorf("item = %+v, want {bbb srv} — it matched the wrong film", item)
	}

	if got.URL.Path != pathItems {
		t.Errorf("path = %q, want %q", got.URL.Path, pathItems)
	}
	if got.Method != http.MethodGet {
		t.Errorf("method = %s, want GET — this lookup must never write", got.Method)
	}
	if got.Header.Get(tokenHeader) != "s3cr3t" {
		t.Errorf("%s = %q, want the API key", tokenHeader, got.Header.Get(tokenHeader))
	}
	// The key must not be reachable from a log line or a *url.Error, exactly as
	// for the refresh.
	if strings.Contains(got.URL.String(), "s3cr3t") {
		t.Errorf("the API key leaked into the URL: %q", got.URL.String())
	}

	q := got.URL.Query()
	for key, want := range map[string]string{
		"Recursive":        "true",
		"IncludeItemTypes": "Movie",
		"Fields":           "ProviderIds",
		"years":            "2014",
	} {
		if q.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, q.Get(key), want)
		}
	}
	// Measured on the real 10.10.7: with a userId the library shrinks to what
	// that user may see — 18 items became 16 — and this link is for whoever is
	// holding the laptop, not for the API key's owner.
	if q.Has("userId") || q.Has("UserId") {
		t.Errorf("the lookup sent a userId, which narrows the library to one user: %q", got.URL.RawQuery)
	}
	// Both are measured to be silently ignored by 10.10.7 (docs/decisions.md
	// D32). Sending one would be a filter that looks like it works.
	if q.Has("Path") || q.Has("AnyProviderIdEquals") {
		t.Errorf("the lookup sent a parameter Jellyfin ignores: %q", got.URL.RawQuery)
	}
}

// A zero year skips the narrowing rather than the query: TMDB has no release
// date for some 2026 releases, and that must cost a bigger response, not a
// missing link.
func TestFindMovieOmitsTheYearWhenThereIsNone(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(itemsBody))
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "k", srv.Client()).FindMovie(context.Background(), 680, 0); err != nil {
		t.Fatalf("FindMovie: %v", err)
	}
	if gotQuery.Has("years") {
		t.Errorf("years = %q, want it absent for a film with no year", gotQuery.Get("years"))
	}
}

// The provider key is "Tmdb" on 10.10.7. A version that wrote "TMDB" must not
// silently stop finding anything.
func TestFindMovieMatchesTheProviderKeyCaseInsensitively(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"Items":[{"Id":"x","ServerId":"s","ProviderIds":{"TMDB":"680"}}]}`))
	}))
	defer srv.Close()

	item, err := New(srv.URL, "k", srv.Client()).FindMovie(context.Background(), 680, 1994)
	if err != nil {
		t.Fatalf("FindMovie: %v", err)
	}
	if item.ID != "x" {
		t.Errorf("item.ID = %q, want x", item.ID)
	}
}

// A film Jellyfin does not have is ErrNotFound and never an error the caller
// would log as a fault: the page falls back to a search link, which is a fine
// outcome and not a failure.
func TestFindMovieReportsAMissAsErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(itemsBody))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "k", srv.Client()).FindMovie(context.Background(), 999999999, 2026)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// An item with no TMDB id at all — a film Jellyfin never matched — is a miss
// and not a panic on a nil map.
func TestFindMovieSurvivesAnItemWithNoProviderIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"Items":[{"Id":"x","ServerId":"s"},{"Id":"y","ServerId":"s","ProviderIds":null}]}`))
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "k", srv.Client()).FindMovie(context.Background(), 680, 1994); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// A revoked key is distinguished from every other failure, because the two want
// opposite handling — and because "Open in Jellyfin is missing" is otherwise a
// question with no evidence anywhere.
func TestFindMovieReportsARevokedKey(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		_, err := New(srv.URL, "k", srv.Client()).FindMovie(context.Background(), 680, 1994)
		if !errors.Is(err, ErrUnauthorized) {
			t.Errorf("status %d: err = %v, want ErrUnauthorized", status, err)
		}
		if errors.Is(err, ErrNotFound) {
			t.Errorf("status %d: a rejected key was reported as a missing film", status)
		}
		srv.Close()
	}
}

// A Jellyfin that is switched off is a transport failure and specifically NOT
// ErrNotFound: the difference is whether the film is absent or the server is.
func TestFindMovieReportsAnUnreachableJellyfin(t *testing.T) {
	// Port 9 is discard; nothing listens.
	_, err := New("http://127.0.0.1:9", "k", &http.Client{Timeout: 2 * time.Second}).
		FindMovie(context.Background(), 680, 1994)
	if err == nil {
		t.Fatal("an unreachable Jellyfin reported success")
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want a transport failure", err)
	}
}

// The lookup sits inside a page load, so it carries its own deadline rather
// than inheriting the fifteen seconds a background refresh can afford.
func TestFindMovieGivesUpOnAWedgedJellyfin(t *testing.T) {
	if testing.Short() {
		t.Skip("this one waits out lookupTimeout")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	start := time.Now()
	_, err := New(srv.URL, "k", srv.Client()).FindMovie(context.Background(), 680, 1994)
	if err == nil {
		t.Fatal("a Jellyfin that never answers reported success")
	}
	if elapsed := time.Since(start); elapsed > lookupTimeout*3 {
		t.Errorf("gave up after %v, want about %v — a page is waiting on this", elapsed, lookupTimeout)
	}
}

// The link the web client itself builds for a Movie, grepped out of
// main.jellyfin.bundle.js on the real 10.10.7 rather than guessed.
func TestWebItemURL(t *testing.T) {
	const want = "http://192.168.1.26:8096/web/index.html#/details?id=bbb&serverId=srv"
	for _, base := range []string{"http://192.168.1.26:8096", "http://192.168.1.26:8096/"} {
		if got := WebItemURL(base, Item{ID: "bbb", ServerID: "srv"}); got != want {
			t.Errorf("WebItemURL(%q) = %q, want %q", base, got, want)
		}
	}
	// No public URL and no item are both "there is no link", and the UI draws
	// nothing rather than a dead one.
	if got := WebItemURL("", Item{ID: "bbb"}); got != "" {
		t.Errorf("WebItemURL with no base = %q, want empty", got)
	}
	if got := WebItemURL("http://x:8096", Item{}); got != "" {
		t.Errorf("WebItemURL with no item = %q, want empty", got)
	}
}

func TestWebSearchURL(t *testing.T) {
	got := WebSearchURL("http://192.168.1.26:8096", "Spider-Man: No Way Home")
	const want = "http://192.168.1.26:8096/web/index.html#/search.html?query=Spider-Man%3A+No+Way+Home"
	if got != want {
		t.Errorf("WebSearchURL = %q, want %q", got, want)
	}
	if got := WebSearchURL("", "Jaws"); got != "" {
		t.Errorf("WebSearchURL with no base = %q, want empty", got)
	}
}
