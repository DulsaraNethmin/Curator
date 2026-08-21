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

/**
 * The two kinds of thing curator handles, spelled exactly as the API spells
 * them — `store.MediaTypeMovie` and `store.MediaTypeTV`, which is what
 * `?media=` and `media_type` are validated against on the Go side.
 *
 * Absent means `movie` on every request that takes one, because every request
 * made before phase 11 was one. A screen that has not been told about
 * television keeps meaning what it meant.
 */
export type MediaType = 'movie' | 'tv';

export type Movie = {
  id: number;
  tmdb_id: number | null;
  title: string;
  /** The FOLDER's year — what `Title (Year)` on disk says, and what the importer
   * writes back out. Not necessarily the film's: see tmdb_year. */
  year: number;
  /** TMDB's release year, and null unless the two can disagree — which only
   * happens for a row somebody matched by hand. Nothing on screen reads it; it
   * is what the server asks Jellyfin with, and it is here so the type matches
   * the row the API actually sends. */
  tmdb_year: number | null;
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

/**
 * MovieRow is one library row as `GET /api/movies/{id}` answers it: everything
 * in `Movie`, plus the one fact that is not in the database.
 *
 * The list endpoint answers `Movie` and not this — a Jellyfin lookup costs a
 * request per film and the Library screen asks for every row at once.
 */
export type MovieRow = Movie & {
  /**
   * Where to open this film in Jellyfin, absent when there is no link to draw —
   * no Jellyfin configured, or a film that is not on disk. Exactly the rule
   * `MovieDetails.jellyfin_url` follows, and for a row TMDB never matched it is
   * always a Jellyfin *search* (D32's fallback), because there is no id to look
   * the film up by.
   */
  jellyfin_url?: string;
};

export type DownloadState =
  | 'queued'
  | 'downloading'
  | 'completed'
  | 'imported'
  | 'failed'
  // Added, wanted, and getting nowhere. Phase 6 shipped the state and this
  // union was not told; `| string` below is why nothing complained.
  | 'stalled';

export type Download = {
  id: number;
  movie_id: number;
  torrent_hash: string;
  indexer: string;
  release_name: string;
  magnet: string;
  state: DownloadState | string;
  progress: number; // 0..1, not a percentage

  // Why this download is getting nowhere, in prose, on `stalled` rows.
  //
  // Optional because the server omits the key rather than sending "": there is
  // no sentence, so there is no field. It is rendered verbatim — the backend
  // that knows why writes the words, and nothing here maps a code onto a
  // string, which would be a second vocabulary to keep in step with the Go one.
  reason?: string;

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

  /**
   * The indexer never started, rather than tried and failed: the companion
   * service it needs is not answering. Today that is 1337x and minter.
   *
   * A separate flag from `ok` because the two lead to different actions and
   * only one of them is the user's — a failed indexer is something to retry, an
   * unconfigured one is a container that was never started. Absent, not false,
   * on every ordinary outcome.
   */
  unconfigured?: boolean;

  /**
   * The indexer was never asked, because this media type is not one it has.
   * YTS is the case it exists for: it is `/list_movies.json` and has no
   * television surface at all, so a TV search skips it rather than sending it a
   * question it cannot answer.
   *
   * **It arrives with `ok: false`**, like the other two, and that is why this
   * field has to be read. Drawn as a failure it paints YTS red with no message
   * on every television search — which is worse than the `ok:true, count:0` lie
   * T90 removed, because it sends somebody hunting a broken indexer. Three
   * states, three actions, and this is the one where the action is none: a
   * failed indexer is something to retry, an unconfigured one is a container to
   * start, and a source that will never have television is neither.
   */
  not_applicable?: boolean;
};

export type SearchResult = {
  title: string;
  year: number;

  /**
   * Which kind of thing was searched for, echoed by the server rather than
   * remembered here. A search that skipped an indexer has to say what it
   * skipped it FOR, and the answer belongs to the request that was actually
   * made — not to whatever the screen's controls say by the time it lands.
   *
   * `| string` because a media type added later must not fail the build of a UI
   * that has not been rewritten yet, which is the rule every other union in
   * this file follows.
   */
  media: MediaType | string;

  releases: Release[];
  indexers: IndexerStatus[];
};

export type ScanResult = {
  /**
   * Folders that hold a FILM, which is narrower than it sounds: a folder with
   * nothing playable in it is not a movie, and `empty` counts those. The two
   * belong together — a library of 29 folders reporting 2 films has to explain
   * itself.
   */
  scanned: number;
  added: number;
  matched: number;
  unmatched: number;
  /** Folders on disk with no film in them. */
  empty: number;
  /** Rows removed. The folders themselves were left exactly where they are. */
  removed: number;
  /** Rows kept because this scan could not account for them — absent, unreadable, or a name that no longer parses. */
  missing: number;
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

  /**
   * Where to open this film in Jellyfin, absent when there is no link to draw.
   *
   * Absent rather than '' in the two states that mean the same thing to a
   * screen: no Jellyfin configured, and a film curator does not have on disk.
   * The UI draws nothing for an absent one — no disabled button and no tooltip
   * explaining what you are missing.
   *
   * It is a deep link to the item when the Go side found one and a link to a
   * Jellyfin search when it did not, and this deliberately cannot tell which.
   * Both land somewhere useful and the difference is not one anybody can act on.
   */
  jellyfin_url?: string;
};

/**
 * PlaybackURLs is what POST /api/movies/{id}/playback answers: how this film
 * can be played, in one round trip.
 *
 * The two URLs are kept apart on purpose. `stream_url` is relative and
 * same-origin, so a <video src> sends the session cookie by itself and no
 * credential ever reaches the DOM. `external_url` is absolute and carries a
 * ticket when there is a password for one to stand in for — it is for the
 * clipboard, and **the page must never fetch it**, which would put a bearer
 * credential into the browser's network log for no gain (D31).
 */
export type PlaybackURLs = {
  stream_url: string;
  external_url: string;

  /**
   * The same film through ffmpeg, **absent when this install has no ffmpeg** —
   * omitted, not empty, and not a URL that answers 503. Its absence is how this
   * screen knows to say direct play only rather than offering a fallback that
   * cannot run.
   */
  remux_url?: string;

  /**
   * One entry per subtitle file beside the film that a <track> can use, and
   * `[]` — never absent — when there are none, which is the ordinary case.
   *
   * Optional in the TYPE only, because a page served by an older binary would
   * have none of this and `subtitles.map` on undefined is a blank screen rather
   * than a missing caption.
   */
  subtitles?: SubtitleTrack[];

  /** Absent with authentication off, because nothing was minted. */
  expires_at?: string;
};

/**
 * SubtitleTrack is one `<track>` the player can offer.
 *
 * The URL is relative and same-origin, exactly like `stream_url`: a `<track>`
 * sends the session cookie by itself and no credential goes near the DOM.
 */
export type SubtitleTrack = {
  /** The file name in the library, which is also the last segment of `url`. */
  name: string;
  /** What the browser draws in its own captions menu. */
  label: string;
  /**
   * An ISO 639-1 code for `srclang`, or `''` when the file name declared none.
   * Empty rather than a guess — `srclang` is what a browser picks a default
   * track with, and a wrong one is worse than none.
   */
  language: string;
  url: string;
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
  /**
   * A folder that was NOT removed, because it sits outside LIBRARY_MOVIES and is
   * therefore not curator's to delete. Absent in the ordinary case — and present
   * rather than silent because the banner otherwise says the folder went.
   */
  folder_left?: string;
};

/**
 * UpdateStatus is what the Updates card renders.
 *
 * `available` and `can_install` are two different questions and the card needs
 * both: a new version can exist with nothing on this box able to install it,
 * which is the normal state for anyone running curator without watchtower
 * beside it (docs/decisions.md D44). That case shows `command`, not a button.
 */
export type UpdateStatus = {
  current: string;
  latest?: string;
  available: boolean;
  url?: string;
  notes?: string;
  checked_at?: string;
  can_install: boolean;
  command?: string;
  busy: boolean;

  /** False when UPDATE_CHECK is off, which is why `latest` is empty. Without
   *  this the card cannot tell "up to date" from "never looked". */
  checking: boolean;

  /** Why the check could not be made. Shown beside the running version rather
   *  than instead of it: a failed check is not a broken curator. */
  error?: string;
};

export type Integration = {
  name: string;
  env: string;
  configured: boolean;
  reachable?: boolean;
  detail?: string;
};

/**
 * SettingKind decides which control the form draws. The list is the Go
 * registry's, and it is a union of literals plus `string` on purpose: a kind
 * added in phase 8 must render as a text box rather than fail the build of a
 * UI that has not been rewritten yet.
 */
export type SettingKind =
  | 'text'
  | 'int'
  | 'bool'
  | 'duration'
  | 'url'
  | 'path'
  | 'enum'
  | 'multiline'
  | 'password';

/** Where the value curator is running on came from. The environment wins. */
export type SettingSource = 'env' | 'stored' | 'default';

/**
 * Setting is one row of the registry as the form sees it.
 *
 * `value` is optional because absent and empty are different answers, and
 * conflating them is the one mistake this screen must not make: it is **absent
 * for every secret, always** — there is no masked value, no length and no
 * prefix to render — and present as `''` for DOWNLOADS_PATH, where empty is a
 * deliberate setting meaning "use the path qBittorrent reported".
 *
 * `pending_change` is the field to branch on, never `pending !== null`. Two
 * states have a pending change with nothing to print: a secret that differs,
 * and a setting that was cleared, whose pending value is a default (D29).
 */
export type Setting = {
  key: string;
  group: string;
  kind: SettingKind | string;
  options?: string[];
  secret: boolean;

  // The variable that overrides this setting, and — for VPN_CONFIG alone — the
  // second one naming a file whose contents are the value. The screen renders
  // them rather than keeping its own copy of the registry, which would be the
  // second vocabulary phase 7 exists to prevent.
  env: string;
  file_env?: string;

  value?: string;
  configured: boolean;
  source: SettingSource | string;
  editable: boolean;

  // A stored value that would not decrypt — a database restored without its
  // key file. It behaves as unset and asks to be entered again (D28).
  unreadable?: boolean;

  pending: string | null;
  pending_change: boolean;
  restart_required: boolean;
};

export type SettingsResult = {
  version: string;
  probed: boolean;
  integrations: Integration[];
  paths: Record<string, string>;
  intervals: Record<string, string>;
  settings: Setting[];
};

/**
 * The four worlds the Jellyfin probe distinguishes, and the screen branches on
 * three of them.
 *
 * `unreachable` and `starting` are the same instruction — keep waiting — and
 * two different sentences, which is why they are two states: nothing listening
 * means the pasted command has not run, and starting means it has.
 */
export type JellyfinProbeState = 'unreachable' | 'starting' | 'needs_setup' | 'configured';

export type JellyfinProbe = {
  state: JellyfinProbeState | string;
  url: string;
  version?: string;

  /**
   * What provisioning will hand Jellyfin. It comes from the API rather than
   * being built here because it is `LIBRARY_MOVIES` as the *server* resolved
   * it, and a second copy in the browser is exactly the path disagreement this
   * whole flow exists to make impossible.
   */
  library_path: string;

  /** The line the user pastes. It names compose.yaml's profile, so it is the
   * server's to spell, not this screen's. */
  command: string;
  detail?: string;
};

/**
 * minter's three worlds.
 *
 * There is no `starting`, unlike Jellyfin's four: minter's port is not open
 * until it is, so there is nothing to distinguish between "not up" and "coming
 * up". Measured at 45 s from `up -d` to healthy, all of it with nothing
 * listening.
 */
export type MinterProbeState = 'unreachable' | 'unhealthy' | 'ready';

export type MinterProbe = {
  state: MinterProbeState | string;
  url: string;

  /**
   * Whether THIS PROCESS is searching 1337x — the running value, not the stored
   * one. A setting applies at the next start (D29), so switching 1337x on and
   * saving leaves curator running with it off, and this is the only place that
   * fact is visible before somebody searches and finds nothing.
   */
  enabled: boolean;

  /** The line the user pastes. It names compose.yaml's profile, so it is the
   * server's to spell, not this screen's. */
  command: string;

  /** minter's patched Firefox, present only when it answered its own health
   * check. It is the field that proves this is minter rather than something
   * else on the port. */
  user_agent?: string;
  detail?: string;
};

export type JellyfinProvisioned = {
  username: string;
  url: string;
  public_url: string;
  library_path: string;
  library: string;
};

/**
 * What curator found out about its own library path on a server it does not
 * own, and the screen offers a button on exactly one of the four.
 *
 * `unseen` and `unknown` are both "curator will not add a library here", and
 * they are two states because they are two sentences: one is a server that said
 * it cannot see that path, and one is a server that would not say. Neither is
 * proof of a problem — Jellyfin reports the path it sees through its own mount
 * and curator reports its own, and D32 recorded that those disagree on every
 * deployment where the two reach the disk differently.
 */
export type JellyfinLibraryState = 'covered' | 'addable' | 'unseen' | 'unknown';

export type JellyfinAdopted = {
  username: string;
  url: string;
  public_url: string;
  library_path: string;
  version: string;
  library: {
    state: JellyfinLibraryState | string;
    detail: string;
    names: string[];
    added: boolean;
  };
  check: {
    film?: string;
    found: boolean;
    detail: string;
  };
};

/**
 * A provisioning failure, read off ApiError.body.
 *
 * `instructions` is the phase's required fallback rather than decoration: the
 * Jellyfin startup endpoints are not a documented contract, so every failure
 * has to end somewhere the user can still reach a working Jellyfin by hand.
 */
export type JellyfinFailure = {
  step?: string;
  adopt?: boolean;
  instructions?: string[];
};

export function jellyfinFailure(error: unknown): JellyfinFailure {
  if (!(error instanceof ApiError)) return {};
  const body = error.body;
  return {
    step: typeof body.step === 'string' ? body.step : undefined,
    adopt: body.adopt === true,
    instructions: Array.isArray(body.instructions)
      ? body.instructions.filter((line): line is string => typeof line === 'string')
      : undefined,
  };
}

/**
 * The six worlds the VPN check distinguishes, and the page branches on all of
 * them because the instruction differs each time.
 *
 * `off` no tunnel configured · `waiting` never handshaken, so a credential or an
 * endpoint · `stale` handshaken too long ago, which on a config without
 * PersistentKeepalive usually means nothing at all · `blocked` up and NOT
 * changing where traffic leaves from, the one state where bytes would go out of
 * the real address while everything else looks healthy · `up` the only good one
 * · `unknown` the device could not be read.
 */
export type VPNState = 'off' | 'waiting' | 'stale' | 'blocked' | 'up' | 'unknown';

export type VPNStatus = {
  state: VPNState | string;
  detail?: string;

  /**
   * The headline, and it is NOT `state === 'up'`. The server ANDs four
   * independently-read facts — a fresh handshake, the engine's socket inside
   * the tunnel, traffic leaving from somewhere else, and nothing held — so this
   * screen cannot draw a green banner off one of them.
   */
  protected: boolean;

  /** The PEER's address: which VPN server, not where this machine appears from.
   * The one that matters is check.address, and it is gated. */
  endpoint?: string;

  handshake_age_seconds?: number;
  uptime_seconds?: number;

  /** Movement keys on `received`, never `sent`: a tunnel whose peer is gone
   * keeps sending keepalives for ever. */
  received: number;
  sent: number;
  keepalive: number;

  enforcement: {
    required: boolean;
    /**
     * False when VPN_REQUIRED is set in the environment, which wins over the
     * settings table. A write would answer 409, so the switch is drawn
     * read-only rather than offering a control that cannot work.
     */
    editable: boolean;
    /**
     * False when this process refused to build an engine at all — no tunnel,
     * enforcement on. The toggle applies to the check and cannot conjure an
     * engine, so turning enforcement off there needs a restart, and the screen
     * has to say so rather than leave somebody clicking.
     */
    engine_started: boolean;
  };

  engine: {
    backend: string;
    /** What the wiring intended, beside what the socket actually is. Both,
     * because a disagreement is the only interesting case. */
    tunnelled: boolean;
    socket?: string;
    inside_tunnel: boolean;
  };

  hold: { held: boolean; reason?: string };

  check: {
    checked: boolean;
    /** Whether the verdict on screen came from a check just now, as opposed to
     * standing on an earlier one. Without it a 3 s poll draws a ten-minute-old
     * proof as though it had just happened. */
    fresh: boolean;
    different: boolean;
    /** Always present once anything has been proved. */
    masked?: string;
    /** Only behind a password (D18). Where traffic leaves from is the one fact
     * the tunnel exists to keep, and this endpoint has no authentication in
     * front of it by default. */
    address?: string;
    at_seconds?: number;
  };
};

/**
 * AuthStatus is what GET /api/auth answers, and it is two booleans because the
 * question is which screen to draw: `required` says whether there is a password
 * at all, `authenticated` whether this browser would get past it.
 */
export type AuthStatus = {
  required: boolean;
  authenticated: boolean;
};

/**
 * ApiError carries the status as well as the message, because the screens
 * branch on it and each status deserves different words:
 *
 *   410  the search that issued this release id has aged out — the one error in
 *        the system whose correct fix is a user action
 *   503  the integration is unconfigured; say which variable to set
 *   502  a dependency is down; it is not our fault and not the user's
 *   409  a deliberate refusal of a well-formed request, and TEN of them across
 *        eight routes: the torrent has not finished, the torrent is not
 *        curator's to delete, curator already has this film in the library, the
 *        row is already matched (POST /match), the row has no match to correct
 *        (PUT /match), another row already holds that TMDB id (either method),
 *        the environment owns that setting, and three Jellyfin refusals
 *   422  four: nothing importable, a title that cannot be a folder name, and
 *        Jellyfin refusing a sign-in on each of provision and adopt
 *
 * **The status is not the situation, and for 409 and 422 nothing derives one
 * from the other** — so `states.tsx` draws no title for either and the server's
 * sentence carries the banner alone (D39). Do not add a status→words entry for
 * them here or there; the count above is why one cannot be right, and it grew
 * from five to ten without anybody noticing, which is the other half of why.
 */
export class ApiError extends Error {
  readonly status: number;

  /**
   * The per-field messages a settings write answers with. `PUT /api/settings`
   * is the only endpoint that sends them, and they are the whole reason a
   * rejected save can put "not a valid duration" under the one input that
   * caused it instead of a banner over eight that did not.
   *
   * 400 is a value the parser refused; 409 is a key the environment owns. Both
   * arrive in the same shape because the form does the same thing with them.
   */
  readonly fields: Record<string, string>;

  /**
   * The rest of the error body, for the endpoints that answer with more than a
   * message. `POST /api/jellyfin/provision` is the one: a provisioning failure
   * carries which step it got to, whether the server is one to adopt rather
   * than retry, and the manual instructions that are the phase's required
   * fallback. Losing those to `throw new Error(message)` would leave the
   * screen with nothing to draw but the sentence.
   */
  readonly body: Record<string, unknown>;

  constructor(
    status: number,
    message: string,
    fields: Record<string, string> = {},
    body: Record<string, unknown> = {},
  ) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.fields = fields;
    this.body = body;
  }

  get expiredSearch() {
    return this.status === 410;
  }

  get unconfigured() {
    return this.status === 503;
  }

  /** The environment owns this key, so the screen may not write it. */
  get shadowed() {
    return this.status === 409 && Object.keys(this.fields).length > 0;
  }
}

/**
 * The one hook <Gate> installs, and the reason authentication needs no state
 * manager: a 401 arriving mid-session is the same event as the first load
 * finding `authenticated: false`, so both flip one piece of state in one
 * component. Everything else in the UI keeps calling the API in ignorance of
 * whether there is a password at all.
 */
let unauthorized: (() => void) | null = null;

export function onUnauthorized(handler: (() => void) | null) {
  unauthorized = handler;
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let response: Response;
  try {
    response = await fetch(base + path, {
      ...init,
      // On EVERY call, not only the ones that need it. The session cookie is
      // HttpOnly, so this is the only way it travels, and the default
      // ('same-origin') is silently correct in the embedded build and silently
      // wrong under `next dev`, where :3000 calls :8090 — which is the setup
      // this would be debugged in.
      credentials: 'include',
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
    let fields: Record<string, string> = {};
    let parsed: Record<string, unknown> = {};
    try {
      const body = JSON.parse(text);
      if (body && typeof body === 'object') parsed = body;
      if (typeof body?.error === 'string') message = body.error;
      if (body?.fields && typeof body.fields === 'object') fields = body.fields;
    } catch {
      // A non-JSON body means something in front of curator answered, not
      // curator. Showing it raw is more useful than hiding it.
    }

    // The login endpoint is excluded deliberately: its 401 is "wrong password",
    // which belongs under the password box on a form that is already showing.
    // Routing it through the gate would reset the form somebody is typing in.
    if (response.status === 401 && path !== authLoginPath) unauthorized?.();

    throw new ApiError(response.status, message, fields, parsed);
  }

  return (text ? JSON.parse(text) : null) as T;
}

const authLoginPath = '/api/auth/login';

export const api = {
  movies: () => request<Movie[]>('/api/movies'),

  // Addressed by curator's own movies.id, which every library row has and a
  // TMDB match is not required for — that is the whole point of the route
  // (D35). Not to be confused with tmdbMovie below, which takes a TMDB id.
  movie: (id: number) => request<MovieRow>(`/api/movies/${id}`),

  scan: () => request<ScanResult>('/api/scan', { method: 'POST' }),

  // `force` is the "Check again" button. A page load must not spend a request:
  // GitHub allows 60 an hour to an unauthenticated address and a household may
  // have more than one curator behind it.
  update: (force = false) =>
    request<UpdateStatus>(`/api/update${force ? '?force=true' : ''}`),

  // Answers 202 and then the connection dies, because the updater restarts the
  // container this request was served by. That is success, not failure — the
  // caller reloads rather than waiting for a body.
  updateNow: () => request<{ state: string; detail: string }>('/api/update', { method: 'POST' }),

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

  /**
   * Point a library row at the film a human picked (T67).
   *
   * It takes curator's own movies.id and a TMDB id, and answers the same
   * `MovieRow` the GET does — including a `jellyfin_url` that has changed from
   * a search into a deep link, because the row now has an id to look up.
   *
   * Only the id travels: the server re-reads the film from TMDB rather than
   * trusting a body, so overview and poster_path come from the same place the
   * scan takes them from. 409 is the two refusals — the row is already matched,
   * or another folder already holds that film.
   */
  matchMovie: (id: number, tmdbID: number) =>
    request<MovieRow>(`/api/movies/${id}/match`, {
      method: 'POST',
      body: JSON.stringify({ tmdb_id: tmdbID }),
    }),

  /**
   * Repoint a row that is already matched at a different film (T69).
   *
   * Same path, same body, same response as `matchMovie` — the method is the
   * whole difference, because the resource is the same and what changes is
   * whether a match is being established or replaced. Sending the wrong one is a
   * 409 that names the right one rather than silently doing the other job.
   *
   * There is deliberately no `unmatchMovie` beside it. Clearing a `tmdb_id` puts
   * the row back on the scan's work list, where it would be re-matched from the
   * folder name that was wrong to begin with, so the correction is one request
   * that overwrites rather than two that pass through nothing.
   */
  correctMatch: (id: number, tmdbID: number) =>
    request<MovieRow>(`/api/movies/${id}/match`, {
      method: 'PUT',
      body: JSON.stringify({ tmdb_id: tmdbID }),
    }),

  logs: (since = 0, level?: string, limit?: number, subsystem?: string) => {
    const query = new URLSearchParams({ since: String(since) });
    if (level) query.set('level', level);
    if (limit) query.set('limit', String(limit));
    // The VPN screen shows its own tail through the endpoint that already
    // exists rather than a second one. Filtered server-side and AFTER the
    // cursor, so an idle poll still transfers nothing.
    if (subsystem) query.set('subsystem', subsystem);
    return request<LogsResult>(`/api/logs?${query}`);
  },

  // probe calls out to real services and one of them may wake a browser, so it
  // is opt-in: the page loads from configuration alone and checks on request.
  settings: (probe = false) =>
    request<SettingsResult>(`/api/settings${probe ? '?probe=1' : ''}`),

  /**
   * Is there a Jellyfin, and what state is it in?
   *
   * Short-timeout on the server side and safe to call on a loop: it is one
   * unauthenticated GET that mutates nothing. The Playback screen polls it
   * while a container starts, which is minutes on a first run — 202 MB of
   * layers to pull before a 14-27 second cold start.
   */
  jellyfinProbe: (url?: string) =>
    request<JellyfinProbe>(
      // A url is T66's branch: the adopt form asks for an address and probes it
      // before it asks for anything else, because an unreachable one is a typo
      // or a firewall and finding that out after somebody has typed a password
      // is a worse experience for no reason.
      url ? `/api/jellyfin/probe?url=${encodeURIComponent(url)}` : '/api/jellyfin/probe',
    ),

  /**
   * Is minter there, and is 1337x actually live in this process?
   *
   * Cheap and safe to call on a loop: it is one GET of minter's own /health,
   * which is NOT a page fetch — the browser only wakes for a real search. The
   * Indexers screen polls it beside the toggle, because whether minter is
   * answering is a fact rather than something the user controls.
   */
  minterProbe: () => request<MinterProbe>('/api/indexers/minter/probe'),

  /**
   * Run Jellyfin's setup wizard and record the result.
   *
   * The password is sent once and is never stored by curator: it creates the
   * account, signs in with it, mints an API key and forgets it. The key never
   * comes back — the response carries no secret at all.
   */
  jellyfinProvision: (body: { username: string; password: string; public_url: string }) =>
    request<JellyfinProvisioned>('/api/jellyfin/provision', {
      method: 'POST',
      body: JSON.stringify(body),
    }),

  /**
   * Connect to a Jellyfin somebody already runs, and change nothing about it.
   *
   * Never touches that server's wizard — curator refuses every startup endpoint
   * against a configured server, which is what stops a household being locked
   * out of what they are watching (D34). It signs in, mints its own key, and
   * reads two things.
   *
   * `add_library` is the one write, and it is opt-in per call: this is safe to
   * send twice, once to find out what is there and once to accept the offer.
   * The second call reuses the key the first one minted rather than making a
   * second.
   */
  jellyfinAdopt: (body: {
    url: string;
    username: string;
    password: string;
    public_url: string;
    add_library: boolean;
  }) =>
    request<JellyfinAdopted>('/api/jellyfin/adopt', {
      method: 'POST',
      body: JSON.stringify(body),
    }),

  /**
   * A partial settings object: key → the text the environment variable would
   * have carried. **Absent leaves a setting alone and `''` clears it**, which
   * is why a form must send only what changed rather than everything it drew —
   * the whole write is one transaction, so one rejected field rejects all of
   * them.
   *
   * It answers with the same body GET does, so a save needs no reload.
   */
  updateSettings: (changes: Record<string, string>) =>
    request<SettingsResult>('/api/settings', {
      method: 'PUT',
      body: JSON.stringify(changes),
    }),

  // One call, then one <video>. It is a POST because it MINTS something when
  // there is a password — a ticket is not a thing to hand out on a GET that a
  // prefetcher might make.
  playback: (movieID: number) =>
    request<PlaybackURLs>(`/api/movies/${movieID}/playback`, { method: 'POST' }),

  /**
   * One HEAD, to tell a codec failure from a transport failure.
   *
   * `MediaError.code` is 4 for a container the browser will not decode *and*
   * for a response it could not load at all, so the error event cannot tell an
   * expired session from an unplayable file. This can: 200 means the bytes are
   * there and the browser simply refuses them, and anything else means the
   * problem is not the codec. Without it an expired cookie silently remuxes,
   * fails again, and offers VLC for a film that would have played fine after
   * logging back in.
   *
   * It goes through `request`, which is what makes a 401 here reach <Gate> and
   * draw the login form, exactly as a 401 from any other call does.
   */
  probe: (url: string) => request<null>(url, { method: 'HEAD' }),

  // Open, and it always answers: it is how the UI knows which screen to draw
  // before it has a cookie.
  authStatus: () => request<AuthStatus>('/api/auth'),

  /**
   * The VPN's live state. Cheap and safe to poll: the sentinel's last verdict
   * plus one read of a device in curator's own process, never a round trip.
   */
  vpn: () => request<VPNStatus>('/api/vpn'),

  /**
   * Force a re-prove. This one DOES make two round trips, one of them through
   * the tunnel, so the server rate-limits it to once per 30 s and answers 429
   * with the wait when it refuses.
   */
  vpnCheck: () => request<VPNStatus>('/api/vpn/check', { method: 'POST' }),

  // The cookie comes back in a Set-Cookie the JS never sees — it is HttpOnly —
  // so the only thing to do with the response is stop drawing the login form.
  login: (password: string) =>
    request<AuthStatus>(authLoginPath, {
      method: 'POST',
      body: JSON.stringify({ password }),
    }),
};

// --- formatting ------------------------------------------------------------

export function formatBytes(bytes: number | null): string {
  // null is "no file", and it is the only way a library row says that now: a
  // wanted film has no size, and since a folder with no film in it is no longer
  // a row at all (D33), a scanned row's size_bytes is a feature file's and can
  // never be 0. The zero branch stays for a release whose indexer reported no
  // size, where "empty" is still the honest word.
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
