'use client';

import { useEffect, useState } from 'react';
import Link from 'next/link';
import { api, formatBytes, posterURL, type Deletion, type Movie, type ScanResult } from '@/lib/api';
import { Empty, Failure, Working } from '@/components/states';

export default function Library() {
  const [movies, setMovies] = useState<Movie[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [scanning, setScanning] = useState(false);
  const [scan, setScan] = useState<ScanResult | null>(null);
  const [onlyUnmatched, setOnlyUnmatched] = useState(false);
  const [confirming, setConfirming] = useState<Movie | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deleted, setDeleted] = useState<Deletion | null>(null);

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
  // library_path is null until an import or a scan puts the film on disk, so a
  // wanted row is emphatically NOT "on disk" and must not be counted as one.
  const onDisk = movies?.filter((m) => m.library_path !== null) ?? [];
  const wanted = movies?.filter((m) => m.library_path === null) ?? [];
  const shown = onlyUnmatched ? unmatched : (movies ?? []);

  async function remove(movie: Movie) {
    setDeleting(true);
    setError(null);
    try {
      setDeleted(await api.deleteMovie(movie.id));
      setConfirming(null);
      await load();
    } catch (e) {
      setError(e);
      setConfirming(null);
    } finally {
      setDeleting(false);
    }
  }

  return (
    <>
      <h1>Library</h1>
      <p className="lede">
        One folder per film, named <span className="mono">Title (Year)</span>. Scanning re-reads the
        disk and never writes to it — a folder with no film in it loses its entry here, and stays
        exactly where it is on disk.
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
          {movies ? `${onDisk.length} on disk` : 'loading…'}
          {wanted.length > 0 && ` · ${wanted.length} wanted, not yet imported`}
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
            Scanned {scan.scanned} film{scan.scanned === 1 ? '' : 's'}, added {scan.added}, matched{' '}
            {scan.matched}
          </strong>
          <span className="small">
            {scan.added === 0
              ? 'Nothing new — a rescan of an unchanged library is meant to add nothing.'
              : `${scan.added} new folder${scan.added === 1 ? '' : 's'}.`}
            {scan.unmatched > 0 && ` ${scan.unmatched} still unmatched by TMDB.`}
          </span>
          {/* Removed and missing are the two the scan must never do quietly:
              one deleted a row, the other could not account for one. */}
          {scan.empty > 0 && (
            <span className="small muted">
              {scan.empty} folder{scan.empty === 1 ? '' : 's'} on disk hold no film and{' '}
              {scan.empty === 1 ? 'was' : 'were'} not recorded.
            </span>
          )}
          {scan.removed > 0 && (
            <span className="small muted">
              {scan.removed} entr{scan.removed === 1 ? 'y' : 'ies'} removed — no film there, or a
              path outside <span className="mono">LIBRARY_MOVIES</span>. The folders were left on
              disk.
            </span>
          )}
          {scan.missing > 0 && (
            <span className="small muted">
              {scan.missing} entr{scan.missing === 1 ? 'y' : 'ies'} kept that this scan could not
              account for — the folder is missing, unreadable, or no longer named{' '}
              <span className="mono">Title (Year)</span>. Nothing was removed for it.
            </span>
          )}
        </div>
      )}

      {error !== null && <Failure error={error} onRetry={load} />}

      {deleted && (
        <div className="banner info">
          <strong>
            Deleted {deleted.title} ({deleted.year})
          </strong>
          <span className="small">
            {deleted.torrents_removed > 0
              ? `${deleted.torrents_removed} torrent${deleted.torrents_removed === 1 ? '' : 's'} removed from qBittorrent, and ${formatBytes(deleted.bytes_freed)} freed.`
              : deleted.folder_left
                ? 'There was no torrent to delete.'
                : 'The library folder was removed. There was no torrent to delete.'}
          </span>
          {/* The row is gone either way, but saying the folder was removed when
              it was not is the kind of small lie that costs an hour later. A
              path outside LIBRARY_MOVIES is not curator's to delete. */}
          {deleted.folder_left && (
            <span className="small muted">
              The folder <span className="mono">{deleted.folder_left}</span> is outside{' '}
              <span className="mono">LIBRARY_MOVIES</span>, so it was left on disk.
            </span>
          )}
        </div>
      )}

      {confirming && (
        <ConfirmDelete
          movie={confirming}
          busy={deleting}
          onCancel={() => setConfirming(null)}
          onConfirm={() => remove(confirming)}
        />
      )}

      {movies && shown.length === 0 && (
        <Empty>
          {onlyUnmatched
            ? 'Every film is matched.'
            : 'Nothing here yet. Search for something, or point LIBRARY_MOVIES at a library and scan.'}
        </Empty>
      )}

      <div className="grid">
        {shown.map((movie) => (
          <Card key={movie.id} movie={movie} onDelete={() => setConfirming(movie)} />
        ))}
      </div>
    </>
  );
}

// The confirmation says exactly what will be destroyed before it is destroyed.
// This is the only request in curator that removes anything, there is no undo,
// and there is no authentication in front of it (docs/decisions.md D19).
function ConfirmDelete({
  movie,
  busy,
  onCancel,
  onConfirm,
}: {
  movie: Movie;
  busy: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  return (
    <div className="banner error" role="alertdialog">
      <strong>
        Delete {movie.title}
        {movie.year ? ` (${movie.year})` : ''}?
      </strong>
      <span className="small">
        This removes the film from the library <em>and from the disk</em>, and cannot be undone.
      </span>
      <ul className="small" style={{ margin: '.5rem 0 0', paddingLeft: '1.1rem' }}>
        {movie.library_path && (
          <li>
            the folder <span className="mono">{movie.library_path}</span>
          </li>
        )}
        <li>
          the downloaded file, deleted by qBittorrent, freeing{' '}
          <strong>{formatBytes(movie.size_bytes)}</strong>
        </li>
        <li>the torrent stops seeding and is removed from qBittorrent</li>
      </ul>
      <div>
        <button onClick={onConfirm} disabled={busy}>
          {busy ? 'Deleting…' : 'Delete it'}
        </button>{' '}
        <button onClick={onCancel} disabled={busy}>
          Keep it
        </button>
      </div>
    </div>
  );
}

function Card({ movie, onDelete }: { movie: Movie; onDelete: () => void }) {
  const poster = posterURL(movie.poster_path);

  const body = (
    <>
      {/* A poster is the exception, not the rule: the unmatched rows have none,
          so the fallback has to look deliberate rather than broken. Plain <img>
          because a static export has no image optimiser. */}
      {poster ? (
        <img src={poster} alt="" loading="lazy" />
      ) : (
        <div className="noposter">{movie.title}</div>
      )}

      {/* title attribute because the CSS clamps to two lines: a longer name is
          ellipsised on screen but still readable on hover. */}
      <div className="title" title={movie.title}>
        {movie.title}
      </div>
      <div className="meta">
        <span>{movie.year || 'no year'}</span>
        <span>·</span>
        <span>{movie.library_path === null ? 'not on disk' : formatBytes(movie.size_bytes)}</span>
        {movie.quality && <span className="badge">{movie.quality}</span>}
        {movie.tmdb_id === null && (
          <span className="badge warn" title="TMDB could not match this folder; it is recorded rather than guessed at">
            unmatched
          </span>
        )}
        {movie.status !== 'imported' && <span className="badge">{movie.status}</span>}
      </div>
    </>
  );

  return (
    // A wrapper, not a <Link> around everything: a <button> nested inside an <a>
    // is invalid HTML and double-fires, and Delete is the one destructive
    // control on this screen. So Delete is the anchor's SIBLING.
    <div className="movie-cell">
      {/* Two addressing modes, and which one a card uses is decided by whether
          the film HAS a catalogue entry rather than by preference. A matched
          row opens its TMDB page, which is the richer one and is addressed by
          the TMDB id (D21). An unmatched row opens a page about the ROW,
          addressed by curator's own movies.id (D35) — it has no catalogue entry
          to open and it is still a film that plays. */}
      <Link
        className="movie"
        href={
          movie.tmdb_id === null
            ? `/library/film/?id=${movie.id}`
            : `/movie/?id=${movie.tmdb_id}`
        }
      >
        {body}
      </Link>
      <button className="small" onClick={onDelete}>
        Delete
      </button>
    </div>
  );
}
