'use client';

import { useState } from 'react';
import Link from 'next/link';
import { posterURL, type LibraryState, type MediaType, type MovieSummary } from '@/lib/api';
import { Rating } from '@/components/rating';
import { useHoverPreview } from '@/components/preview';

/**
 * A poster that fades in when its bytes have actually decoded, instead of
 * popping. data-loaded drives the CSS; the box it fades into (aspect-ratio
 * plus background) is laid out regardless, so the fade never shifts anything.
 *
 * The ref callback is not decoration: a cached image can be `complete` before
 * React attaches onLoad, and without the check such a poster would stay at
 * opacity 0 for ever. `alt=""` because the title is always written next to
 * the artwork — a screen reader should not hear it twice.
 *
 * No src renders the deliberate fallback instead, same as always.
 */
export function Poster({
  src,
  title,
  className,
}: {
  src: string | null;
  title: string;
  className?: string;
}) {
  const [loaded, setLoaded] = useState(false);

  if (!src) {
    return <div className={className ? `${className} noposter` : 'noposter'}>{title}</div>;
  }
  return (
    <img
      className={className}
      src={src}
      alt=""
      loading="lazy"
      data-loaded={loaded}
      ref={(el) => {
        if (el?.complete && el.naturalWidth > 0) setLoaded(true);
      }}
      onLoad={() => setLoaded(true)}
      onError={() => setLoaded(true)}
    />
  );
}

/**
 * MovieCard is one film — or one show — in a grid or a rail, and a link to its
 * page.
 *
 * The href is /movie/?id= and not /movie/{id}/ because the UI is a static
 * export: `output: 'export'` cannot build a dynamic route without
 * generateStaticParams, and TMDB ids cannot be enumerated at build time (D21).
 *
 * **`media` decides WHICH page, and it is not cosmetic.** TMDB's movie and tv
 * id sequences overlap, so /movie/?id=95396 and /show/?id=95396 are two
 * different titles that both exist (D48). A show card that took the default
 * would open a film, with a straight face — which is exactly the failure the
 * server-side split of these routes was built to make impossible, and it would
 * be reintroduced here.
 *
 * **With `onPick` it is a button instead** (T67). The manual matcher shows the
 * same grid of the same posters to answer a different question — "which of
 * these is the film in this folder" rather than "show me this film" — and a
 * card that navigated away mid-choice would abandon the row being matched. It
 * is one prop rather than a second component because every other thing about
 * the card, the poster fallback and the library badge included, is the same
 * question answered the same way.
 */
export function MovieCard({
  film,
  media = 'movie',
  onPick,
}: {
  film: MovieSummary;
  media?: MediaType;
  onPick?: (film: MovieSummary) => void;
}) {
  const poster = posterURL(film.poster_path);

  // The hover preview (T103). Called for both variants because hooks must be,
  // but only the link spreads its handlers: the matcher's button asks "which
  // of these is the folder", and a preview opening over the next candidate
  // would be noise in the middle of an answer.
  const preview = useHoverPreview(film, media);

  const inside = (
    <>
      {/* A poster is usual here and absent in the library, which is the
          opposite of the Library screen's problem — but the fallback still has
          to look deliberate, because TMDB has films with no artwork. */}
      <Poster src={poster} title={film.title} />

      {/* title attribute because the CSS clamps to two lines. */}
      <div className="title" title={film.title}>
        {film.title}
      </div>
      <div className="meta">
        {/* A film with no release date has year 0, and so does a show with no
            first air date. Neither is a folder curator can write, and saying
            "no date" is the honest version of that. */}
        <span>{film.year || 'no date'}</span>
        <Rating score={film.vote_average} />
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
    <>
      <Link
        className="movie"
        href={media === 'tv' ? `/show/?id=${film.tmdb_id}` : `/movie/?id=${film.tmdb_id}`}
        {...preview.bind}
      >
        {inside}
      </Link>
      {preview.node}
    </>
  );
}

/**
 * LibraryBadge is the green check on a poster: curator already has this film or
 * this show, or is fetching it.
 *
 * It needs no media type of its own. `library` is only ever non-null when the
 * server matched this card against a row of the SAME media type — the map it
 * comes from is scoped, precisely so that Severance's poster is not badged with
 * the film holding movie id 95396.
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
