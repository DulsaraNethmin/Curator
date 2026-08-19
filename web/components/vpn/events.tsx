'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { api, type LogEntry } from '@/lib/api';
import { LogLine } from '@/components/logline';

/**
 * The tunnel's own log lines, from the buffer and endpoint that already exist.
 *
 * `?subsystem=vpn` is the whole of it. Everything internal/vpn writes is tagged
 * at the point cmd/curator builds it, so this needs no second buffer, no second
 * endpoint and nothing at all inside internal/vpn.
 *
 * Its own cursor, not the Logs screen's: the two poll independently and a shared
 * one would make whichever ran second see nothing.
 */
export function Events() {
  const [entries, setEntries] = useState<LogEntry[]>([]);
  const cursor = useRef(0);

  const poll = useCallback(async () => {
    try {
      const result = await api.logs(cursor.current, undefined, 200, 'vpn');
      cursor.current = result.cursor;
      if (result.entries.length > 0) {
        setEntries((prev) => [...prev, ...result.entries].slice(-KEEP));
      }
    } catch {
      // Swallowed. curator being briefly unreachable is not something to put a
      // banner on under a log tail; the verdict above is where a real failure
      // shows.
    }
  }, []);

  useEffect(() => {
    void poll();
    const id = setInterval(() => {
      if (!document.hidden) void poll();
    }, POLL_MS);
    return () => clearInterval(id);
  }, [poll]);

  return (
    <div className="panel" style={{ padding: '.85rem' }}>
      <h3 className="small">What the tunnel has said</h3>
      {entries.length === 0 ?
        <p className="small muted">
          Nothing yet. curator writes here when the tunnel comes up, changes state, or stops
          protecting downloads — so an empty list on a healthy install is the expected reading.
        </p>
      : <div className="logs" style={{ maxHeight: '20rem' }}>
          {entries.map((entry) => (
            <LogLine key={entry.seq} entry={entry} />
          ))}
        </div>
      }
    </div>
  );
}

// Slower than the status poll. These lines are written on transitions rather
// than on a timer, so most polls return nothing — and the cursor makes an idle
// one an empty array either way.
const POLL_MS = 5000;
const KEEP = 200;
