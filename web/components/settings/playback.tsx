'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  api,
  jellyfinFailure,
  type JellyfinAdopted,
  type JellyfinProbe,
  type JellyfinProvisioned,
  type SettingsResult,
} from '@/lib/api';
import { Copyable } from './copyable';

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

  // The adoption, and the way to run it again. `again` is a closure the form
  // hands over so that accepting the library offer does not ask for the
  // password a second time — it stays inside the component that collected it
  // and is never lifted into state up here.
  const [adopted, setAdopted] = useState<{ result: JellyfinAdopted; again: Again } | null>(null);

  // Whether the person has said they already run one somewhere else. It is a
  // way in and not a mode: the screen still decides what to draw from what the
  // server says, because "is this a new Jellyfin?" is a question the software
  // can answer and a person can get wrong.
  const [elsewhere, setElsewhere] = useState(false);

  // Polling stops once there is something to do, and once it is done. It is a
  // ref rather than state so the interval closure reads the current answer
  // instead of the one it was created with.
  const stop = useRef(false);
  stop.current = done !== null || adopted !== null || elsewhere;

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
  if (adopted) {
    return (
      <Connected
        adopted={adopted.result}
        again={adopted.again}
        onSaved={onSaved}
        onChange={onChange}
      />
    );
  }

  // Somebody said they run one elsewhere, so the screen stops describing the
  // bundled one entirely: the address is theirs to type and nothing else on
  // this page applies to it.
  if (elsewhere) {
    return (
      <div className="panel playback-ask">
        <h2 style={{ marginTop: 0 }}>Connecting to your Jellyfin</h2>
        <AdoptForm
          libraryPath={probe?.library_path ?? ''}
          initialURL=""
          onSaved={onSaved}
          onDone={(result, again) => setAdopted({ result, again })}
        />
        <button type="button" style={{ marginTop: '1rem' }} onClick={() => setElsewhere(false)}>
          Back
        </button>
      </div>
    );
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

      {/* A Jellyfin at the bundle's own address that is already set up. The
          branch was chosen by the server, so the form opens straight onto it
          with the address filled in rather than asking a question that has
          already been answered. */}
      {probe?.state === 'configured' && (
        <>
          <div className="banner warn" role="status">
            <strong>There is already a Jellyfin at {probe.url}, and it is set up.</strong>
            <span>
              curator will not run its wizard again — that guard is what stops a household being
              locked out of a server they are watching. Sign in instead and curator will mint its own
              key, add nothing you did not ask for, and change nothing else.
            </span>
          </div>
          <AdoptForm
            libraryPath={probe.library_path}
            initialURL={probe.url}
            onSaved={onSaved}
            onDone={(result, again) => setAdopted({ result, again })}
          />
        </>
      )}

      <div className="row" style={{ gap: '.75rem', marginTop: '1rem', flexWrap: 'wrap' }}>
        <button type="button" onClick={onChange}>
          Change how you watch
        </button>
        {probe?.state !== 'configured' && (
          <button type="button" onClick={() => setElsewhere(true)}>
            I already run Jellyfin somewhere else
          </button>
        )}
      </div>
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
 * Running the adoption again, with the credential still in the form's closure.
 *
 * It is a function rather than the password itself for a reason worth the type
 * alias: the password is used by the component that collected it and by nothing
 * else, so accepting the library offer cannot become a second place in this file
 * that holds one.
 */
type Again = (addLibrary: boolean) => Promise<JellyfinAdopted>;

/**
 * Connecting to a Jellyfin somebody already runs — a NAS install, or the Pi at
 * cutover.
 *
 * **The address is asked for first and probed before anything else.** An
 * unreachable one is a typo or a firewall, and finding that out after somebody
 * has typed a password is a worse experience for no reason.
 *
 * The public URL defaults from the address typed here and **not** from
 * `window.location.hostname`, which is what the setup branch uses. That default
 * is wrong on this branch by construction: the browser reached *curator*, and an
 * adopted Jellyfin is usually on a different host entirely.
 */
function AdoptForm({
  libraryPath,
  initialURL,
  onSaved,
  onDone,
}: {
  libraryPath: string;
  initialURL: string;
  onSaved: (next: SettingsResult) => void;
  onDone: (adopted: JellyfinAdopted, again: Again) => void;
}) {
  const [url, setURL] = useState(initialURL);
  const [probe, setProbe] = useState<JellyfinProbe | null>(null);
  const [looking, setLooking] = useState(false);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [publicURL, setPublicURL] = useState(initialURL);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [step, setStep] = useState<string | undefined>();
  const [instructions, setInstructions] = useState<string[] | undefined>();

  // The address is the only thing asked for until something answers at it.
  async function look(event: React.FormEvent) {
    event.preventDefault();
    setLooking(true);
    setError(null);
    setInstructions(undefined);
    setStep(undefined);
    try {
      const found = await api.jellyfinProbe(url);
      setProbe(found);
      // Seeded from the address that was just typed, and only once: the URL a
      // browser reaches Jellyfin at is usually the one a person opens it at.
      if (found.url) {
        setURL(found.url);
        setPublicURL((current) => (current === '' || current === initialURL ? found.url : current));
      }
    } catch (e) {
      setProbe(null);
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLooking(false);
    }
  }

  // The call, on its own, so it can be handed to the ending as `again` without
  // carrying this component's state with it.
  const again: Again = (addLibrary) =>
    api.jellyfinAdopt({
      url,
      username,
      password,
      public_url: publicURL.trim(),
      add_library: addLibrary,
    });

  async function connect() {
    setBusy(true);
    setError(null);
    setStep(undefined);
    setInstructions(undefined);
    try {
      const result = await again(false);
      onSaved(await api.settings());
      onDone(result, again);
    } catch (e) {
      const failure = jellyfinFailure(e);
      setError(e instanceof Error ? e.message : String(e));
      setStep(failure.step);
      setInstructions(failure.instructions);
    } finally {
      setBusy(false);
    }
  }

  if (probe?.state !== 'configured') {
    return (
      <form onSubmit={look} className="playback-form">
        <label>
          <span>Where is your Jellyfin?</span>
          <input
            value={url}
            onChange={(e) => setURL(e.target.value)}
            placeholder="http://192.168.1.26:8096"
            className="mono"
            required
          />
          <span className="small muted">
            The address you open Jellyfin at. curator runs in a container, so{' '}
            <span className="mono">localhost</span> here means curator&apos;s own container and not
            this machine — use the LAN address.
          </span>
        </label>

        {probe !== null && probe.state !== 'configured' && (
          <div className="banner warn" role="status">
            {probe.state === 'needs_setup' ? (
              <>
                <strong>There is a Jellyfin at {probe.url}, and it has never been set up.</strong>
                <span>
                  There is no account on it to sign in with yet. Finish its own setup wizard first
                  and then come back — or, for a Jellyfin curator starts beside itself, use the
                  bundled one on the previous screen, which needs no password from you.
                </span>
              </>
            ) : (
              <>
                <strong>Nothing that looks like Jellyfin answered at {probe.url}.</strong>
                <span>{probe.detail}</span>
              </>
            )}
          </div>
        )}

        {error !== null && (
          <div className="banner error" role="alert">
            {error}
          </div>
        )}

        <button className="primary" disabled={looking || url.trim() === ''}>
          {looking ? 'Looking…' : 'Look for it'}
        </button>
      </form>
    );
  }

  return (
    <form
      className="playback-form"
      onSubmit={(event) => {
        event.preventDefault();
        void connect();
      }}
    >
      <div className="banner info" role="status">
        <strong>
          Jellyfin {probe.version} answered at {probe.url}, and it is already set up.
        </strong>
        <span>
          Sign in with an account that already exists there. curator uses it once to mint its own API
          key and does not keep the password — it never changes that server&apos;s setup, its
          accounts, or any library on it.
        </span>
      </div>

      <label>
        <span>Jellyfin username</span>
        <input
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          autoComplete="off"
          required
        />
        <span className="small muted">
          An administrator account — creating an API key needs one.
        </span>
      </label>

      <label>
        <span>Jellyfin password</span>
        <input
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete="current-password"
          required
        />
      </label>

      <label>
        <span>Your TV will reach Jellyfin at</span>
        <input value={publicURL} onChange={(e) => setPublicURL(e.target.value)} className="mono" />
        <span className="small muted">
          Taken from the address you just typed rather than from this page&apos;s own — you reached
          curator here, and your Jellyfin is somewhere else.
        </span>
      </label>

      <label>
        <span>curator keeps its films at</span>
        <input value={libraryPath} readOnly className="mono" />
        <span className="small muted">
          Read-only. curator will look for a library on your server that covers this and tell you
          what it found — it adds nothing unless you ask.
        </span>
      </label>

      {error !== null && (
        <div className="banner error" role="alert">
          <strong>{step ? `Connecting failed at ${step}` : 'Connecting failed'}</strong>
          <span>{error}</span>
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
        {busy ? 'Connecting…' : 'Connect to this Jellyfin'}
      </button>
    </form>
  );
}

/**
 * The ending of the adopt branch, and the three sentences it exists to say.
 *
 * Exactly one of them offers a button. The other two are curator saying what it
 * found and stopping — including the one where it cannot help, because a library
 * pointed at a path that server cannot see produces an empty scan and no error
 * anywhere, which is the failure this whole flow exists to remove rather than
 * reproduce.
 */
function Connected({
  adopted,
  again,
  onSaved,
  onChange,
}: {
  adopted: JellyfinAdopted;
  again: Again;
  onSaved: (next: SettingsResult) => void;
  onChange: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState(adopted);

  // Accepting the offer runs the same call again with the one flag set. It is
  // safe to run twice: curator reuses the key it minted the first time rather
  // than leaving a second one behind on somebody's server.
  async function add() {
    setBusy(true);
    setError(null);
    try {
      const next = await again(true);
      onSaved(await api.settings());
      setResult(next);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="panel playback-ask">
      <h2 style={{ marginTop: 0 }}>Connected to your Jellyfin</h2>
      <div className="banner info" role="status">
        <strong>curator has its own API key for Jellyfin {result.version}.</strong>
        <span>
          Open the Jellyfin app on your TV, phone or Apple TV, point it at{' '}
          <span className="mono">{result.public_url || result.url}</span>, and sign in as you already
          do. curator changed nothing about that server
          {result.library.added ? ' except the library you just asked for' : ''}.
        </span>
      </div>

      <LibraryOffer
        adopted={result}
        busy={busy}
        error={error}
        onAdd={result.library.state === 'addable' ? add : undefined}
      />

      <dl className="playback-choices">
        <dt>The check curator ran</dt>
        <dd className="small muted">{result.check.detail}.</dd>
        <dt>Libraries on that server</dt>
        <dd className="small muted">
          {result.library.names.length > 0 ? result.library.names.join(', ') : 'none at all'}.
          curator read all of them and edited none.
        </dd>
      </dl>

      <button type="button" onClick={onChange}>
        Change how you watch
      </button>
    </div>
  );
}

/** The one offer, and the two sentences that are not one. */
function LibraryOffer({
  adopted,
  busy,
  error,
  onAdd,
}: {
  adopted: JellyfinAdopted;
  busy: boolean;
  error: string | null;
  onAdd?: () => Promise<void>;
}) {
  const { state, detail } = adopted.library;

  if (state === 'covered') {
    return (
      <div className="banner info" role="status">
        <strong>
          {adopted.library.added
            ? 'curator added the library.'
            : 'Your films are already covered.'}
        </strong>
        <span>{detail}.</span>
      </div>
    );
  }

  if (state === 'unseen' || state === 'unknown') {
    return (
      <div className="banner warn" role="status">
        <strong>curator cannot add a library to that server.</strong>
        <span>{detail}.</span>
        <span style={{ display: 'block', marginTop: '.5rem' }}>
          That is usually because the two run on different machines and do not share a disk — or
          because Jellyfin sees the same disk at a path of its own, in which case your films are
          already there and nothing is wrong. curator cannot tell which from here, so it is not
          going to guess and point a library at a path that server may not have. Open in Jellyfin
          works either way: it looks films up by their TMDB id, not by their path.
        </span>
      </div>
    );
  }

  if (!onAdd) return null;

  return (
    <div className="banner warn" role="status">
      <strong>No library on that server covers curator&apos;s films.</strong>
      <span>{detail}. curator can add one, which adds a library and changes nothing else.</span>
      <span style={{ display: 'block', marginTop: '.5rem' }}>
        Your existing libraries, accounts and settings are untouched either way — and if you would
        rather do it yourself, add a Movies library at{' '}
        <span className="mono">{adopted.library_path}</span> in Jellyfin instead.
      </span>
      <button type="button" className="primary" disabled={busy} onClick={() => void onAdd()}>
        {busy ? 'Adding…' : `Add a Movies library at ${adopted.library_path}`}
      </button>
      {error !== null && (
        <span className="small" role="alert" style={{ display: 'block', marginTop: '.5rem' }}>
          That did not work: {error}
        </span>
      )}
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
