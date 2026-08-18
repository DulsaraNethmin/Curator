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
  noYearReason = 'Search with a year first — it becomes the library folder name',
  onSearchAgain,
}: {
  result: SearchResult | null;
  film: Film;
  searching?: boolean;
  empty?: React.ReactNode;
  // Why a year of 0 disables the button, which is a different sentence on each
  // screen. Typing a title without a year is the caller's own omission; TMDB
  // not knowing a release date is nobody's, and telling someone to "add a year"
  // for a film they cannot edit would be advice they cannot take.
  noYearReason?: string;
  onSearchAgain?: () => void;
}) {
  const [dispatching, setDispatching] = useState<string | null>(null);
  const [dispatched, setDispatched] = useState<Record<string, Download>>({});
  const [dispatchError, setDispatchError] = useState<unknown>(null);

  const [downloadsConfigured, setDownloadsConfigured] = useState<boolean | null>(null);

  // Why dispatch is refused, in the API's own words rather than this file's
  // guess. The backend is a choice (docs/decisions.md D22), so the sentence that
  // fixes it differs per backend: `embedded` needs no credentials at all, and
  // telling that user to set QBIT_USER is an instruction that cannot work.
  const [downloadsDetail, setDownloadsDetail] = useState<string | null>(null);

  // Ask once whether dispatch is even possible, rather than letting the button
  // fail with a 503.
  useEffect(() => {
    let cancelled = false;
    api
      .settings()
      .then((s) => {
        if (cancelled) return;
        // `torrents`, which is what the API calls it — NOT `qbittorrent`.
        // This read `'qbittorrent'` until T79 and the name has been `torrents`
        // since D22 moved the engine into this binary, so `find` returned
        // undefined on every install and `?? false` turned that into a hard
        // "not configured": every Download button disabled, everywhere, for a
        // backend reporting itself ready. Dispatch worked over the API the
        // whole time, which is why it survived so long.
        const torrents = s.integrations.find((i) => i.name === 'torrents');
        // Missing means the contract changed, not that downloads are off. Fail
        // OPEN — leave it unknown, let the button work, and let a real dispatch
        // return a real error. A rename must never silently disable the product
        // again, which is exactly what the old `?? false` did.
        setDownloadsConfigured(torrents ? torrents.configured : null);
        setDownloadsDetail(torrents?.detail ?? null);
      })
      .catch(() => {
        if (cancelled) return;
        setDownloadsConfigured(null);
        setDownloadsDetail(null);
      });
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
                              ? downloadsDetail
                                ? `Downloads are not configured: ${downloadsDetail}`
                                : 'Downloads are not configured'
                              : !film.year
                                ? noYearReason
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
            {downloadsDetail ?? 'No torrent backend can accept a release.'} Search works without it.
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
 *
 * **Two kinds of not-working, and they are separated here because they need
 * different sentences.** An indexer that is *down* is something to retry or
 * wait out. An indexer that is *unconfigured* never started at all — the
 * companion service it needs is not running — and no amount of waiting fixes
 * it; one line in a terminal does. Telling somebody 1337x is "down" when minter
 * was never started sends them to look at the wrong thing entirely
 * (docs/tasks/T49-minter-on-demand.md).
 */
function Indexers({ result }: { result: SearchResult }) {
  const broken = result.indexers.filter((i) => !i.ok);
  const unconfigured = broken.filter((i) => i.unconfigured);
  const failed = broken.filter((i) => !i.unconfigured);

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
              {indexer.name}{' '}
              {indexer.ok ? indexer.count : indexer.unconfigured ? 'not set up' : 'failed'}
            </span>
          </span>
        ))}
      </p>

      {unconfigured.length > 0 && (
        <div className="banner warn">
          <strong>
            {unconfigured.map((i) => i.name).join(', ')}{' '}
            {unconfigured.length === 1 ? 'is switched on but not set up' : 'are switched on but not set up'}{' '}
            — these results are partial
          </strong>
          <span>
            {unconfigured.length === 1 ? 'It needs' : 'They need'} a companion service that is not
            running. Settings → Indexers has the one line to start it, and says whether it came up.
          </span>
        </div>
      )}

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
