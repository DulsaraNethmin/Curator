'use client';

import type { LogEntry } from '@/lib/api';

/**
 * One line of curator's log.
 *
 * Extracted from the Logs screen rather than copied, because the VPN screen
 * shows its own tail — the same lines, filtered to one subsystem — and two
 * renderers for one shape drift. The Logs screen imports this now.
 */
export function LogLine({ entry }: { entry: LogEntry }) {
  return (
    <div className="logline">
      <span className="when">{clock(entry.time)}</span>
      <span className={`lvl ${entry.level}`}>{entry.level}</span>
      <span className="body">
        {entry.msg}
        {entry.attrs &&
          Object.entries(entry.attrs).map(([key, value]) => (
            <span className="attr" key={key}>
              <b>{key}</b>={value}
            </span>
          ))}
      </span>
    </div>
  );
}

// Time only. A log you are watching live is always today, and a full timestamp
// on every line crowds out the message it is there to date.
function clock(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '—';
  return date.toLocaleTimeString(undefined, { hour12: false });
}
