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
 */
export function Failure({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const api = error instanceof ApiError ? error : null;

  return (
    <div className="banner error" role="alert">
      <strong>{title(api)}</strong>
      <span>{error instanceof Error ? error.message : String(error)}</span>
      {onRetry && (
        <div>
          <button onClick={onRetry}>Try again</button>
        </div>
      )}
    </div>
  );
}

function title(error: ApiError | null): string {
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
      return 'Not finished yet';
    case 422:
      return 'Nothing to import';
    case 404:
      return 'Not found';
    default:
      return `Failed (${error.status})`;
  }
}

export function Empty({ children }: { children: React.ReactNode }) {
  return <div className="empty">{children}</div>;
}
