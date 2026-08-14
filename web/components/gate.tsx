'use client';

import { useCallback, useEffect, useState } from 'react';
import { api, onUnauthorized } from '@/lib/api';

/**
 * Gate is the login screen, and it is a component in the layout rather than a
 * route.
 *
 * **The whole design is in what it does with `children`.** A locked gate does
 * not redirect and does not render the page: it returns the login form
 * *instead of* `children`, so the page it is standing in front of is never
 * mounted and its URL is never left. A deep link to `/movie/?id=299536` while
 * logged out asks for a password and then **is that page**, with the query
 * string intact, because nothing ever navigated. A `/login` route would need a
 * `?next=` parameter, a redirect back, and a way to encode a query string
 * inside a query string — and D21 is the standing evidence that routes cost
 * more than components in a static export.
 *
 * Not rendering `children` is also what stops the page underneath firing its
 * own fetches into a 401 and drawing an error banner behind the login form.
 * React unmounts them on the way in and mounts them fresh on the way out, so
 * every screen re-fetches after a login with no cache to invalidate and no
 * generation counter to remember to bump.
 */
type Phase = 'checking' | 'open' | 'locked';

export function Gate({ children }: { children: React.ReactNode }) {
  const [phase, setPhase] = useState<Phase>('checking');

  const lock = useCallback(() => setPhase('locked'), []);

  useEffect(() => {
    // One hook, for the 401 that arrives mid-session — a password turned on
    // from another browser, a session that expired, or this browser's own save
    // switching authentication on. It is the same event as the first check
    // failing, so it flips the same state.
    onUnauthorized(lock);
    return () => onUnauthorized(null);
  }, [lock]);

  useEffect(() => {
    let live = true;
    api
      .authStatus()
      .then((status) => {
        if (!live) return;
        setPhase(status.required && !status.authenticated ? 'locked' : 'open');
      })
      .catch(() => {
        // Curator being unreachable is not a locked door, and drawing a login
        // form for it would be a lie somebody would type a password into. Open
        // the gate and let the screen underneath report the failure in its own
        // words — every one of them already does.
        if (live) setPhase('open');
      });
    return () => {
      live = false;
    };
  }, []);

  // Nothing, deliberately. GET /api/auth reads one atomic pointer and answers
  // without touching the database, so this is a frame or two on a same-origin
  // request; a spinner here would flash on every page load to report a wait
  // that does not happen.
  if (phase === 'checking') return null;
  if (phase === 'locked') return <Login onIn={() => setPhase('open')} />;
  return <>{children}</>;
}

function Login({ onIn }: { onIn: () => void }) {
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [sending, setSending] = useState(false);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setSending(true);
    setError(null);
    try {
      await api.login(password);
      // Cleared before the state flips, so a password does not sit in a React
      // tree for as long as the tab is open.
      setPassword('');
      onIn();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSending(false);
    }
  }

  return (
    <div className="gate">
      <h1>curator is locked</h1>
      <p className="lede">
        This curator has a password. It is one password for the whole API — there are no accounts,
        so there is nothing to put in a username.
      </p>

      <form className="panel gate-form" onSubmit={submit}>
        <label htmlFor="gate-password">Password</label>
        <input
          id="gate-password"
          type="password"
          autoFocus
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        {error && (
          <p className="banner error" role="alert" style={{ margin: '.4rem 0 0' }}>
            {error}
          </p>
        )}
        <div>
          <button className="primary" disabled={sending || password === ''}>
            {sending ? 'Checking…' : 'Log in'}
          </button>
        </div>
      </form>

      <p className="small muted gate-note">
        Locked out? The environment beats what is stored, so starting curator with{' '}
        <span className="mono">AUTH_ENABLED=false</span> turns the password off without touching the
        database — <span className="mono">docker run -e AUTH_ENABLED=false …</span>
      </p>
      <p className="small muted gate-note">
        There is no TLS. This password crosses the network in clear, so it raises the bar against a
        browser on your LAN and is not protection against something reading it.
      </p>
    </div>
  );
}
