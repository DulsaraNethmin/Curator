'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { api, type VPNStatus } from '@/lib/api';
import { Failure } from '@/components/states';
import { Verdict } from '@/components/vpn/verdict';
import { Tunnel } from '@/components/vpn/tunnel';
import { Enforcement } from '@/components/vpn/enforcement';
import { Scope } from '@/components/vpn/scope';
import { Events } from '@/components/vpn/events';

// Three seconds. GET /api/vpn is the sentinel's last verdict plus one read of a
// device in curator's own process — never a round trip — so it is affordable at
// the rate a person watching a tunnel come up would want.
const POLL_MS = 3000;

export default function VPN() {
  const [status, setStatus] = useState<VPNStatus | null>(null);
  const [error, setError] = useState<unknown>(null);

  // One request in flight at a time, the guard the Indexers screen learned to
  // need: a curator that is slow to answer must not collect a request per tick
  // on top of whatever is already making it slow.
  const inFlight = useRef(false);

  const load = useCallback(async () => {
    if (inFlight.current) return;
    inFlight.current = true;
    try {
      setStatus(await api.vpn());
      setError(null);
    } catch (e) {
      setError(e);
    } finally {
      inFlight.current = false;
    }
  }, []);

  const timer = useRef<ReturnType<typeof setInterval> | null>(null);

  useEffect(() => {
    void load();

    const start = () => {
      if (timer.current === null) timer.current = setInterval(load, POLL_MS);
    };
    const stop = () => {
      if (timer.current !== null) {
        clearInterval(timer.current);
        timer.current = null;
      }
    };

    // A background tab does not need a fresh verdict, and a laptop lid does not
    // need a request every three seconds until the battery runs out.
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
  }, [load]);

  return (
    <>
      <h1>VPN</h1>
      <p className="lede">
        Whether curator&apos;s downloads are going through the tunnel, read from four places rather
        than asserted in one. Refreshing every {POLL_MS / 1000}s.
      </p>

      {error !== null && <Failure error={error} onRetry={load} />}

      {status && (
        <>
          <Verdict status={status} />

          {/* Persistent, and it stays until the switch goes back on. Somebody
              who turned enforcement off for one download should not be able to
              forget they did. */}
          {!status.enforcement.required && (
            <div className="banner error" role="alert">
              <strong>The kill switch is off</strong>
              <span>
                curator will dispatch downloads it cannot prove are protected, and say so in the log
                each time instead of refusing.
              </span>
            </div>
          )}

          <Tunnel status={status} onChecked={setStatus} />
          <Enforcement status={status} onSaved={setStatus} />
          <Scope status={status} />
          <Events />
        </>
      )}
    </>
  );
}
