'use client';

import { useEffect, useState } from 'react';
import { api, type Movie, type SettingsResult } from '@/lib/api';
import { Failure } from '@/components/states';
import { FirstRun } from '@/components/first-run';
import { Loading, SkeletonLines } from '@/components/skeleton';

/**
 * The first run at a permanent URL.
 *
 * Discover draws the same component when nothing is configured; this route is
 * how it is reached afterwards, and it draws unconditionally. A container
 * started with `-e TMDB_API_KEY=…` is not a first run — it is configured, and
 * being asked again on every restart is the failure the derivation exists to
 * avoid — but the person running it can still want the tour, and each step here
 * then says the environment owns that setting rather than offering a box it
 * would ignore.
 *
 * No skip: the nav is one line above, so there is nothing to dismiss.
 */
export default function Setup() {
  const [settings, setSettings] = useState<SettingsResult | null>(null);
  const [movies, setMovies] = useState<Movie[] | null>(null);
  const [error, setError] = useState<unknown>(null);

  async function load() {
    setError(null);
    try {
      // Both, because the page is a function of both — and neither leaves this
      // machine, so there is nothing to stagger.
      const [next, films] = await Promise.all([api.settings(), api.movies()]);
      setSettings(next);
      setMovies(films);
    } catch (cause) {
      setError(cause);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  if (error !== null) {
    return (
      <>
        <h1>Set curator up</h1>
        <Failure error={error} onRetry={() => void load()} />
      </>
    );
  }

  if (!settings || !movies) {
    return (
      <>
        <h1>Set curator up</h1>
        <Loading>
          <SkeletonLines lines={4} />
        </Loading>
      </>
    );
  }

  return <FirstRun settings={settings} movies={movies} onSettings={setSettings} />;
}
