'use client';

import type { VPNStatus } from '@/lib/api';

/**
 * What the guarantee covers, said on the screen rather than in a comment.
 *
 * Two limits, and neither is a footnote. The claim is about the EMBEDDED engine
 * — with an external qBittorrent curator does not own the socket and compares
 * exit addresses instead — and about the TORRENT subsystem only, because
 * searches, TMDB, artwork and Jellyfin are on the host stack by design (D27).
 *
 * It is here because a green badge invites exactly the generalisation these two
 * lines refuse, and somebody reading this page is the person most likely to
 * make it.
 */
export function Scope({ status }: { status: VPNStatus }) {
  const embedded = status.engine.backend === 'embedded';

  return (
    <div className="panel" style={{ padding: '.85rem' }}>
      <h3 className="small">What this covers</h3>

      {embedded ?
        <p className="small muted" style={{ maxWidth: '46rem' }}>
          Every network operation of the torrent subsystem — payload, peers, uTP, DHT, trackers,
          webseeds, WebRTC, DNS and local discovery — is tunnel-bound or disabled. There is no third
          option. curator owns the socket, so a dead tunnel is a failed dial rather than a leak.
        </p>
      : <p className="small muted" style={{ maxWidth: '46rem' }}>
          <strong>This install uses an external qBittorrent.</strong> curator does not own that
          socket and cannot make the guarantee above for it: what it can do is compare exit
          addresses before a dispatch and refuse when they match. Put that client behind its own
          VPN, or run the embedded engine, which curator routes itself.
        </p>
      }

      <p className="small muted" style={{ maxWidth: '46rem' }}>
        <strong>Only download traffic goes through the tunnel.</strong> Searches, TMDB, artwork, the
        update check and Jellyfin leave from this machine&apos;s own address, deliberately — a bad
        tunnel must not be able to lock you out of the screen that fixes it. minter, which fetches
        1337x with a real browser in its own container, is not curator&apos;s to route either.
      </p>
    </div>
  );
}
