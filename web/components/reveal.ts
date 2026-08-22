'use client';

import { useEffect, useRef, useState } from 'react';

/**
 * Reveal-on-scroll: `shown` flips true once the element's top clears the last
 * tenth of the viewport, and never flips back — an entrance is a greeting,
 * not a loop, and content re-hiding on scroll-up is how a page starts feeling
 * haunted.
 *
 * The CSS half lives in globals.css under `[data-reveal]`, and only inside
 * prefers-reduced-motion: no-preference — under reduced motion the attribute
 * styles nothing, so content is simply there, in its final state, immediately.
 * The observer still runs; it just toggles an attribute no rule reads.
 *
 * The -10% rootMargin is the preset's "top 90%" trigger. No IntersectionObserver
 * (an old WebView on the LAN) means everything is shown from the start, which
 * is the same page with less theatre.
 */
export function useReveal<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [shown, setShown] = useState(false);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (typeof IntersectionObserver === 'undefined') {
      setShown(true);
      return;
    }
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
          setShown(true);
          observer.disconnect();
        }
      },
      { rootMargin: '0px 0px -10% 0px' }
    );
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  return { ref, shown };
}
