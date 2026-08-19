'use client';

import type { VPNStatus } from '@/lib/api';

/**
 * The headline: PROTECTED or NOT PROTECTED, and the four facts it rests on.
 *
 * The verdict is `status.protected`, which the server computes as an AND of all
 * four. It is deliberately not recomputed here — one place decides, and a
 * screen that ANDed its own copy would eventually disagree with the API about
 * the same tunnel.
 *
 * The four rows are shown whatever the verdict, including when everything is
 * fine. A guarantee nobody can see the working of is a slogan, and the whole
 * point of this screen is that each line is read from somewhere different.
 */
export function Verdict({ status }: { status: VPNStatus }) {
  const facts = [
    {
      ok: status.state === 'up',
      label: 'The tunnel handshook recently',
      detail:
        status.handshake_age_seconds === undefined ?
          'no handshake yet'
        : `${formatAge(status.handshake_age_seconds)} ago${status.keepalive ? `, keepalive every ${status.keepalive}s` : ', no keepalive configured'}`,
    },
    {
      ok: status.engine.inside_tunnel,
      label: "The download engine's socket is inside the tunnel",
      detail:
        status.engine.socket ?
          `bound to ${status.engine.socket}`
        : 'this process has no download engine of its own',
    },
    {
      ok: status.check.different,
      label: 'Traffic leaves from somewhere other than this machine',
      detail:
        status.check.checked ?
          `${status.check.address ?? status.check.masked ?? 'checked'}${status.check.fresh ? '' : `, last proved ${formatAge(status.check.at_seconds ?? 0)} ago`}`
        : 'not established yet',
    },
    {
      ok: !status.hold.held,
      label: 'Downloads are running',
      detail: status.hold.held ? (status.hold.reason ?? 'held') : 'nothing is being held back',
    },
  ];

  return (
    <div className={`banner ${status.protected ? 'info' : 'error'}`} role="status">
      <strong>{status.protected ? 'PROTECTED' : 'NOT PROTECTED'}</strong>
      <span>{status.detail ?? ''}</span>

      <ul className="small" style={{ margin: '.75rem 0 0', paddingLeft: '1.1rem' }}>
        {facts.map((fact) => (
          <li key={fact.label} style={{ marginBottom: '.2rem' }}>
            <span className={`badge ${fact.ok ? 'ok' : 'bad'}`}>{fact.ok ? 'yes' : 'no'}</span>{' '}
            {fact.label} <span className="muted">— {fact.detail}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** Seconds into something a person reads. */
export function formatAge(seconds: number): string {
  if (seconds < 60) return `${Math.max(seconds, 0)}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`;
  return `${Math.floor(seconds / 86400)}d`;
}
