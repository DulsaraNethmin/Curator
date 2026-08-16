'use client';

import { Suspense, useEffect, useState } from 'react';
import { useSearchParams } from 'next/navigation';
import Link from 'next/link';
import {
  api,
  formatBytes,
  formatWhen,
  type MovieRow,
  type SettingsResult,
} from '@/lib/api';
import { Empty, Failure } from '@/components/states';
import { Player } from '@/components/player';
import { MatchPicker } from '@/components/match-picker';

/**
 * The film's page for a row curator has and TMDB does not: /library/film/?id=7.
 *
 * **The id is curator's own `movies.id`, not a TMDB id** — that is the entire
 * reason this page exists beside `/movie/`
 * ([D35](../../../../docs/decisions.md)). `/movie/` is addressed by the TMDB id
 * (D21) and is a page ABOUT a catalogue entry: every fetch it makes is a TMDB
 * call. A row TMDB never matched has no such entry and never will, so it gets a
 * page about the ROW instead — title, year, size, path and the player, all of
 * which come from `GET /api/movies/{id}` and none of which need a catalogue.
 *
 * It is not restricted to unmatched rows, and refusing a matched one would be
 * an invented rule: every library row has a `movies.id`, so every library row
 * has an address here. A matched one simply also has a richer page, and links
 * to it.
 *
 * The `<Suspense>` boundary is not decoration — `useSearchParams()` without one
 * fails the export build, which is the toolchain remembering D21 so nobody has
 * to.
 */
export default function LibraryFilmPage() {
  return (
    <Suspense fallback={<p className="lede">Loading…</p>}>
      <LibraryFilm />
    </Suspense>
  );
}

function LibraryFilm() {
  const params = useSearchParams();
  const raw = params.get('id');
  // Digits or nothing, exactly as /movie/ does it: Number(null) is 0 and
  // Number('abc') is NaN, and NaN would reach the API as /api/movies/NaN.
  const id = raw !== null && /^\d+$/.test(raw) ? Number(raw) : 0;

  const [movie, setMovie] = useState<MovieRow | null>(null);
  const [settings, setSettings] = useState<SettingsResult | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [watching, setWatching] = useState(false);
  // Retry is a new run of the effect rather than a second copy of the fetch, so
  // there is one code path to the row and it keeps the cancellation the effect
  // already has.
  const [attempt, setAttempt] = useState(0);
  const [matching, setMatching] = useState(false);

  useEffect(() => {
    if (!id) return;
    let cancelled = false;

    setMovie(null);
    setError(null);
    setWatching(false);
    setMatching(false);

    api
      .movie(id)
      .then((row) => !cancelled && setMovie(row))
      .catch((cause) => !cancelled && setError(cause));

    // Only to tell the two reasons a row is unmatched apart, below. It is a
    // separate fetch rather than a field on the row because it is a fact about
    // the INSTALL and not about this film — every row on a keyless install is
    // unmatched for the same reason, and putting it on each of them would say
    // it once per film.
    api
      .settings()
      .then((next) => !cancelled && setSettings(next))
      .catch(() => {
        // The page is complete without it. All that is lost is the sharper of
        // two sentences about why there is no poster.
      });

    return () => {
      cancelled = true;
    };
  }, [id, attempt]);

  if (!id) {
    return (
      <>
        <h1>No film</h1>
        <Empty>
          This page shows one film from your library, addressed by curator&apos;s own id —{' '}
          <span className="mono">/library/film/?id=7</span>. Pick one from the{' '}
          <Link href="/library/">Library</Link>.
        </Empty>
      </>
    );
  }

  if (error !== null) {
    return (
      <>
        <h1>Film</h1>
        <Failure error={error} onRetry={() => setAttempt((n) => n + 1)} />
      </>
    );
  }

  if (!movie) return <p className="lede">Loading…</p>;

  // Only an imported row has a library_path, which is also why this can never
  // reach a partial download. A wanted row is in the database and not on disk.
  const onDisk = movie.library_path !== null;

  // **`integrations`, not `settings`, and the difference is load-bearing.** A key
  // saved on the Settings screen makes the settings row `configured` immediately,
  // while the process still has no TMDB client at all — it is built at startup, so
  // the key does nothing until a restart (D35, and T50's wizard says the same).
  // `integrations[].configured` is the RUNNING value, so it is the only one that
  // answers "can curator ask TMDB anything right now".
  //
  // Reading the settings row here instead would go wrong in both directions at
  // once: it would offer a matcher that answers 503, and it would tell somebody
  // their folder name was rejected when no question has been put yet — which is
  // the small lie this page was built to avoid.
  const keyInForce = settings?.integrations.find((row) => row.name === 'tmdb')?.configured ?? false;
  // undefined until /api/settings answers. The picker must not flash into
  // existence and out again, and the sentence below must not guess either.
  const knowKeyState = settings !== null;

  return (
    <>
      <h1>
        {movie.title}
        {movie.year > 0 && <span className="year"> {movie.year}</span>}
      </h1>

      <div className="meta">
        <span>{onDisk ? formatBytes(movie.size_bytes) : 'not on disk'}</span>
        {movie.quality && <span className="badge">{movie.quality}</span>}
        {movie.tmdb_id === null && (
          <span className="badge warn" title="No TMDB match, so there is no poster and no overview">
            unmatched
          </span>
        )}
        {movie.status !== 'imported' && <span className="badge">{movie.status}</span>}
      </div>

      {/* Why there is no poster, and the two answers are not the same sentence.
          A keyless install has not asked TMDB anything — saying the match
          failed would blame the folder name for the absence of a key, and the
          remedy is completely different. */}
      {movie.tmdb_id === null && knowKeyState && (
        <p className="lede">
          {!keyInForce ? (
            <>
              curator has no TMDB key in force, so it has never asked what this film is. Add one on
              the <Link href="/settings/">Settings screen</Link>, restart curator, and scan again —
              every scan retries the rows that still have no match. The film plays either way.
            </>
          ) : (
            <>
              TMDB had no match for this folder name, so there is no poster, no overview and no
              catalogue page — the row is kept and shown rather than guessed at. You can point it at
              the right film below, and the film plays either way.
            </>
          )}
        </p>
      )}

      <div className="actions">
        {onDisk && (
          <button className="primary" onClick={() => setWatching(true)} disabled={watching}>
            ▶ Watch here
          </button>
        )}

        {/* Absent means there is nothing to draw — no Jellyfin configured, or a
            film not on disk. For an unmatched row this is always a Jellyfin
            SEARCH rather than a deep link (D32), and the UI is deliberately not
            told which: both land somewhere useful. */}
        {movie.jellyfin_url && (
          <a className="button" href={movie.jellyfin_url} target="_blank" rel="noreferrer">
            Open in Jellyfin
          </a>
        )}

        {/* A matched row reached here anyway. The catalogue page is the better
            one for it, so say so rather than quietly being a worse version. */}
        {movie.tmdb_id !== null && (
          <Link className="button" href={`/movie/?id=${movie.tmdb_id}`}>
            Poster, cast and releases →
          </Link>
        )}

        {/* Only where there is a key in force to search with. A keyless install
            is the population this cannot help at all — there is no key to ask —
            and the sentence above sends it to Settings instead (T67, D35). */}
        {movie.tmdb_id === null && keyInForce && !matching && (
          <button onClick={() => setMatching(true)}>Find the right film</button>
        )}
      </div>

      {matching && movie.tmdb_id === null && (
        <MatchPicker
          movie={movie}
          onCancel={() => setMatching(false)}
          onMatched={(next) => {
            // The API answers the same body the GET does, so the row is simply
            // replaced — no refetch, and jellyfin_url arrives already changed
            // from a search into a deep link.
            setMovie(next);
            setMatching(false);
          }}
        />
      )}

      {/* The player takes movies.id, which is the id this page is addressed by
          — no translation, and nothing to get wrong. It mounts on the button
          above rather than drawing one of its own, so the two ways to watch sit
          together. */}
      {onDisk && watching && <Player movieID={movie.id} title={movie.title} autoStart />}

      {!onDisk && (
        <Empty>
          curator has a row for this film but no file: it is wanted, not imported. Nothing to play
          yet.
        </Empty>
      )}

      <dl className="facts">
        {movie.library_path && (
          <div>
            <dt>In the library at</dt>
            <dd>
              <span className="mono">{movie.library_path}</span>
            </dd>
          </div>
        )}
        <div>
          <dt>Added</dt>
          <dd>{formatWhen(movie.added_at)}</dd>
        </div>
        {movie.imported_at && (
          <div>
            <dt>Imported</dt>
            <dd>{formatWhen(movie.imported_at)}</dd>
          </div>
        )}
        <div>
          <dt>curator id</dt>
          <dd>{movie.id}</dd>
        </div>
      </dl>
    </>
  );
}
