'use client';

import { useEffect, useState } from 'react';

/**
 * How long a search box waits after the last keystroke.
 *
 * 300 ms, argued off a number this UI has already judged: the hover preview
 * opens at 350 ms (`web/components/preview.tsx`) and nobody has called that a
 * delay. TMDB answers in ~150 ms, so the grid lands about 450 ms after typing
 * stops — and "severance" costs one request instead of nine.
 */
export const SEARCH_DEBOUNCE_MS = 300;

/**
 * The value, `ms` after it stopped changing.
 *
 * **The first value is returned immediately, not `ms` later.** That is what
 * keeps `/search/?q=severance` searching on arrival rather than a beat after it:
 * the URL-driven search and the typed one are the same effect, and a shared
 * link must not feel slower than a keystroke.
 *
 * Deliberately a value hook rather than a debounced *callback*. A callback would
 * have to be stable across renders — `findFilms` is a new function every render
 * — and the ref dance that makes that work is how a debounce ends up firing with
 * a stale closure over `media`. Debouncing the input and letting the effect
 * depend on it keeps the dependency list honest.
 */
export function useDebounced<T>(value: T, ms: number = SEARCH_DEBOUNCE_MS): T {
  const [settled, setSettled] = useState(value);

  useEffect(() => {
    if (settled === value) return;
    const t = setTimeout(() => setSettled(value), ms);
    return () => clearTimeout(t);
    // `settled` is deliberately out of the list: including it would restart the
    // timer when the timer itself fires, and the guard above is what makes that
    // safe to leave out.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value, ms]);

  return settled;
}
