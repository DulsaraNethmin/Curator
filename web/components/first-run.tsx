'use client';

import { useState } from 'react';
import Link from 'next/link';
import { api, type Movie, type ScanResult, type Setting, type SettingsResult } from '@/lib/api';
import { Icon } from '@/components/icons';
import { Section } from './settings/section';
import { Playback } from './settings/playback';

/**
 * Is this a first run?
 *
 * **Derived every time, never remembered.** No TMDB key configured and nothing
 * in the library is a first run, and that is the whole rule. A
 * `first_run_complete` row would be a second source of truth about a state the
 * settings already describe, and it goes wrong in the one direction that
 * matters: somebody clears the database, keeps the volume, and is shown a
 * configured product with an empty library and no way back to this page.
 *
 * It reads `configured` rather than `source`, which is what keeps the other
 * mistake out. A key handed in with `-e TMDB_API_KEY` is configured, so that
 * container is **not** a first run and is not asked again on every restart —
 * the failure mode of any rule phrased as "nothing is stored". Such a container
 * can still reach this page, because `/setup/` is a real URL; the step there
 * says the environment owns the key rather than offering a box that would be
 * ignored.
 */
export function isFirstRun(settings: SettingsResult, movies: Movie[]): boolean {
  const tmdb = settings.settings.find((row) => row.key === 'tmdb_api_key');
  return movies.length === 0 && tmdb !== undefined && !tmdb.configured;
}

/**
 * The first run: what curator needs, in the order it blocks you.
 *
 * **It is an offer and never a gate.** It draws in place of Discover, which is
 * the screen a stranger lands on and the one screen that is genuinely broken
 * without a TMDB key — every rail on it is a TMDB call. The nav above it is the
 * layout's and is untouched, so every other screen is one click away with
 * nothing dismissed at all.
 *
 * **It is an ordering and a set of sentences over the settings API, not a
 * second editor.** Phase 7 built the fields, the kinds, the validation, the
 * "set in the environment" sentence and the transaction boundary; every step
 * that writes a setting here is a `<Section>` over one or two registry rows,
 * and the playback question is T65's own component rather than a second copy of
 * it with a second set of defaults.
 */
export function FirstRun({
  settings,
  movies,
  onSettings,
  onSkip,
}: {
  settings: SettingsResult;
  movies: Movie[];
  onSettings: (next: SettingsResult) => void;
  /** Rendered only when there is somewhere to skip to — the /setup/ route has the nav instead. */
  onSkip?: () => void;
}) {
  const row = (key: string): Setting | undefined =>
    settings.settings.find((setting) => setting.key === key);

  const tmdb = row('tmdb_api_key');
  const library = row('library_movies');
  const vpn = row('vpn_config');
  const vpnRequired = row('vpn_required');
  const authEnabled = row('auth_enabled');
  const authPassword = row('auth_password');

  return (
    <>
      <h1>Set curator up</h1>
      <p className="lede">
        Four things, in the order they block you. Every one of them is skippable and curator runs
        without any of them — it just does less, and says so where it does. Nothing here is
        remembered as &ldquo;done&rdquo;: this page appears while there is no TMDB key and nothing
        in the library, and it stops appearing when either of those changes.
      </p>

      {tmdb && (tmdb.editable ? <TMDBKey setting={tmdb} onSaved={onSettings} /> : <TMDBFromEnv setting={tmdb} />)}

      <LibraryPath path={settings.paths.library_movies ?? ''} setting={library} />

      {vpn && vpnRequired && (
        <Section
          title="A VPN, or downloads are refused"
          blurb="The wg-quick .conf your provider hands you, pasted whole. It is encrypted at rest, it is never sent back to a browser, and only the torrent engine is bound to the tunnel — a bad config here cannot lock you out of this screen."
          warning={
            <div className="banner warn section-warning" role="status">
              <strong>Not a bug, and not optional by accident</strong>
              <span>
                <span className="mono">VPN_REQUIRED</span> is <span className="mono">true</span>{' '}
                unless you type otherwise, so with no tunnel configured curator refuses to dispatch
                a download and names this setting when it does. A mandatory VPN that defaults to off
                is a slogan.
              </span>
              <span style={{ display: 'block', marginTop: '.5rem' }}>
                On a machine that is already behind one, untick{' '}
                <span className="mono">VPN_REQUIRED</span>. That is the documented escape rather
                than a workaround — curator then warns on every dispatch instead of refusing,
                because an arrangement it cannot see is not one it can vouch for.
              </span>
            </div>
          }
          settings={[vpn, vpnRequired]}
          onSaved={onSettings}
        />
      )}

      {/* T65's screen, not a copy of it. Asking "browser or TV?" twice with two
          sets of defaults is exactly how the two answers end up disagreeing, and
          it owns PLAYBACK_TARGET — this page never writes that on anyone's
          behalf, because the value records an answer somebody gave. */}
      <Playback settings={settings} onSaved={onSettings} />

      {authEnabled && authPassword && (
        <PasswordOffer enabled={authEnabled} password={authPassword} onSaved={onSettings} />
      )}

      <ScanStep films={movies.length} tmdb={tmdb} />

      {onSkip && (
        <p className="small muted" style={{ marginTop: '1.5rem' }}>
          <button type="button" className="linkbutton" onClick={onSkip}>
            Skip all of this and show me the app
          </button>{' '}
          — nothing above is required, and this page is always at{' '}
          <Link href="/setup/">/setup/</Link>.
        </p>
      )}
    </>
  );
}

/**
 * The one thing without which nothing resolves.
 *
 * A `<Section>` over the single registry row, so the box, the secret handling
 * ("what you type is encrypted at rest and never returned") and the
 * restart-to-apply sentence are phase 7's and not written twice. The link is
 * the whole reason this step is not just the Settings screen: the key is free
 * and thirty seconds away, and a wizard that says "configure TMDB" without
 * saying where has moved the problem rather than solved it.
 */
function TMDBKey({ setting, onSaved }: { setting: Setting; onSaved: (next: SettingsResult) => void }) {
  return (
    <Section
      title="A key for TMDB"
      blurb="Without one a film has no title, no poster, no year and no overview. The library still scans and the files still play; every row simply comes back unmatched."
      warning={
        <div className="banner info section-warning" role="status">
          <strong>It is free, and it takes about a minute</strong>
          <span>
            Sign up, open <span className="mono">Settings → API</span>, request a developer key and
            copy the one labelled <span className="mono">API Key (v3 auth)</span>.{' '}
            <a href="https://www.themoviedb.org/settings/api" target="_blank" rel="noreferrer">
              themoviedb.org/settings/api
            </a>
          </span>
        </div>
      }
      settings={[setting]}
      onSaved={onSaved}
    />
  );
}

/**
 * The same step for a container started with `-e TMDB_API_KEY=…`.
 *
 * Drawn as a settled line rather than a disabled form: there is nothing to do
 * here, and the environment winning is a fact worth stating plainly rather than
 * discovering by typing into a box that would be ignored (D28).
 */
function TMDBFromEnv({ setting }: { setting: Setting }) {
  return (
    <div className="panel firstrun-step">
      <h2>A key for TMDB</h2>
      <p className="small muted">
        Already set, in the environment, by <span className="mono">{setting.env}</span> — which wins
        over anything stored, so there is nothing to type here. Unset it to configure this from the{' '}
        <Link href="/settings/">Settings screen</Link> instead.
      </p>
    </div>
  );
}

/**
 * Where the films are — shown, and deliberately not typed.
 *
 * The path comes from `paths`, which is what this process actually resolved
 * rather than what the form would save, so it cannot disagree with what the
 * scanner is about to read. It is not a field for T65's reason: two services
 * disagreeing about a path is the quietest failure in self-hosted media — the
 * scan runs, nothing appears, and no error is produced anywhere — and a text
 * input is how it gets reintroduced. In the bundle this is `/media/movies`, the
 * volume Jellyfin is pointed at, and this step is a confirmation.
 */
function LibraryPath({ path, setting }: { path: string; setting: Setting | undefined }) {
  return (
    <div className="panel firstrun-step">
      <h2>Where your films are</h2>
      <p className="small muted">
        curator scans this directory and everything with a film in it becomes the library. Folders
        are <span className="mono">Title (Year)</span>; nothing is moved, renamed or written there.
      </p>
      <p className="mono command">{path === '' ? '(not set)' : path}</p>
      <p className="small muted">
        {setting?.source === 'env' ? (
          <>
            Set in the environment by <span className="mono">{setting.env}</span>, which wins here.
            In the compose bundle that is the shared volume, and it is what makes curator and
            Jellyfin agree about where the films are.
          </>
        ) : (
          <>
            Change it on the <Link href="/settings/">Settings screen</Link> if that is not where
            they live — it applies the next time curator starts.
          </>
        )}
      </p>
    </div>
  );
}

/**
 * The password, offered once and honestly.
 *
 * **Not a `<Section>`, and this is the only step that had to be written rather
 * than composed.** Setting `auth_password` through the settings form is live on
 * the very next request (D29) while this browser holds no cookie for it, so the
 * next fetch anything makes 401s into the login form — a wizard that locks you
 * out of itself the moment you accept its offer. The plaintext is in hand
 * exactly once, here, so this writes it and signs in with it before letting a
 * single re-render happen.
 */
function PasswordOffer({
  enabled,
  password,
  onSaved,
}: {
  enabled: Setting;
  password: Setting;
  onSaved: (next: SettingsResult) => void;
}) {
  const [value, setValue] = useState('');
  const [confirm, setConfirm] = useState('');
  const [saving, setSaving] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  const owned = !enabled.editable || !password.editable;
  const already = password.configured || password.pending_change;
  const mismatch = value !== '' && confirm !== value;

  async function turnOn(event: React.FormEvent) {
    event.preventDefault();
    if (value === '' || mismatch || saving) return;

    setSaving(true);
    setFailure(null);

    let next: SettingsResult;
    try {
      // Both keys in one write. The API refuses `auth_enabled` with no password
      // to log in with, which is the same ordering mistake said from the other
      // side, and one transaction cannot land half of it.
      next = await api.updateSettings({ auth_enabled: 'true', auth_password: value });
    } catch (cause) {
      setFailure(cause instanceof Error ? cause.message : String(cause));
      setSaving(false);
      return;
    }

    try {
      await api.login(value);
    } catch (cause) {
      // The password is set and live either way — saying so is the difference
      // between "try again" and "reload and use it".
      setFailure(
        `The password is set and is already in force, but signing in from this page failed: ${
          cause instanceof Error ? cause.message : String(cause)
        }. Reload and log in with it.`,
      );
      onSaved(next);
      setSaving(false);
      return;
    }

    // Cleared before the state flips, so a password does not sit in a React tree
    // for as long as the tab is open — the same reason the login form clears it.
    setValue('');
    setConfirm('');
    setDone(true);
    setSaving(false);
    onSaved(next);
  }

  if (owned) {
    return (
      <div className="panel firstrun-step">
        <h2>A password, if you want one</h2>
        <p className="small muted">
          Set in the environment by <span className="mono">{enabled.env}</span> /{' '}
          <span className="mono">{password.env}</span>, which wins over anything stored. Nothing to
          do here.
        </p>
      </div>
    );
  }

  if (already && !done) {
    return (
      <div className="panel firstrun-step">
        <h2>A password, if you want one</h2>
        <p className="small muted">
          There is already one stored. Change or remove it under Access on the{' '}
          <Link href="/settings/">Settings screen</Link> — it is never sent back to a browser, so
          there is nothing to show here.
        </p>
      </div>
    );
  }

  return (
    <form className="panel firstrun-step" onSubmit={turnOn}>
      <h2>A password, if you want one</h2>
      <p className="small muted">
        Off unless you turn it on, and that stays true — curator works exactly the same without one.
        There are no accounts, so there is no username: one password in front of the whole API,
        which is what deletes films, reads the log and holds a WireGuard private key.
      </p>

      <div className="banner warn" role="status">
        <strong>There is no TLS, and this is what that means</strong>
        <span>
          This password crosses your network in clear on every request. It raises the bar against a
          browser on your LAN — a housemate, a guest, a device that should not be deleting films —
          and it is <em>not</em> protection against something reading the network.
        </span>
        <span style={{ display: 'block', marginTop: '.5rem' }}>
          Locked out later? The environment beats what is stored:{' '}
          <span className="mono">-e AUTH_ENABLED=false</span> starts curator with the password off
          and the stored one untouched. There is no database to edit by hand.
        </span>
      </div>

      {done ? (
        <p className="small" role="status">
          The password is on and applies from the next request. You are signed in here — every other
          browser is not.
        </p>
      ) : (
        <div className="firstrun-password">
          <input
            type="password"
            autoComplete="new-password"
            aria-label="Password"
            placeholder="A password"
            value={value}
            onChange={(e) => setValue(e.target.value)}
          />
          <input
            type="password"
            autoComplete="new-password"
            aria-label="Confirm the password"
            aria-invalid={mismatch}
            placeholder="Confirm the password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          />
          {mismatch && (
            <p className="small field-error" role="alert">
              The two passwords do not match.
            </p>
          )}
          {failure !== null && (
            <p className="small field-error" role="alert">
              {failure}
            </p>
          )}
          <div className="row" style={{ gap: '.75rem', flexWrap: 'wrap', alignItems: 'center' }}>
            <button className="primary" disabled={saving || value === '' || mismatch}>
              {saving ? 'Turning it on…' : 'Turn the password on'}
            </button>
            <span className="small muted">This one applies at once, not at the next start.</span>
          </div>
        </div>
      )}
    </form>
  );
}

/**
 * The ending, because finding somebody's films is the right last thing to do.
 *
 * It reports what the scan actually returned rather than "done" — a library of
 * 29 folders answering with 2 films has to be able to explain itself, and
 * `empty` is the number that does it (D33: a folder with no film in it is not a
 * movie).
 */
function ScanStep({ films, tmdb }: { films: number; tmdb: Setting | undefined }) {
  const [result, setResult] = useState<ScanResult | null>(null);
  const [running, setRunning] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);

  // Saved a moment ago and not yet in force: the key is in the database, this
  // process started without one, and D29 does not make an exception for it.
  const keyPending = tmdb?.pending_change === true;

  async function scan() {
    setRunning(true);
    setFailure(null);
    try {
      setResult(await api.scan());
    } catch (cause) {
      setFailure(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setRunning(false);
    }
  }

  return (
    <div className="panel firstrun-step">
      <h2>Find your films</h2>
      <p className="small muted">
        The last thing a first run should do. It reads the directory above, records what it finds
        and asks TMDB about anything it has no match for. Nothing is written to your disk.
      </p>

      {keyPending && (
        <div className="banner warn" role="status">
          <strong>The key you just saved is not in force yet</strong>
          <span>
            curator is still running without one, so this scan will find your films and record them
            with the titles and years from their folder names, and report every one as unmatched.
            Restart curator and scan again: every scan retries the rows that still have no match, so
            nothing is lost by running it now.
          </span>
        </div>
      )}

      {failure !== null && (
        <div className="banner error" role="alert">
          <strong>The scan failed</strong>
          <span>{failure}</span>
        </div>
      )}

      {result && (
        <div className="banner info" role="status">
          <strong>
            {result.scanned} {result.scanned === 1 ? 'film' : 'films'} found, {result.added} new
          </strong>
          <span className="small">
            {result.matched} matched by TMDB, {result.unmatched} unmatched
            {result.empty > 0 && `, ${result.empty} folder${result.empty === 1 ? '' : 's'} with no film in it`}
            .
          </span>
        </div>
      )}

      <div className="row" style={{ gap: '.75rem', flexWrap: 'wrap', alignItems: 'center' }}>
        <button type="button" className="primary" disabled={running} onClick={() => void scan()}>
          {running ? 'Scanning…' : result ? 'Scan again' : 'Scan the library'}
        </button>
        {(result !== null || films > 0) && (
          <Link href="/library/">
            Go to the library <Icon name="arrow-right" size="sm" />
          </Link>
        )}
      </div>
    </div>
  );
}
