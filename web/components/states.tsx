'use client';

import { useEffect, useState } from 'react';
import { ApiError } from '@/lib/api';

/**
 * Working is the difference between "slow" and "broken".
 *
 * A first search takes up to thirteen seconds because a real browser is
 * clearing a Cloudflare challenge behind minter (D2), and a cold library scan
 * takes about nine. A bare spinner for that long reads as a hang, so this says
 * what is happening and counts, which a hang cannot do.
 */
export function Working({ what, hint }: { what: string; hint?: string }) {
  const [seconds, setSeconds] = useState(0);

  useEffect(() => {
    const timer = setInterval(() => setSeconds((s) => s + 1), 1000);
    return () => clearInterval(timer);
  }, []);

  return (
    <div className="banner info" role="status" aria-live="polite">
      <strong>
        {what}… {seconds}s
      </strong>
      {hint && <span className="small">{hint}</span>}
    </div>
  );
}

/**
 * Failure renders the API's own message. Every handler answers
 * {"error": "..."} and those messages were written to be read — replacing them
 * with "something went wrong" throws away the only useful part.
 *
 * The line above it is a CATEGORY and it is optional, because for two statuses
 * there is no true one to draw (D39). A banner with no title is not a degraded
 * banner — it is what the match picker has always rendered for its own 409s.
 */
export function Failure({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const api = error instanceof ApiError ? error : null;
  const heading = title(api);

  return (
    <div className="banner error" role="alert">
      {heading !== null && <strong>{heading}</strong>}
      <span>{error instanceof Error ? error.message : String(error)}</span>
      {onRetry && (
        <div>
          <button onClick={onRetry}>Try again</button>
        </div>
      )}
    </div>
  );
}

/**
 * The category an error belongs in, or null when its status has none.
 *
 * **A title may only say something true of EVERY situation its status covers.**
 * That is the whole rule, and it is what keeps most of this list: 503 is always
 * an unconfigured integration and 502 is always a dependency that is down, so a
 * category can cover many situations without lying about one. It is also why a
 * status number is the wrong thing to derive a *situation* from, which is what
 * the two nulls below are.
 *
 * 409 is ten refusals across eight routes — the torrent has not finished, the
 * torrent is not curator's to delete, curator already has this film, the row is
 * already matched, the row has no match to correct, that TMDB id is taken, the
 * environment owns that setting, and three Jellyfin ones. "Not finished yet"
 * was true of the first and was being printed over all ten.
 *
 * 422 is four, and two of them arrive at the same banner: `ErrNoVideo` and
 * `ErrBadTitle` both leave `failImport` as a 422, and "Nothing to import" is
 * false for the second — there is something to import and its title is the
 * problem.
 *
 * **A caller cannot supply the missing title either**, which is why there is no
 * prop for one: every error state in this UI holds more than one status. The
 * Library screen's single `error` carries load, rescan and delete failures, so
 * any fixed word above it is wrong for two of the three. The only thing that
 * knows which refusal happened is the sentence the server already sent.
 */
function title(error: ApiError | null): string | null {
  if (!error) return 'Something failed';
  switch (error.status) {
    case 0:
      return 'curator is not reachable';
    case 410:
      // The one error whose correct fix is a user action.
      return 'That search has aged out';
    case 503:
      return 'Not configured';
    case 502:
      return 'A dependency is down';
    case 409:
    case 422:
      // Null DELIBERATELY, and not deleted: a removed case falls through to
      // `default` and titles these "Failed (409)", which claims less than
      // nothing — it is the status number dressed as a diagnosis.
      return null;
    case 404:
      return 'Not found';
    default:
      return `Failed (${error.status})`;
  }
}

export function Empty({ children }: { children: React.ReactNode }) {
  return <div className="empty">{children}</div>;
}
