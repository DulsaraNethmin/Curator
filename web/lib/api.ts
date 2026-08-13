// The only place in the UI that calls fetch.
//
// Every type here mirrors a response the Go side already produces and that
// phases 1-4 verified against live services. Nullable fields are nullable
// because the database says so, not defensively: tmdb_id is null for a folder
// TMDB could not match (D6), library_path is null for a film that is wanted but
// not yet on disk, and a release's magnet is null until it is resolved. A UI
// that assumes any of them exists breaks on the real library, where 15 of the
// 29 folders are empty.

// base is empty in the embedded build, which makes every call same-origin.
// `output: 'export'` disables rewrites, so the usual dev proxy does not exist
// and `next dev` sets this to http://localhost:8090 instead.
const base = process.env.NEXT_PUBLIC_API_BASE ?? '';

export type Movie = {
  id: number;
  tmdb_id: number | null;
  title: string;
  year: number;
  media_type: string;
  overview: string | null;
  poster_path: string | null;
  status: 'wanted' | 'downloading' | 'imported' | string;
  library_path: string | null;
  quality: string | null;
  size_bytes: number | null;
  added_at: string;
  imported_at: string | null;
};

export type DownloadState =
  | 'queued'
  | 'downloading'
  | 'completed'
  | 'imported'
  | 'failed';

export type Download = {
  id: number;
  movie_id: number;
  torrent_hash: string;
  indexer: string;
  release_name: string;
  magnet: string;
  state: DownloadState | string;
  progress: number; // 0..1, not a percentage
  added_at: string;
  completed_at: string | null;
};

export type Release = {
  id: string;
  title: string;
  year: number;
  quality: string;
  size_bytes: number;
  seeders: number;
  indexers: string[];
  // null until resolved. 1337x keeps its magnets on detail pages, so resolving
  // one costs a page fetch and a browser — it is deliberately lazy, and the UI
  // must never resolve a list to render it.
  magnet: string | null;
};

export type IndexerStatus = {
  name: string;
  ok: boolean;
  count: number;
  error?: string;
};

export type SearchResult = {
  title: string;
  year: number;
  releases: Release[];
  indexers: IndexerStatus[];
};

export type ScanResult = {
  scanned: number;
  added: number;
  matched: number;
  unmatched: number;
};

export type Integration = {
  name: string;
  env: string;
  configured: boolean;
  reachable?: boolean;
  detail?: string;
};

export type SettingsResult = {
  version: string;
  probed: boolean;
  integrations: Integration[];
  paths: Record<string, string>;
  intervals: Record<string, string>;
};

/**
 * ApiError carries the status as well as the message, because the screens
 * branch on it and each status deserves different words:
 *
 *   410  the search that issued this release id has aged out — the one error in
 *        the system whose correct fix is a user action
 *   503  the integration is unconfigured; say which variable to set
 *   502  a dependency is down; it is not our fault and not the user's
 *   409  the torrent has not finished
 *   422  nothing importable, or a title that cannot be a folder name
 */
export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }

  get expiredSearch() {
    return this.status === 410;
  }

  get unconfigured() {
    return this.status === 503;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let response: Response;
  try {
    response = await fetch(base + path, {
      ...init,
      headers: { 'content-type': 'application/json', ...(init?.headers ?? {}) },
    });
  } catch (cause) {
    // A transport failure is curator itself being unreachable, which is a
    // different sentence from any status it could return.
    throw new ApiError(0, `cannot reach curator at ${base || 'this origin'}: ${cause}`);
  }

  const text = await response.text();
  if (!response.ok) {
    // Every handler answers {"error": "..."} — phase 1 established the shape and
    // phases 2-4 kept it, so the message is worth showing verbatim.
    let message = text || response.statusText;
    try {
      const body = JSON.parse(text);
      if (typeof body?.error === 'string') message = body.error;
    } catch {
      // A non-JSON body means something in front of curator answered, not
      // curator. Showing it raw is more useful than hiding it.
    }
    throw new ApiError(response.status, message);
  }

  return (text ? JSON.parse(text) : null) as T;
}

export const api = {
  movies: () => request<Movie[]>('/api/movies'),
  scan: () => request<ScanResult>('/api/scan', { method: 'POST' }),

  search: (title: string, year?: number, quality?: string) => {
    const query = new URLSearchParams({ title });
    if (year) query.set('year', String(year));
    if (quality) query.set('quality', quality);
    return request<SearchResult>(`/api/search?${query}`);
  },

  downloads: () => request<Download[]>('/api/downloads'),

  dispatch: (body: {
    release_id: string;
    title: string;
    year: number;
    tmdb_id?: number | null;
  }) => request<Download>('/api/downloads', { method: 'POST', body: JSON.stringify(body) }),

  import: (hash: string) =>
    request<Movie>(`/api/downloads/${encodeURIComponent(hash)}/import`, { method: 'POST' }),

  // probe calls out to real services and one of them may wake a browser, so it
  // is opt-in: the page loads from configuration alone and checks on request.
  settings: (probe = false) =>
    request<SettingsResult>(`/api/settings${probe ? '?probe=1' : ''}`),
};

// --- formatting ------------------------------------------------------------

export function formatBytes(bytes: number | null): string {
  // 0 is not "unknown" here: 15 of the 29 real library folders are empty, so a
  // zero-size movie is the ordinary case and says something true.
  if (bytes === null) return '—';
  if (bytes === 0) return 'empty';

  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value < 10 && unit > 0 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
}

export function formatWhen(iso: string | null): string {
  if (!iso) return '—';
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '—';
  return date.toLocaleString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
}

// TMDB serves poster_path as a bare path like "/abc.jpg"; the size segment is
// ours to choose. w342 is about right for a grid and costs a fifth of the
// original.
export function posterURL(path: string | null): string | null {
  return path ? `https://image.tmdb.org/t/p/w342${path}` : null;
}
