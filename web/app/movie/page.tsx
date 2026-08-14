'use client';

import { Suspense, useEffect, useState } from 'react';
import { useSearchParams } from 'next/navigation';
import Link from 'next/link';
import {
  api,
  backdropURL,
  formatRuntime,
  posterURL,
  type MovieDetails,
  type SearchResult,
} from '@/lib/api';
import { Empty, Failure, Working } from '@/components/states';
import { LibraryBadge } from '@/components/movie-card';
import { Releases } from '@/components/releases';
import { Player } from '@/components/player';

/**
 * The film's page: /movie/?id=299534.
 *
 * The id rides in a query string rather than a path segment because the UI is a
 * static export and `output: 'export'` cannot build /movie/[id]/ without
 * enumerating every TMDB id at build time (D21). The <Suspense> boundary is not
 * decoration either: useSearchParams() without one fails the build, which is the
 * good outcome — the toolchain remembers this so nobody has to.
 */
export default function MoviePage() {
  return (
    <Suspense fallback={<p className="lede">Loading…</p>}>
      <Movie />
    </Suspense>
  );
}

function Movie() {
  const params = useSearchParams();
  const raw = params.get('id');
  // Not Number(raw): Number(null) is 0 and Number('abc') is NaN, and NaN would
  // reach the API as the literal URL /api/tmdb/movies/NaN. Digits or nothing.
  const id = raw !== null && /^\d+$/.test(raw) ? Number(raw) : 0;

  const [details, setDetails] = useState<MovieDetails | null>(null);
  const [error, setError] = useState<unknown>(null);

  const [releases, setReleases] = useState<SearchResult | null>(null);
  const [searching, setSearching] = useState(false);
  const [searchError, setSearchError] = useState<unknown>(null);

  // Metadata and releases are two fetches and never a Promise.all. TMDB answers
  // in about 150 ms; a cold release search takes up to thirteen seconds because
  // a real browser is clearing Cloudflare behind minter. Awaiting both would
  // hold a poster hostage to a browser launch — and browsing must not launch one
  // at all, which is why the second fetch is a button.
  useEffect(() => {
    if (!id) return;
    let cancelled = false;

    setDetails(null);
    setError(null);
    setReleases(null);
    setSearchError(null);

    api
      .tmdbMovie(id)
      .then((d) => {
        if (!cancelled) setDetails(d);
      })
      .catch((e) => {
        if (!cancelled) setError(e);
      });

    return () => {
      cancelled = true;
    };
  }, [id]);

  async function findReleases() {
    if (!details) return;
    setSearching(true);
    setSearchError(null);
    try {
      // The canonical title, colon and all. Stripping it is NormaliseQuery's
      // job, server-side and above the cache (D20) — a client that stripped it
      // here would go on to send the stripped title to dispatch and write
      // "Avengers Endgame (2019)" as the folder.
      setReleases(await api.search(details.title, details.year || undefined));
    } catch (e) {
      setSearchError(e);
      setReleases(null);
    } finally {
      setSearching(false);
    }
  }

  if (!id) {
    return (
      <>
        <h1>No film</h1>
        <Empty>
          This page shows one film, addressed by its TMDB id —{' '}
          <span className="mono">/movie/?id=299534</span>. Pick one from{' '}
          <Link href="/">Discover</Link> or <Link href="/search/">Search</Link>.
        </Empty>
      </>
    );
  }

  if (error !== null) {
    return (
      <>
        <h1>Film</h1>
        <Failure error={error} />
      </>
    );
  }

  if (!details) return <p className="lede">Loading…</p>;

  const backdrop = backdropURL(details.backdrop_path);
  const poster = posterURL(details.poster_path);
  const runtime = formatRuntime(details.runtime);

  return (
    <>
      <div className="hero" style={backdrop ? { backgroundImage: `url(${backdrop})` } : undefined}>
        <div className="hero-scrim">
          {poster ? (
            <img className="hero-poster" src={poster} alt="" />
          ) : (
            <div className="hero-poster noposter">{details.title}</div>
          )}

          <div className="hero-text">
            <h1>
              {details.title}
              {details.year > 0 && <span className="year"> {details.year}</span>}
            </h1>

            <div className="meta">
              {runtime && <span>{runtime}</span>}
              {details.genres.length > 0 && <span>{details.genres.join(' · ')}</span>}
              {details.vote_average > 0 && <span>★ {details.vote_average.toFixed(1)}</span>}
              {details.library && <LibraryBadge library={details.library} />}
            </div>

            {details.tagline && <p className="tagline">{details.tagline}</p>}
            {details.overview && <p className="overview">{details.overview}</p>}

            <div className="actions">
              {/* Nothing has touched an indexer up to this point, and nothing
                  will until this is pressed. That is what makes browsing free. */}
              <button className="primary" onClick={findReleases} disabled={searching}>
                {searching ? 'Searching…' : releases ? 'Search again' : 'Find releases'}
              </button>

              {/* Absent means there is nothing to draw — no Jellyfin
                  configured, or a film curator does not have on disk. Not a
                  disabled button and not a tooltip explaining what you are
                  missing, which is the rule the rest of the UI follows. */}
              {details.jellyfin_url && (
                <a className="button" href={details.jellyfin_url} target="_blank" rel="noreferrer">
                  Open in Jellyfin
                </a>
              )}
            </div>
          </div>
        </div>
      </div>

      {/* Play appears ONLY for a film that is actually on disk — only imported
          rows have a library_path, which is also why this can never reach a
          partial download. The id it hands the player is library.movie_id,
          curator's own movies.id, and NOT the TMDB id this page is addressed
          by: they are different numbers and the wrong one is a 404 on a film
          you can see (D21).

          Below the hero rather than beside the poster, because what this
          renders once it starts is a video and not a button. */}
      {details.library?.state === 'imported' && (
        <Player movieID={details.library.movie_id} title={details.title} />
      )}

      <Facts details={details} />

      <h2>Releases</h2>

      {searching && (
        <Working
          what="Searching YTS, TPB and 1337x"
          hint="1337x needs a real browser to clear Cloudflare, so the first search for a title takes ~13s. The next one this hour is instant."
        />
      )}
      {searchError !== null && <Failure error={searchError} onRetry={findReleases} />}

      {!releases && !searching && searchError === null && (
        <Empty>
          Press <strong>Find releases</strong> to ask YTS, TPB and 1337x for{' '}
          <span className="mono">{details.title}</span>.
        </Empty>
      )}

      <Releases
        result={releases}
        // TMDB's title, year and id, which is the whole point of the redesign:
        // the folder and the movies row come from the catalogue rather than from
        // whatever was typed into a box.
        film={{ title: details.title, year: details.year, tmdb_id: details.tmdb_id }}
        searching={searching}
        onSearchAgain={findReleases}
        noYearReason="TMDB has no release date for this film, so there is no year — and Title (Year) is the folder name curator would have to write."
        empty={
          <>
            No releases for <strong>{details.title}</strong> on any indexer. The film exists; nobody
            has uploaded it under this name.
          </>
        }
      />
    </>
  );
}

/**
 * The facts panel — everything true about the film that is not the pitch.
 *
 * Every row is conditional because TMDB's coverage is uneven: a 2026 release
 * often has no runtime, no studio and no IMDb id, and a row reading "—" four
 * times is worse than four rows that are not there.
 */
function Facts({ details }: { details: MovieDetails }) {
  const facts: [string, React.ReactNode][] = [];

  if (details.status) facts.push(['Status', details.status]);
  // "Status: Released" and "Released: 2019-04-24" one row apart read as the
  // same fact twice, so the date says it is a date.
  if (details.release_date) facts.push(['Release date', details.release_date]);
  if (details.original_language)
    facts.push(['Original language', details.original_language.toUpperCase()]);
  if (details.spoken_languages.length > 0)
    facts.push(['Spoken', details.spoken_languages.join(', ')]);
  if (details.studios.length > 0) facts.push(['Studios', details.studios.join(', ')]);

  if (details.library?.library_path) {
    facts.push(['In the library at', <span className="mono">{details.library.library_path}</span>]);
  }

  facts.push([
    'TMDB',
    <a href={`https://www.themoviedb.org/movie/${details.tmdb_id}`} target="_blank" rel="noreferrer">
      {details.tmdb_id}
    </a>,
  ]);
  if (details.imdb_id) {
    facts.push([
      'IMDb',
      <a href={`https://www.imdb.com/title/${details.imdb_id}/`} target="_blank" rel="noreferrer">
        {details.imdb_id}
      </a>,
    ]);
  }

  return (
    <dl className="facts">
      {facts.map(([label, value]) => (
        <div key={label}>
          <dt>{label}</dt>
          <dd>{value}</dd>
        </div>
      ))}
    </dl>
  );
}
