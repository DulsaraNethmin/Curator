/**
 * The UI is a static export embedded in the Go binary — there is no Node
 * process at runtime, so everything in Next.js that needs a server is
 * unavailable: server components doing I/O, route handlers, middleware, ISR,
 * image optimisation, and `rewrites`.
 *
 * That last one is the surprise. The usual "proxy /api to the backend during
 * development" trick is a rewrite, so it does not exist here; `lib/api.ts`
 * reads NEXT_PUBLIC_API_BASE instead, which is empty in the embedded build
 * (making every call same-origin) and http://localhost:8090 under `next dev`.
 *
 * @type {import('next').NextConfig}
 */
const nextConfig = {
  output: 'export',

  // Writes dist/search/index.html rather than dist/search.html, which is the
  // layout http.FileServer resolves with no rewrite rules on the Go side —
  // and it is what makes typing a URL directly work for every screen.
  trailingSlash: true,

  // There is no optimiser in a static export. Posters are plain <img> tags
  // pointed at TMDB.
  images: { unoptimized: true },

  // Fail the build on a type error rather than shipping one.
  typescript: { ignoreBuildErrors: false },
};

export default nextConfig;
