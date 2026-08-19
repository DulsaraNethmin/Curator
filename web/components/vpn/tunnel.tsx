'use client';

import { api, formatBytes, type VPNStatus } from '@/lib/api';
import { formatAge } from './verdict';

/**
 * The tunnel itself: which peer, how long it has been up, and how much has
 * moved.
 *
 * **Movement keys on `received` and never on `sent`.** A tunnel whose peer has
 * gone away keeps sending keepalives for ever, so bytes out prove nothing at
 * all; bytes in are the only half that requires somebody at the other end.
 */
export function Tunnel({
  status,
  onChecked,
}: {
  status: VPNStatus;
  onChecked: (next: VPNStatus) => void;
}) {
  return (
    <div className="panel">
      <table>
        <tbody>
          <Row label="Peer" value={status.endpoint ?? '—'} mono />
          <Row label="State" value={<State state={status.state} />} />
          <Row
            label="Last handshake"
            value={
              status.handshake_age_seconds === undefined ?
                'never'
              : `${formatAge(status.handshake_age_seconds)} ago`
            }
          />
          <Row
            label="Up"
            value={status.uptime_seconds === undefined ? '—' : formatAge(status.uptime_seconds)}
          />
          <Row
            label="Received"
            value={
              <>
                {formatBytes(status.received)}{' '}
                <span className="muted small">
                  — the half that needs somebody at the other end
                </span>
              </>
            }
          />
          <Row label="Sent" value={formatBytes(status.sent)} />
          <Row
            label="Keepalive"
            value={
              status.keepalive ?
                `every ${status.keepalive}s`
              : 'none — an idle tunnel will read as stale, which is normal'
            }
          />
        </tbody>
      </table>
      <div className="row" style={{ padding: '.6rem .85rem' }}>
        <CheckButton onChecked={onChecked} />
      </div>
    </div>
  );
}

function CheckButton({ onChecked }: { onChecked: (next: VPNStatus) => void }) {
  return (
    <button
      onClick={async () => {
        try {
          onChecked(await api.vpnCheck());
        } catch {
          // Swallowed here: a 429 is the rate limiter doing its job and a
          // failed check arrives as the state, which the poll renders a
          // moment later anyway.
        }
      }}
    >
      Check again
    </button>
  );
}

function State({ state }: { state: string }) {
  switch (state) {
    case 'up':
      return <span className="badge ok">up</span>;
    case 'stale':
      // Warn rather than bad: on a config with no PersistentKeepalive an idle
      // tunnel is legitimately stale and the next download rekeys it.
      return <span className="badge warn">stale</span>;
    case 'off':
      return <span className="badge">not configured</span>;
    case 'waiting':
    case 'blocked':
    case 'unknown':
      return <span className="badge bad">{state}</span>;
    default:
      return <span className="badge">{state}</span>;
  }
}

function Row({
  label,
  value,
  mono,
}: {
  label: string;
  value: React.ReactNode;
  mono?: boolean;
}) {
  return (
    <tr>
      <th style={{ width: '11rem' }}>{label}</th>
      <td className={mono ? 'mono' : undefined}>{value}</td>
    </tr>
  );
}
