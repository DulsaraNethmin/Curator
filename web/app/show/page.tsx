'use client';

import { Suspense, useEffect, useState } from 'react';
import { useSearchParams } from 'next/navigation';
import Link from 'next/link';
import {
  api,
  backdropURL,
  formatRuntime,
  posterURL,
  type SearchResult,
  type ShowDetails,
} from '@/lib/api';
import { Empty, Failure, Working } from '@/components/states';
import { LibraryBadge, Poster } from '@/components/movie-card';
import { Rating } from '@/components/rating';
import { Releases } from '@/components/releases';
import { recall, releaseKey } from '@/lib/recent';
import { ago } from '@/lib/duration';
import { Loading, SkeletonHero } from '@/components/skeleton';

/**
 * The show's page: /show/?id=95396.
 *
 * It mirrors /movie/ and is a second page rather than a mode of it, for the
 * reason `/api/tmdb/shows/{id}` is a second route: **TMDB's movie and tv id
 * sequences overlap.** 95396 is Severance's tv id and also a film's movie id,
 * so one page disambiguating a bare number by a query parameter is how a film
 * ends up rendered as a show — with the wrong poster, the wrong year, and a
 * release search for the wrong title.
 *
 * The id rides in a query string for D21's reason: `output: 'export'` cannot
 * build /show/[id]/ without enumerating every TMDB id at build time. The
 * <Suspense> boundary is what useSearchParams() requires of that export, and
 * without it the build FAILS — which is the toolchain remembering so nobody has
 * to.
 *
 * **There is no player here, and that is phase 11's scope rather than an
 * omission.** `GET /api/movies/{id}/stream` serves one file per row and a show
 * is not one file; episodes play in Jellyfin, which is what the Shows library
 * is for. `stream.go` refuses a row under the TV root by construction.
 */
export default function ShowPage() {
  return (
    <Suspense
      fallback={
        <Loading>
          <SkeletonHero />
        </Loading>
      }
    >
      <Show />
    </Suspense>
  );
}

function Show() {
  const params = useSearchParams();
  const raw = params.get('id');
  // Digits or nothing, exactly as /movie/ does it: Number(null) is 0 and
  // Number('abc') is NaN, and NaN would reach the API as /api/tmdb/shows/NaN.
  const id = raw !== null && /^\d+$/.test(raw) ? Number(raw) : 0;

  const [details, setDetails] = useState<ShowDetails | null>(null);
  const [error, setError] = useState<unknown>(null);

  const [releases, setReleases] = useState<SearchResult | null>(null);
  const [searching, setSearching] = useState(false);
  const [searchError, setSearchError] = useState<unknown>(null);
  // 0 is every season, which is what the API reads an absent season as.
  const [season, setSeason] = useState(0);
  // 0 is the whole season. It is only ever sent alongside a season — the server
  // answers 400 for an episode without one.
  const [episode, setEpisode] = useState(0);

  // When the list on screen was fetched, when it came back from the cache. Null
  // after a real search — see the notice below <Releases>.
  const [restored, setRestored] = useState<number | null>(null);

  // One fetch, and the release search is emphatically not part of it. TMDB
  // answers in about 150 ms; a cold release search takes up to thirteen seconds
  // because a real browser is clearing Cloudflare behind minter. Browsing must
  // never launch one, which is why the second fetch is a button.
  useEffect(() => {
    if (!id) return;
    let cancelled = false;

    setDetails(null);
    setError(null);
    setSearchError(null);
    setSeason(0);
    setEpisode(0);

    // The last search for THIS show, if it was for the whole series rather than
    // a season — which is what the two lines above have just reset to. A season
    // list is keyed on its own number and is not what this arrival is asking
    // for, so it correctly misses (T108).
    //
    // Only this effect reads the cache; `findReleases` never does, so a season
    // button and "Search again" both always reach the network.
    const held = recall(releaseKey('tv', id, 0, 0) ?? '');
    setReleases(held?.result ?? null);
    setRestored(held?.at ?? null);

    api
      .tmdbShow(id)
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

  async function findReleases(want = season, wantEpisode = episode) {
    if (!details) return;
    setSeason(want);
    setEpisode(wantEpisode);
    setSearching(true);
    setSearchError(null);
    try {
      // The canonical title, colon and all — stripping it is NormaliseQuery's
      // job, server-side and above the cache (D20). `undefined` for quality:
      // this screen has no quality control, and the show page's own year is
      // the first air year, which is what `Show (Year)` on disk is named for.
      //
      // The IMDb id last, and it is the only reason EZTV is searched at all:
      // that source is keyed by it and has no keyword surface, so without one
      // it reports itself not searched rather than failing. This screen is the
      // only one that has an id to send.
      setReleases(
        await api.search(
          details.title,
          details.year || undefined,
          undefined,
          'tv',
          want,
          wantEpisode,
          details.imdb_id || undefined,
        ),
      );
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
        <h1>No show</h1>
        <Empty>
          This page shows one series, addressed by its TMDB <em>tv</em> id —{' '}
          <span className="mono">/show/?id=95396</span>, which is a different number from the film
          holding movie id 95396. Pick one from <Link href="/">Discover</Link> or{' '}
          <Link href="/search/?media=tv">Search</Link>.
        </Empty>
      </>
    );
  }

  if (error !== null) {
    // Includes the 503 naming LIBRARY_TV, which is what somebody who typed this
    // URL on an install with television off gets. <Failure> titles it "Not
    // configured" and prints the server's own sentence, which names the
    // variable — there is nothing better this screen could say.
    return (
      <>
        <h1>Show</h1>
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
  const runtime = formatRuntime(details.episode_runtime);

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
              {details.seasons > 0 && (
                <span>
                  {details.seasons} season{details.seasons === 1 ? '' : 's'}
                  {details.episodes > 0 && `, ${details.episodes} episodes`}
                </span>
              )}
              {runtime && <span>{runtime} an episode</span>}
              {details.genres.length > 0 && <span>{details.genres.join(' · ')}</span>}
              <Rating score={details.vote_average} size="md" />
              {details.library && <LibraryBadge library={details.library} />}
            </div>

            {details.tagline && <p className="tagline">{details.tagline}</p>}
            {details.overview && <p className="overview">{details.overview}</p>}

            <div className="actions">
              {/* **Offered even for a show curator already has**, which is the
                  one place this page deliberately differs from /movie/. A film
                  is finished and the film page refuses to help you acquire it
                  twice; a show is never finished — season 2 arrives six months
                  after season 1 and lands on the same row, which is exactly
                  what MarkImported's twin reconciliation is for (D48). The
                  server does not refuse a television dispatch either, for the
                  same reason. */}
              <button className="primary" onClick={() => findReleases()} disabled={searching}>
                {searching ? 'Searching…' : releases ? 'Search again' : 'Find releases'}
              </button>

              {/* Absent means there is nothing to draw — no Jellyfin
                  configured, or a show curator does not have on disk. Asked of
                  Jellyfin as a Series rather than a Movie: a tv id looked up as
                  a film can LAND on an unrelated film rather than merely miss
                  (T92). */}
              {details.jellyfin_url && (
                <a className="button" href={details.jellyfin_url} target="_blank" rel="noreferrer">
                  Open in Jellyfin
                </a>
              )}
            </div>

            {/* Said once, here, rather than as a disabled ▶ button. Episodes
                are not a thing this UI plays, and a control that explained
                itself would be curator apologising for a decision. */}
            {onDisk && (
              <p className="small muted" style={{ margin: '.6rem 0 0' }}>
                Episodes play in Jellyfin — a show is not one file, and curator&apos;s player
                serves one file per row.
              </p>
            )}
          </div>
        </div>
      </div>

      <Facts details={details} />

      <h2>Releases</h2>

      {searching && (
        <Working
          what="Searching TPB and 1337x"
          hint="1337x needs a real browser to clear Cloudflare, so the first search for a title takes ~13s. The next one this hour is instant."
        />
      )}
      {searchError !== null && <Failure error={searchError} onRetry={() => findReleases()} />}

      {!releases && !searching && searchError === null && (
        <Empty>
          Press <strong>Find releases</strong> to ask the indexers for{' '}
          <span className="mono">{details.title}</span>. Season packs and single episodes both
          import.
        </Empty>
      )}

      {/* Same sentence as the film screen's, and for the same reason: a list
          that appeared instantly where a 13-second wait used to be has to say
          that it is the old one. Only ever the whole-series list — a season
          search is keyed on its own number and is never restored on arrival. */}
      {releases && restored !== null && (
        <p className="small muted">
          These are the releases from your last search for this show, {ago(restored)}. Press{' '}
          <strong>Search again</strong> for fresh ones.
        </p>
      )}

      <Releases
        result={releases}
        // TMDB's title, year and TV id. The year is the first air year, which
        // is Jellyfin's own series convention and what `Show (Year)` on disk is
        // named for — `library.DestFolder` round-trips it unchanged.
        film={{ title: details.title, year: details.year, tmdb_id: details.tmdb_id, media: 'tv' }}
        searching={searching}
        onSearchAgain={() => findReleases()}
        // The LIST, not the count. `details.seasons` is 4 for Silo and three
        // of them have aired; the list is what says so, and it is the only
        // thing that knows how many episodes each one has.
        seasons={details.season_list}
        season={season}
        // A new season is a new search, and the state moves inside it so the
        // control and the request can never disagree about which one is in
        // flight.
        //
        // The episode resets to 0 with it. Carrying "episode 5" across a season
        // change would answer a question nobody asked — season 3's episode 5,
        // from a control the person was using to change season — and switching
        // to Every season would leave an episode with no season to belong to,
        // which is the one combination the server refuses.
        onSeason={(next) => void findReleases(next, 0)}
        episode={episode}
        // Searched, not merely typed — which is what T98 changed. This was
        // `setEpisode` while the control was a number field that fired per
        // keystroke and therefore could not search on change; a button is a
        // discrete choice, so it behaves exactly like the season above it.
        onEpisode={(next) => void findReleases(season, next)}
        noYearReason="TMDB has no first air date for this show, so there is no year — and Show (Year) is the folder name curator would have to write."
        empty={
          <>
            No releases for <strong>{details.title}</strong>
            {season > 0 ? ` season ${season}` : ''} on any indexer that carries television.
            {season > 0 && ' Try every season — a complete-series pack covers it too.'}
          </>
        }
      />
    </>
  );
}

/**
 * The facts panel — everything true about the show that is not the pitch.
 *
 * Every row is conditional for the same reason the film's are: TMDB's coverage
 * is uneven, and a row reading "—" four times is worse than four rows that are
 * not there. What is NOT here is as deliberate: no runtime and no release date,
 * because a series has neither — it has a per-episode length and a first air
 * date.
 *
 * The IMDb row IS here since T97, and it is the film's row unchanged. It was
 * absent because a show's id cost TMDB a second request; it now arrives on the
 * one already being made, and the same id is what EZTV is searched by. IMDb's
 * /title/ space covers films and series alike, so the link needs no branch —
 * unlike TMDB's below, where the same number under /movie/ is a different
 * title entirely.
 */
function Facts({ details }: { details: ShowDetails }) {
  const facts: [string, React.ReactNode][] = [];

  // A show's status is its own vocabulary — "Returning Series", "Ended",
  // "Canceled" — and it is the one fact on this panel a film has no version of.
  if (details.status) facts.push(['Status', details.status]);
  if (details.first_air_date) facts.push(['First aired', details.first_air_date]);
  // Not a promise that the show has finished. Status above is where that is
  // said, and the two are drawn together so neither is read as the other.
  if (details.last_air_date) facts.push(['Last aired', details.last_air_date]);
  if (details.seasons > 0) facts.push(['Seasons', details.seasons]);
  if (details.episodes > 0) facts.push(['Episodes', details.episodes]);
  if (details.original_language)
    facts.push(['Original language', details.original_language.toUpperCase()]);
  if (details.spoken_languages.length > 0)
    facts.push(['Spoken', details.spoken_languages.join(', ')]);
  if (details.studios.length > 0) facts.push(['Networks', details.studios.join(', ')]);

  if (details.library?.library_path) {
    facts.push(['In the library at', <span className="mono">{details.library.library_path}</span>]);
  }

  facts.push([
    'TMDB',
    // /tv/, not /movie/ — the same id under the other path is a different
    // title, which is the whole reason this page exists.
    <a href={`https://www.themoviedb.org/tv/${details.tmdb_id}`} target="_blank" rel="noreferrer">
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
