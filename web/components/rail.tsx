'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import type { DiscoverRow, MediaType } from '@/lib/api';
import { Empty } from '@/components/states';
import { Icon } from '@/components/icons';
import { MovieCard } from '@/components/movie-card';
import { useReveal } from '@/components/reveal';

/**
 * One rail, its scroll controls, and its own failure.
 *
 * ok and error are per-row for the same reason indexers[] is per-indexer: one
 * rail failing is a success carrying the other. A rail that rendered as an empty
 * row would be indistinguishable from a rail TMDB had nothing for, which is
 * minter's 200-carrying-a-failure wearing a third hat.
 *
 * **The arrows are an addition to the scroll, never a replacement for it.** The
 * strip is still a native overflow container: a trackpad, a shift-wheel, a touch
 * drag and the keyboard all work on it untouched, and the buttons exist because
 * a mouse with a wheel and no horizontal axis is otherwise stuck. That is also
 * why they are removed from the accessibility tree — every card in the rail is
 * already a tab stop, so a screen reader gains nothing from two more buttons
 * that only move pixels, and `aria-hidden` on a control that duplicates existing
 * keyboard access is the correct reading of it.
 */
export function Rail({
  row,
  media,
  index = 0,
}: {
  row: DiscoverRow;
  media: MediaType;
  /** Position on the screen, for the load-time stagger only. */
  index?: number;
}) {
  const strip = useRef<HTMLDivElement>(null);
  const [atStart, setAtStart] = useState(true);
  const [atEnd, setAtEnd] = useState(true);

  // The entrance: rails in the first viewport stagger in on load (.06s a
  // step, capped — a twelfth rail must not owe 660ms to its position), and
  // rails below the fold reveal when scrolled to. The SECTION animates, never
  // a card inside the strip: .rail is an overflow container and a transform
  // in there is shaved off, which is the trap .rail's padding documents.
  const reveal = useReveal<HTMLElement>();
  const enter = {
    ref: reveal.ref,
    'data-reveal': reveal.shown ? ('shown' as const) : ('' as const),
    style: { '--enter-delay': `${Math.min(index, 5) * 60}ms` } as React.CSSProperties,
  };

  // Which arrows are live. scrollWidth and clientWidth are both integers, and
  // sub-pixel layout means a fully-scrolled strip lands a fraction short of the
  // end — hence the slack, without which the right arrow never switches off.
  const measure = useCallback(() => {
    const el = strip.current;
    if (!el) return;
    const slack = 2;
    setAtStart(el.scrollLeft <= slack);
    setAtEnd(el.scrollLeft + el.clientWidth >= el.scrollWidth - slack);
  }, []);

  useEffect(() => {
    const el = strip.current;
    if (!el) return;
    measure();
    // ResizeObserver and not a window resize listener: the rail also changes
    // width when the sidebar-less layout reflows at a breakpoint, and when the
    // posters finish loading and the strip's scrollWidth settles.
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [measure, row.results.length]);

  // Just under a full strip, so the card at the edge stays half in view and the
  // eye keeps its place. A whole viewport of scroll is how somebody loses track
  // of where they were.
  const page = (direction: -1 | 1) => {
    const el = strip.current;
    if (!el) return;
    el.scrollBy({ left: direction * el.clientWidth * 0.85, behavior: 'smooth' });
  };

  if (!row.ok) {
    return (
      <section className="rail-section" {...enter}>
        <h2>{row.title}</h2>
        <div className="banner warn">
          <strong>{row.title} could not be loaded</strong>
          <span className="small">{row.error ?? 'no reason given'}</span>
        </div>
      </section>
    );
  }

  if (row.results.length === 0) {
    return (
      <section className="rail-section" {...enter}>
        <h2>{row.title}</h2>
        <Empty>TMDB returned nothing for {row.title.toLowerCase()}.</Empty>
      </section>
    );
  }

  return (
    <section className="rail-section" {...enter}>
      <h2>{row.title}</h2>

      {/* data-* rather than conditional classes so the two arrows and the two
          edge fades are one rule each in CSS instead of four. */}
      <div className="rail-wrap" data-start={atStart} data-end={atEnd}>
        <button
          type="button"
          className="rail-arrow left"
          aria-hidden="true"
          tabIndex={-1}
          onClick={() => page(-1)}
        >
          <Icon name="chevron-left" size="lg" />
        </button>

        <div className="rail" ref={strip} onScroll={measure}>
          {row.results.map((film) => (
            // The key is scoped by the rail, not global: a film and a show can
            // hold the same TMDB id, and one screen never draws both.
            <MovieCard key={film.tmdb_id} film={film} media={media} />
          ))}
        </div>

        <button
          type="button"
          className="rail-arrow right"
          aria-hidden="true"
          tabIndex={-1}
          onClick={() => page(1)}
        >
          <Icon name="chevron-right" size="lg" />
        </button>
      </div>
    </section>
  );
}
