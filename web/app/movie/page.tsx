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
import { Icon } from '@/components/icons';
import { LibraryBadge, Poster } from '@/components/movie-card';
import { Rating } from '@/components/rating';
import { Releases } from '@/components/releases';
import { recall, releaseKey } from '@/lib/recent';
import { ago } from '@/lib/duration';
import { Loading, SkeletonHero } from '@/components/skeleton';
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
    <Suspense
      fallback={
        <Loading>
          <SkeletonHero />
        </Loading>
      }
    >
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

  // Whether the hero's ▶ Watch here has been pressed. The <Player> mounts on it
  // and starts itself, which is what lets the two watch actions — here, and in
  // Jellyfin — sit together in the hero instead of one being a button below it.
  const [watching, setWatching] = useState(false);

  // When the list on screen was fetched, when it came from the cache rather than
  // from a search just now. Null means these releases are fresh — a restored
  // list that said nothing about its age would be the one dishonest thing about
  // showing it at all.
  const [restored, setRestored] = useState<number | null>(null);

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
    setSearchError(null);
    setWatching(false);

    // The releases from the last search for THIS film, if it was recent enough
    // and nothing has been searched since. This is the whole of T108: the button
    // below costs 7.08 s, and routing away by accident used to cost it twice.
    //
    // Only this effect reads the cache. `findReleases` never does — so "Search
    // again" always goes to the network, which is what it says, and `result`
    // identity always changes on a real search, which is what keeps
    // <Releases>'s `[result]` reset firing.
    const held = recall(releaseKey('movie', id) ?? '');
    setReleases(held?.result ?? null);
    setRestored(held?.at ?? null);

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
      setRestored(null);
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

  if (!details) {
    return (
      <Loading>
        <SkeletonHero />
      </Loading>
    );
  }

  const backdrop = backdropURL(details.backdrop_path);
  const poster = posterURL(details.poster_path);
  const runtime = formatRuntime(details.runtime);

  // `imported` and nothing else. library.state is the only truthful source —
  // movies.status never says "downloading", the Go side derives that from the
  // downloads table — and a film mid-download is emphatically not one curator
  // already has: when a torrent stalls, dispatching a different release is
  // exactly what you want to do.
  const onDisk = details.library?.state === 'imported';

  return (
    <>
      <div className="hero" style={backdrop ? { backgroundImage: `url(${backdrop})` } : undefined}>
        <div className="hero-scrim">
          <Poster className="hero-poster" src={poster} title={details.title} />

          <div className="hero-text">
            <h1>
              {details.title}
              {details.year > 0 && <span className="year"> {details.year}</span>}
            </h1>

            <div className="meta">
              {runtime && <span>{runtime}</span>}
              {details.genres.length > 0 && <span>{details.genres.join(' · ')}</span>}
              <Rating score={details.vote_average} size="md" />
              {details.library && <LibraryBadge library={details.library} />}
            </div>

            {details.tagline && <p className="tagline">{details.tagline}</p>}
            {details.overview && <p className="overview">{details.overview}</p>}

            <div className="actions">
              {onDisk ? (
                // The two ways to watch, side by side, because that is what
                // they are. "Find releases" is not drawn at all for a film
                // curator already has: acquiring it again is not an action this
                // page should offer, and the releases section below says so
                // rather than leaving the absence a mystery.
                <button
                  className="primary"
                  onClick={() => setWatching(true)}
                  disabled={watching}
                >
                  <Icon name="play" size="sm" /> Watch here
                </button>
              ) : (
                /* Nothing has touched an indexer up to this point, and nothing
                   will until this is pressed. That is what makes browsing free. */
                <button className="primary" onClick={findReleases} disabled={searching}>
                  {searching ? 'Searching…' : releases ? 'Search again' : 'Find releases'}
                </button>
              )}

              {/* Absent means there is nothing to draw — no Jellyfin
                  configured, or a film curator does not have on disk. Not a
                  disabled button and not a tooltip explaining what you are
                  missing, which is the rule the rest of the UI follows. */}
              {details.jellyfin_url && (
                <a className="button" href={details.jellyfin_url} target="_blank" rel="noreferrer">
                  Open in Jellyfin
                </a>
              )}

              {/* **The way in to correcting a wrong match, and the only one
                  (T69).** This page is where somebody arrives holding the
                  evidence: they clicked a card in their Library and got a page
                  about a film that is not the one in that folder. Everything
                  else on this screen asserts the match is right — the poster,
                  the title, "curator already has this film" below — so the one
                  control that doubts it belongs here rather than a screen away.

                  It links by `library.movie_id`, curator's own movies.id, and
                  NOT the TMDB id this page is addressed by (D21) — the same
                  distinction the player below depends on.

                  Only for a film on disk. A wanted row has a tmdb_id because a
                  dispatch gave it one, not because a folder name was resolved,
                  so there is no wrong match there to correct. */}
              {onDisk && details.library && (
                <Link className="button quiet" href={`/library/film/?id=${details.library.movie_id}`}>
                  Not this film?
                </Link>
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

          Still below the hero, and now mounted by the hero's own button rather
          than drawing one of its own. What it renders once it starts is a video
          and not a button, so this is where it belongs — and the button that
          starts it is above the fold, which is what the arrangement was for. */}
      {onDisk && watching && details.library && (
        <Player movieID={details.library.movie_id} title={details.title} autoStart />
      )}

      <Facts details={details} />

      <h2>Releases</h2>

      {onDisk ? (
        // The heading stays and the list does not. A section that silently
        // vanished would be a mystery; this one says why there is nothing to
        // fetch, and what to do if you want a different release anyway. The
        // server refuses the dispatch too — this is the explanation, not the
        // guard.
        <Empty>
          curator already has this film
          {details.library?.library_path && (
            <>
              , at <span className="mono">{details.library.library_path}</span>
            </>
          )}
          . To replace it with a different release, delete it from the{' '}
          <Link href="/library/">Library</Link> first.
        </Empty>
      ) : (
        <>
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

          {/* A list that appeared instantly where a 7-second wait used to be
              needs to say why, or it is the one dishonest thing about caching
              it: these are the releases from before, and the seeder counts have
              moved on. `restored` is null after a real search, so this is drawn
              exactly when the list did not come from the network just now. */}
          {releases && restored !== null && (
            <p className="small muted">
              These are the releases from your last search for this film, {ago(restored)}. Press{' '}
              <strong>Search again</strong> for fresh ones.
            </p>
          )}

          <Releases
            result={releases}
            // TMDB's title, year and id, which is the whole point of the
            // redesign: the folder and the movies row come from the catalogue
            // rather than from whatever was typed into a box.
            film={{ title: details.title, year: details.year, tmdb_id: details.tmdb_id }}
            searching={searching}
            onSearchAgain={findReleases}
            noYearReason="TMDB has no release date for this film, so there is no year — and Title (Year) is the folder name curator would have to write."
            empty={
              <>
                No releases for <strong>{details.title}</strong> on any indexer. The film exists;
                nobody has uploaded it under this name.
              </>
            }
          />
        </>
      )}
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
