# T22 — Embed the UI and serve it

**Owns:** `internal/web/` — `web.go`, `web_test.go`, `dist/index.html` (the committed placeholder) —
plus the mount in `cmd/curator/main.go` and the `.gitignore` entry
**Depends on:** nothing

## Goal

Put a static site inside the binary and serve it from the same port as the API, without either one
being able to shadow the other.

## Do

1. `//go:embed all:dist` — **`all:` is not optional.** `go:embed` excludes names beginning with `.`
   or `_`, and Next.js puts every script and stylesheet under `_next/`. Without it the binary
   compiles, starts, serves `index.html`, and every asset 404s with nothing wrong anywhere in the Go
   ([D16](../decisions.md#d16--the-ui-is-embedded-with-all-and-a-committed-placeholder-keeps-go-build-honest)).
2. Commit **`dist/index.html`** as a placeholder: a real page saying the UI has not been built, with
   the command that builds it. `//go:embed` is a compile-time directive, so without a committed file
   a fresh clone — and every commit under `git bisect` — fails to build.
3. `Handler() http.Handler` over `fs.Sub(dist)`, mounted at `/` **last**, so `/api/…` and `/healthz`
   keep their patterns. Go 1.22 routing prefers the more specific pattern, but the ordering is worth
   being deliberate about rather than lucky.
4. A request for a path that is not in the filesystem returns the UI's own **404 page** if the export
   produced one, and a plain 404 otherwise. It must **not** fall back to `index.html` for anything
   under `/api/` — a missing endpoint answering 200 with HTML is a debugging afternoon.
5. Cache headers, and they differ by path. Everything under `_next/` is content-hashed and gets
   `Cache-Control: public, max-age=31536000, immutable`; every HTML document gets `no-cache`, so a
   redeployed binary is not shadowed by a stale page pointing at bundles that no longer exist.
6. `IsPlaceholder() bool`, so `cmd/curator` can log one warning at startup when the binary is
   carrying the placeholder rather than a real UI. Silence there is how somebody ships a binary that
   serves a "run npm" page to the household.

## Do not

- Import `internal/api`, `internal/store` or anything else. This package serves bytes.
- Add a catch-all rewrite to `index.html`. The export is a real multi-page site with
  `trailingSlash: true`; directory resolution is `http.FileServer`'s job and it does it correctly.
- Commit the real build output — see D16 for why the two-command build is the cheaper cost.
- Serve from disk, or take a directory flag. One binary, one artifact; a `-ui-dir` escape hatch is
  how the embedded copy stops being the one that gets tested.

## Verify

`go test -race ./internal/web`, plus the checks the trap deserves:

- **the embedded filesystem contains `_next/`** — walk it and assert. This is the `all:` test, and it
  must fail if someone drops the prefix. Use a fixture directory in the test rather than the real
  export, so it passes on a clone that has never run npm.
- `GET /` serves HTML; `GET /search/` serves that route's `index.html`
- `GET /_next/static/<file>` returns **200** with the immutable cache header
- an HTML response carries `no-cache`
- an unknown path is 404 and is **not** `index.html`
- `/api/movies` and `/healthz` still route to their handlers with the UI mounted — assert against the
  real mux from `cmd/curator`'s wiring, not a hand-built one
- `IsPlaceholder()` is true for the committed `dist` and false for a fixture with a real export
- `go build ./...` succeeds with only the placeholder present, which is the fresh-clone case
