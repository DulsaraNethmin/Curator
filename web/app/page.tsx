'use client';

import { useEffect, useState } from 'react';
import Link from 'next/link';
import { api, type DiscoverRow, type Download, type Movie, type SettingsResult } from '@/lib/api';
import { Empty, Failure } from '@/components/states';
import { MovieCard } from '@/components/movie-card';
import { FirstRun, isFirstRun } from '@/components/first-run';

/**
 * Discover: what curator has, in one strip, and then what there is to want.
 *
 * The two halves are two fetches and stay independent on purpose. /api/movies
 * is the library and works with no TMDB key at all; everything under
 * /api/tmdb/ goes dark without one. A Promise.all would let the catalogue take
 * the counters down with it, on the first screen anyone sees.
 *
 * **And on a first run it is not this screen at all.** Every rail here is a
 * TMDB call, so with no key the landing screen is a failure banner over three
 * zeroes — which is precisely the empty library T50 exists to not show a
 * stranger. The setup draws in its place. It is not a gate: the nav belongs to
 * the layout, so every other screen is reachable without dismissing anything.
 */
export default function Home() {
  const [movies, setMovies] = useState<Movie[] | null>(null);
  const [downloads, setDownloads] = useState<Download[] | null>(null);
  const [error, setError] = useState<unknown>(null);

  const [rows, setRows] = useState<DiscoverRow[] | null>(null);
  const [discoverError, setDiscoverError] = useState<unknown>(null);

  // The third fetch, and the cheapest: no probe, so it is configuration this
  // process has already resolved. A failure is not a first run — it is a
  // curator that could not answer, and this screen says so in its own words.
  const [settings, setSettings] = useState<SettingsResult | null>(null);
  const [settingsFailed, setSettingsFailed] = useState(false);
  const [skipped, setSkipped] = useState(false);

  useEffect(() => {
    let cancelled = false;

    Promise.all([api.movies(), api.downloads()])
      .then(([m, d]) => {
        if (cancelled) return;
        setMovies(m);
        setDownloads(d);
      })
      .catch((e) => !cancelled && setError(e));

    api
      .discover()
      .then((d) => !cancelled && setRows(d.rows))
      .catch((e) => !cancelled && setDiscoverError(e));

    api
      .settings()
      .then((s) => !cancelled && setSettings(s))
      .catch(() => !cancelled && setSettingsFailed(true));

    return () => {
      cancelled = true;
    };
  }, []);

  const active = downloads?.filter((d) => d.state !== 'imported' && d.state !== 'failed').length ?? 0;
  // A null tmdb_id is the row that wants human attention — the entire reason D6
  // made the column nullable instead of dropping the folder.
  const unmatched = movies?.filter((m) => m.tmdb_id === null).length ?? 0;

  // Which of the two screens this is. Held until both local answers are in:
  // /api/movies and /api/settings are a SQLite read and a map lookup, and a
  // stranger flashing "Not configured" on the way to the page that explains it
  // is the one thing this must not do. The slow call is discover(), and it is
  // not waited on.
  const decided = (movies !== null || error !== null) && (settings !== null || settingsFailed);

  if (!decided) {
    return (
      <>
        <h1>curator</h1>
        <p className="lede">Loading…</p>
      </>
    );
  }

  if (!skipped && settings && movies && isFirstRun(settings, movies)) {
    return (
      <FirstRun
        settings={settings}
        movies={movies}
        onSettings={setSettings}
        onSkip={() => setSkipped(true)}
      />
    );
  }

  return (
    <>
      <h1>curator</h1>
      <p className="lede">
        Pick a film, and it downloads, hardlinks itself into the library and tells Jellyfin. One
        binary where seven containers used to be.
      </p>

      {error !== null && <Failure error={error} />}

      {/* Compacted into a strip: these three numbers are a status line, and they
          were taking the whole screen above the only content on it. */}
      <div className="strip">
        <Link href="/library/">
          <b>{movies?.length ?? '—'}</b>
          <span>in the library</span>
        </Link>
        <Link href="/activity/">
          <b>{downloads ? active : '—'}</b>
          <span>{active === 1 ? 'download in flight' : 'downloads in flight'}</span>
        </Link>
        <Link href="/library/">
          <b>{movies ? unmatched : '—'}</b>
          <span>unmatched by TMDB</span>
        </Link>
        <Link href="/search/" className="push">
          <span>Search for a film →</span>
        </Link>
      </div>

      {/* The way back in, and the only place it is offered. Two people see an
          empty library that is not a first run: one handed the key in with -e,
          who is configured and must not be asked again on every restart, and
          one who pressed skip. Both may still want the setup, and it disappears
          the moment there is a film. */}
      {movies?.length === 0 && (
        <p className="small muted" style={{ margin: '0 0 1.5rem' }}>
          Nothing in the library yet — <Link href="/setup/">set curator up</Link>, or point{' '}
          <span className="mono">LIBRARY_MOVIES</span> at your films and scan.
        </p>
      )}

      {/* A failed catalogue is a banner under the counters, never an error page:
          the library above it is still true and still worth seeing. */}
      {discoverError !== null && <Failure error={discoverError} />}

      {!rows && discoverError === null && <p className="lede">Loading…</p>}

      {rows?.map((row) => (
        <Rail key={row.id} row={row} />
      ))}
    </>
  );
}

/**
 * One rail, and its own failure.
 *
 * ok and error are per-row for the same reason indexers[] is per-indexer: one
 * rail failing is a success carrying the other. A rail that rendered as an
 * empty row would be indistinguishable from a rail TMDB had nothing for, which
 * is minter's 200-carrying-a-failure wearing a third hat.
 */
function Rail({ row }: { row: DiscoverRow }) {
  return (
    <section>
      <h2>{row.title}</h2>

      {!row.ok ? (
        <div className="banner warn">
          <strong>{row.title} could not be loaded</strong>
          <span className="small">{row.error ?? 'no reason given'}</span>
        </div>
      ) : row.results.length === 0 ? (
        <Empty>TMDB returned nothing for {row.title.toLowerCase()}.</Empty>
      ) : (
        <div className="rail">
          {row.results.map((film) => (
            <MovieCard key={film.tmdb_id} film={film} />
          ))}
        </div>
      )}
    </section>
  );
}
