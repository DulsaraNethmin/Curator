'use client';

import { useCallback, useEffect, useState } from 'react';
import { api, type UpdateStatus } from '@/lib/api';

/**
 * The Updates card.
 *
 * It renders three different things because there are three different states,
 * and collapsing them is how a screen ends up lying:
 *
 * 1. **A newer version, and something here can install it** — a button.
 * 2. **A newer version, and nothing here can install it** — the command to
 *    paste. This is the ordinary state for an install without watchtower, and
 *    it is not an error (docs/decisions.md D44).
 * 3. **No newer version, or checking is off** — the running version, and which
 *    of those two it is. "Up to date" and "never looked" are not the same
 *    sentence and the card must not print the first when it means the second.
 *
 * Pressing the button kills this page. The updater restarts the container that
 * served it, so the request is expected to fail in flight and that failure IS
 * the success — see internal/update's package comment.
 */
export function Updates() {
  const [status, setStatus] = useState<UpdateStatus | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [checking, setChecking] = useState(false);
  const [restarting, setRestarting] = useState(false);

  const load = useCallback(async (force = false) => {
    setError(null);
    if (force) setChecking(true);
    try {
      setStatus(await api.update(force));
    } catch (e) {
      setError(e);
    } finally {
      setChecking(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function install() {
    setError(null);
    setRestarting(true);
    try {
      await api.updateNow();
    } catch {
      // Deliberately swallowed. The connection dropping is what a successful
      // update looks like from here, and reporting it as a failure would tell
      // somebody their update broke at the moment it started working.
    }
  }

  if (!status && !error) return null;

  return (
    <section className="card">
      <h2>Updates</h2>

      {restarting ? (
        <div className="banner" role="status">
          <strong>Restarting</strong>
          <span>
            curator has asked the updater to pull and restart. This page will stop answering for a
            moment — reload it to see the new version.
          </span>
        </div>
      ) : null}

      {status ? (
        <>
          <p className="small muted">
            Running <span className="mono">{status.current}</span>
            {status.checked_at ? (
              <> · last checked {new Date(status.checked_at).toLocaleString()}</>
            ) : null}
          </p>

          {!status.checking ? (
            <p className="small muted">
              Update checks are off. curator will not ask whether a newer version exists.
            </p>
          ) : status.available ? (
            <>
              <p>
                <strong>{status.latest} is available.</strong>{' '}
                {status.url ? (
                  <a href={status.url} target="_blank" rel="noreferrer">
                    Release notes
                  </a>
                ) : null}
              </p>

              {status.busy ? (
                <div className="banner warn" role="status">
                  <strong>Something is still downloading</strong>
                  <span>
                    Updating restarts curator. The download resumes afterwards, but the progress
                    bar will disappear while it does.
                  </span>
                </div>
              ) : null}

              {status.can_install ? (
                <button onClick={install} disabled={restarting}>
                  {restarting ? 'Restarting…' : `Update to ${status.latest}`}
                </button>
              ) : (
                <>
                  <p className="small muted">
                    curator cannot install this itself — it has no updater to ask, and it does not
                    hold the Docker socket on purpose. Run:
                  </p>
                  <pre className="mono">{status.command}</pre>
                </>
              )}
            </>
          ) : (
            <p className="small muted">This is the newest release.</p>
          )}

          {status.error ? (
            <p className="small muted">Could not check for updates: {status.error}</p>
          ) : null}
        </>
      ) : null}

      <button onClick={() => load(true)} disabled={checking || restarting}>
        {checking ? 'Checking…' : 'Check again'}
      </button>

      {error ? <p className="small muted">Could not read update status.</p> : null}
    </section>
  );
}
