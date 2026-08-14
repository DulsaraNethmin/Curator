'use client';

import { useEffect, useState } from 'react';
import { api, type Setting, type SettingsResult } from '@/lib/api';
import { Failure, Working } from '@/components/states';
import { Section } from '@/components/settings/section';

/**
 * Settings, in two halves that answer two different questions.
 *
 * **Above:** what curator will be running next time — a form generated from the
 * registry, one section per group, one transaction per Save. **Below:** what
 * this process is actually running now, which is phase 5's screen unchanged.
 * The distinction between the two is the screen's whole job, because a written
 * setting applies at the next start and not at once (D29). The password is the
 * one exception, and it is in the section that says so.
 *
 * It is still never told a secret. `value` is absent for every one of them, and
 * absent is not `''` — there is no masked value here because there is none in
 * the payload (D17, D28).
 */
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

  // Never on a timer. This is a form, not a dashboard: a poll would overwrite
  // what somebody is typing, and there is nothing here that changes on its own.

  const rows = settings?.settings ?? [];
  const pending = rows.filter((row) => row.pending_change);

  return (
    <>
      <h1>Settings</h1>
      <p className="lede">
        The form is what curator will run the next time it starts. What it is running{' '}
        <em>right now</em> is below it, and the two differ until you restart — except the password,
        which applies at once. An API key or a private key is never sent back to this screen, so
        every secret box is empty whether or not one is stored.
      </p>

      {error !== null && <Failure error={error} onRetry={() => load(false)} />}

      {pending.length > 0 && (
        <div className="banner warn" role="status">
          <strong>Saved. Restart curator for these to take effect</strong>
          <span className="mono small">{pending.map((row) => row.key).join(' · ')}</span>
        </div>
      )}

      {settings &&
        groupsOf(rows).map(({ group, settings: fields }) => (
          <Section
            key={group}
            title={groups[group]?.title ?? group}
            blurb={groups[group]?.blurb}
            warning={groups[group]?.warning}
            settings={fields}
            onSaved={setSettings}
          />
        ))}

      <h2 className="running">What this process is running</h2>
      <p className="small muted" style={{ margin: '0 0 1rem', maxWidth: '52rem' }}>
        Read-only, and read from the configuration this process actually resolved rather than from
        the table above — which is why it cannot disagree with what curator is doing.
      </p>

      <form className="row" onSubmit={(e) => (e.preventDefault(), load(true))}>
        <button disabled={probing}>{probing ? 'Checking…' : 'Check what is reachable'}</button>
        <span className="small muted">
          Probing calls each configured service. It is a button rather than automatic because one of
          them can wake a browser.
        </span>
      </form>

      {probing && <Working what="Asking each configured service whether it is there" />}

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
                          page says different things about them.

                          A dash rather than "not checked" — the latter reads as
                          "we forgot to look", when the truth is that there is
                          nothing to look at. The Notes column already says why. */}
                      {integration.reachable === undefined ? (
                        <span className="muted small">—</span>
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
            curator {settings.version}
          </p>
        </>
      )}
    </>
  );
}

/**
 * The sections, in the order the API sent them — which is the registry's own
 * screen order. Grouping here rather than by a list of group names is what
 * keeps a phase 8 setting from needing a line in this file.
 */
function groupsOf(rows: Setting[]): { group: string; settings: Setting[] }[] {
  const out: { group: string; settings: Setting[] }[] = [];
  for (const row of rows) {
    const last = out[out.length - 1];
    if (last && last.group === row.group) last.settings.push(row);
    else out.push({ group: row.group, settings: [row] });
  }
  return out;
}

/**
 * Prose, and only prose. A group that is not here still renders — with its own
 * name as the heading — so this table is what a section *says*, never whether
 * it exists.
 */
const groups: Record<string, { title: string; blurb?: string; warning?: React.ReactNode }> = {
  library: {
    title: 'Library',
    blurb: 'Where the films live on disk, and the key that gives them titles, posters and years.',
  },
  downloads: {
    title: 'Downloads',
    blurb:
      "curator's own torrent engine is the default backend and the only one whose traffic it can guarantee stays inside the tunnel. Leave a number empty to use the built-in default.",
  },
  qbittorrent: {
    title: 'qBittorrent',
    blurb:
      'Only read when the backend above is qbittorrent. curator can verify that instance is behind a VPN by comparing exit addresses; it cannot guarantee it, because the socket is not ours.',
  },
  vpn: {
    title: 'VPN',
    blurb:
      'The wg-quick .conf your provider hands you, pasted whole. It is stored encrypted, it is never sent back, and only the torrent engine is bound to the tunnel — a bad config cannot lock you out of this screen.',
  },
  indexers: {
    title: 'Indexers',
    blurb:
      '1337x needs minter running to get past Cloudflare; YTS and TPB do not. A source that is off is not searched at all.',
  },
  playback: {
    title: 'Playback',
    blurb:
      'Optional. Films play in the browser directly; when the browser refuses the container, curator hands the file to ffmpeg and rewrites the container without re-encoding anything. With no ffmpeg it is direct play only, and anything the browser refuses offers a VLC link instead.',
  },
  jellyfin: {
    title: 'Jellyfin',
    blurb:
      'Optional. With no key, curator imports films and simply does not ask Jellyfin to rescan.',
  },
  access: {
    title: 'Access',
    blurb:
      'One password in front of the whole API. There are no accounts, so there is no username. Off unless you turn it on.',
    warning: (
      <div className="banner warn section-warning">
        <strong>There is no TLS, and this is what that means</strong>
        <span>
          The password crosses your network in clear on every request. It raises the bar against a
          browser on the LAN — a housemate, a guest, a device that should not be deleting films — and
          it is <em>not</em> protection against something reading the network.
        </span>
        <span style={{ display: 'block', marginTop: '.5rem' }}>
          Locked yourself out? The environment beats what is stored:{' '}
          <span className="mono">docker run -e AUTH_ENABLED=false …</span> starts curator with the
          password off and the stored one untouched. Nothing here needs a database editor.
        </span>
      </div>
    ),
  },
};
