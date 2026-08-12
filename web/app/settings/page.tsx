'use client';

import { useEffect, useState } from 'react';
import { api, type SettingsResult } from '@/lib/api';
import { Failure, Working } from '@/components/states';

// Read-only, and it never receives a secret — not even masked (D17). There is
// no authentication in front of this page, so `configured` is the only fact it
// is safe to know, and it happens to be the only fact it needs: every screen's
// real question is "can I press this button".
export default function Settings() {
  const [settings, setSettings] = useState<SettingsResult | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [probing, setProbing] = useState(false);

  async function load(probe = false) {
    setError(null);
    if (probe) setProbing(true);
    try {
      setSettings(await api.settings(probe));
    } catch (e) {
      setError(e);
    } finally {
      setProbing(false);
    }
  }

  useEffect(() => {
    void load(false);
  }, []);

  return (
    <>
      <h1>Settings</h1>
      <p className="lede">
        Configuration comes from the environment and is read once at startup. This screen reports it;
        it cannot change it, and it is never told a password or an API key.
      </p>

      <form className="row" onSubmit={(e) => (e.preventDefault(), load(true))}>
        <button className="primary" disabled={probing}>
          {probing ? 'Checking…' : 'Check what is reachable'}
        </button>
        <span className="small muted">
          Probing calls each configured service. It is a button rather than automatic because one of
          them can wake a browser.
        </span>
      </form>

      {probing && <Working what="Asking each configured service whether it is there" />}
      {error !== null && <Failure error={error} onRetry={() => load(false)} />}

      {settings && (
        <>
          <div className="panel">
            <table>
              <thead>
                <tr>
                  <th>Integration</th>
                  <th>Configured</th>
                  <th>Reachable</th>
                  <th>Notes</th>
                </tr>
              </thead>
              <tbody>
                {settings.integrations.map((integration) => (
                  <tr key={integration.name}>
                    <td>
                      {integration.name}
                      <div className="small muted mono">{integration.env}</div>
                    </td>
                    <td>
                      {integration.configured ? (
                        <span className="badge ok">yes</span>
                      ) : (
                        <span className="badge">not set</span>
                      )}
                    </td>
                    <td>
                      {/* Absent, not false, when unconfigured: "never set up"
                          and "set up but broken" are different facts and this
                          page says different things about them. */}
                      {integration.reachable === undefined ? (
                        <span className="muted small">
                          {settings.probed ? 'not checked' : '—'}
                        </span>
                      ) : integration.reachable ? (
                        <span className="badge ok">reachable</span>
                      ) : (
                        <span className="badge bad">unreachable</span>
                      )}
                    </td>
                    <td className="small muted">{integration.detail ?? ''}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <h2>Paths</h2>
          <div className="panel">
            <table>
              <tbody>
                {Object.entries(settings.paths).map(([key, value]) => (
                  <tr key={key}>
                    <td className="mono small" style={{ width: '15rem' }}>
                      {key}
                    </td>
                    <td className="mono small">
                      {/* An empty DOWNLOADS_PATH is a deliberate setting — "use
                          the path qBittorrent reported verbatim" — not a
                          missing one, and has to read that way. */}
                      {value === '' ? (
                        <span className="muted">
                          (empty — qBittorrent&apos;s own path is used as-is)
                        </span>
                      ) : (
                        value
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <h2>Intervals</h2>
          <div className="panel">
            <table>
              <tbody>
                {Object.entries(settings.intervals).map(([key, value]) => (
                  <tr key={key}>
                    <td className="mono small" style={{ width: '15rem' }}>
                      {key}
                    </td>
                    <td className="mono small">{value}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <p className="small muted" style={{ marginTop: '1.25rem' }}>
            curator {settings.version} · to change any of this, edit{' '}
            <span className="mono">.env</span> and restart.
          </p>
        </>
      )}
    </>
  );
}
