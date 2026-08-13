// Package web serves the embedded UI: a Next.js static export compiled into the
// binary, so curator ships as one artifact and one process.
//
// Two things here are load-bearing and neither is obvious.
//
// The embed pattern is `all:dist`, and the prefix is not decoration. go:embed
// excludes every file and directory whose name begins with "." or "_" unless a
// pattern says `all:`, and Next.js puts EVERY script and stylesheet under
// `_next/`. Drop the prefix and the binary still compiles, still starts, still
// serves index.html — and every asset on the page 404s, with nothing wrong
// anywhere in the Go. See docs/decisions.md D16.
//
// dist/ holds only a .gitkeep in a fresh clone, because go:embed is a
// COMPILE-TIME directive: a missing directory is a build failure, and the real
// export is generated and gitignored. So `go build ./...` on its own always
// works and produces a complete API; it just serves placeholder.html until
// `npm --prefix web run build` has run.
package web

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist is the Next.js export. `all:` is required — see the package comment.
//
//go:embed all:dist
var dist embed.FS

// placeholder is served when the UI has not been built. It lives outside dist/
// so that a build, which wipes and rewrites that directory, cannot delete it —
// and so `git status` stays clean after a build.
//
//go:embed placeholder.html
var placeholder []byte

// indexFile is what a directory request resolves to. The export is generated
// with trailingSlash: true precisely so that every route is a directory holding
// one of these, which needs no rewrite rules on this side.
const indexFile = "index.html"

// assetPrefix is Next.js's content-hashed output. Everything under it is
// immutable by construction: a changed file gets a changed name.
const assetPrefix = "/_next/"

// Handler serves the embedded UI. Mount it at "/" LAST, after the API patterns
// — Go 1.22 routing prefers the more specific pattern, but the ordering is
// worth being deliberate about rather than lucky.
func Handler() http.Handler { return handlerFor(distFS()) }

// IsPlaceholder reports that the binary is carrying the placeholder rather than
// a real UI, so main can say so once at startup. Silence is how somebody ships
// a binary that serves "run npm" to the household.
func IsPlaceholder() bool {
	_, err := fs.Stat(distFS(), indexFile)
	return err != nil
}

func distFS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// Unreachable: the directory is embedded at compile time, so this can
		// only fail if the embed directive itself is malformed, which does not
		// compile either.
		panic("web: embedded dist is unreadable: " + err.Error())
	}
	return sub
}

// handlerFor is Handler over any filesystem, so the tests can drive a fixture
// export on a clone that has never run npm.
func handlerFor(fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(fsys, indexFile); err != nil {
			servePlaceholder(w)
			return
		}

		name, ok := resolve(fsys, r.URL.Path)
		if !ok {
			// The export's own 404 page if it produced one, so a mistyped URL
			// looks like the rest of the site. Deliberately NOT index.html: a
			// catch-all rewrite means a wrong path answers 200 with the app,
			// and a missing endpoint under /api answering HTML is a debugging
			// afternoon.
			serveNotFound(w, r, fsys)
			return
		}

		setCacheControl(w, r.URL.Path, name)
		serveFile(w, r, fsys, name)
	})
}

// resolve maps a URL path onto a file in the export, following the directory
// convention trailingSlash: true produces.
func resolve(fsys fs.FS, urlPath string) (string, bool) {
	// path.Clean collapses "..", so a traversal cannot reach outside the
	// embedded tree even though embed.FS would refuse it anyway.
	name := strings.TrimPrefix(path.Clean("/"+urlPath), "/")
	if name == "" {
		name = indexFile
	}

	info, err := fs.Stat(fsys, name)
	if err == nil && info.IsDir() {
		name = path.Join(name, indexFile)
		info, err = fs.Stat(fsys, name)
	}
	if err != nil || info.IsDir() {
		return "", false
	}
	return name, true
}

// setCacheControl is two rules, and they must disagree.
//
// _next/ files are content-hashed, so they can be cached for a year; an HTML
// document must not be, or a redeployed binary is shadowed by a stale page
// pointing at bundles whose hashes no longer exist — a white screen that
// survives a reload and is fixed only by a hard refresh nobody thinks to try.
func setCacheControl(w http.ResponseWriter, urlPath, name string) {
	switch {
	case strings.HasPrefix(urlPath, assetPrefix):
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case strings.HasSuffix(name, ".html"):
		w.Header().Set("Cache-Control", "no-cache")
	default:
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
}

func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) {
	f, err := fsys.Open(name)
	if err != nil {
		serveNotFound(w, r, fsys)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		http.Error(w, "unreadable", http.StatusInternalServerError)
		return
	}

	// embed.FS files are seekable, which gets range requests and content-type
	// sniffing for free. The fallback is for any fs.FS a test might pass.
	if seeker, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, name, info.ModTime(), seeker)
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), strings.NewReader(readAll(f)))
}

func serveNotFound(w http.ResponseWriter, _ *http.Request, fsys fs.FS) {
	w.Header().Set("Cache-Control", "no-cache")
	if body, err := fs.ReadFile(fsys, "404.html"); err == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write(body)
		return
	}
	http.Error(w, "not found", http.StatusNotFound)
}

func servePlaceholder(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write(placeholder)
}

func readAll(f fs.File) string {
	b, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	return string(b)
}
