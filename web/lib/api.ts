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

/**
 * LibraryState is what curator already has for a film, or null.
 *
 * `state` is a status the store writes, plus 'downloading', which
 * movies.status never says — the Go side derives it from the downloads table
 * so that a film at 60% is not labelled "wanted" on its poster.
 */
export type LibraryState = {
  movie_id: number;
  state: 'wanted' | 'downloading' | 'imported' | string;
  library_path?: string;
};

/**
 * MovieSummary is one film from TMDB as a poster grid shows it.
 *
 * This is the catalogue, not the library: `Movie` above is a row in curator's
 * database and this is a film that may not be. The only overlap is `library`,
 * which is non-null exactly when the two are the same film.
 *
 * poster_path and backdrop_path are '' rather than null when TMDB has no image
 * — the Go side omits nothing — so both are strings and posterURL treats the
 * empty one as absent.
 */
export type MovieSummary = {
  tmdb_id: number;
  title: string;
  year: number;
  overview: string;
  poster_path: string;
  backdrop_path: string;
  vote_average: number;
  library: LibraryState | null;
};

/** MovieDetails is a summary plus everything the movie screen shows. */
export type MovieDetails = MovieSummary & {
  tagline: string;
  runtime: number;
  genres: string[];
  status: string;
  release_date: string;
  original_language: string;
  spoken_languages: string[];
  studios: string[];
  homepage: string;
  imdb_id: string;
};

export type TMDBSearchResult = {
  query: string;
  year: number;
  results: MovieSummary[];
};

/**
 * DiscoverRow is one rail on the home screen.
 *
 * ok and error are the indexers[] shape deliberately: one rail failing is a
 * success carrying the other, and a failing source that renders as an empty row
 * is indistinguishable from a source with nothing in it.
 */
export type DiscoverRow = {
  id: string;
  title: string;
  ok: boolean;
  error?: string;
  results: MovieSummary[];
};

export type DiscoverResult = { rows: DiscoverRow[] };

export type LogEntry = {
  seq: number;
  time: string;
  level: 'DEBUG' | 'INFO' | 'WARN' | 'ERROR' | string;
  msg: string;
  attrs?: Record<string, string>;
};

export type LogsResult = {
  entries: LogEntry[];
  // The cursor to send back next poll, so an idle tick transfers an empty
  // array rather than the whole tail again.
  cursor: number;
  // How many lines fell off the ring before we asked. Surfaced rather than
  // hidden: a log with a silent gap is worse than one that admits the gap.
  missed: number;
  buffered: number;
};

export type Deletion = {
  title: string;
  year: number;
  library_path?: string;
  torrents_removed: number;
  bytes_freed: number;
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

  // The release-name search. It asks three indexers what files exist, which is
  // a different question from tmdbSearch's "which film is this" — and it is the
  // escape hatch for a film TMDB does not have, as well as the fallback when
  // there is no key.
  search: (title: string, year?: number, quality?: string) => {
    const query = new URLSearchParams({ title });
    if (year) query.set('year', String(year));
    if (quality) query.set('quality', quality);
    return request<SearchResult>(`/api/search?${query}`);
  },

  // Everything under /api/tmdb/ goes dark without a key, and the prefix is the
  // rule: these three answer 503 naming TMDB_API_KEY, while /api/movies stays
  // the library and keeps working.
  discover: () => request<DiscoverResult>('/api/tmdb/discover'),

  tmdbSearch: (query: string, year?: number) => {
    const params = new URLSearchParams({ query });
    if (year) params.set('year', String(year));
    return request<TMDBSearchResult>(`/api/tmdb/search?${params}`);
  },

  tmdbMovie: (id: number) => request<MovieDetails>(`/api/tmdb/movies/${id}`),

  downloads: () => request<Download[]>('/api/downloads'),

  dispatch: (body: {
    release_id: string;
    title: string;
    year: number;
    tmdb_id?: number | null;
  }) => request<Download>('/api/downloads', { method: 'POST', body: JSON.stringify(body) }),

  import: (hash: string) =>
    request<Movie>(`/api/downloads/${encodeURIComponent(hash)}/import`, { method: 'POST' }),

  // The only destructive call in the API. It removes the library folder, asks
  // qBittorrent to delete the downloaded file, and then deletes the rows.
  deleteMovie: (id: number) =>
    request<Deletion>(`/api/movies/${id}`, { method: 'DELETE' }),

  logs: (since = 0, level?: string, limit?: number) => {
    const query = new URLSearchParams({ since: String(since) });
    if (level) query.set('level', level);
    if (limit) query.set('limit', String(limit));
    return request<LogsResult>(`/api/logs?${query}`);
  },

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

export function formatRuntime(minutes: number): string | null {
  // 0 is TMDB's "we do not know", not a film of no length, so it is absent
  // rather than "0m".
  if (!minutes) return null;
  const hours = Math.floor(minutes / 60);
  return hours ? `${hours}h ${minutes % 60}m` : `${minutes}m`;
}

// TMDB serves poster_path as a bare path like "/abc.jpg"; the size segment is
// ours to choose. w342 is about right for a grid and costs a fifth of the
// original.
export function posterURL(path: string | null): string | null {
  return path ? `https://image.tmdb.org/t/p/w342${path}` : null;
}

// A backdrop is one image behind one title rather than forty in a grid, so it
// can afford w1280. There is no next/image optimiser in a static export, so the
// size is chosen here or not at all.
export function backdropURL(path: string | null): string | null {
  return path ? `https://image.tmdb.org/t/p/w1280${path}` : null;
}
