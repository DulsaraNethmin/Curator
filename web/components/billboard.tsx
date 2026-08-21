'use client';

import Link from 'next/link';
import { backdropURL, type MediaType, type MovieSummary } from '@/lib/api';
import { LibraryBadge } from '@/components/movie-card';

/**
 * The billboard: one title, full width, at the top of Discover.
 *
 * It is the trending rail's FIRST card and not a second request. That rail is
 * already being fetched, its first entry is by definition the thing TMDB thinks
 * most people want this week, and asking for a "featured" title separately would
 * be a thirteenth call to answer a question already on the page.
 *
 * **It is deliberately dark in both themes, which nothing else here is.** Every
 * other surface in curator paints --text on --panel and flips with the system;
 * this one paints white on a photograph. The reason is that the background is
 * not ours — it is an arbitrary film still — so the honest scrim is one that
 * makes the image dark enough for one known text colour, rather than one that
 * tries to stay legible for two. `.hero` on the movie screen takes the opposite
 * approach and is right to: it is a panel with a poster beside it, and its text
 * sits over the page's own background at 92%.
 *
 * It renders NOTHING without a backdrop. A billboard is the image; without one
 * this is a large empty box with a title in it, which is worse than the rail
 * that follows.
 */
export function Billboard({ film, media }: { film: MovieSummary; media: MediaType }) {
  const backdrop = backdropURL(film.backdrop_path);
  if (!backdrop) return null;

  const href = media === 'tv' ? `/show/?id=${film.tmdb_id}` : `/movie/?id=${film.tmdb_id}`;

  return (
    <section className="billboard" style={{ backgroundImage: `url(${backdrop})` }}>
      <div className="billboard-scrim">
        <div className="billboard-text">
          <p className="billboard-eyebrow">
            {media === 'tv' ? 'Trending show' : 'Trending film'}
          </p>
          <h2 className="billboard-title">{film.title}</h2>

          <div className="billboard-meta">
            {/* Year 0 is a title TMDB has no date for — the same honest wording
                the cards use rather than a blank space. */}
            <span>{film.year || 'no date'}</span>
            {film.vote_average > 0 && <span>★ {film.vote_average.toFixed(1)}</span>}
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
      </div>
    </section>
  );
}
