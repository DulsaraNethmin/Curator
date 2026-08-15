'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  api,
  jellyfinFailure,
  type JellyfinProbe,
  type JellyfinProvisioned,
  type SettingsResult,
} from '@/lib/api';

/**
 * How do you want to watch?
 *
 * One question, asked once, with **two equal answers** — and the browser one is
 * complete. Phase 8's player is a real answer, not a step on the way to the
 * real one, so choosing it runs no second container, asks nothing further and
 * never asks again.
 *
 * Choosing Jellyfin walks the whole way: one pasted line, a poll while the
 * container starts, a username and password typed here rather than in
 * Jellyfin's own wizard, and an ending. **curator never runs the command** — it
 * cannot, deliberately, because the only way to start a container from in here
 * is to be handed root on the host (D34).
 *
 * The library path is shown and never typed. Two services disagreeing about a
 * path is the number-one silent failure in self-hosted media — the scan runs,
 * nothing appears, and no error is produced anywhere — and a text input is
 * exactly how it gets reintroduced.
 */
export function Playback({
  settings,
  onSaved,
}: {
  settings: SettingsResult;
  onSaved: (next: SettingsResult) => void;
}) {
  const target = settings.settings.find((row) => row.key === 'playback_target');
  const apiKey = settings.settings.find((row) => row.key === 'jellyfin_api_key');

  // What is SAVED, not what is running: this screen is about the answer having
  // been given, and an answer given a moment ago has not restarted anything.
  // pending wins when there is one, exactly as the form's own banner reports.
  const answer = (target?.pending_change ? (target.pending ?? '') : (target?.value ?? '')).trim();
  const linked = apiKey?.configured === true || apiKey?.pending_change === true;

  const [choosing, setChoosing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);

  // The question is open when nobody has answered it, or when somebody has
  // pressed Change. Those are the only two ways in — it is never a modal in
  // front of an empty library.
  const asking = answer === '' || choosing;

  async function choose(value: string) {
    setSaving(true);
    setFailure(null);
    try {
      onSaved(await api.updateSettings({ playback_target: value }));
      setChoosing(false);
    } catch (e) {
      setFailure(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  if (asking) {
    return (
      <div className="panel playback-ask">
        <h2 style={{ marginTop: 0 }}>How do you want to watch?</h2>
        <p className="small muted" style={{ maxWidth: '46rem' }}>
          Both answers are complete. You can change this later, and nothing else on this screen
          depends on it — search, the library and downloads all work either way.
        </p>

        {failure !== null && (
          <div className="banner error" role="alert">
            {failure}
          </div>
        )}

        <div className="row" style={{ gap: '.75rem', flexWrap: 'wrap' }}>
          <button className="primary" disabled={saving} onClick={() => void choose('browser')}>
            In this browser
          </button>
          <button className="primary" disabled={saving} onClick={() => void choose('jellyfin')}>
            On my TV, phone or Apple TV
          </button>
          {choosing && (
            <button type="button" disabled={saving} onClick={() => setChoosing(false)}>
              Cancel
            </button>
          )}
        </div>

        <dl className="playback-choices">
          <dt>In this browser</dt>
          <dd className="small muted">
            curator plays films itself. It streams the file directly, and hands the container the
            browser refuses to ffmpeg without re-encoding anything. Nothing else runs.
          </dd>
          <dt>On my TV, phone or Apple TV</dt>
          <dd className="small muted">
            Those need Jellyfin, which has the apps curator is never going to write. curator will
            set it up for you — including the one step people get wrong, which is making both
            services agree about where the films are.
          </dd>
        </dl>
      </div>
    );
  }

  if (answer === 'browser') {
    return (
      <div className="panel playback-ask">
        <h2 style={{ marginTop: 0 }}>Watching in this browser</h2>
        <p className="small muted" style={{ maxWidth: '46rem' }}>
          Press Play on any film. Nothing else is running and nothing else is needed.
        </p>
        <button type="button" onClick={() => setChoosing(true)}>
          Change how you watch
        </button>
      </div>
    );
  }

  return <JellyfinSetup linked={linked} onSaved={onSaved} onChange={() => setChoosing(true)} />;
}

/** How long between probes while a container starts. */
const pollInterval = 3000;

/**
 * The Jellyfin half: paste one line, wait, fill in two fields, done.
 *
 * It polls patiently and **never times out into an error**. A first run pulls
 * 202 MB of layers before a 14-27 second cold start, so the honest wait is
 * minutes on a slow line — and a screen that gave up would need a reload to
 * leave, at the exact moment the thing it was waiting for arrived.
 */
function JellyfinSetup({
  linked,
  onSaved,
  onChange,
}: {
  linked: boolean;
  onSaved: (next: SettingsResult) => void;
  onChange: () => void;
}) {
  const [probe, setProbe] = useState<JellyfinProbe | null>(null);
  const [done, setDone] = useState<JellyfinProvisioned | null>(null);

  // Polling stops once there is something to do, and once it is done. It is a
  // ref rather than state so the interval closure reads the current answer
  // instead of the one it was created with.
  const stop = useRef(false);
  stop.current = done !== null;

  const look = useCallback(async () => {
    try {
      setProbe(await api.jellyfinProbe());
    } catch {
      // Swallowed on purpose. This runs on a loop while a container starts, and
      // curator itself being briefly unreachable is not a thing to put a red
      // banner on — the next tick answers. A real failure shows as the probe
      // state, which is what the screen renders anyway.
    }
  }, []);

  useEffect(() => {
    void look();
    const id = setInterval(() => {
      if (!stop.current) void look();
    }, pollInterval);
    return () => clearInterval(id);
  }, [look]);

  if (done) {
    return <Finished done={done} onChange={onChange} />;
  }

  // Already linked and not mid-setup: this is the resting state of an install
  // that finished. It is deliberately not the ending panel — that one is the
  // end of an action, and this is a status.
  if (linked && probe?.state === 'configured') {
    return (
      <div className="panel playback-ask">
        <h2 style={{ marginTop: 0 }}>Watching on your TV, through Jellyfin</h2>
        <p className="small muted" style={{ maxWidth: '46rem' }}>
          Jellyfin {probe.version} is set up at <span className="mono">{probe.url}</span> and curator
          has its own API key. Films are refreshed there on every import.
        </p>
        <button type="button" onClick={onChange}>
          Change how you watch
        </button>
      </div>
    );
  }

  return (
    <div className="panel playback-ask">
      <h2 style={{ marginTop: 0 }}>Setting up Jellyfin</h2>

      <Command probe={probe} />

      {probe?.state === 'needs_setup' && (
        <ProvisionForm probe={probe} onSaved={onSaved} onDone={setDone} />
      )}

      {probe?.state === 'configured' && <AlreadyConfigured probe={probe} />}

      <button
        type="button"
       
        style={{ marginTop: '1rem' }}
        onClick={onChange}
      >
        Change how you watch
      </button>
    </div>
  );
}

/**
 * The one pasted line, and why it is pasted.
 *
 * Not an apology. curator cannot start containers on purpose: the only way to
 * do it is `-v /var/run/docker.sock`, which is root on the host handed to a
 * service that ships with authentication off by default (D25, D34).
 */
function Command({ probe }: { probe: JellyfinProbe | null }) {
  const command = probe?.command ?? 'docker compose --profile jellyfin up -d';
  const waiting = probe === null || probe.state === 'unreachable' || probe.state === 'starting';

  return (
    <>
      <p className="small muted" style={{ maxWidth: '46rem' }}>
        Run this in the same terminal you installed curator from. curator will not run it for you —
        starting a container from in here would mean handing curator root on this machine, and it
        ships with its password off by default. The line adds Jellyfin and leaves curator running.
      </p>

      <Copyable text={command} />

      {waiting && (
        <p className="small" role="status" style={{ marginTop: '.75rem' }}>
          {probe?.state === 'starting' ? (
            <>
              <strong>Jellyfin is starting.</strong> It answers in about 15 to 30 seconds once it is
              up.
            </>
          ) : (
            <>
              <strong>Waiting for Jellyfin at {probe?.url ?? 'the compose network'}.</strong> A first
              run downloads about 200 MB before it starts, so this can take a few minutes. This page
              keeps checking — you can leave it open.
            </>
          )}
        </p>
      )}
    </>
  );
}

/**
 * A command with a copy button that works without TLS.
 *
 * `navigator.clipboard` is undefined outside a secure context, and this product
 * is served over plain HTTP on a LAN address by design (D25) — so the copy
 * button is unavailable on exactly the install this screen was written for. It
 * falls back to selecting the text, which is one keystroke from the same
 * result, and says so rather than failing silently.
 */
function Copyable({ text }: { text: string }) {
  const [state, setState] = useState<'idle' | 'copied' | 'select'>('idle');
  const box = useRef<HTMLElement>(null);

  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setState('copied');
    } catch {
      // No clipboard, so select it instead and let the keyboard finish.
      const node = box.current;
      if (node) {
        const range = document.createRange();
        range.selectNodeContents(node);
        const selection = window.getSelection();
        selection?.removeAllRanges();
        selection?.addRange(range);
      }
      setState('select');
    }
  }

  return (
    <div className="row" style={{ alignItems: 'center', gap: '.75rem', flexWrap: 'wrap' }}>
      <code className="mono command" ref={box}>
        {text}
      </code>
      <button type="button" onClick={() => void copy()}>
        Copy
      </button>
      {state === 'copied' && <span className="small muted">copied</span>}
      {state === 'select' && (
        <span className="small muted">
          selected — press ⌘C or Ctrl+C. (Copying needs HTTPS, and this page is not served over it.)
        </span>
      )}
    </div>
  );
}

/**
 * The credentials, the path, and the address the TV needs.
 *
 * Three fields, and only two of them are typed freely. The path is read-only
 * because it is the whole point of the shared volume, and the public URL is
 * **guessed from this browser** — curator runs in a container and cannot learn
 * its host's LAN address, but the page was reached over one, so
 * `window.location.hostname` is the answer that is already on screen. It is
 * offered and editable and never written silently: a wrong one produces an Open
 * in Jellyfin link that fails only for other people's devices, which is the
 * hardest kind of bug to be told about.
 */
function ProvisionForm({
  probe,
  onSaved,
  onDone,
}: {
  probe: JellyfinProbe;
  onSaved: (next: SettingsResult) => void;
  onDone: (done: JellyfinProvisioned) => void;
}) {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [publicURL, setPublicURL] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [step, setStep] = useState<string | undefined>();
  const [instructions, setInstructions] = useState<string[] | undefined>();
  const [adopt, setAdopt] = useState(false);

  // Seeded once, from the address this page was reached at. Inside a useEffect
  // because `window` does not exist while the static export is prerendered.
  useEffect(() => {
    setPublicURL(`http://${window.location.hostname}:8096`);
  }, []);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError(null);
    setStep(undefined);
    setInstructions(undefined);
    setAdopt(false);
    try {
      const result = await api.jellyfinProvision({
        username,
        password,
        public_url: publicURL.trim(),
      });
      // The settings the provision wrote are now stale on this page, so it is
      // re-read rather than patched: one source of truth, and the pending
      // banner above updates with it.
      onSaved(await api.settings());
      onDone(result);
    } catch (e) {
      const failure = jellyfinFailure(e);
      setError(e instanceof Error ? e.message : String(e));
      setStep(failure.step);
      setInstructions(failure.instructions);
      setAdopt(failure.adopt === true);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} className="playback-form">
      <div className="banner info" role="status">
        <strong>Jellyfin {probe.version} is up and has never been set up.</strong>
        <span>
          Choose the account you will sign in with on your TV. curator creates it, uses it once to
          mint its own key, and does not keep the password.
        </span>
      </div>

      <label>
        <span>Username</span>
        <input
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          autoComplete="off"
          required
        />
        <span className="small muted">This is a Jellyfin account, not a curator one.</span>
      </label>

      <label>
        <span>Password</span>
        <input
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete="new-password"
          required
        />
        <span className="small muted">
          You will type this into the Jellyfin app on your TV. curator forgets it.
        </span>
      </label>

      <label>
        <span>Films are at</span>
        <input value={probe.library_path} readOnly className="mono" />
        <span className="small muted">
          Read-only, and this is the point: curator hands Jellyfin the exact path it writes to, so
          the two cannot disagree. Getting this wrong by hand is the failure that produces an empty
          library and no error anywhere.
        </span>
      </label>

      <label>
        <span>Your TV will reach Jellyfin at</span>
        <input value={publicURL} onChange={(e) => setPublicURL(e.target.value)} className="mono" />
        <span className="small muted">
          Guessed from the address you opened this page at — curator runs in a container and cannot
          see this machine&apos;s network address. Correct it if your TV reaches this machine some
          other way.
        </span>
      </label>

      {error !== null && (
        <div className="banner error" role="alert">
          <strong>
            {step ? `Setting up Jellyfin failed at ${step}` : 'Setting up Jellyfin failed'}
          </strong>
          <span>{error}</span>
          {adopt && (
            <span style={{ display: 'block', marginTop: '.5rem' }}>
              That Jellyfin already belongs to somebody. curator will not rewrite a server a
              household is watching — connecting to an existing one is a separate flow, and until it
              exists the steps below get you there by hand.
            </span>
          )}
          {instructions && instructions.length > 0 && (
            <ol className="small" style={{ marginTop: '.5rem' }}>
              {instructions.map((line) => (
                <li key={line}>{line}</li>
              ))}
            </ol>
          )}
        </div>
      )}

      <button className="primary" disabled={busy}>
        {busy ? 'Setting Jellyfin up…' : 'Set up Jellyfin'}
      </button>
      {busy && (
        <p className="small muted" role="status">
          Creating the account, adding the library and minting a key. The library scan starts
          straight away and can take a moment.
        </p>
      )}
    </form>
  );
}

/**
 * The branch this task does not own.
 *
 * A Jellyfin that has finished its own wizard is somebody's — a NAS install, or
 * the Pi at cutover — and curator will not touch a server a household is
 * watching. Connecting to one is its own flow; until that exists this says so
 * and gives the manual route, which is better than a button that does nothing.
 */
function AlreadyConfigured({ probe }: { probe: JellyfinProbe }) {
  return (
    <div className="banner warn" role="status">
      <strong>There is already a Jellyfin at {probe.url}, and it is set up.</strong>
      <span>
        curator will not run its wizard again — that guard is what stops a household being locked
        out of a server they are watching. Connecting to an existing Jellyfin is a separate flow and
        is not built yet. Until it is:
      </span>
      <ol className="small" style={{ marginTop: '.5rem' }}>
        <li>
          In Jellyfin, add a Movies library at exactly{' '}
          <span className="mono">{probe.library_path}</span> if one does not already cover it.
        </li>
        <li>In Jellyfin: Dashboard → API Keys → new key.</li>
        <li>
          Paste it into the Jellyfin section below as <span className="mono">JELLYFIN_API_KEY</span>,
          with <span className="mono">JELLYFIN_URL</span> set to{' '}
          <span className="mono">{probe.url}</span>.
        </li>
      </ol>
    </div>
  );
}

/**
 * The ending.
 *
 * This screen's whole purpose, and it should read like one: a thing that
 * finished, with the two facts needed to act on it — where to point the TV, and
 * who to sign in as.
 */
function Finished({ done, onChange }: { done: JellyfinProvisioned; onChange: () => void }) {
  return (
    <div className="panel playback-ask">
      <h2 style={{ marginTop: 0 }}>You are done</h2>
      <div className="banner info" role="status">
        <strong>Jellyfin is set up and curator is linked to it.</strong>
        <span>
          Open the Jellyfin app on your TV, phone or Apple TV, point it at{' '}
          <span className="mono">{done.public_url || done.url}</span>, and sign in as{' '}
          <span className="mono">{done.username}</span> with the password you just chose.
        </span>
      </div>

      <dl className="playback-choices">
        <dt>Your films</dt>
        <dd className="small muted">
          The <span className="mono">{done.library}</span> library points at{' '}
          <span className="mono">{done.library_path}</span> — the same directory curator imports
          into, so everything already there is being scanned now and everything new appears on
          import.
        </dd>
        <dt>The API key</dt>
        <dd className="small muted">
          curator minted its own and stored it encrypted. It is never shown again, here or anywhere
          else. The password you chose was used once and not kept.
        </dd>
      </dl>

      <button type="button" onClick={onChange}>
        Change how you watch
      </button>
    </div>
  );
}
