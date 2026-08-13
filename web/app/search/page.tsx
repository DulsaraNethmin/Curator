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
import { Empty, Failure, Working } from '@/components/states';

// Search and Releases are one screen, not two.
//
// A release id is only valid while the search that issued it is still cached —
// an hour (D10) — and resolving an expired one is a 410. A separate /releases/
// route would have to either search again, getting different ids, or stash the
// results somewhere and pretend. Rendering the list from the response already
// in hand is the only version that cannot go stale between the two halves.
export default function Search() {
  const [title, setTitle] = useState('');
  const [year, setYear] = useState('');
  const [quality, setQuality] = useState('');

  const [searching, setSearching] = useState(false);
  const [result, setResult] = useState<SearchResult | null>(null);
  const [error, setError] = useState<unknown>(null);

  const [dispatching, setDispatching] = useState<string | null>(null);
  const [dispatched, setDispatched] = useState<Record<string, Download>>({});
  const [dispatchError, setDispatchError] = useState<unknown>(null);

  const [downloadsConfigured, setDownloadsConfigured] = useState<boolean | null>(null);

  // Ask once whether dispatch is even possible, rather than letting the button
  // fail with a 503. QBIT_USER is unset right now, so this is the live case.
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

  async function runSearch(event?: React.FormEvent) {
    event?.preventDefault();
    if (!title.trim()) return;

    setSearching(true);
    setError(null);
    setDispatchError(null);
    setDispatched({});
    try {
      setResult(await api.search(title.trim(), year ? Number(year) : undefined, quality || undefined));
    } catch (e) {
      setError(e);
      setResult(null);
    } finally {
      setSearching(false);
    }
  }

  async function dispatch(release: Release) {
    if (!result) return;
    setDispatching(release.id);
    setDispatchError(null);
    try {
      const saved = await api.dispatch({
        release_id: release.id,
        // The film, not the release. A release id says nothing about which movie
        // it is for — the caller picked it out of a search it made and is the
        // only party that knows.
        title: result.title,
        year: result.year,
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
      <h1>Search</h1>
      <p className="lede">
        Four indexers at once. A first search takes about thirteen seconds, because a real browser is
        clearing a Cloudflare challenge for 1337x; a repeat within the hour is instant.
      </p>

      <form className="row" onSubmit={runSearch}>
        <input
          name="title"
          placeholder="Interstellar"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          aria-label="Title"
          autoFocus
        />
        <input
          name="year"
          placeholder="Year"
          value={year}
          onChange={(e) => setYear(e.target.value.replace(/\D/g, '').slice(0, 4))}
          inputMode="numeric"
          size={6}
          aria-label="Year"
        />
        <select value={quality} onChange={(e) => setQuality(e.target.value)} aria-label="Quality">
          <option value="">Any quality</option>
          <option value="2160p">2160p</option>
          <option value="1080p">1080p</option>
          <option value="720p">720p</option>
        </select>
        <button className="primary" type="submit" disabled={searching || !title.trim()}>
          {searching ? 'Searching…' : 'Search'}
        </button>
      </form>

      {searching && (
        <Working
          what="Searching YTS, TPB and 1337x"
          hint="1337x needs a real browser to clear Cloudflare, so the first search for a title takes ~13s. The next one this hour is instant."
        />
      )}
      {error !== null && <Failure error={error} onRetry={() => runSearch()} />}

      {dispatchError !== null && (
        <Failure
          error={dispatchError}
          // A 410 is the one error whose correct fix is a user action: the ids
          // belong to a search that has aged out, so offer to run it again.
          onRetry={expired ? () => runSearch() : undefined}
        />
      )}

      {result && <Indexers result={result} />}

      {result && result.releases.length === 0 && (
        <Empty>
          Nothing found for <strong>{result.title}</strong>
          {result.year ? ` (${result.year})` : ''}. Try dropping the year, or a shorter title.
        </Empty>
      )}

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
                            dispatching !== null || downloadsConfigured === false || searching
                          }
                          title={
                            downloadsConfigured === false
                              ? 'Downloads are not configured: set QBIT_USER and QBIT_PASS'
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
        <strong>{result.title}</strong>
        {result.year ? ` (${result.year})` : ''} ·{' '}
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
