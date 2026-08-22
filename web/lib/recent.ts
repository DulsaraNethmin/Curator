/**
 * The last release search, held so that leaving the page and coming back does
 * not cost it again.
 *
 * **One entry, deliberately.** A release search is 7.08 s for a film and up to
 * 13 s for television, and the case this exists for is routing away by accident
 * and pressing Back — which is one title, the one you were just looking at. A
 * second entry would buy the "two tabs open" case and pay for it with a
 * question nobody has an answer to: how many, and evicted how.
 *
 * **In memory, not sessionStorage.** Every link in this UI is a `next/link`, so
 * App Router keeps this module alive across every in-app navigation and across
 * Back and Forward. A hard reload drops it, which is correct: a reload is when
 * a stale list is least wanted, and serialising a 91-release answer through
 * JSON to survive one would be work in exchange for a worse answer.
 *
 * **The TTL is an optimisation. The 410 is the guarantee.** Release ids are
 * stamped `now + SEARCH_CACHE_TTL` in a map that lives in curator's process
 * (`internal/indexer/aggregate.go`, `issue`), so a restart expires every id
 * whatever any clock says — no arithmetic here can be trusted to know whether
 * these ids still resolve. What makes this safe is the refusal path:
 * `POST /api/downloads` answers **410** for a dead id, `<Releases>` catches it
 * and calls `forget()`, and the screen offers to search again. The expiry below
 * only keeps the common case from showing an obviously old list.
 */
import type { MediaType, SearchResult } from '@/lib/api';

/** What was held, and when — `at` is what lets the screen say how old it is. */
export type Held = {
  key: string;
  result: SearchResult;
  at: number;
  expires: number;
};

/**
 * The identity of one release search, or **null when there is nothing stable to
 * key on**.
 *
 * The TMDB id and not the title, because the page is addressed by the id and two
 * films can share a name. Season and episode because `/show/` searches per
 * season, and serving season 2's release ids to a season 3 question is silently
 * wrong in exactly the way `internal/indexer/cache.go`'s own `cacheKey`
 * describes — 1337x's failure mode is `ok:true, count:0` rather than an error,
 * so nothing on screen would look amiss.
 *
 * The media type is in the key rather than assumed: 95396 is Severance's tv id
 * and also some film's movie id (D48), so a bare number keys two titles.
 *
 * Returning null without an id is what excludes `/search/`'s release-name mode
 * by construction rather than by a flag each caller has to remember to pass —
 * that mode searches a typed string, which has no stable identity, and the user
 * asked for the most recent *film or show*.
 */
export function releaseKey(
  media: MediaType | string | undefined,
  tmdbID: number | null | undefined,
  season = 0,
  episode = 0,
): string | null {
  if (tmdbID === null || tmdbID === undefined) return null;
  return `${media ?? 'movie'}:${tmdbID}:${season}:${episode}`;
}

let held: Held | null = null;

/**
 * Hold this answer under this key until `expires`.
 *
 * An absolute expiry rather than a TTL, so `recall` needs no notion of how long
 * anything lasts — the writer is the only party that has read
 * `intervals.search_cache_ttl`, and it stamps the deadline once.
 */
export function remember(key: string, result: SearchResult, expires: number, now = Date.now()): void {
  held = { key, result, at: now, expires };
}

/**
 * What is held under this key, or null.
 *
 * **The identity of `result` is preserved**, and that is load-bearing rather
 * than incidental: `<Releases>` resets its dispatch state on
 * `useEffect(…, [result])`, so handing back a fresh object each time would
 * clear the "queued" marks on every render.
 */
export function recall(key: string, now = Date.now()): Held | null {
  if (held === null || held.key !== key) return null;
  if (now >= held.expires) {
    held = null;
    return null;
  }
  return held;
}

/**
 * Drop whatever is held. No argument, because there is only ever one — and the
 * caller that matters is the 410 handler, which knows the ids are dead but not
 * necessarily which key issued them.
 */
export function forget(): void {
  held = null;
}
