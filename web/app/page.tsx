'use client';

import { useEffect, useState } from 'react';
import Link from 'next/link';
import {
  api,
  televisionOn,
  type DiscoverSlot,
  type Download,
  type MediaType,
  type Movie,
  type SettingsResult,
} from '@/lib/api';
import { Failure } from '@/components/states';
import { Icon } from '@/components/icons';
import { MediaSwitch } from '@/components/media-switch';
import { FirstRun, isFirstRun } from '@/components/first-run';
import { Billboard } from '@/components/billboard';
import {
  Loading,
  LoadingRails,
  SkeletonBillboard,
  SkeletonRails,
} from '@/components/skeleton';
import { Rail } from '@/components/rail';

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

  const [slots, setSlots] = useState<DiscoverSlot[] | null>(null);
  const [discoverError, setDiscoverError] = useState<unknown>(null);
  const [media, setMedia] = useState<MediaType>('movie');

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
      .settings()
      .then((s) => !cancelled && setSettings(s))
      .catch(() => !cancelled && setSettingsFailed(true));

    return () => {
      cancelled = true;
    };
  }, []);

  // The rails, on their own, because the switch re-runs them. Twelve more TMDB
  // calls per press behind a fifteen-minute cache, which is what the strip
  // above deliberately does not pay: /api/movies is the library and answers
  // whether TMDB is reachable or not.
  //
  // The stream names every rail first and then fills them in, so `slots` goes
  // from null to twelve titled placeholders in one step and each row lands
  // where its id already sits. An AbortController and not a `cancelled` flag,
  // because there is a live connection to close: a media switch with a flag
  // would leave the old stream reading twelve rails nobody is going to draw.
  useEffect(() => {
    const abort = new AbortController();
    setSlots(null);
    setDiscoverError(null);

    api
      .discoverStream(
        media,
        {
          onRails: (_, opening) => setSlots(opening),
          // Placed by id, never appended: rails arrive in the order TMDB
          // answers, which is not the order they are drawn (T102).
          onRow: (row) =>
            setSlots(
              (current) =>
                current?.map((slot) => (slot.id === row.id ? { ...slot, row } : slot)) ?? current,
            ),
        },
        abort.signal,
      )
      .catch((e) => {
        if (abort.signal.aborted) return;
        setDiscoverError(e);
        // Whatever arrived stays; what did not is marked failed rather than
        // left shimmering. A skeleton that never fills is the one state this
        // screen must not have, because it is indistinguishable from a rail
        // still in flight and there is nothing left to fill it.
        setSlots((current) =>
          current?.map((slot) =>
            slot.row
              ? slot
              : { ...slot, row: { id: slot.id, title: slot.title, ok: false, results: [] } },
          ) ?? current,
        );
      });

    return () => abort.abort();
  }, [media]);

  // Television, read off the settings call this screen already makes rather
  // than through useTelevision — a second /api/settings on the landing screen
  // to learn a fact already in hand would be a request for nothing.
  const tvOn = settings !== null && televisionOn(settings);

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
    // The screen this almost always becomes is the one below, so its stand-in
    // is that screen's shape — not a sentence that shoves everything down when
    // the real page lands a beat later. The rare other outcome is FirstRun,
    // and one brief skeleton before a setup page is a fair price for the
    // common path never shifting.
    return (
      <>
        <h1 className="visually-hidden">Discover</h1>
        <Loading>
          <SkeletonBillboard />
          <SkeletonRails rails={2} />
        </Loading>
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

  // The billboard is the trending rail's top few cards, drawn only when that
  // rail actually answered — a failed trending rail is a banner further down,
  // and a blank hero above it would be the same failure said twice, in the
  // largest element on the page. The backdrop check mirrors the component's
  // own contract (a billboard IS the image), so the pitch can be drawn when
  // Billboard would render nothing.
  //
  // **It waits on the trending rail and nothing else now, which is the whole
  // point of the stream.** It used to wait on the response, so the largest
  // element on the screen was held up by whichever of twelve rails TMDB
  // answered last — documentaries, usually.
  const trendingSlot = slots?.find((slot) => slot.id === 'trending');
  const trendingRow = trendingSlot?.row ?? null;
  const trending = trendingRow?.ok ? trendingRow.results : undefined;
  const billboard = trending?.some((film) => film.backdrop_path) ? trending : undefined;
  const trendingPending = discoverError === null && trendingRow === null;
  const pending = slots === null || slots.some((slot) => slot.row === null);

  return (
    <>
      {/* The page's heading, and the only one it has when the billboard is
          drawn: a document whose first heading is the <h2> over the trending
          rail starts its outline halfway down. */}
      <h1 className="visually-hidden">Discover</h1>

      {billboard ? (
        <Billboard films={billboard} media={media} />
      ) : trendingPending ? (
        // The billboard's own shape while trending is in flight. The pitch
        // below is for an install whose trending rail truly answered with
        // nothing — drawing it for the loading beat instead meant the whole
        // page shoved down when the billboard landed.
        <SkeletonBillboard />
      ) : (
        <p className="lede" style={{ marginTop: 'var(--sp-8)' }}>
          Pick {tvOn ? 'a film or a show' : 'a film'}, and it downloads, hardlinks itself into the
          library and tells Jellyfin. One binary where seven containers used to be.
        </p>
      )}

      {error !== null && <Failure error={error} />}

      {/* Compacted into a strip: these three numbers are a status line, and they
          were taking the whole screen above the only content on it. */}
      <div className="strip">
        <Link href="/library/">
          <b>{movies?.length ?? '—'}</b>
          {/* "films" rather than "in the library" once there is a second half
              of the library to be counted separately from. /api/movies is
              films and always was; saying "in the library" beside a Shows tab
              would read as the total. */}
          <span>{tvOn ? 'films in the library' : 'in the library'}</span>
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
          <span>
            Search for a film <Icon name="arrow-right" size="sm" />
          </span>
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

      {/* Absent when LIBRARY_TV is unset, which is the whole of D48's opt-in on
          this screen: no switch, no TV rails, and the landing page of an
          install that wanted films is exactly the one it had.

          The negative bottom margin this row carried was tuned against a rail
          whose <h2> set its own top margin. The rails own their spacing now
          (.rail-section), so a pull-up here collides the switch with the first
          heading instead of tightening it. */}
      {tvOn && (
        <div className="row" style={{ margin: 'var(--sp-6) 0' }}>
          <MediaSwitch media={media} onChange={setMedia} />
        </div>
      )}

      {/* A failed catalogue is a banner under the counters, never an error page:
          the library above it is still true and still worth seeing. */}
      {discoverError !== null && <Failure error={discoverError} />}

      {/* The only beat with no rails at all: the moment before the stream's
          opening event, which measured 3ms against a live server. Generic,
          because nothing yet knows how many rails there are or what they are
          called — and short enough that it is mostly a guard against a slow
          connection rather than a screen anybody reads. */}
      {slots === null && discoverError === null && (
        <Loading>
          <SkeletonRails />
        </Loading>
      )}

      {/* One status for twelve rails. The sections below are silent: they are
          skeletons inside real headings now, not a loading screen, and twelve
          polite announcements of "Loading…" is one fact said twelve times. */}
      <LoadingRails pending={pending} />

      {/* The rail titles are the same for both media types where they mean the
          same thing — "Trending this week" is unambiguous because this screen
          shows one media type at a time, and a "Trending shows" heading would be
          the switch's job said twice. `media` is what makes each card open the
          right page. */}
      {slots?.map((slot, index) => (
        <Rail key={slot.id} slot={slot} media={media} index={index} />
      ))}
    </>
  );
}
