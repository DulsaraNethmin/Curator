'use client';

import { useEffect, useState } from 'react';
import Link from 'next/link';
import { backdropURL, type MediaType, type MovieSummary } from '@/lib/api';
import { Icon } from '@/components/icons';
import { LibraryBadge } from '@/components/movie-card';
import { Rating } from '@/components/rating';

/**
 * The billboard: the top of Discover, rotating through the top of trending.
 *
 * It is the trending rail's first FEW cards and not a second request — that
 * rail is already fetched, and asking for "featured" titles separately would
 * be a thirteenth call to answer a question already on the page. A slide
 * without a backdrop is skipped rather than papered over: a billboard IS the
 * image, and none at all renders nothing, same contract as before.
 *
 * **The rotation is decorative, so it answers for itself three ways.** It has
 * a pause control, because an infinite animation without one is the one kind
 * this UI bans; it stops entirely under prefers-reduced-motion, where the
 * dots still work as a jump cut; and it holds while the pointer is over it,
 * because rotating a paragraph out from under a reader is theft. The
 * crossfade itself is two stacked background layers — the new one fades in
 * over the old, opacity only, nothing moves.
 *
 * **It is deliberately dark in both themes, which nothing else here is.**
 * Every other surface paints --text on --panel and flips with the system;
 * this one paints white on a photograph, because the background is an
 * arbitrary film still and a scrim dark enough for ONE known text colour is
 * honest where one trying to serve two is a guess. The controls follow the
 * eyebrow: dark-palette literals, not tokens that flip.
 */
const ROTATE_MS = 8000;
const SLIDES = 5;

export function Billboard({ films, media }: { films: MovieSummary[]; media: MediaType }) {
  const slides = films.filter((film) => backdropURL(film.backdrop_path)).slice(0, SLIDES);

  // One state for both halves of the crossfade, so an advance is atomic and
  // the interval below can step it functionally without owning the index.
  const [slide, setSlide] = useState<{ index: number; previous: number | null }>({
    index: 0,
    previous: null,
  });
  const [paused, setPaused] = useState(false);
  const [hovered, setHovered] = useState(false);
  const [reduced, setReduced] = useState(false);

  useEffect(() => {
    const query = window.matchMedia('(prefers-reduced-motion: reduce)');
    const read = () => setReduced(query.matches);
    read();
    query.addEventListener('change', read);
    return () => query.removeEventListener('change', read);
  }, []);

  // A media switch hands in a new trending list, and an index held from the
  // old one would read off its end.
  useEffect(() => {
    setSlide({ index: 0, previous: null });
  }, [media]);

  const rotating = slides.length > 1 && !reduced;

  function go(next: number) {
    setSlide((current) => ({ index: next, previous: current.index }));
  }

  // An interval, not a timeout re-armed per advance: a hidden tab SKIPS its
  // tick, and a skipped timeout would never re-arm itself — the rotation
  // would quietly die the first time the tab was backgrounded.
  useEffect(() => {
    if (!rotating || paused || hovered) return;
    const count = slides.length;
    const timer = window.setInterval(() => {
      if (document.hidden) return;
      setSlide((current) => ({ index: (current.index + 1) % count, previous: current.index }));
    }, ROTATE_MS);
    return () => window.clearInterval(timer);
  }, [rotating, paused, hovered, slides.length]);

  if (slides.length === 0) return null;

  const { previous } = slide;
  const index = Math.min(slide.index, slides.length - 1);
  const film = slides[index];
  const href = media === 'tv' ? `/show/?id=${film.tmdb_id}` : `/movie/?id=${film.tmdb_id}`;

  return (
    <section
      className="billboard"
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
    >
      {previous !== null && previous < slides.length && (
        <div
          className="billboard-bg"
          style={{ backgroundImage: `url(${backdropURL(slides[previous].backdrop_path)})` }}
        />
      )}
      {/* Keyed so a slide change remounts the layer and re-runs its fade —
          over the previous layer, which just sits there being the past. */}
      <div
        className="billboard-bg billboard-bg-top"
        key={film.tmdb_id}
        style={{ backgroundImage: `url(${backdropURL(film.backdrop_path)})` }}
      />

      <div className="billboard-scrim">
        {/* Keyed too: the text crossfades with its image, not after it. */}
        <div className="billboard-text" key={film.tmdb_id}>
          <p className="billboard-eyebrow">
            {media === 'tv' ? 'Trending show' : 'Trending film'}
          </p>
          <h2 className="billboard-title">{film.title}</h2>

          <div className="billboard-meta">
            {/* Year 0 is a title TMDB has no date for — the same honest wording
                the cards use rather than a blank space. */}
            <span>{film.year || 'no date'}</span>
            <Rating score={film.vote_average} />
            {film.library && <LibraryBadge library={film.library} />}
          </div>

          {/* Clamped to three lines in CSS. TMDB overviews run to a paragraph
              and a billboard is a glance, not a synopsis. */}
          {film.overview && <p className="billboard-overview">{film.overview}</p>}

          <div className="billboard-actions">
            <Link className="button primary" href={href}>
              {film.library ? 'Open' : 'Find a release'}
            </Link>
          </div>
        </div>

        {slides.length > 1 && (
          <div className="billboard-controls">
            {slides.map((slide, i) => (
              <button
                key={slide.tmdb_id}
                type="button"
                className="billboard-dot"
                data-active={i === index}
                aria-label={`Show ${slide.title}`}
                onClick={() => go(i)}
              />
            ))}
            {/* Absent under reduced motion, where there is no rotation to
                pause — a control for a thing that is not happening. */}
            {rotating && (
              <button
                type="button"
                className="billboard-pauseplay"
                aria-pressed={paused}
                aria-label={paused ? 'Resume the rotation' : 'Pause the rotation'}
                onClick={() => setPaused((value) => !value)}
              >
                <Icon name={paused ? 'play' : 'pause'} size="sm" />
              </button>
            )}
          </div>
        )}
      </div>
    </section>
  );
}
