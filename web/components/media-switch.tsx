'use client';

import { useEffect, useState } from 'react';
import { api, televisionOn, type MediaType } from '@/lib/api';

/**
 * Is television on, and do we know yet?
 *
 * Two booleans rather than one, because they answer different questions and a
 * screen needs both. `on` decides whether a Shows affordance exists at all;
 * `known` is what stops it flickering into existence a beat after the page
 * draws — a switch that appears under a cursor that is already moving is worse
 * than one that was always there.
 *
 * **A failed /api/settings answers "off", and that is deliberate.** The Search
 * screen makes the opposite call for TMDB and says why: falling back there
 * would blame a missing key for curator being unreachable. This is not that
 * situation. Nothing here blames anything — it decides whether to draw a
 * control, and drawing one that leads to a route this install answers 503 for
 * is precisely the present-and-broken state D48's opt-in exists to prevent. If
 * curator is unreachable, no screen works and there is nothing to hide.
 *
 * One request per mounted screen, and no cache: `/api/settings` without
 * `?probe=1` is configuration this process has already resolved, which is the
 * same call `<Releases>` and `/library/film/` each make on their own.
 */
export function useTelevision(): { on: boolean; known: boolean } {
  const [on, setOn] = useState<boolean | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .settings()
      .then((settings) => !cancelled && setOn(televisionOn(settings)))
      .catch(() => !cancelled && setOn(false));
    return () => {
      cancelled = true;
    };
  }, []);

  return { on: on === true, known: on !== null };
}

/**
 * The Movies | Shows switch, and the only one — the Library, Discover and
 * Search all draw this rather than three toggles that could disagree about
 * which word means which media type.
 *
 * It is NOT drawn behind its own `useTelevision`. The caller has to know
 * anyway, because the same fact decides which endpoint it asks and what it
 * says when there is nothing there, and a component that hid itself would let a
 * screen forget the rest.
 *
 * Two buttons rather than a <select>: there are exactly two and there will not
 * be a third soon, and a select hides the alternative behind a click.
 */
export function MediaSwitch({
  media,
  onChange,
  labels = ['Movies', 'Shows'],
  disabled,
}: {
  media: MediaType;
  onChange: (media: MediaType) => void;
  // The Search screen says "Films | Shows", because next to it is a second
  // control offering release-name search and "Movies" there reads as a place
  // rather than a kind.
  labels?: [string, string];
  disabled?: boolean;
}) {
  return (
    <div className="switch" role="group" aria-label="Media type">
      <button
        type="button"
        data-active={media === 'movie'}
        aria-pressed={media === 'movie'}
        disabled={disabled}
        onClick={() => onChange('movie')}
      >
        {labels[0]}
      </button>
      <button
        type="button"
        data-active={media === 'tv'}
        aria-pressed={media === 'tv'}
        disabled={disabled}
        onClick={() => onChange('tv')}
      >
        {labels[1]}
      </button>
    </div>
  );
}

/**
 * `?media=tv` off a URL, and `movie` for everything else.
 *
 * The switch rides in a query string for D21's reason twice over: `/library/`
 * is one exported page and the selection has to survive a reload and a shared
 * link, and a static export cannot have a `/library/[media]/` segment. Anything
 * that is not exactly `tv` is a film, including a typo — the server answers 400
 * for a media type it does not know, and a URL is a place people type.
 */
export function mediaFromParams(raw: string | null): MediaType {
  return raw === 'tv' ? 'tv' : 'movie';
}
