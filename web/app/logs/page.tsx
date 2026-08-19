'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { api, type LogEntry } from '@/lib/api';
import { Empty, Failure } from '@/components/states';
import { LogLine } from '@/components/logline';

// Two seconds. The log is written as things happen rather than reconciled on a
// timer like Activity, so there is no server-side interval to match — this is
// just often enough to feel live. The cursor makes an idle poll an empty array,
// so the cost of being wrong here is small.
const POLL_MS = 2000;

// What is kept in the browser. The server's ring holds LOG_BUFFER_LINES; there
// is no point holding more than that here, and a page that grows without bound
// eventually becomes the thing that is slow.
const KEEP = 2000;

export default function Logs() {
  const [entries, setEntries] = useState<LogEntry[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [level, setLevel] = useState('');
  const [paused, setPaused] = useState(false);
  const [missed, setMissed] = useState(0);
  const [buffered, setBuffered] = useState(0);

  const cursor = useRef(0);
  const pane = useRef<HTMLDivElement | null>(null);
  // Follow the tail only while the reader is already at the bottom. Yanking
  // someone back down while they are reading something further up is the
  // single most irritating thing a log viewer can do.
  const following = useRef(true);

  const poll = useCallback(async () => {
    try {
      const result = await api.logs(cursor.current, level || undefined);
      cursor.current = result.cursor;
      setBuffered(result.buffered);
      if (result.missed > 0) setMissed((m) => m + result.missed);
      if (result.entries.length > 0) {
        setEntries((prev) => [...prev, ...result.entries].slice(-KEEP));
      }
      setError(null);
    } catch (e) {
      setError(e);
    }
  }, [level]);

  // Changing the filter re-reads from the start of the ring, because entries
  // the old filter hid were never fetched.
  useEffect(() => {
    cursor.current = 0;
    setEntries([]);
    setMissed(0);
    void poll();
  }, [poll]);

  useEffect(() => {
    if (paused) return;

    const timer = setInterval(() => {
      if (!document.hidden) void poll();
    }, POLL_MS);
    return () => clearInterval(timer);
  }, [paused, poll]);

  // Autoscroll, but only when already at the bottom.
  useEffect(() => {
    if (following.current && pane.current) {
      pane.current.scrollTop = pane.current.scrollHeight;
    }
  }, [entries]);

  function onScroll() {
    const el = pane.current;
    if (!el) return;
    following.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  }

  return (
    <>
      <h1>Logs</h1>
      <p className="lede">
        The last {buffered || '—'} lines curator has written, live. This is the same log that goes to
        stderr — it is kept in memory so you do not have to SSH into the Pi to read it. Known secrets
        are stripped before anything is stored.
      </p>

      <form className="row" onSubmit={(e) => e.preventDefault()}>
        <select value={level} onChange={(e) => setLevel(e.target.value)} aria-label="Minimum level">
          <option value="">All levels</option>
          <option value="info">Info and above</option>
          <option value="warn">Warnings and errors</option>
          <option value="error">Errors only</option>
        </select>
        <button type="button" onClick={() => setPaused((p) => !p)}>
          {paused ? 'Resume' : 'Pause'}
        </button>
        <button
          type="button"
          onClick={() => {
            setEntries([]);
            setMissed(0);
          }}
        >
          Clear view
        </button>
        <span className="small muted">
          {paused ? 'paused' : `refreshing every ${POLL_MS / 1000}s`} · {entries.length} shown
        </span>
      </form>

      {error !== null && <Failure error={error} onRetry={poll} />}

      {missed > 0 && (
        <div className="banner warn">
          <strong>
            {missed} line{missed === 1 ? '' : 's'} scrolled out of the buffer before they were read
          </strong>
          <span className="small">
            The server keeps a fixed number of lines and drops the oldest. Raise LOG_BUFFER_LINES if
            this keeps happening, or watch stderr directly for a busy period.
          </span>
        </div>
      )}

      {entries.length === 0 ? (
        <Empty>
          Nothing logged yet at this level. curator is quiet when idle — trigger a scan or a search
          and it will fill up.
        </Empty>
      ) : (
        <div className="logs" ref={pane} onScroll={onScroll}>
          {entries.map((entry) => (
            <LogLine key={entry.seq} entry={entry} />
          ))}
        </div>
      )}
    </>
  );
}
