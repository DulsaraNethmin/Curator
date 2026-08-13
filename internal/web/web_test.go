package web

import (
	"embed"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// exportFS is a stand-in for a real Next.js export, including the `_next/`
// directory that is the whole reason the embed pattern needs `all:`. It is a
// fixture rather than the real export so these tests pass on a clone that has
// never run npm.
//
//go:embed all:testdata/export
var exportFS embed.FS

func fixture(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(exportFS, "testdata/export")
	if err != nil {
		t.Fatalf("sub: %v", err)
	}
	return sub
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// THE test of this package.
//
// go:embed drops every name beginning with "." or "_" unless the pattern says
// `all:`, and Next.js puts every script and stylesheet under `_next/`. Without
// the prefix the binary compiles, starts, serves index.html, and every asset
// 404s — a blank page with nothing wrong anywhere in the Go.
//
// This is asserted against the source directive rather than only against
// behaviour, because on a fresh clone dist/ holds nothing but a .gitkeep: there
// is no real export to serve, so no runtime test could catch the regression
// until someone deployed. Reading the one line that matters can.
func TestEmbedDirectiveUsesTheAllPrefix(t *testing.T) {
	source, err := os.ReadFile("web.go")
	if err != nil {
		t.Fatalf("read web.go: %v", err)
	}

	directive := regexp.MustCompile(`//go:embed\s+(\S*dist)\b`)
	match := directive.FindSubmatch(source)
	if match == nil {
		t.Fatal("no //go:embed directive for dist found in web.go")
	}
	if got := string(match[1]); got != "all:dist" {
		t.Errorf("//go:embed %s — want `all:dist`. Without the all: prefix, go:embed "+
			"silently drops _next/, which is where Next.js puts every script and stylesheet: "+
			"the binary compiles, serves index.html, and every asset on the page 404s.", got)
	}
}

// The behavioural half: a `_`-prefixed directory is embedded and served when the
// pattern is right. If `all:` were missing here, the fixture's app.js would not
// be in the binary at all and this fails.
func TestUnderscoreAssetsAreEmbeddedAndServed(t *testing.T) {
	fsys := fixture(t)

	if _, err := fs.Stat(fsys, "_next/static/app.js"); err != nil {
		t.Fatalf("_next/ is not in the embedded filesystem: %v", err)
	}

	rec := get(t, handlerFor(fsys), "/_next/static/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "all: prefix") {
		t.Errorf("body = %q, want the fixture asset", rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control = %q, want immutable — _next/ is content-hashed", got)
	}
}

func TestServesRoutesAsDirectories(t *testing.T) {
	h := handlerFor(fixture(t))

	for _, target := range []string{"/", "/search/"} {
		rec := get(t, h, target)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", target, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Errorf("GET %s Content-Type = %q, want html", target, ct)
		}
		// A redeployed binary must not be shadowed by a stale document pointing
		// at bundle hashes that no longer exist.
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q, want no-cache", target, got)
		}
	}

	// Typing the URL without the trailing slash still has to work: this is what
	// trailingSlash: true plus directory resolution buys.
	if rec := get(t, h, "/search"); rec.Code != http.StatusOK {
		t.Errorf("GET /search = %d, want 200 — every screen must be reachable by typing its URL", rec.Code)
	}
}

// A catch-all rewrite to index.html would make every wrong path answer 200 with
// the app, and a missing endpoint under /api answer HTML. Both are debugging
// afternoons.
func TestUnknownPathIs404AndNotTheApp(t *testing.T) {
	rec := get(t, handlerFor(fixture(t)), "/nope/")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<title>curator</title>") {
		t.Error("the unknown path was served index.html; a catch-all rewrite hides real 404s")
	}
	if !strings.Contains(rec.Body.String(), "no such page") {
		t.Errorf("body = %q, want the export's own 404 page", rec.Body)
	}
}

func TestTraversalCannotEscape(t *testing.T) {
	h := handlerFor(fixture(t))
	for _, target := range []string{"/../web.go", "/_next/../../web.go", "/%2e%2e/web.go"} {
		if rec := get(t, h, target); rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "package web") {
			t.Errorf("GET %s served a file outside the export", target)
		}
	}
}

// The fresh-clone state: dist/ holds only .gitkeep, so there is no index.html
// and the binary says so legibly instead of serving a white screen.
func TestPlaceholderIsServedWhenTheUIHasNotBeenBuilt(t *testing.T) {
	if !IsPlaceholder() {
		t.Skip("a real export is present in dist/; this test covers the fresh-clone state")
	}

	rec := get(t, Handler(), "/")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 — the UI is genuinely unavailable, and 200 would lie", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "npm --prefix web run build") {
		t.Errorf("the placeholder does not say how to build the UI: %q", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want html", ct)
	}
}

func TestIsPlaceholderTracksTheIndexFile(t *testing.T) {
	// The fixture is a real export, so its own index.html is present.
	if _, err := fs.Stat(fixture(t), indexFile); err != nil {
		t.Fatalf("the fixture has no %s: %v", indexFile, err)
	}
	// And the committed dist/ has none, which is what makes the placeholder the
	// fresh-clone default.
	if _, err := fs.Stat(distFS(), ".gitkeep"); err != nil {
		t.Errorf("dist/.gitkeep is missing — go:embed needs the directory to exist on a fresh clone: %v", err)
	}
}
