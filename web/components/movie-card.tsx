'use client';

import Link from 'next/link';
import { posterURL, type LibraryState, type MovieSummary } from '@/lib/api';

/**
 * MovieCard is one film in a grid or a rail, and a link to its page.
 *
 * The href is /movie/?id= and not /movie/{id}/ because the UI is a static
 * export: `output: 'export'` cannot build a dynamic route without
 * generateStaticParams, and TMDB ids cannot be enumerated at build time (D21).
 *
 * **With `onPick` it is a button instead** (T67). The manual matcher shows the
 * same grid of the same posters to answer a different question — "which of
 * these is the film in this folder" rather than "show me this film" — and a
 * card that navigated away mid-choice would abandon the row being matched. It
 * is one prop rather than a second component because every other thing about
 * the card, the poster fallback and the library badge included, is the same
 * question answered the same way.
 */
export function MovieCard({ film, onPick }: { film: MovieSummary; onPick?: (film: MovieSummary) => void }) {
  const poster = posterURL(film.poster_path);

  const inside = (
    <>
      {/* A poster is usual here and absent in the library, which is the
          opposite of the Library screen's problem — but the fallback still has
          to look deliberate, because TMDB has films with no artwork. */}
      {poster ? (
        <img src={poster} alt="" loading="lazy" />
      ) : (
        <div className="noposter">{film.title}</div>
      )}

      {/* title attribute because the CSS clamps to two lines. */}
      <div className="title" title={film.title}>
        {film.title}
      </div>
      <div className="meta">
        {/* A film with no release date has year 0. It is not a folder curator
            can write, and saying "no date" is the honest version of that. */}
        <span>{film.year || 'no date'}</span>
        {film.vote_average > 0 && <span>★ {film.vote_average.toFixed(1)}</span>}
        {film.library && <LibraryBadge library={film.library} />}
      </div>
    </>
  );

  if (onPick) {
    return (
      <button type="button" className="movie" onClick={() => onPick(film)}>
        {inside}
      </button>
    );
  }

  return (
    <Link className="movie" href={`/movie/?id=${film.tmdb_id}`}>
      {inside}
    </Link>
  );
}

/**
 * LibraryBadge is the green check on a poster: curator already has this film,
 * or is fetching it.
 */
export function LibraryBadge({ library }: { library: LibraryState }) {
  switch (library.state) {
    case 'imported':
      return (
        <span className="badge ok" title={library.library_path}>
          in library
        </span>
      );
    case 'downloading':
      return <span className="badge warn">downloading</span>;
    default:
      // 'wanted' — a row exists, and nothing is on disk for it yet.
      return <span className="badge">{library.state}</span>;
  }
}
