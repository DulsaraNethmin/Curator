'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { api, formatWhen, type Download } from '@/lib/api';
import { Empty, Failure } from '@/components/states';

// The fallback if /api/settings cannot be read. It matches
// defaultDownloadPollInterval on the Go side.
const FALLBACK_POLL_MS = 10_000;

export default function Activity() {
  const [downloads, setDownloads] = useState<Download[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [pollMs, setPollMs] = useState(FALLBACK_POLL_MS);
  const [importing, setImporting] = useState<string | null>(null);
  const [importError, setImportError] = useState<unknown>(null);
  const [imported, setImported] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setDownloads(await api.downloads());
      setError(null);
    } catch (e) {
      setError(e);
    }
  }, []);

  // Poll at the rate the server actually reconciles at, read from the server
  // rather than guessed. Anything faster is load with no new data: the rows
  // only change when the download poller has run.
  useEffect(() => {
    let cancelled = false;
    api
      .settings()
      .then((s) => !cancelled && setPollMs(parseGoDuration(s.intervals.download_poll) ?? FALLBACK_POLL_MS))
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, []);

  const timer = useRef<ReturnType<typeof setInterval> | null>(null);

  useEffect(() => {
    void load();

    const start = () => {
      if (timer.current === null) timer.current = setInterval(load, pollMs);
    };
    const stop = () => {
      if (timer.current !== null) {
        clearInterval(timer.current);
        timer.current = null;
      }
    };

    // A background tab does not need fresh rows, and a laptop lid does not need
    // a request every ten seconds until the battery runs out.
    const onVisibility = () => {
      if (document.hidden) {
        stop();
      } else {
        void load();
        start();
      }
    };

    if (!document.hidden) start();
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      stop();
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, [load, pollMs]);

  async function runImport(download: Download) {
    setImporting(download.torrent_hash);
    setImportError(null);
    setImported(null);
    try {
      const movie = await api.import(download.torrent_hash);
      setImported(`${movie.title} (${movie.year}) is in the library.`);
      await load();
    } catch (e) {
      setImportError(e);
    } finally {
      setImporting(null);
    }
  }

  return (
    <>
      <h1>Activity</h1>
      <p className="lede">
        What qBittorrent is doing with the releases curator dispatched, reconciled every{' '}
        {Math.round(pollMs / 1000)}s. Nothing here can pause, resume or delete a torrent — that stays
        qBittorrent&apos;s business.
      </p>

      {error !== null && <Failure error={error} onRetry={load} />}
      {importError !== null && <Failure error={importError} />}
      {imported && (
        <div className="banner info">
          <strong>Imported</strong>
          <span>{imported}</span>
        </div>
      )}

      {downloads && downloads.length === 0 && (
        <Empty>Nothing has been dispatched yet. Find something on the Search screen.</Empty>
      )}

      {downloads && downloads.length > 0 && (
        <div className="panel">
          <table>
            <thead>
              <tr>
                <th>Release</th>
                <th>State</th>
                <th style={{ width: '9rem' }}>Progress</th>
                <th>Source</th>
                <th className="tight">Added</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {downloads.map((download) => (
                <tr key={download.torrent_hash}>
                  <td>
                    {download.release_name}
                    <div className="small muted mono">{download.torrent_hash.slice(0, 12)}…</div>
                  </td>
                  <td>
                    <State state={download.state} />
                    {/* The badge is the state; this is why. Only on `stalled`,
                        and only when the server sent one — every other state
                        either needs no explanation or has one on screen
                        already, and a reason left under a recovered download
                        would say nobody is seeding a file that is arriving.
                        Rendered verbatim: the backend that knows writes the
                        sentence, and there is no table of codes here. */}
                    {download.state === 'stalled' && download.reason && (
                      <div className="small muted reason">{download.reason}</div>
                    )}
                  </td>
                  <td>
                    <div className="bar" title={`${Math.round(download.progress * 100)}%`}>
                      {/* progress is 0..1 from qBittorrent, not a percentage. */}
                      <i style={{ width: `${Math.round(download.progress * 100)}%` }} />
                    </div>
                  </td>
                  <td className="small muted">{download.indexer}</td>
                  <td className="small muted tight">{formatWhen(download.added_at)}</td>
                  <td className="tight">
                    {/* Only for a row that is completed but not imported —
                        exactly the state phase 4 built this endpoint for. A
                        failed row gets nothing: a human picks another release
                        (D5), and there is no retry to offer. */}
                    {download.state === 'completed' && (
                      <button
                        onClick={() => runImport(download)}
                        disabled={importing !== null}
                      >
                        {importing === download.torrent_hash ? 'Importing…' : 'Import now'}
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

function State({ state }: { state: string }) {
  switch (state) {
    case 'imported':
      return <span className="badge ok">imported</span>;
    case 'completed':
      // The file is on disk but not yet in the library — the one state with
      // something a human can usefully do about it.
      return <span className="badge warn">completed</span>;
    case 'stalled':
      // Added, wanted, and getting nowhere: nobody is seeding it. Warn rather
      // than bad, because the fix is to pick another release rather than to
      // repair anything — and the sentence saying which it is now sits under
      // this badge rather than only in Logs.
      return <span className="badge warn">stalled</span>;
    case 'failed':
      return <span className="badge bad">failed</span>;
    default:
      return <span className="badge">{state}</span>;
  }
}

/** parseGoDuration understands what time.Duration.String() writes: "10s", "1h0m0s". */
function parseGoDuration(value: string | undefined): number | null {
  if (!value) return null;

  let ms = 0;
  let matched = false;
  for (const [, amount, unit] of value.matchAll(/([\d.]+)(ns|us|µs|ms|s|m|h)/g)) {
    const n = Number(amount);
    if (Number.isNaN(n)) continue;
    matched = true;
    ms +=
      unit === 'h' ? n * 3_600_000
      : unit === 'm' ? n * 60_000
      : unit === 's' ? n * 1000
      : unit === 'ms' ? n
      : 0; // sub-millisecond units round to nothing useful for a UI timer
  }
  // Never poll faster than a second, whatever the server says: below that it is
  // load with no new data.
  return matched ? Math.max(ms, 1000) : null;
}
