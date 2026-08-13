'use client';

import { useEffect, useState } from 'react';
import Link from 'next/link';
import { api, type DiscoverRow, type Download, type Movie } from '@/lib/api';
import { Empty, Failure } from '@/components/states';
import { MovieCard } from '@/components/movie-card';

/**
 * Discover: what curator has, in one strip, and then what there is to want.
 *
 * The two halves are two fetches and stay independent on purpose. /api/movies
 * is the library and works with no TMDB key at all; everything under
 * /api/tmdb/ goes dark without one. A Promise.all would let the catalogue take
 * the counters down with it, on the first screen anyone sees.
 */
export default function Home() {
  const [movies, setMovies] = useState<Movie[] | null>(null);
  const [downloads, setDownloads] = useState<Download[] | null>(null);
  const [error, setError] = useState<unknown>(null);

  const [rows, setRows] = useState<DiscoverRow[] | null>(null);
  const [discoverError, setDiscoverError] = useState<unknown>(null);

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

    return () => {
      cancelled = true;
    };
  }, []);

  const active = downloads?.filter((d) => d.state !== 'imported' && d.state !== 'failed').length ?? 0;
  // A null tmdb_id is the row that wants human attention — the entire reason D6
  // made the column nullable instead of dropping the folder.
  const unmatched = movies?.filter((m) => m.tmdb_id === null).length ?? 0;

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
