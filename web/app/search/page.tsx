'use client';

import { Suspense, useEffect, useRef, useState } from 'react';
import { useSearchParams } from 'next/navigation';
import { api, type MediaType, type MovieSummary, type SearchResult } from '@/lib/api';
import { Empty, Failure, Working } from '@/components/states';
import { MovieCard } from '@/components/movie-card';
import { MediaSwitch, mediaFromParams, useTelevision } from '@/components/media-switch';
import { Releases } from '@/components/releases';

/**
 * Search finds films, not release names.
 *
 * The old version of this screen learned what a film was called from whatever
 * was typed, which recorded title='avengers', year=0 and produced three
 * failures from one row (D20). Now TMDB answers "which film", the grid lets a
 * human pick — Doomsday is a card among cards rather than silently chosen at
 * position one — and the release search happens on the film's own page.
 *
 * Release-name search survives as a mode, because it is the escape hatch for a
 * film TMDB does not have and the fallback when there is no key. It stays
 * films-only: a show found by release name would have no TMDB tv id and so no
 * page to hang the other seasons off.
 *
 * Since phase 11 the catalogue mode asks one of TWO catalogues, chosen by a
 * Films | Shows switch that exists only where LIBRARY_TV does. `?q=` and
 * `?media=` ride in the query string and the <Suspense> boundary is what
 * useSearchParams() requires of a static export (D21).
 */
export default function SearchPage() {
  return (
    <Suspense fallback={<p className="lede">Loading…</p>}>
      <Search />
    </Suspense>
  );
}

type Mode = 'films' | 'releases';

function Search() {
  const params = useSearchParams();
  const initial = (params.get('q') ?? '').trim();
  const television = useTelevision();

  // '?' until /api/settings answers. The screen must not guess, because the
  // guess decides which mode it opens in.
  const [tmdb, setTmdb] = useState<'?' | 'yes' | 'no'>('?');
  const [chosen, setChosen] = useState<Mode | null>(null);
  // An explicit toggle wins; otherwise no key means release mode, since a film
  // search would only 503.
  const mode: Mode = chosen ?? (tmdb === 'no' ? 'releases' : 'films');

  // Films or shows, WITHIN the catalogue mode — a third value of `mode` would
  // have been wrong, because release-name search is a different question and
  // this is the same question asked of a different catalogue. It rides in the
  // URL beside ?q= so a shared link comes back to the same grid, and it is
  // forced to films when television is off.
  const [chosenMedia, setChosenMedia] = useState<MediaType | null>(null);
  const media: MediaType = television.on
    ? (chosenMedia ?? mediaFromParams(params.get('media')))
    : 'movie';
  const tv = media === 'tv';

  const [query, setQuery] = useState(initial);
  const [year, setYear] = useState('');
  const [quality, setQuality] = useState('');

  const [films, setFilms] = useState<MovieSummary[] | null>(null);
  const [found, setFound] = useState('');
  const [looking, setLooking] = useState(false);
  const [filmError, setFilmError] = useState<unknown>(null);

  const [result, setResult] = useState<SearchResult | null>(null);
  const [searching, setSearching] = useState(false);
  const [error, setError] = useState<unknown>(null);

  // Read from /api/settings exactly as the qBittorrent check already does. No
  // new mechanism: configured: true|false is the only fact either screen needs.
  useEffect(() => {
    let cancelled = false;
    api
      .settings()
      .then((s) => {
        if (cancelled) return;
        setTmdb(s.integrations.find((i) => i.name === 'tmdb')?.configured ? 'yes' : 'no');
      })
      // A settings call that fails says nothing about the key. Falling back to
      // release mode here would blame a missing TMDB_API_KEY for curator being
      // unreachable, so the film search stays available and reports its own
      // error if it has one.
      .catch(() => !cancelled && setTmdb('yes'));
    return () => {
      cancelled = true;
    };
  }, []);

  // A URL carrying ?q= searches on arrival, which is what makes it worth
  // sharing. Only the film search, though: that is one TMDB call in ~150 ms,
  // where a release search launches a browser for up to thirteen seconds, and
  // no URL should do that on sight.
  //
  // It waits on `television.known` as well as on the key, because the answer
  // decides WHICH catalogue is asked — auto-searching films for a link that
  // said ?media=tv would answer a different question and then look settled.
  const auto = useRef('');
  useEffect(() => {
    if (tmdb === '?' || !television.known || !initial || mode !== 'films') return;
    if (auto.current === `${media}:${initial}`) return;
    // The ref, not the dependency list, is what makes this happen once:
    // findFilms is a new function every render and would loop as a dependency.
    auto.current = `${media}:${initial}`;
    void findFilms(initial, media);
  }, [initial, tmdb, mode, media, television.known]);

  async function findFilms(q: string, kind: MediaType = media) {
    setLooking(true);
    setFilmError(null);
    try {
      const answer = await api.tmdbSearch(q, undefined, kind);
      setFilms(answer.results);
      setFound(answer.query);
    } catch (e) {
      setFilmError(e);
      setFilms(null);
    } finally {
      setLooking(false);
    }
  }

  // The switch re-runs the search rather than leaving the previous catalogue's
  // grid under a heading that now says the other word. An unsearched screen
  // just changes which catalogue the next press asks.
  function chooseMedia(next: MediaType) {
    setChosenMedia(next);
    setFilms(null);
    setFilmError(null);

    const q = query.trim();
    window.history.replaceState(null, '', searchURL(q, next));

    if (q) {
      auto.current = `${next}:${q}`;
      void findFilms(q, next);
    }
  }

  async function findReleases(title: string) {
    setSearching(true);
    setError(null);
    try {
      setResult(await api.search(title, year ? Number(year) : undefined, quality || undefined));
    } catch (e) {
      setError(e);
      setResult(null);
    } finally {
      setSearching(false);
    }
  }

  function submit(event: React.FormEvent) {
    event.preventDefault();
    const q = query.trim();
    if (!q) return;

    // Next supports window.history for query-only updates, and it keeps
    // useSearchParams in step. The URL is the shareable half of this screen; a
    // reload of /search/?q=avengers has to come back to the same grid.
    window.history.replaceState(null, '', searchURL(q, media));
    auto.current = `${media}:${q}`;

    if (mode === 'films') void findFilms(q);
    else void findReleases(q);
  }

  return (
    <>
      <h1>Search</h1>
      <p className="lede">
        {mode === 'films'
          ? tv
            ? 'Find the show first. Its page is where releases are searched for, season by season, so nothing here touches an indexer.'
            : 'Find the film first. Its page is where releases are searched for, so nothing here touches an indexer.'
          : 'Search the indexers by release name. A first search takes about thirteen seconds, because a real browser is clearing a Cloudflare challenge for 1337x; a repeat within the hour is instant.'}
      </p>

      {tmdb === 'no' && (
        <div className="banner warn">
          <strong>TMDB is not configured, so this is release-name search</strong>
          <span className="small">
            Set <span className="mono">TMDB_API_KEY</span> to search for films and get posters,
            canonical titles and a page per film. Everything else works without it — this is the
            search curator has always had.
          </span>
        </div>
      )}

      <form className="row" onSubmit={submit}>
        {/* Only in catalogue mode, and only where television is on. Release-name
            search is deliberately films-only: it is the escape hatch for a
            title TMDB does not have, and a show found that way would have no
            season to file its episodes under. */}
        {mode === 'films' && television.on && (
          <MediaSwitch
            media={media}
            onChange={chooseMedia}
            // "Films", not "Movies": the control beside it offers release-name
            // search, and "Movies" there reads as a place rather than a kind.
            labels={['Films', 'Shows']}
            disabled={looking}
          />
        )}
        <input
          name="title"
          placeholder={mode === 'films' ? (tv ? 'Severance' : 'Avengers') : 'Interstellar'}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          aria-label={mode === 'films' ? (tv ? 'Show' : 'Film') : 'Title'}
          autoFocus
        />
        {mode === 'releases' && (
          <>
            <input
              name="year"
              placeholder="Year"
              value={year}
              onChange={(e) => setYear(e.target.value.replace(/\D/g, '').slice(0, 4))}
              inputMode="numeric"
              size={6}
              aria-label="Year"
            />
            <select value={quality} onChange={(e) => setQuality(e.target.value)} aria-label="Quality">
              <option value="">Any quality</option>
              <option value="2160p">2160p</option>
              <option value="1080p">1080p</option>
              <option value="720p">720p</option>
            </select>
          </>
        )}
        <button
          className="primary"
          type="submit"
          disabled={looking || searching || !query.trim()}
        >
          {looking || searching ? 'Searching…' : 'Search'}
        </button>
      </form>

      <p className="small muted" style={{ margin: '-.5rem 0 1.25rem' }}>
        {mode === 'films' ? (
          <>
            Searching TMDB for {tv ? 'shows' : 'films'}.{' '}
            <button className="linkbutton" onClick={() => setChosen('releases')}>
              Search release names instead
            </button>{' '}
            — for a film TMDB does not have.
          </>
        ) : (
          <>
            Searching YTS, TPB and 1337x by name.{' '}
            <button
              className="linkbutton"
              onClick={() => setChosen('films')}
              disabled={tmdb === 'no'}
              title={tmdb === 'no' ? 'TMDB is not configured: set TMDB_API_KEY' : undefined}
            >
              Search for films instead
            </button>
          </>
        )}
      </p>

      {mode === 'films' ? (
        <>
          {looking && <Working what="Asking TMDB" />}
          {filmError !== null && <Failure error={filmError} onRetry={() => findFilms(query.trim())} />}

          {films && films.length === 0 && (
            <Empty>
              TMDB has nothing for <strong>{found}</strong>.{' '}
              {tv
                ? 'Try fewer words — TMDB indexes shows under their original title as often as a translated one.'
                : 'Try fewer words, or search release names — some films are only ever a file.'}
            </Empty>
          )}

          {films && films.length > 0 && (
            <>
              <p className="small muted">
                {films.length} {tv ? 'show' : 'film'}
                {films.length === 1 ? '' : 's'} for <strong>{found}</strong> — pick the right one.
                Its year and title come from TMDB from there on.
              </p>
              <div className="grid">
                {films.map((film) => (
                  // media, not the default: TMDB's two id sequences overlap, so
                  // a show card taking /movie/?id= opens an unrelated film.
                  <MovieCard key={film.tmdb_id} film={film} media={media} />
                ))}
              </div>
            </>
          )}
        </>
      ) : (
        <>
          {searching && (
            <Working
              what="Searching YTS, TPB and 1337x"
              hint="1337x needs a real browser to clear Cloudflare, so the first search for a title takes ~13s. The next one this hour is instant."
            />
          )}
          {error !== null && <Failure error={error} onRetry={() => findReleases(query.trim())} />}

          {/* The banner stays on this path, where its reasoning is still true:
              here the year really is something the searcher can supply, and
              without one there is no folder name to write. */}
          {result && !result.year && (
            <div className="banner warn">
              <strong>Add a year before downloading</strong>
              <span className="small">
                The title and year you searched for become the library folder —{' '}
                <span className="mono">Title (Year)</span> — and curator will not guess a year it was
                not given. A release name is not a safe source for one either:{' '}
                <span className="mono">Blade Runner 2049 (2017)</span> would yield 2049. Search again
                with the year and the download button will work.
              </span>
            </div>
          )}

          <Releases
            result={result}
            // What was typed, echoed by the search. This mode has no better
            // source — which is the whole reason the film mode exists.
            film={{ title: result?.title ?? '', year: result?.year ?? 0 }}
            searching={searching}
            onSearchAgain={() => findReleases(query.trim())}
            empty={
              result && (
                <>
                  Nothing found for <strong>{result.title}</strong>
                  {result.year ? ` (${result.year})` : ''}. Try dropping the year, or a shorter
                  title.
                </>
              )
            }
          />
        </>
      )}
    </>
  );
}

/**
 * The shareable form of this screen: /search/?q=severance&media=tv.
 *
 * One function rather than three template literals, because the two parameters
 * are optional independently and the empty combination has to come out as
 * `/search/` and not `/search/?`. `media` is omitted for films, which keeps
 * every link written before phase 11 — and every link written since for a film
 * — exactly the URL it always was.
 */
function searchURL(query: string, media: MediaType): string {
  const url = new URLSearchParams();
  if (query) url.set('q', query);
  if (media === 'tv') url.set('media', 'tv');
  const rest = url.toString();
  return rest ? `/search/?${rest}` : '/search/';
}
