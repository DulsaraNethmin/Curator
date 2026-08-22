/**
 * Go durations, as the API sends them.
 *
 * `GET /api/settings` publishes `intervals` straight off the running
 * `*config.Config`, so every value here is whatever `time.Duration.String()`
 * wrote — `"10s"`, `"1h0m0s"`, `"30s"`. Two screens read that object for two
 * unrelated reasons (Activity for its poll interval, the release screens for
 * the search cache's TTL), and one parser between them is the point: a second
 * copy is a second set of units to get wrong.
 *
 * **The floor lives at the call site, not here.** This used to be private to
 * `web/app/activity/page.tsx` and clamped its answer to 1000 ms, which is a
 * *polling* rule — "never poll faster than a second, whatever the server says"
 * — and not a fact about durations. Imposing it on a cache TTL would be
 * harmless today and wrong for ever, so the clamp stayed where it belongs and
 * this returns what the string actually says.
 */
export function parseGoDuration(value: string | undefined): number | null {
  if (!value) return null;

  let ms = 0;
  let matched = false;
  for (const [, amount, unit] of value.matchAll(/([\d.]+)(ns|us|µs|ms|s|m|h)/g)) {
    const n = Number(amount);
    if (Number.isNaN(n)) continue;
    matched = true;
    ms +=
      unit === 'h' ? n * 3_600_000
      : unit === 'm' ? n * 60_000
      : unit === 's' ? n * 1000
      : unit === 'ms' ? n
      : 0; // sub-millisecond units round to nothing useful for a UI timer
  }
  return matched ? ms : null;
}

/**
 * How long ago, in words, for something that happened within the hour.
 *
 * Deliberately narrow. Its one caller is the notice above a restored release
 * list, and that list cannot be older than `SEARCH_CACHE_TTL` — an hour by
 * default — because it is dropped at its deadline. So there are three cases and
 * no need for days, weeks or a dependency: `formatWhen` in `api.ts` is the
 * absolute one, and it is what every other screen wants.
 *
 * Anything an hour or more reads "over an hour ago" rather than a number,
 * because past the TTL the honest statement is that it is old, not how old.
 */
export function ago(then: number, now = Date.now()): string {
  const seconds = Math.max(0, Math.round((now - then) / 1000));
  if (seconds < 60) return 'moments ago';
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} minute${minutes === 1 ? '' : 's'} ago`;
  return 'over an hour ago';
}

/**
 * Seconds remaining, in the words a download screen wants.
 *
 * Coarser than `ago` on purpose. An ETA is an estimate that moves every poll,
 * so second-level precision would be a number flickering next to a progress bar
 * that is not — "about 12 minutes" is both more honest and more readable than
 * "11m 47s". Under a minute it says so without a number at all.
 *
 * Returns null for anything not worth showing, which is the server's `omitempty`
 * arriving as `undefined`: no rate, no metadata, or already finished.
 */
export function formatETA(seconds: number | undefined): string | null {
  if (!seconds || seconds <= 0) return null;
  if (seconds < 60) return 'under a minute left';

  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} min left`;

  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  if (hours < 24) return rest === 0 ? `${hours}h left` : `${hours}h ${rest}m left`;

  const days = Math.round(hours / 24);
  return `${days} day${days === 1 ? '' : 's'} left`;
}
