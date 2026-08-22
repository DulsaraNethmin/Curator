'use client';

import { Suspense, useEffect, useState } from 'react';
import { useSearchParams } from 'next/navigation';
import Link from 'next/link';
import {
  api,
  formatBytes,
  posterURL,
  type Deletion,
  type MediaType,
  type Movie,
  type ScanResult,
} from '@/lib/api';
import { Empty, Failure, Working } from '@/components/states';
import { Loading, SkeletonGrid } from '@/components/skeleton';
import { MediaSwitch, mediaFromParams, useTelevision } from '@/components/media-switch';
import { Poster } from '@/components/movie-card';

/**
 * The library, in one of two halves: /library/ and /library/?media=tv.
 *
 * The half rides in a query string rather than a path segment for D21's reason —
 * `output: 'export'` builds one page per static route and there is no
 * `/library/[media]/` to enumerate — and it rides in the URL at all so that the
 * Shows half survives a reload and a shared link. `useSearchParams()` without a
 * <Suspense> boundary fails the build, which is the toolchain remembering D21 so
 * nobody has to.
 *
 * **The Shows half does not exist when LIBRARY_TV is unset.** No switch, and a
 * ?media=tv typed by hand falls back to films rather than asking a route that
 * answers 503. That is D48's opt-in, and it is the difference between a feature
 * that is off and one that is broken.
 */
export default function LibraryPage() {
  return (
    <Suspense
      fallback={
        <Loading>
          <SkeletonGrid />
        </Loading>
      }
    >
      <Library />
    </Suspense>
  );
}

function Library() {
  const params = useSearchParams();
  const television = useTelevision();

  const [chosen, setChosen] = useState<MediaType | null>(null);
  // The URL decides until somebody presses the switch, and television being off
  // overrides both: a bookmark to ?media=tv on an install that has since turned
  // television off is a films page, not a 503.
  const media: MediaType = television.on ? (chosen ?? mediaFromParams(params.get('media'))) : 'movie';
  const tv = media === 'tv';

  const [rows, setRows] = useState<Movie[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [scanning, setScanning] = useState(false);
  const [scan, setScan] = useState<ScanResult | null>(null);
  const [onlyUnmatched, setOnlyUnmatched] = useState(false);
  const [confirming, setConfirming] = useState<Movie | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deleted, setDeleted] = useState<Deletion | null>(null);

  async function load(kind: MediaType) {
    setError(null);
    try {
      setRows(kind === 'tv' ? await api.shows() : await api.movies());
    } catch (e) {
      setError(e);
    }
  }

  // Held until useTelevision has answered, so the films list is not fetched and
  // then thrown away a beat later on an install where the URL asked for shows.
  useEffect(() => {
    if (!television.known) return;
    setRows(null);
    void load(media);
  }, [media, television.known]);

  function choose(next: MediaType) {
    setChosen(next);
    setOnlyUnmatched(false);
    // Next supports window.history for query-only updates and keeps
    // useSearchParams in step. The URL is the shareable half of this screen.
    window.history.replaceState(null, '', next === 'tv' ? '/library/?media=tv' : '/library/');
  }

  async function rescan() {
    setScanning(true);
    setScan(null);
    setError(null);
    try {
      // One scan, both roots. It is deliberately not scoped to the half being
      // shown — there is one prune over both, and a scan that walked only one
      // would be a different operation wearing the same button.
      setScan(await api.scan());
      await load(media);
    } catch (e) {
      setError(e);
    } finally {
      setScanning(false);
    }
  }

  // The row that wants human attention, and **which column says so depends on
  // the media type**: a show's tmdb_id is NULL by construction, because its id
  // lives in tmdb_tv_id — TMDB numbers films and shows independently (D48).
  // Reading tmdb_id here would report every show as unmatched, for ever.
  const isUnmatched = (row: Movie) => (tv ? row.tmdb_tv_id === null : row.tmdb_id === null);

  const unmatched = rows?.filter(isUnmatched) ?? [];
  // library_path is null until an import or a scan puts the film on disk, so a
  // wanted row is emphatically NOT "on disk" and must not be counted as one.
  const onDisk = rows?.filter((m) => m.library_path !== null) ?? [];
  const wanted = rows?.filter((m) => m.library_path === null) ?? [];
  const shown = onlyUnmatched ? unmatched : (rows ?? []);

  async function remove(movie: Movie) {
    setDeleting(true);
    setError(null);
    try {
      setDeleted(await api.deleteMovie(movie.id));
      setConfirming(null);
      await load(media);
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
        {tv ? (
          <>
            One folder per show, named <span className="mono">Show (Year)</span>, with a{' '}
            <span className="mono">Season NN</span> under it per season. Scanning re-reads the disk
            and never writes to it — a folder with no episode in it loses its entry here, and stays
            exactly where it is on disk.
          </>
        ) : (
          <>
            One folder per film, named <span className="mono">Title (Year)</span>. Scanning re-reads
            the disk and never writes to it — a folder with no film in it loses its entry here, and
            stays exactly where it is on disk.
          </>
        )}
      </p>

      {/* Absent, not disabled, when LIBRARY_TV is unset. There is no second half
          to switch to on that install, and a switch with one working side is
          worse than no switch. */}
      {television.on && (
        <div className="row" style={{ margin: '0 0 1.1rem' }}>
          <MediaSwitch media={media} onChange={choose} disabled={scanning} />
        </div>
      )}

      <form className="row" onSubmit={(e) => (e.preventDefault(), rescan())}>
        <button className="primary" disabled={scanning}>
          {scanning ? 'Scanning…' : 'Scan the library'}
        </button>
        {unmatched.length > 0 && (
          <button type="button" onClick={() => setOnlyUnmatched((v) => !v)}>
            {onlyUnmatched ? `Show all ${rows?.length ?? 0}` : `Show ${unmatched.length} unmatched`}
          </button>
        )}
        <span className="small muted">
          {rows ? `${onDisk.length} on disk` : 'loading…'}
          {wanted.length > 0 && ` · ${wanted.length} wanted, not yet imported`}
          {unmatched.length > 0 && ` · ${unmatched.length} unmatched by TMDB`}
        </span>
      </form>

      {scanning && (
        <Working
          what={
            television.on
              ? 'Walking both libraries and asking TMDB'
              : 'Walking the library and asking TMDB'
          }
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

          {/* The television half of the same pass, and it is drawn on the
              CONFIGURED state rather than on `scan.shows > 0`. All six counters
              are zero when LIBRARY_TV is unset, so a zero there is a root that
              was never walked — but with television on, "0 shows" is a real
              finding and the one somebody needs to see after pointing the
              variable at the wrong directory. */}
          {television.on && (
            <span className="small">
              {scan.shows} show{scan.shows === 1 ? '' : 's'} holding {scan.episodes} episode
              {scan.episodes === 1 ? '' : 's'}, added {scan.shows_added}, matched{' '}
              {scan.shows_matched}.
              {scan.shows_unmatched > 0 && ` ${scan.shows_unmatched} still unmatched by TMDB.`}
            </span>
          )}

          {/* Removed and missing are the two the scan must never do quietly:
              one deleted a row, the other could not account for one. Both count
              BOTH media types — there is one prune over both roots. */}
          {scan.empty > 0 && (
            <span className="small muted">
              {scan.empty} folder{scan.empty === 1 ? '' : 's'} on disk hold no film and{' '}
              {scan.empty === 1 ? 'was' : 'were'} not recorded.
            </span>
          )}
          {television.on && scan.shows_empty > 0 && (
            <span className="small muted">
              {scan.shows_empty} folder{scan.shows_empty === 1 ? '' : 's'} under{' '}
              <span className="mono">LIBRARY_TV</span> hold no episode and{' '}
              {scan.shows_empty === 1 ? 'was' : 'were'} not recorded.
            </span>
          )}
          {scan.removed > 0 && (
            <span className="small muted">
              {scan.removed} entr{scan.removed === 1 ? 'y' : 'ies'} removed — nothing there, or a
              path outside the library root that owns it. The folders were left on disk.
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

      {error !== null && <Failure error={error} onRetry={() => load(media)} />}

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
              path outside the library root is not curator's to delete. */}
          {deleted.folder_left && (
            <span className="small muted">
              The folder <span className="mono">{deleted.folder_left}</span> is outside the library
              root, so it was left on disk.
            </span>
          )}
        </div>
      )}

      {confirming && (
        <ConfirmDelete
          movie={confirming}
          media={media}
          busy={deleting}
          onCancel={() => setConfirming(null)}
          onConfirm={() => remove(confirming)}
        />
      )}

      {/* The grid's own shape while the list is fetched, so the screen does
          not sit as a heading over blank space and then jump to posters. */}
      {rows === null && error === null && (
        <Loading>
          <SkeletonGrid />
        </Loading>
      )}

      {rows && shown.length === 0 && (
        <Empty>
          {onlyUnmatched ? (
            tv ? (
              'Every show is matched.'
            ) : (
              'Every film is matched.'
            )
          ) : tv ? (
            <>
              No shows yet. Find one from <Link href="/search/?media=tv">Search</Link>, or point{' '}
              <span className="mono">LIBRARY_TV</span> at a television library and scan.
            </>
          ) : (
            <>
              Nothing here yet. Search for something, or point{' '}
              <span className="mono">LIBRARY_MOVIES</span> at a library and scan.
            </>
          )}
        </Empty>
      )}

      <div className="grid">
        {shown.map((row) => (
          <Card
            key={row.id}
            movie={row}
            media={media}
            unmatched={isUnmatched(row)}
            onDelete={() => setConfirming(row)}
          />
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
  media,
  busy,
  onCancel,
  onConfirm,
}: {
  movie: Movie;
  media: MediaType;
  busy: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const tv = media === 'tv';
  return (
    <div className="banner error" role="alertdialog">
      <strong>
        Delete {movie.title}
        {movie.year ? ` (${movie.year})` : ''}?
      </strong>
      <span className="small">
        This removes the {tv ? 'show' : 'film'} from the library <em>and from the disk</em>, and
        cannot be undone.
      </span>
      <ul className="small" style={{ margin: '.5rem 0 0', paddingLeft: '1.1rem' }}>
        {movie.library_path && (
          <li>
            the folder <span className="mono">{movie.library_path}</span>
            {/* Said out loud, because a show folder holds every season anyone
                ever grabbed and the row gives no hint of how many that is. */}
            {tv && ', and every season under it'}
          </li>
        )}
        <li>
          {tv
            ? 'every downloaded season pack, deleted by the torrent backend, freeing '
            : 'the downloaded file, deleted by qBittorrent, freeing '}
          <strong>{formatBytes(movie.size_bytes)}</strong>
        </li>
        <li>
          {tv ? 'every torrent for it stops seeding and is removed' : 'the torrent stops seeding and is removed from qBittorrent'}
        </li>
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

function Card({
  movie,
  media,
  unmatched,
  onDelete,
}: {
  movie: Movie;
  media: MediaType;
  unmatched: boolean;
  onDelete: () => void;
}) {
  const poster = posterURL(movie.poster_path);
  const tv = media === 'tv';

  const body = (
    <>
      {/* A poster is the exception, not the rule: the unmatched rows have none,
          so the fallback has to look deliberate rather than broken. Poster is a
          plain <img> underneath — a static export has no image optimiser. */}
      <Poster src={poster} title={movie.title} />

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
        {unmatched && (
          <span className="badge warn" title="TMDB could not match this folder; it is recorded rather than guessed at">
            unmatched
          </span>
        )}
        {movie.status !== 'imported' && <span className="badge">{movie.status}</span>}
      </div>
    </>
  );

  /**
   * Where this card goes, and there are three answers rather than two.
   *
   * A matched film opens its TMDB page, addressed by the TMDB id (D21); an
   * unmatched film opens a page about the ROW, addressed by curator's own
   * movies.id (D35). A matched show opens /show/?id= with the **tv** id, which
   * is a different number in a different id space and never movies.id.
   *
   * An unmatched show has no third page to open, and this deliberately links
   * nowhere rather than somewhere broken: `/library/film/` is addressed by
   * movies.id but asks `GET /api/movies/{id}`, which answers 404 for a show on
   * purpose — one table, two media types, and a route that quietly served the
   * other kind is how a screen draws a show as a film. The card still says
   * everything the list knows about the row.
   */
  const href = tv
    ? movie.tmdb_tv_id === null
      ? null
      : `/show/?id=${movie.tmdb_tv_id}`
    : movie.tmdb_id === null
      ? `/library/film/?id=${movie.id}`
      : `/movie/?id=${movie.tmdb_id}`;

  return (
    // A wrapper, not a <Link> around everything: a <button> nested inside an <a>
    // is invalid HTML and double-fires, and Delete is the one destructive
    // control on this screen. So Delete is the anchor's SIBLING.
    <div className="movie-cell">
      {href ? (
        <Link className="movie" href={href}>
          {body}
        </Link>
      ) : (
        <div className="movie" title="No TMDB match, so there is no page to open. The row and the episodes are unaffected.">
          {body}
        </div>
      )}
      <button className="small" onClick={onDelete}>
        Delete
      </button>
    </div>
  );
}
