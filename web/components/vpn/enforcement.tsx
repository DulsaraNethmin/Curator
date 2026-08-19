'use client';

import { useState } from 'react';
import { api, type VPNStatus } from '@/lib/api';

/**
 * The kill switch itself.
 *
 * It renders from the poll and never optimistically. A toggle that flips to
 * where you clicked and then quietly fails is worse than one that takes a
 * moment, and this is the one control in curator whose wrong position means
 * downloads leaving from the household address.
 *
 * Turning it OFF is confirmed. There is no modal component in this codebase and
 * this does not add one — the confirmation is an inline alertdialog banner in
 * the page flow, the same shape the Library screen's delete uses.
 */
export function Enforcement({
  status,
  onSaved,
}: {
  status: VPNStatus;
  onSaved: (next: VPNStatus) => void;
}) {
  const [confirming, setConfirming] = useState(false);
  const [saving, setSaving] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);

  async function write(required: boolean) {
    setSaving(true);
    setFailure(null);
    try {
      await api.updateSettings({ vpn_required: String(required) });
      setConfirming(false);
      onSaved(await api.vpn());
    } catch (cause) {
      setFailure(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <div className="panel" style={{ padding: '.85rem' }}>
        <label className="check">
          <input
            type="checkbox"
            checked={status.enforcement.required}
            disabled={saving || !status.enforcement.editable}
            onChange={(e) => (e.target.checked ? void write(true) : setConfirming(true))}
          />
          <span>
            <strong>Refuse downloads that would not be protected</strong>
            <span className="small muted" style={{ display: 'block' }}>
              VPN_REQUIRED. This applies from the next download, not the next restart.
            </span>
          </span>
        </label>

        {/* The environment wins over the settings table, so a write here would
            answer 409. Said in place of the failure rather than after it: a
            control that looks operable and is not is worse than one that
            explains itself. Same rule the settings screen has drawn shadowed
            fields by since phase 7. */}
        {!status.enforcement.editable && (
          <p className="small muted" style={{ marginTop: '.5rem' }}>
            <span className="badge">environment</span> This is set by{' '}
            <span className="mono">VPN_REQUIRED</span> where curator was started, which wins over
            anything saved here. Unset it to control the kill switch from this screen.
          </p>
        )}

        {failure && (
          <div className="banner error" style={{ marginTop: '.75rem' }} role="alert">
            <span>{failure}</span>
          </div>
        )}

        {/* The honest limit, and it is only shown when it is true. The toggle
            applies to the CHECK; it cannot conjure an engine. A curator that
            started with no tunnel and this switched on has no torrent client at
            all, so turning it off here needs a restart to take effect — the
            same shape the Indexers screen uses for 1337x. */}
        {!status.enforcement.engine_started && (
          <div className="banner warn" style={{ marginTop: '.75rem' }}>
            <strong>This process has no download engine</strong>
            <span>
              It started with no tunnel configured and this switch on, so curator built no torrent
              client at all rather than one bound to this machine&apos;s own connection. Configure a
              tunnel, or turn this off and restart curator — the setting applies at once, but an
              engine that was never started cannot be switched on.
            </span>
          </div>
        )}
      </div>

      {confirming && (
        <div className="banner error" role="alertdialog">
          <strong>Turn the kill switch off?</strong>
          <span className="small">
            curator will dispatch downloads it cannot prove are protected. It keeps saying so in the
            log, every time, but it stops refusing.
          </span>
          <ul className="small" style={{ margin: '.5rem 0 0', paddingLeft: '1.1rem' }}>
            <li>peer traffic still goes through the tunnel whenever there is one</li>
            <li>with no tunnel, it leaves from this machine&apos;s own address</li>
            <li>this is the documented setting for a machine that is already behind a VPN</li>
          </ul>
          <div>
            <button onClick={() => void write(false)} disabled={saving}>
              {saving ? 'Saving…' : 'Turn it off'}
            </button>{' '}
            <button onClick={() => setConfirming(false)} disabled={saving}>
              Keep it on
            </button>
          </div>
        </div>
      )}
    </>
  );
}
