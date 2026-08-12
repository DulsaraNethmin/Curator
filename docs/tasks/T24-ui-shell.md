# T24 — The app shell and the build wiring

**Owns:** `web/` — `package.json`, `next.config.js`, `tsconfig.json`, `app/layout.tsx`,
`app/page.tsx`, `app/globals.css`, `lib/api.ts`, `components/` — and `.gitignore`
**Depends on:** T22

## Goal

A Next.js static export that lands where the Go embed expects it, one typed client for the API, and
the frame every screen sits in. No screen logic — T25 and T26 own that.

## Do

1. Scaffold `web/` **by hand**, not with `create-next-app`: it prompts, and it writes a template
   nobody asked for. App Router, TypeScript, no Tailwind unless it earns its place — five screens of
   tables do not need a utility framework, and plain CSS in `globals.css` is one fewer build step to
   explain.
2. `next.config.js`:
   ```js
   output: 'export',
   trailingSlash: true,           // dist/search/index.html, which http.FileServer resolves
   distDir: '.next',              // build cache stays here
   images: { unoptimized: true }, // there is no optimiser in a static export
   ```
   and an export destination of **`../internal/web/dist`** — `go:embed` cannot reach outside its own
   package directory, so the conventional `web/out` is unusable.
3. `.gitignore`: replace phase 1's stale `/web/out/` with `/internal/web/dist/*` and a negation for
   the committed placeholder, so `dist/index.html` survives and the real export never lands in a
   commit ([D16](../decisions.md#d16--the-ui-is-embedded-with-all-and-a-committed-placeholder-keeps-go-build-honest)).
4. `lib/api.ts` — one typed client, and the **only** place `fetch` appears:
   - the base URL is `process.env.NEXT_PUBLIC_API_BASE ?? ''`. Empty means same-origin, which is the
     embedded case; `next dev` sets it to `http://localhost:8090`. `output: 'export'` disables
     `rewrites`, so the usual dev proxy is not available and this is the substitute.
   - types mirroring the **existing** responses exactly, `null` included: `tmdb_id`, `overview`,
     `poster_path`, `library_path`, `size_bytes`, `imported_at` and `completed_at` are all nullable,
     and `magnet` on a release is `null` until it is resolved.
   - errors carry the API's `{"error": "..."}` shape and the **status**, because the screens branch
     on status: `410` means the search aged out, `503` means unconfigured, `502` means a dependency
     is down, and each deserves different words.
5. `app/layout.tsx` — a nav across the five screens, and nothing else clever. It must render
   correctly when reached by **typing a URL**, not only by client-side navigation.
6. A handful of shared components: an error banner that renders the API's message, a loading state
   that survives **thirteen seconds** without looking hung, and an empty state. These get used by
   every screen and are the difference between "slow" and "broken" to whoever is watching.
7. npm scripts: `dev`, `build`, `lint`. `build` writes the export and nothing else.

## Do not

- Add a state library, a component library, a data-fetching library or a CSS framework. Five screens
  over nine endpoints; `useState` and `fetch` are the whole requirement, and every dependency here is
  one more thing that has to still install in two years for the binary to be rebuildable.
- Use anything `output: 'export'` disables: server components doing I/O, `next/image` optimisation,
  middleware, route handlers, ISR, or `rewrites`. They fail at build time if you are lucky and at
  runtime if you are not.
- Call `fetch` anywhere but `lib/api.ts`.
- Commit `internal/web/dist/` beyond the placeholder, or `node_modules`.
- Change any Go file. T22 owns the mount; this task only has to land the files where it expects.

## Verify

```bash
npm --prefix web install && npm --prefix web run build
ls internal/web/dist/_next            # the directory that go:embed all: exists for
go build ./... && go run ./cmd/curator
```

- the export writes `internal/web/dist/index.html` and `internal/web/dist/_next/…`
- the binary serves the real UI, and the startup warning about the placeholder is **gone**
- every route is reachable by typing its URL, having never clicked a link
- `git status` is clean after a build — the export is ignored, the placeholder is not
- `next dev` with `NEXT_PUBLIC_API_BASE=http://localhost:8090` talks to a locally running curator
- the production build has **no** `NEXT_PUBLIC_API_BASE` baked in, so every call is same-origin
