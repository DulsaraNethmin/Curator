'use client';

import { useEffect, useState } from 'react';
import Link from 'next/link';
import { api, type Download, type Movie } from '@/lib/api';
import { Failure } from '@/components/states';

// The home screen exists to answer "is anything happening" in one glance and
// then get out of the way. Every number on it is a link to the screen that owns
// it.
export default function Home() {
  const [movies, setMovies] = useState<Movie[] | null>(null);
  const [downloads, setDownloads] = useState<Download[] | null>(null);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    let cancelled = false;
    Promise.all([api.movies(), api.downloads()])
      .then(([m, d]) => {
        if (cancelled) return;
        setMovies(m);
        setDownloads(d);
      })
      .catch((e) => !cancelled && setError(e));
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
        Search four indexers, pick a release, and it downloads, hardlinks itself into the library and
        tells Jellyfin. One binary where seven containers used to be.
      </p>

      {error !== null && <Failure error={error} />}

      <div className="cards">
        <Link className="card" href="/library/">
          <b>{movies?.length ?? '—'}</b>
          <span>in the library</span>
        </Link>
        <Link className="card" href="/activity/">
          <b>{downloads ? active : '—'}</b>
          <span>{active === 1 ? 'download in flight' : 'downloads in flight'}</span>
        </Link>
        <Link className="card" href="/library/">
          <b>{movies ? unmatched : '—'}</b>
          <span>unmatched by TMDB</span>
        </Link>
        <Link className="card" href="/search/">
          <b>＋</b>
          <span>search for something</span>
        </Link>
      </div>
    </>
  );
}
