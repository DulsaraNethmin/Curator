'use client';

import type { Setting } from '@/lib/api';

/**
 * One registry row, rendered by its `kind`.
 *
 * The switch is over the kind and nothing else — no list of keys, no per-setting
 * branch. A setting added to the Go registry in phase 8 appears on this screen
 * with no change to this file, which is the point of there being a registry at
 * all, and it is how playback's settings arrive as one entry rather than as a
 * screen.
 *
 * **The label is the environment variable.** Not a humanised key: `.env`, the
 * docs, the validation messages and the "set by …" sentence all name the
 * variable, and a prettier second name for it would be the second vocabulary
 * phase 7 exists to prevent. It also means a new setting needs no label anywhere.
 */
export function Field({
  setting,
  edit,
  error,
  confirm,
  onConfirm,
  mismatch,
  onChange,
}: {
  setting: Setting;
  /** undefined is untouched. A string is what was typed; `''` is a clear. */
  edit: string | undefined;
  /** The server's message for this field, from a 400 or a 409. */
  error?: string;
  /** Only read for kind `password`; the confirm box is not a setting. */
  confirm?: string;
  onConfirm?: (value: string) => void;
  mismatch?: boolean;
  onChange: (value: string | undefined) => void;
}) {
  const id = `set-${setting.key}`;
  const shown = displayed(setting, edit);
  const cleared = edit === '';

  // A control the environment owns is disabled, and one queued for clearing is
  // disabled while it says so — an input you can type into that will be erased
  // on save is the trap the explicit control exists to avoid.
  const locked = !setting.editable || (cleared && !typeable(setting));

  const common = {
    id,
    disabled: locked,
    'aria-invalid': error !== undefined || mismatch === true,
  };

  return (
    <div className="field">
      <div className="field-name">
        <label htmlFor={id} className="mono">
          {setting.env}
        </label>
        <div className="field-badges">
          {setting.source === 'env' && <span className="badge">environment</span>}
          {setting.secret &&
            (setting.configured ? (
              <span className="badge ok">configured</span>
            ) : (
              <span className="badge">not set</span>
            ))}
          {setting.unreadable && <span className="badge bad">unreadable</span>}
          {setting.pending_change && <span className="badge warn">restart to apply</span>}
        </div>
      </div>

      <div className="field-control">
        {setting.kind === 'bool' ? (
          <label className="check">
            <input
              {...common}
              type="checkbox"
              checked={truthy(shown)}
              onChange={(e) => onChange(String(e.target.checked))}
            />
            <span className="small muted">{truthy(shown) ? 'on' : 'off'}</span>
          </label>
        ) : setting.kind === 'enum' ? (
          <select {...common} value={shown} onChange={(e) => onChange(e.target.value)}>
            {/* The resolved value is always one of the options — it came
                through the same parser — so there is no empty entry to pick,
                and "clear" is the control that unsets it. */}
            {(setting.options ?? []).map((option) => (
              <option key={option} value={option}>
                {option}
              </option>
            ))}
          </select>
        ) : setting.kind === 'multiline' ? (
          <textarea
            {...common}
            className="mono"
            rows={11}
            spellCheck={false}
            placeholder={setting.key === 'vpn_config' ? wgSkeleton : undefined}
            value={shown}
            onChange={(e) => onChange(typed(setting, e.target.value))}
          />
        ) : (
          <input
            {...common}
            // `password` is the only type that is not text, and every other
            // kind is text on purpose: type="number" turns anything
            // unparseable into an empty string, which this form reads as a
            // clear, and type="url" raises a native tooltip that would preempt
            // the server's own message — which is the message the next
            // start-up would print, and the one worth reading.
            type={setting.kind === 'password' ? 'password' : 'text'}
            inputMode={setting.kind === 'int' ? 'numeric' : undefined}
            autoComplete={setting.kind === 'password' ? 'new-password' : 'off'}
            placeholder={placeholder(setting)}
            value={shown}
            onChange={(e) => onChange(typed(setting, e.target.value))}
          />
        )}

        {setting.kind === 'password' && setting.editable && (
          <input
            type="password"
            autoComplete="new-password"
            placeholder="Confirm the password"
            aria-label={`Confirm ${setting.env}`}
            aria-invalid={mismatch === true}
            value={confirm ?? ''}
            onChange={(e) => onConfirm?.(e.target.value)}
          />
        )}

        <div className="field-notes">
          {/* The shadow case, said before it is discovered. A screen that
              silently drops what you typed is worse than one that refuses it,
              and this is the same sentence the 409 carries when the fact
              arrives late instead. */}
          {!setting.editable && (
            <p className="small muted">
              Set in the environment by <span className="mono">{setting.env}</span>
              {setting.file_env && (
                <>
                  {' '}
                  (or <span className="mono">{setting.file_env}</span>)
                </>
              )}
              , which wins here. Unset it to configure this from the screen.
            </p>
          )}

          {setting.unreadable && (
            <p className="small">
              This value is stored but could not be decrypted — the database and its key file have
              been separated. It is behaving as unset. Restore{' '}
              <span className="mono">curator.key</span> beside the database, or point{' '}
              <span className="mono">SECRET_KEY_FILE</span> at it, or enter the value again to
              replace it.
            </p>
          )}

          {setting.editable && setting.secret && !cleared && (
            <p className="small muted">
              {setting.configured
                ? 'Something is stored. Leave this blank to keep it — it is never sent back to a browser, so there is nothing to show here.'
                : 'Nothing is stored. What you type is encrypted at rest and never returned.'}
            </p>
          )}

          {cleared && (
            <p className="small">
              Will be cleared when this section is saved.{' '}
              <button type="button" className="linkbutton" onClick={() => onChange(undefined)}>
                undo
              </button>
            </p>
          )}

          {/* D29's honest half: what is saved is not what is running, and the
              box shows the saved one — resetting it to the running value after
              a save would look exactly like a save that did not work. */}
          {setting.pending_change && !cleared && (
            <p className="small">
              {setting.secret
                ? 'Saved. Restart curator to apply it.'
                : setting.value === ''
                  ? 'Saved. curator is still running with this unset — restart to apply.'
                  : `Saved. curator is still running on "${setting.value}" — restart to apply.`}
            </p>
          )}

          {error !== undefined && (
            <p className="small field-error" role="alert">
              {error}
            </p>
          )}
          {mismatch && (
            <p className="small field-error" role="alert">
              The two passwords do not match.
            </p>
          )}

          {clearable(setting) && !cleared && (
            <p className="small">
              <button type="button" className="linkbutton" onClick={() => onChange('')}>
                {setting.secret ? 'Clear what is stored' : 'Clear it and use the default'}
              </button>
            </p>
          )}
        </div>
      </div>
    </div>
  );
}

/**
 * What an untouched control shows.
 *
 * The **saved** value, not the running one, whenever they differ: the running
 * value is reported in words underneath. A form that snapped back to what the
 * process is using would make every save look like it had been discarded, and
 * "restart to apply" is only acceptable because it is visible (D29).
 *
 * A secret is always empty. `value` is absent for one — absent, not `''` — and
 * there is no masked stand-in to draw, because there is nothing to mask.
 */
export function baseline(setting: Setting): string {
  if (setting.secret) return '';
  return setting.pending ?? setting.value ?? '';
}

function displayed(setting: Setting, edit: string | undefined): string {
  return edit ?? baseline(setting);
}

/**
 * What a keystroke means.
 *
 * For a secret, an empty box is **untouched**, never a clear: somebody who
 * types a key and deletes it again has not asked to erase what is stored, and
 * the only way to say that is the explicit control. For everything else an
 * empty box is exactly the `''` the API reads as "clear it" — the box is
 * showing the value, so emptying it is the ordinary way to remove one.
 */
function typed(setting: Setting, value: string): string | undefined {
  return setting.secret && value === '' ? undefined : value;
}

/** Kinds with a box that can be emptied by hand. */
function typeable(setting: Setting): boolean {
  return setting.kind !== 'bool' && setting.kind !== 'enum' && !setting.secret;
}

/**
 * Whether to offer an explicit clear.
 *
 * For a secret, always when there is one: an empty box cannot mean "erase this"
 * without also erasing it every time somebody opens the page and saves. For a
 * checkbox or a select there is no empty state to type, so clearing is the only
 * way back to the default — and for VPN_REQUIRED the default is `true`, so
 * unticking it stores `false` and does not restore the default.
 */
function clearable(setting: Setting): boolean {
  if (!setting.editable) return false;
  if (setting.secret) return setting.configured;
  return setting.source === 'stored' && !typeable(setting);
}

function placeholder(setting: Setting): string | undefined {
  if (setting.secret) return setting.configured ? 'Leave blank to keep it' : undefined;
  // An empty DOWNLOADS_PATH is a deliberate setting, not a missing one, and it
  // has to read that way here exactly as it does in the table below.
  if (setting.key === 'downloads_path') return "empty — qBittorrent's own path is used as-is";
  return undefined;
}

/**
 * strconv.ParseBool's vocabulary, because that is what validates the value on
 * the way in and what reads it at the next start. A stored `1` or `True` is a
 * legal boolean that a stricter check here would draw as off.
 */
function truthy(value: string): boolean {
  return ['1', 't', 'true'].includes(value.trim().toLowerCase());
}

/**
 * A shape, not a value. It is the file a provider hands you, with the two
 * secrets replaced by angle brackets so it cannot be mistaken for what is
 * stored — a placeholder that looked like a configured tunnel would be the
 * masked secret D17 refuses, drawn by the UI instead of sent by the API.
 */
const wgSkeleton = `[Interface]
PrivateKey = <the private key your provider issued>
Address = 10.5.0.2/32
DNS = 10.5.0.1

[Peer]
PublicKey = <the server's public key>
Endpoint = <server host>:51820
AllowedIPs = 0.0.0.0/0`;
