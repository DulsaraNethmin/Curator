'use client';

import { useEffect, useState } from 'react';
import { api, type MinterProbe } from '@/lib/api';
import { Copyable } from './copyable';

/**
 * Is minter there?
 *
 * The Indexers group's one non-field, and the same shape the Playback screen
 * uses for Jellyfin: a probe, one pasted line, and a service that reports
 * itself unconfigured until something answers
 * (docs/tasks/T49-minter-on-demand.md). minter and Jellyfin are the only two
 * things in this product that need a second container, curator cannot start
 * either — `-v /var/run/docker.sock` is root on the host handed to a service
 * that ships with its password off (D25, D34) — and one shape for both is one
 * fewer concept than a flow per companion service.
 *
 * **The toggle is what you control; whether minter is answering is a fact.**
 * That is why this sits beside the switch rather than in the integrations table
 * at the bottom of the page: the table answers "what is this process running",
 * once, on a button, and this has to answer while somebody is in the middle of
 * turning 1337x on.
 */
export function MinterStatus({ on }: { on: boolean }) {
  const [probe, setProbe] = useState<MinterProbe | null>(null);

  useEffect(() => {
    let live = true;
    // One probe in flight at a time. The server budgets 20 s for minter's
    // /health (minterProbeTimeout) because a HEALTHY minter takes 6.7-8.6 s to
    // answer it, and this interval is 5 s — so without this guard a busy minter
    // collects a new request every tick while the previous one is still queued
    // behind its browser. That is precisely the state this component exists to
    // report, and polling harder is the one thing that makes it worse.
    let inFlight = false;
    async function look() {
      if (inFlight) return;
      inFlight = true;
      try {
        const next = await api.minterProbe();
        if (live) setProbe(next);
      } catch {
        // Swallowed. curator itself being briefly unreachable is not something
        // to put a red banner on inside a settings section — the next tick
        // answers, and a real failure arrives as the probe state, which is what
        // this renders anyway.
      } finally {
        inFlight = false;
      }
    }
    void look();
    // Always polling, never once. "Do not report a stale probe" is a
    // requirement rather than a nicety: a minter that was up when the page
    // loaded and has since been stopped must stop reading as up. It is
    // affordable because the server side is one GET of minter's /health and
    // never a page fetch — the browser only wakes for a real search.
    const id = setInterval(() => void look(), pollInterval);
    return () => {
      live = false;
      clearInterval(id);
    };
  }, []);

  // Nothing at all until 1337x is switched on, and `on` is the form's current
  // value including an unsaved edit. YTS and TPB are plain JSON and need no
  // browser, so on an install that never enables 1337x this section says
  // nothing about minter — which is the honest amount.
  if (!on) return null;

  const state = probe?.state ?? null;

  return (
    <div className="minter-status">
      <h3 className="small">1337x needs minter</h3>
      <p className="small muted" style={{ maxWidth: '46rem' }}>
        1337x is behind Cloudflare, and getting past it needs a real browser — that is minter, and
        it is a second container. Run this in the same terminal you installed curator from; curator
        will not run it for you, because starting a container from in here would mean handing curator
        root on this machine. The line adds minter and leaves everything else running.
      </p>

      <Copyable text={probe?.command ?? 'docker compose --profile 1337x up -d'} />

      <p className="small" role="status" style={{ marginTop: '.75rem' }}>
        {state === 'ready' ? (
          <>
            <span className="badge ok">minter is up</span>{' '}
            <span className="muted">
              answering at <span className="mono">{probe?.url}</span>
            </span>
          </>
        ) : state === 'unhealthy' ? (
          <>
            <span className="badge bad">minter is not ready</span>{' '}
            <span className="muted">
              {probe?.detail} — searches through 1337x will fail until it settles.
            </span>
          </>
        ) : state === 'unreachable' ? (
          <>
            <span className="badge bad">minter is not running</span>{' '}
            <span className="muted">
              Nothing answered at <span className="mono">{probe?.url}</span>. A first run downloads
              about 2.2 GB before it starts, and it took 45 seconds from <em>up</em> to ready here —
              this keeps checking, so you can leave the page open.
            </span>
          </>
        ) : (
          <span className="muted">Checking whether minter is up…</span>
        )}
      </p>

      {/* The restart, said once and only when it is true. A setting written
          through the API applies at the next start (D29), so the ordinary path
          — switch 1337x on, save, paste the line — leaves curator searching
          two indexers while this section shows a healthy minter. Without this
          the next search silently returns YTS and TPB only, which is exactly
          the "an indexer that is off is invisible" failure the rest of this
          task removes. */}
      {probe !== null && !probe.enabled && (
        <div className="banner warn" style={{ marginTop: '.75rem' }}>
          <strong>curator is not searching 1337x yet</strong>
          <span>
            This process started with it off. Save below if you have not, then restart curator — the
            indexer comes up with the process, not with the setting.
          </span>
        </div>
      )}
    </div>
  );
}

/**
 * How long between probes.
 *
 * Slower than the Playback screen's 3 s, because that one is watching a
 * container the user has just been told to start and this one is a status
 * beside a form somebody may leave open all afternoon.
 */
const pollInterval = 5000;
