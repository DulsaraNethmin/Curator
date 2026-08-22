'use client';

import { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import Link from 'next/link';
import { backdropURL, posterURL, type MediaType, type MovieSummary } from '@/lib/api';
import { LibraryBadge } from '@/components/movie-card';
import { Rating } from '@/components/rating';

/**
 * The hover preview: rest on a card and it expands into the backdrop, the
 * rating and three lines of overview.
 *
 * **Hover-only, and it must cost a touch or keyboard user nothing.** The card
 * itself is already a link to a page that says everything the preview says,
 * so the preview is strictly a duplicate — which is why the portal is
 * aria-hidden with tabIndex -1, exactly the reading the rail arrows already
 * have. It only ever opens on a fine pointer that can hover; a touch drag
 * never sees it.
 *
 * It is a portal on document.body, not a child of the card: a card lives in
 * .rail, an overflow container, and anything that grows inside one is shaved
 * off top and bottom — the trap .rail's own padding documents. Fixed
 * positioning from the card's measured rect sidesteps the container entirely,
 * at the price of closing on any scroll, which is also the correct behaviour:
 * a preview that tracked a moving card would be a cursor fight.
 *
 * The 350ms open delay is what separates resting from passing through; the
 * short close delay lets the pointer cross the gap onto the preview itself
 * without it vanishing underneath.
 */
const OPEN_DELAY = 350;
const CLOSE_DELAY = 80;

export function useHoverPreview(film: MovieSummary, media: MediaType) {
  const [rect, setRect] = useState<DOMRect | null>(null);
  const openTimer = useRef(0);
  const closeTimer = useRef(0);

  useEffect(() => {
    return () => {
      window.clearTimeout(openTimer.current);
      window.clearTimeout(closeTimer.current);
    };
  }, []);

  // Any scroll closes it — including the rail's own, which does not bubble
  // but is caught by capture — because the card has moved out from under it.
  useEffect(() => {
    if (rect === null) return;
    const close = () => setRect(null);
    window.addEventListener('scroll', close, { capture: true, passive: true });
    window.addEventListener('resize', close);
    return () => {
      window.removeEventListener('scroll', close, { capture: true });
      window.removeEventListener('resize', close);
    };
  }, [rect]);

  function onMouseEnter(event: React.MouseEvent) {
    if (!window.matchMedia('(hover: hover) and (pointer: fine)').matches) return;
    const card = event.currentTarget as HTMLElement;
    window.clearTimeout(closeTimer.current);
    window.clearTimeout(openTimer.current);
    openTimer.current = window.setTimeout(() => setRect(card.getBoundingClientRect()), OPEN_DELAY);
  }

  function onMouseLeave() {
    window.clearTimeout(openTimer.current);
    window.clearTimeout(closeTimer.current);
    closeTimer.current = window.setTimeout(() => setRect(null), CLOSE_DELAY);
  }

  function keepOpen() {
    window.clearTimeout(closeTimer.current);
  }

  const node =
    rect === null ? null : (
      <PreviewCard
        film={film}
        media={media}
        anchor={rect}
        onKeep={keepOpen}
        onLeave={onMouseLeave}
        onClose={() => setRect(null)}
      />
    );

  return { bind: { onMouseEnter, onMouseLeave }, node };
}

function PreviewCard({
  film,
  media,
  anchor,
  onKeep,
  onLeave,
  onClose,
}: {
  film: MovieSummary;
  media: MediaType;
  anchor: DOMRect;
  onKeep: () => void;
  onLeave: () => void;
  onClose: () => void;
}) {
  const href = media === 'tv' ? `/show/?id=${film.tmdb_id}` : `/movie/?id=${film.tmdb_id}`;
  // The backdrop is the preview's reason to exist; a poster cropped to the
  // same box is the fallback, and no artwork at all just means more room for
  // the overview.
  const image = backdropURL(film.backdrop_path) ?? posterURL(film.poster_path);

  const width = Math.min(Math.max(anchor.width * 1.45, 240), 340);
  const left = Math.min(
    Math.max(anchor.left + anchor.width / 2 - width / 2, 8),
    window.innerWidth - width - 8
  );
  // An estimate, not a measurement: the image is width * 9/16 and the text
  // block is clamped, so the guess is close enough to keep the preview on
  // screen for cards at the bottom edge.
  const estimated = (image ? width * 0.5625 : 0) + 150;
  const top = Math.max(8, Math.min(anchor.top - 16, window.innerHeight - estimated - 8));

  return createPortal(
    <Link
      href={href}
      className="preview"
      style={{ top, left, width }}
      aria-hidden="true"
      tabIndex={-1}
      onMouseEnter={onKeep}
      onMouseLeave={onLeave}
      onClick={onClose}
    >
      {image && <div className="preview-backdrop" style={{ backgroundImage: `url(${image})` }} />}
      <div className="preview-body">
        <div className="preview-title">{film.title}</div>
        <div className="preview-meta">
          <span>{film.year || 'no date'}</span>
          <Rating score={film.vote_average} />
          {film.library && <LibraryBadge library={film.library} />}
        </div>
        {film.overview && <p className="preview-overview">{film.overview}</p>}
      </div>
    </Link>,
    document.body
  );
}
