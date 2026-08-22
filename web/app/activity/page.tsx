'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { api, formatBytes, formatWhen, type Download, type Movie } from '@/lib/api';
import { formatETA, parseGoDuration } from '@/lib/duration';
import { useTelevision } from '@/components/media-switch';
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

  // The hash currently being paused, resumed or removed, so one row can say so
  // without disabling the rest ambiguously.
  const [working, setWorking] = useState<string | null>(null);
  const [actionError, setActionError] = useState<unknown>(null);

  // The row Remove is asking about, or null. A confirmation, because
  // DELETE /api/movies/{id} takes the torrent, the partial files, the download
  // rows, the movie row AND the library folder — and on this screen the row is
  // named by its release, so what actually goes has to be spelled out.
  const [removing, setRemoving] = useState<Download | null>(null);

  /**
   * The library rows, keyed by movies.id, fetched once on mount rather than per
   * poll.
   *
   * store.Download carries no title, no size and no media type — only a release
   * name — so this is what lets the Remove dialog name the FILM it is about to
   * delete. It pays for itself twice: the table gains the film's name beside the
   * release name, which it has never had.
   */
  const [films, setFilms] = useState<Map<number, Movie>>(new Map());
  const television = useTelevision();

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
      .then((s) => !cancelled && setPollMs(pollInterval(s.intervals.download_poll) ?? FALLBACK_POLL_MS))
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

  // One request on mount, plus one more when television is on. Not per poll:
  // the library changes when an import lands, and `load` refreshes the rows that
  // matter without needing the titles again.
  useEffect(() => {
    if (!television.known) return;
    let cancelled = false;

    const wanted = television.on ? [api.movies(), api.shows()] : [api.movies()];
    Promise.all(wanted)
      .then((lists) => {
        if (cancelled) return;
        const next = new Map<number, Movie>();
        for (const list of lists) for (const row of list) next.set(row.id, row);
        setFilms(next);
      })
      // A failure here costs the titles and nothing else: every row still draws,
      // and Remove falls back to naming the release. It must not take the screen
      // down, because the screen's job is the downloads.
      .catch(() => undefined);

    return () => {
      cancelled = true;
    };
  }, [television.known, television.on]);

  async function act(download: Download, call: () => Promise<unknown>) {
    setWorking(download.torrent_hash);
    setActionError(null);
    try {
      await call();
      await load();
    } catch (e) {
      setActionError(e);
    } finally {
      setWorking(null);
    }
  }

  async function remove(download: Download) {
    setRemoving(null);
    await act(download, () => api.deleteMovie(download.movie_id));
  }

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
      {/* This used to say "nothing here can pause, resume or delete a torrent —
          that stays qBittorrent's business", which was stale twice over: the
          default backend has been curator's own since D22, and D19 added
          delete-via-movie. T107 made the first half wrong as well. */}
      <p className="lede">
        What curator is doing with the releases it dispatched, reconciled every{' '}
        {Math.round(pollMs / 1000)}s. Speed and time remaining are read live and are not recorded —
        they are blank when the download backend cannot be reached.
      </p>

      {error !== null && <Failure error={error} onRetry={load} />}
      {importError !== null && <Failure error={importError} />}
      {actionError !== null && <Failure error={actionError} />}
      {imported && (
        <div className="banner info">
          <strong>Imported</strong>
          <span>{imported}</span>
        </div>
      )}

      {removing !== null && (
        <ConfirmRemove
          download={removing}
          film={films.get(removing.movie_id) ?? null}
          busy={working !== null}
          onCancel={() => setRemoving(null)}
          onConfirm={() => void remove(removing)}
        />
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
                <th style={{ width: '11rem' }}>Progress</th>
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
                    <div className="small muted">
                      {/* The film, which store.Download does not carry — this
                          row has only a release name, and "Interstellar" is
                          what somebody is actually looking for. Falls back to
                          the hash when the library read failed. */}
                      {films.get(download.movie_id)?.title ?? (
                        <span className="mono">{download.torrent_hash.slice(0, 12)}…</span>
                      )}
                    </div>
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
                      {/* progress is 0..1 from the backend, not a percentage. */}
                      <i style={{ width: `${Math.round(download.progress * 100)}%` }} />
                    </div>
                    <div className="small muted">
                      <Progress download={download} />
                    </div>
                  </td>
                  <td className="small muted">{download.indexer}</td>
                  <td className="small muted tight">{formatWhen(download.added_at)}</td>
                  <td className="tight">
                    <div className="row-actions">
                      {/* Import: only for a row that is completed but not
                          imported — exactly the state phase 4 built this
                          endpoint for. A failed row gets nothing: a human picks
                          another release (D5), and there is no retry to offer. */}
                      {download.state === 'completed' && (
                        <button onClick={() => runImport(download)} disabled={importing !== null}>
                          {importing === download.torrent_hash ? 'Importing…' : 'Import now'}
                        </button>
                      )}

                      {/* Pause and Resume, on the states where there is
                          something running to stop. An imported row has its
                          files in the library and the server refuses it with a
                          409, so the button is not drawn rather than drawn to
                          fail. */}
                      {download.state === 'paused' ? (
                        <button
                          onClick={() => void act(download, () => api.resume(download.torrent_hash))}
                          disabled={working !== null}
                        >
                          {working === download.torrent_hash ? 'Resuming…' : 'Resume'}
                        </button>
                      ) : (
                        stoppable(download.state) && (
                          <button
                            onClick={() => void act(download, () => api.pause(download.torrent_hash))}
                            disabled={working !== null}
                          >
                            {working === download.torrent_hash ? 'Pausing…' : 'Pause'}
                          </button>
                        )
                      )}

                      {/* Remove is the destructive one and never fires on the
                          click: it opens the confirmation below, because what
                          it deletes is the FILM and this row is named by a
                          release. Not drawn on an imported row — that film is in
                          the library and the Library screen is where it is
                          deleted from, with the dialog that already says so. */}
                      {download.state !== 'imported' && (
                        <button
                          className="danger"
                          onClick={() => setRemoving(download)}
                          disabled={working !== null}
                        >
                          Remove
                        </button>
                      )}
                    </div>
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

/**
 * What Remove is about to do, said before it does it.
 *
 * **It is not the torrent that goes.** There is no per-download delete and
 * deliberately no second destructive path: this calls the same
 * DELETE /api/movies/{id} the Library screen does, which removes the torrent and
 * its partial files, the download rows, the movie row and the library folder, in
 * D19's order. On this screen the row is named by a RELEASE, so the film it
 * belongs to has to be spelled out or the button is a trap.
 *
 * `film` is null when the library read failed. The dialog then names the release
 * and says plainly that it could not identify the title, rather than showing a
 * confident sentence about something it cannot see.
 */
function ConfirmRemove({
  download,
  film,
  busy,
  onCancel,
  onConfirm,
}: {
  download: Download;
  film: Movie | null;
  busy: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const tv = film?.media_type === 'tv';
  return (
    <div className="banner error" role="alertdialog">
      <strong>
        Remove {film ? `${film.title}${film.year ? ` (${film.year})` : ''}` : download.release_name}?
      </strong>
      <span className="small">
        This stops the download and deletes the {tv ? 'show' : 'film'} — not just this release — and
        cannot be undone.
      </span>
      <ul className="small" style={{ margin: '.5rem 0 0', paddingLeft: '1.1rem' }}>
        <li>
          the torrent <span className="mono">{download.release_name}</span> stops and is removed,
          with whatever it has downloaded so far
        </li>
        {film?.library_path && (
          <li>
            the folder <span className="mono">{film.library_path}</span>
            {/* Said out loud, because a show folder holds every season anyone
                ever grabbed and this row gives no hint of how many that is. */}
            {tv && ', and every season under it'}
          </li>
        )}
        {film?.size_bytes ? (
          <li>
            freeing <strong>{formatBytes(film.size_bytes)}</strong> already in the library
          </li>
        ) : null}
        {!film && (
          <li>
            curator could not read the library just now, so it cannot name the film this release is
            for — it will still be deleted
          </li>
        )}
      </ul>
      <div>
        <button className="danger" onClick={onConfirm} disabled={busy}>
          {busy ? 'Removing…' : 'Remove it'}
        </button>{' '}
        <button onClick={onCancel} disabled={busy}>
          Keep it
        </button>
      </div>
    </div>
  );
}

/**
 * Whether there is something running to stop.
 *
 * `imported` is out because the files are in the library and the server answers
 * 409 for it; `completed` is out because the payload is on disk and the only
 * useful thing left is the import. Everything else — queued, downloading,
 * stalled, failed — is a torrent the backend is still holding.
 */
function stoppable(state: string): boolean {
  return state !== 'imported' && state !== 'completed' && state !== 'paused';
}

/**
 * The numbers under the bar: how far, how fast, how long.
 *
 * Every one of them is drawn only when the server sent it. `download_rate`,
 * `size_bytes` and `eta_seconds` are omitempty on the Go side, so absent means
 * "the backend did not say" — a backend that is down, or a torrent it has never
 * heard of — and that is a different thing from zero. Printing "0 B/s" for it
 * would report a stopped download where the truth is that nothing was asked.
 */
function Progress({ download }: { download: Download }) {
  const percent = `${Math.round(download.progress * 100)}%`;
  const size =
    download.size_bytes ?
      `${formatBytes(Math.round(download.size_bytes * download.progress))} of ${formatBytes(download.size_bytes)}`
    : null;
  // A rate of 0 is a real answer — paused, or stalled — and reads better as
  // nothing than as "0 B/s" beside a bar that is not moving anyway.
  const rate = download.download_rate ? `${formatBytes(download.download_rate)}/s` : null;
  const eta = formatETA(download.eta_seconds);

  return <>{[percent, size, rate, eta].filter(Boolean).join(' · ')}</>;
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
    case 'paused':
      // Neutral, not `warn`. It is a state somebody chose, and warning about a
      // download they stopped on purpose would be the screen second-guessing
      // them.
      return <span className="badge">paused</span>;
    case 'failed':
      return <span className="badge bad">failed</span>;
    default:
      return <span className="badge">{state}</span>;
  }
}

/**
 * The poll interval this screen should use, from what the server said.
 *
 * The parsing moved to `web/lib/duration.ts` when a second reader appeared, and
 * **the floor did not go with it**: "never poll faster than a second, whatever
 * the server says" is a rule about polling, not about durations, and the other
 * reader is a cache TTL that a one-second floor would be nonsense for. Below a
 * second it is load with no new data.
 */
function pollInterval(value: string | undefined): number | null {
  const ms = parseGoDuration(value);
  return ms === null ? null : Math.max(ms, 1000);
}
