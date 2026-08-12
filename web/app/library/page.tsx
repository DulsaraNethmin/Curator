'use client';

import { useEffect, useState } from 'react';
import { api, formatBytes, posterURL, type Movie, type ScanResult } from '@/lib/api';
import { Empty, Failure, Working } from '@/components/states';

export default function Library() {
  const [movies, setMovies] = useState<Movie[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [scanning, setScanning] = useState(false);
  const [scan, setScan] = useState<ScanResult | null>(null);
  const [onlyUnmatched, setOnlyUnmatched] = useState(false);

  async function load() {
    setError(null);
    try {
      setMovies(await api.movies());
    } catch (e) {
      setError(e);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  async function rescan() {
    setScanning(true);
    setScan(null);
    setError(null);
    try {
      setScan(await api.scan());
      await load();
    } catch (e) {
      setError(e);
    } finally {
      setScanning(false);
    }
  }

  // A null tmdb_id is the row that wants human attention. D6 made the column
  // nullable rather than dropping the folder precisely so this list can exist,
  // so hiding it would defeat the point of recording it.
  const unmatched = movies?.filter((m) => m.tmdb_id === null) ?? [];
  const shown = onlyUnmatched ? unmatched : (movies ?? []);

  return (
    <>
      <h1>Library</h1>
      <p className="lede">
        One folder per film, named <span className="mono">Title (Year)</span>. Scanning re-reads the
        disk; it never writes to it.
      </p>

      <form className="row" onSubmit={(e) => (e.preventDefault(), rescan())}>
        <button className="primary" disabled={scanning}>
          {scanning ? 'Scanning…' : 'Scan the library'}
        </button>
        {unmatched.length > 0 && (
          <button type="button" onClick={() => setOnlyUnmatched((v) => !v)}>
            {onlyUnmatched ? `Show all ${movies?.length ?? 0}` : `Show ${unmatched.length} unmatched`}
          </button>
        )}
        <span className="small muted">
          {movies ? `${movies.length} on disk` : 'loading…'}
          {unmatched.length > 0 && ` · ${unmatched.length} unmatched by TMDB`}
        </span>
      </form>

      {scanning && (
        <Working
          what="Walking the library and asking TMDB"
          hint="A cold scan of 29 folders takes about nine seconds. A rescan makes no TMDB calls at all and is instant."
        />
      )}

      {scan && !scanning && (
        <div className="banner info">
          <strong>
            Scanned {scan.scanned}, added {scan.added}, matched {scan.matched}
          </strong>
          <span className="small">
            {scan.added === 0
              ? 'Nothing new — a rescan of an unchanged library is meant to add nothing.'
              : `${scan.added} new folder${scan.added === 1 ? '' : 's'}.`}
            {scan.unmatched > 0 && ` ${scan.unmatched} still unmatched by TMDB.`}
          </span>
        </div>
      )}

      {error !== null && <Failure error={error} onRetry={load} />}

      {movies && shown.length === 0 && (
        <Empty>
          {onlyUnmatched
            ? 'Every film is matched.'
            : 'Nothing here yet. Search for something, or point LIBRARY_MOVIES at a library and scan.'}
        </Empty>
      )}

      <div className="grid">
        {shown.map((movie) => (
          <Card key={movie.id} movie={movie} />
        ))}
      </div>
    </>
  );
}

function Card({ movie }: { movie: Movie }) {
  const poster = posterURL(movie.poster_path);

  return (
    <div className="movie">
      {/* A poster is the exception, not the rule: 15 of the 29 folders in the
          real library are empty and unmatched, so the fallback is the majority
          case and has to look deliberate rather than broken. Plain <img>
          because a static export has no image optimiser. */}
      {poster ? (
        <img src={poster} alt="" loading="lazy" />
      ) : (
        <div className="noposter">{movie.title}</div>
      )}

      <div className="title">{movie.title}</div>
      <div className="meta">
        <span>{movie.year || '—'}</span>
        <span>·</span>
        <span>{formatBytes(movie.size_bytes)}</span>
        {movie.quality && <span className="badge">{movie.quality}</span>}
        {movie.tmdb_id === null && (
          <span className="badge warn" title="TMDB could not match this folder; it is recorded rather than guessed at">
            unmatched
          </span>
        )}
        {movie.status !== 'imported' && <span className="badge">{movie.status}</span>}
      </div>
    </div>
  );
}
