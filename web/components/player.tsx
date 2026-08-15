'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError, api, type PlaybackURLs } from '@/lib/api';
import { Failure } from '@/components/states';

/**
 * The player, and the fallback chain behind it.
 *
 *     direct play  →  (error)  →  remux, if there is one  →  (error)  →  the VLC card
 *
 * **Nothing here asks what is inside the file** (`docs/phase-8.md`, "No codec
 * probe"). curator keeps no codec-support matrix, because that is a table which
 * is wrong the week it is written, and it never runs ffprobe to form an opinion
 * that could then disagree with the browser sitting in front of the user. The
 * browser is the authority and it answers by failing.
 *
 * Two things make that harder than it sounds, and both are measured rather than
 * anticipated. They are `stall()` and `advance()` below.
 */

/**
 * How long a source gets to produce metadata before it is treated as failed.
 *
 * **The chain cannot wait on the `error` event alone, and this is the measured
 * reason.** A `<video>` pointed at an .mkv Chrome will not decode fired
 * `loadstart`, then `stalled`, and then NOTHING for twenty seconds — no error
 * event at all, `networkState` stuck at 2 and `readyState` at 0. `phase-8.md`
 * calls the error event the codec authority and it is, *when it fires*; Chrome
 * is entitled to simply hang instead. Without a timer beside the handler, Play
 * spins for ever and the fallback chain never runs.
 *
 * Twelve seconds because both ends are real: a film that is going to play
 * announces its metadata almost immediately over a LAN, and a remux has to
 * start an ffmpeg and produce a first fragment before anything arrives. Long
 * enough not to cut off a slow success, short enough that the whole chain
 * cannot outlast somebody's patience.
 */
const STALL_MS = 12_000;

/**
 * How long a source gets to produce a *picture* after its metadata has arrived.
 *
 * Metadata means the container parsed; it does not mean anything can be
 * decoded, and the two are further apart than they look. Measured both ways in
 * Chrome 151: an MKV whose video is MPEG-2 reports metadata and then never
 * produces a frame, and an MKV that plays perfectly reports metadata with
 * `videoWidth` still 0 and fills it in a moment later. So neither event nor
 * dimension is conclusive on its own — only dimensions *after a grace* are.
 *
 * Three seconds is far longer than the tens of milliseconds a working source
 * takes and far shorter than STALL_MS, which stays the cap for a source that
 * never says anything at all.
 */
const PICTURE_MS = 3_000;

/** How often the two deadlines above are checked. Cheap enough not to matter. */
const POLL_MS = 250;

/** `HTMLMediaElement.HAVE_METADATA` — the container parsed. */
const HAVE_METADATA = 1;

/** Where in the chain we are. */
type Step = 'direct' | 'remux' | 'vlc';

/**
 * Why the chain stopped early, when it did.
 *
 * A transport failure is NOT a codec failure and must not be treated as one:
 * remuxing an expired session produces a second failure and a VLC card for a
 * film that would have played fine after logging back in.
 */
type Halt = { title: string; detail: string };

/**
 * @param autoStart fetch the playback URLs as soon as this mounts, and draw no
 *   Play button of its own.
 *
 *   It exists so that "watch it here" and "open it in Jellyfin" can sit together
 *   in the hero, which they could not while this component owned the only button
 *   that starts playback. The caller decides when to mount it; everything below
 *   — the stall deadlines, the picture poll, the HEAD probe, the VLC card — is
 *   unchanged and unaware.
 */
export function Player({
  movieID,
  title,
  autoStart,
}: {
  movieID: number;
  title: string;
  autoStart?: boolean;
}) {
  const [urls, setURLs] = useState<PlaybackURLs | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const [step, setStep] = useState<Step>('direct');
  const [halt, setHalt] = useState<Halt | null>(null);

  // Stable, and that is load-bearing rather than tidy: an inline arrow here is
  // a new function on every render, which changes `advance`'s identity, which
  // restarts the stall timer inside <Video> every time this component renders.
  // The timer is the thing that catches a source that never fires an event, so
  // a timer that keeps restarting is the guard quietly not being there.
  const giveUp = useCallback((next: Step, why?: Halt) => {
    setStep(next);
    setHalt(why ?? null);
  }, []);

  async function start() {
    setLoading(true);
    setError(null);
    setHalt(null);
    setStep('direct');
    try {
      // One call, and one <video> after it. Everything the chain might need
      // arrives together, so a fallback is a change of src and not another
      // round trip at the moment something has already gone wrong.
      setURLs(await api.playback(movieID));
    } catch (e) {
      setError(e);
      setURLs(null);
    } finally {
      setLoading(false);
    }
  }

  // Guarded by a ref rather than by the effect's dependencies: start() is a new
  // function on every render, so listing it would re-fire the fetch for ever,
  // and autoStart is a one-shot instruction rather than a state to keep in sync.
  // A retry after a failure is the Failure card's button, deliberately — an
  // automatic one would hammer an endpoint that has already said no.
  const autoStarted = useRef(false);
  useEffect(() => {
    if (!autoStart || autoStarted.current) return;
    autoStarted.current = true;
    void start();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoStart]);

  if (!urls) {
    return (
      <div className="player">
        {/* With autoStart the caller owns the button, so drawing one here would
            be a second control for the same act. What is left is the progress
            and the failure, which are still this component's to report. */}
        {autoStart ? (
          loading && <p className="small muted">Starting…</p>
        ) : (
          <button className="primary" onClick={start} disabled={loading}>
            {loading ? 'Starting…' : '▶ Play'}
          </button>
        )}
        {error !== null && <Failure error={error} onRetry={start} />}
      </div>
    );
  }

  return (
    <div className="player">
      {step !== 'vlc' && (
        <Video key={step} step={step} urls={urls} onGiveUp={giveUp} />
      )}
      <Status step={step} urls={urls} halt={halt} />
      {step === 'vlc' && <VLCCard urls={urls} title={title} />}
    </div>
  );
}

/**
 * One source, one `<video controls>`, and the two ways it can fail.
 *
 * `controls` and nothing else: play, seek, volume, fullscreen,
 * picture-in-picture and captions, keyboard-accessible, in every browser, for
 * free. A custom skin is a week of work and a permanent accessibility bug.
 */
function Video({
  step,
  urls,
  onGiveUp,
}: {
  step: Exclude<Step, 'vlc'>;
  urls: PlaybackURLs;
  onGiveUp: (next: Step, why?: Halt) => void;
}) {
  const src = step === 'direct' ? urls.stream_url : (urls.remux_url ?? '');
  const video = useRef<HTMLVideoElement>(null);
  const done = useRef(false);

  /**
   * What the chain does when a source does not work: **one HEAD, and then a
   * decision the error event could not have supported**.
   *
   * `MediaError.code` is 4 for a container the browser refuses *and* for a
   * response it could not load at all — an expired session, a 404, an ffmpeg
   * that died. The status tells the difference the event cannot:
   *
   *   200          the bytes are there and the browser will not decode them → next step
   *   401          the session went away while the page stayed open → stop, and log back in
   *   404, 5xx     the film or the process is the problem → stop, and say which
   */
  const advance = useCallback(async () => {
    if (done.current) return;
    done.current = true;

    const next: Step = step === 'direct' && urls.remux_url ? 'remux' : 'vlc';

    // **Stop this source before probing it, and the first reason is measured.**
    // curator speaks HTTP/1.1 with no TLS and no h2, and a HEAD sent to a URL
    // this element is still streaming gets queued behind that stream on the
    // same connection: the probe took 20 SECONDS instead of the 4 ms it takes
    // against an idle server, and the whole fallback chain waited on it. The
    // second reason needs no measurement — this source has already failed, so
    // its download should stop rather than run to completion in the background
    // while the next one plays.
    //
    // Removing the attribute and calling load() is what actually aborts the
    // request; setting src to '' resolves against the page URL and fetches the
    // document. A load() with no source fires its own error event, which
    // re-enters here and returns at the guard above.
    const element = video.current;
    element?.removeAttribute('src');
    element?.load();

    try {
      await api.probe(src);
      // 200: a real codec failure, which is the one case the chain is for.
      onGiveUp(next);
    } catch (e) {
      const status = e instanceof ApiError ? e.status : -1;
      if (status === 401) {
        // <Gate> is already drawing the login form — api.probe goes through the
        // same request() every other call does, so a 401 here reaches it. All
        // that is left is to not remux, and to say why nothing is playing.
        onGiveUp('vlc', {
          title: 'The session expired while this page was open',
          detail:
            'Log back in and press Play again. Nothing was remuxed: this was not the file, it was the password.',
        });
        return;
      }
      onGiveUp('vlc', {
        title: status === 404 ? 'That file is not there any more' : 'curator could not serve this film',
        detail:
          e instanceof Error && e.message
            ? e.message
            : 'The stream could not be loaded, and the reason did not come back with it.',
      });
    }
  }, [src, step, urls.remux_url, onGiveUp]);

  /**
   * The two ways a source fails without ever firing `error`, both measured in a
   * real visible Chrome 151 rather than anticipated.
   *
   * **It can hang.** A `<video>` fires `loadstart`, then `stalled`, and then
   * nothing at all — no error event, `networkState` 2 and `readyState` 0. So
   * there is a timer, and without it Play spins for ever.
   *
   * **It can lie.** Handed an MKV whose video codec it cannot decode — MPEG-2
   * here, and HEVC or VC-1 would do the same — Chrome fires `loadedmetadata`,
   * `canplay` and `playing`, reports `readyState` 4, and advances
   * `currentTime`, while `videoWidth` stays 0 and not one frame is ever
   * decoded. It plays the audio track and silently drops the video. Nothing in
   * the event stream says so, and "it started" is therefore not proof that it
   * works: a film would sit there as a black rectangle making noise, with the
   * fallback chain never running because nothing ever reported a failure.
   *
   * So the test for life is a decoded picture — `videoWidth > 0` — and not an
   * event. It is polled rather than taken from any one event, because the event
   * that carries the truth differs per source and per browser, and a listener
   * on the wrong one is a false verdict in whichever direction it errs: too
   * early it remuxes a film that plays, too late it hangs on one that never
   * will. Both of those were live regressions here before this loop replaced
   * them.
   */
  useEffect(() => {
    const element = video.current;
    if (!element) return;

    const startedAt = Date.now();
    let metadataAt = 0;

    const finish = (giveUp: boolean) => {
      clearInterval(poll);
      if (giveUp) void advance();
    };

    const poll = setInterval(() => {
      // A picture. This source works, and nothing more is owed.
      if (element.videoWidth > 0) {
        finish(false);
        return;
      }
      if (element.readyState >= HAVE_METADATA && metadataAt === 0) {
        metadataAt = Date.now();
      }
      // The container parsed and no frame ever came out of it: a codec this
      // browser will not decode, reported as a success by an element that is
      // happily playing the audio track.
      if (metadataAt !== 0 && Date.now() - metadataAt > PICTURE_MS) {
        finish(true);
        return;
      }
      // Nothing at all, for long enough that nothing is coming.
      if (Date.now() - startedAt > STALL_MS) {
        finish(true);
      }
    }, POLL_MS);

    return () => clearInterval(poll);
  }, [advance]);

  return (
    <video
      ref={video}
      className="film"
      src={src}
      controls
      // Not the autoplay the task forbids. That rule is about a film starting
      // on its own when a page loads — a jump scare with sound — and this only
      // ever mounts because somebody pressed Play, which is also the user
      // gesture Chrome's autoplay policy wants.
      autoPlay
      // 'metadata' is what the whole chain turns on, and 'none' would mean
      // nothing is attempted until the user presses play again — which is a
      // second click and a fallback that never runs.
      preload="metadata"
      onError={() => void advance()}
    >
      {/*
       * One <track> per sidecar the library actually holds, and nothing else:
       * the CC button in the browser's own control bar is the whole UI, for the
       * same reason `controls` is — it is keyboard-accessible, translated, and
       * in the place every user already looks.
       *
       * **None of them is `default`.** A default track turns captions on for
       * everybody the moment a film starts, which is a preference nobody
       * expressed; the button is there for the people who want them.
       *
       * The list is empty for almost every film and draws nothing at all.
       */}
      {(urls.subtitles ?? []).map((track) => (
        <track
          key={track.url}
          kind="subtitles"
          src={track.url}
          label={track.label}
          // Omitted rather than empty when the file name declared no language:
          // srclang is what a browser matches against the user's own language
          // preference, and '' is a value it would try to match.
          srcLang={track.language || undefined}
        />
      ))}
    </video>
  );
}

/** What is happening, in one line, and only when it is not simply playing. */
function Status({ step, urls, halt }: { step: Step; urls: PlaybackURLs; halt: Halt | null }) {
  if (halt) {
    return (
      <div className="banner warn" role="status">
        <strong>{halt.title}</strong>
        <span>{halt.detail}</span>
      </div>
    );
  }
  if (step === 'remux') {
    return (
      <div className="banner info" role="status">
        <strong>This browser will not play the file, so ffmpeg is repacking it</strong>
        <span>
          The container is being rewritten and the video and audio are copied untouched — nothing is
          re-encoded, so nothing is lost. It may take a few seconds to start.
        </span>
      </div>
    );
  }
  if (step === 'vlc') {
    return (
      <div className="banner warn" role="status">
        <strong>This browser cannot play this film</strong>
        <span>
          {urls.remux_url
            ? 'Direct play and a remux both failed, which means it is the video or audio itself that this browser will not decode — HEVC, DTS or TrueHD, most likely. A container change moves those bytes without making them decodable, so the answer is a player that can.'
            : 'This install has no ffmpeg, so there is no remux to fall back to — direct play only. A player that decodes more than a browser does will play it.'}
        </span>
      </div>
    );
  }
  return null;
}

/**
 * The end of the chain, and a real fallback rather than an apology: the URL
 * that works in VLC, mpv, an Apple TV or a phone.
 *
 * **The URL is for the clipboard and is never fetched from here.** It carries a
 * bearer credential when there is a password, and fetching it would put that
 * into the browser's network log for no gain at all.
 */
function VLCCard({ urls, title }: { urls: PlaybackURLs; title: string }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(urls.external_url);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Clipboard access is refused outside a secure context, and http://
      // over a LAN is exactly that. The input below is selectable, so there is
      // nothing to recover here and nothing worth an error banner.
    }
  }

  return (
    <div className="panel vlc">
      <h3>Open {title} in a real player</h3>
      <p className="small muted">
        Paste this into VLC — <span className="mono">File ▸ Open Network…</span> — or mpv, or
        anything that takes a URL.
      </p>
      <div className="vlc-url">
        <input readOnly value={urls.external_url} onFocus={(e) => e.currentTarget.select()} />
        <button onClick={copy}>{copied ? 'Copied' : 'Copy'}</button>
      </div>
      <p className="small muted">
        {urls.expires_at ? (
          <>
            This URL contains a key that works for <strong>this film only</strong>, for 12 hours —
            until {new Date(urls.expires_at).toLocaleString()} — and it stops working the moment the
            password changes. Treat it like the password itself.
          </>
        ) : (
          <>
            This install has no password, so the URL contains no key and does not expire. Anyone who
            can reach curator on the network can already play this film.
          </>
        )}
      </p>
    </div>
  );
}
