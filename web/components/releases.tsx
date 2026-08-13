'use client';

import { useEffect, useState } from 'react';
import {
  api,
  ApiError,
  formatBytes,
  type Download,
  type Release,
  type SearchResult,
} from '@/lib/api';
import { Empty, Failure } from '@/components/states';

/**
 * Film is what dispatch sends: the movie a release is for.
 *
 * It is a separate prop from `result` rather than read off it, because the two
 * are not the same string (D20). The release-name search knows only what was
 * typed, so it passes the search's own echo; the movie page passes TMDB's
 * canonical title, year and id, colon and all — `library.DestFolder` spells the
 * colon " - " on the way to disk, and a client that pre-stripped it would write
 * the wrong folder.
 */
export type Film = {
  title: string;
  year: number;
  tmdb_id?: number | null;
};

/**
 * Releases is the list, the dispatch button, and everything that can go wrong
 * with pressing it — in one place, used by both screens that show releases.
 *
 * There is exactly one implementation of dispatch on purpose. Two would be two
 * places for the 410 wording to drift apart, and the 410 is the one error in
 * this system whose correct fix is a user action rather than a retry.
 *
 * `result` is nullable so the caller can mount this before it has searched: the
 * "downloads are not configured" warning is worth showing next to a button that
 * is about to be pressed, not only after the wait.
 */
export function Releases({
  result,
  film,
  searching,
  empty,
  onSearchAgain,
}: {
  result: SearchResult | null;
  film: Film;
  searching?: boolean;
  empty?: React.ReactNode;
  onSearchAgain?: () => void;
}) {
  const [dispatching, setDispatching] = useState<string | null>(null);
  const [dispatched, setDispatched] = useState<Record<string, Download>>({});
  const [dispatchError, setDispatchError] = useState<unknown>(null);

  const [downloadsConfigured, setDownloadsConfigured] = useState<boolean | null>(null);

  // Ask once whether dispatch is even possible, rather than letting the button
  // fail with a 503.
  useEffect(() => {
    let cancelled = false;
    api
      .settings()
      .then((s) => {
        if (cancelled) return;
        const qbit = s.integrations.find((i) => i.name === 'qbittorrent');
        setDownloadsConfigured(qbit?.configured ?? false);
      })
      .catch(() => !cancelled && setDownloadsConfigured(null));
    return () => {
      cancelled = true;
    };
  }, []);

  // A new search issues new release ids, so what was queued against the old ones
  // says nothing about these.
  useEffect(() => {
    setDispatched({});
    setDispatchError(null);
  }, [result]);

  async function dispatch(release: Release) {
    if (!film.year) return;
    setDispatching(release.id);
    setDispatchError(null);
    try {
      const saved = await api.dispatch({
        release_id: release.id,
        // The film, not the release. A release id says nothing about which movie
        // it is for — the caller picked it out of a search it made and is the
        // only party that knows.
        title: film.title,
        year: film.year,
        tmdb_id: film.tmdb_id,
      });
      setDispatched((prev) => ({ ...prev, [release.id]: saved }));
    } catch (e) {
      setDispatchError(e);
    } finally {
      setDispatching(null);
    }
  }

  const expired = dispatchError instanceof ApiError && dispatchError.expiredSearch;

  return (
    <>
      {dispatchError !== null && (
        <Failure
          error={dispatchError}
          // A 410 is the one error whose correct fix is a user action: the ids
          // belong to a search that has aged out, so offer to run it again.
          onRetry={expired ? onSearchAgain : undefined}
        />
      )}

      {result && <Indexers result={result} />}

      {result && result.releases.length === 0 && empty && <Empty>{empty}</Empty>}

      {result && result.releases.length > 0 && (
        <div className="panel">
          <table>
            <thead>
              <tr>
                <th>Release</th>
                <th className="num">Seeders</th>
                <th>Quality</th>
                <th className="num">Size</th>
                <th>Source</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {result.releases.map((release) => {
                const saved = dispatched[release.id];
                return (
                  <tr key={release.id}>
                    <td>{release.title}</td>
                    {/* Seeders lead, because they are the only column that
                        predicts whether the download finishes — which is the
                        question a manual picker is actually asking (D11). */}
                    <td className="num">{release.seeders.toLocaleString()}</td>
                    <td>
                      <span className="badge">{release.quality || '—'}</span>
                    </td>
                    <td className="num tight">{formatBytes(release.size_bytes)}</td>
                    <td className="small muted">{release.indexers.join(', ')}</td>
                    <td className="tight">
                      {saved ? (
                        <span className="badge ok">queued</span>
                      ) : (
                        <button
                          onClick={() => dispatch(release)}
                          disabled={
                            dispatching !== null ||
                            downloadsConfigured === false ||
                            searching ||
                            !film.year
                          }
                          title={
                            downloadsConfigured === false
                              ? 'Downloads are not configured: set QBIT_USER and QBIT_PASS'
                              : !film.year
                                ? 'Search with a year first — it becomes the library folder name'
                                : undefined
                          }
                        >
                          {dispatching === release.id ? 'Sending…' : 'Download'}
                        </button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {downloadsConfigured === false && (
        <div className="banner warn" style={{ marginTop: '1rem' }}>
          <strong>Downloads are not configured</strong>
          <span>
            Set <span className="mono">QBIT_USER</span> and <span className="mono">QBIT_PASS</span>{' '}
            to dispatch a release. Search works without them.
          </span>
        </div>
      )}
    </>
  );
}

/**
 * Per-indexer status.
 *
 * A search with one source down is a SUCCESS carrying the other two's releases,
 * not an error page — but the failure has to be named. An aggregator that
 * silently hides a downed indexer is how somebody concludes a film does not
 * exist.
 */
function Indexers({ result }: { result: SearchResult }) {
  const failed = result.indexers.filter((i) => !i.ok);

  return (
    <>
      <p className="small muted" style={{ margin: '0 0 .75rem' }}>
        {result.releases.length} release{result.releases.length === 1 ? '' : 's'} for{' '}
        {/* Shown exactly as it will be written to the library, so a sloppy
            search term is visible before it becomes a folder name. */}
        <strong>
          {result.title}
          {result.year ? ` (${result.year})` : ''}
        </strong>{' '}
        ·{' '}
        {result.indexers.map((indexer, i) => (
          <span key={indexer.name}>
            {i > 0 && ' · '}
            <span className={indexer.ok ? '' : 'badge bad'}>
              {indexer.name} {indexer.ok ? indexer.count : 'failed'}
            </span>
          </span>
        ))}
      </p>

      {failed.length > 0 && (
        <div className="banner warn">
          <strong>
            {failed.length === 1 ? 'One indexer is down' : `${failed.length} indexers are down`} —
            these results are partial
          </strong>
          {failed.map((indexer) => (
            <span key={indexer.name} className="small">
              <span className="mono">{indexer.name}</span>: {indexer.error ?? 'no reason given'}
              <br />
            </span>
          ))}
        </div>
      )}
    </>
  );
}
