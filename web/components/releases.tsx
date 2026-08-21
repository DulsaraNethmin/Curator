'use client';

import { Fragment, useEffect, useState } from 'react';
import {
  api,
  ApiError,
  formatBytes,
  type Download,
  type MediaType,
  type Release,
  type SearchResult,
  type Season,
} from '@/lib/api';
import { Empty, Failure } from '@/components/states';

/**
 * Film is what dispatch sends: the thing a release is for.
 *
 * It is a separate prop from `result` rather than read off it, because the two
 * are not the same string (D20). The release-name search knows only what was
 * typed, so it passes the search's own echo; the movie page passes TMDB's
 * canonical title, year and id, colon and all — `library.DestFolder` spells the
 * colon " - " on the way to disk, and a client that pre-stripped it would write
 * the wrong folder.
 *
 * Since phase 11 it is as often a show as a film. The name is the one dispatch
 * has always had and renaming it would churn two screens to say nothing new —
 * what matters is `media`, below, which is a completely different fact.
 */
export type Film = {
  title: string;
  year: number;

  /**
   * TMDB's id in the id space `media` names, and **the two are different
   * spaces**: 95396 is Severance's tv id and also some film's movie id (D48).
   * The server reads it as whichever `media_type` says, so sending one with the
   * wrong media type does not mislabel a row, it attaches the grab to a
   * different title entirely.
   */
  tmdb_id?: number | null;

  /**
   * Absent is a film, which is what every dispatch made before phase 11 was.
   *
   * It is not cosmetic. It decides which library root the import lands under,
   * and a show dispatched as a film is deleted by the next movie scan for
   * sitting outside LIBRARY_MOVIES — with its downloads, through the foreign
   * key. It also decides whether a season may be sent at all: the server
   * answers 400 for a season on a film request rather than ignoring it.
   */
  media?: MediaType;
};

/**
 * Releases is the list, the dispatch button, and everything that can go wrong
 * with pressing it — in one place, used by both screens that show releases.
 *
 * There is exactly one implementation of dispatch on purpose. Two would be two
 * places for the 410 wording to drift apart, and the 410 is the one error in
 * this system whose correct fix is a user action rather than a retry.
 *
 * `result` is nullable so the caller can mount this before it has searched: the
 * "downloads are not configured" warning is worth showing next to a button that
 * is about to be pressed, not only after the wait.
 */
export function Releases({
  result,
  film,
  searching,
  empty,
  noYearReason = 'Search with a year first — it becomes the library folder name',
  onSearchAgain,
  seasons = [],
  season = 0,
  onSeason,
  episode = 0,
  onEpisode,
}: {
  result: SearchResult | null;
  film: Film;
  searching?: boolean;
  empty?: React.ReactNode;
  // Why a year of 0 disables the button, which is a different sentence on each
  // screen. Typing a title without a year is the caller's own omission; TMDB
  // not knowing a release date is nobody's, and telling someone to "add a year"
  // for a film they cannot edit would be advice they cannot take.
  noYearReason?: string;
  onSearchAgain?: () => void;

  /**
   * The seasons TMDB lists, and which one is selected. An empty list means
   * there is no picker to draw — nothing is known to pick from — and season 0
   * means "every season", which is what the API reads an absent season as and
   * what finds a complete-series pack.
   *
   * It is the LIST and not the count, which is the difference T98 turned on.
   * `ShowDetails.seasons` is a number and cannot say how many episodes any of
   * them has, so a control built from it offered Silo a Season 4 with nothing
   * behind it and could not offer an episode row at all.
   *
   * The search itself belongs to the caller, which is why this is a callback
   * rather than a search made here: the show page owns the title, the year and
   * the request, and a second search implementation inside this component would
   * be a second place for the 410 wording to drift (see above).
   */
  seasons?: Season[];
  season?: number;
  onSeason?: (season: number) => void;

  /**
   * The episode within the selected season, and 0 is the whole season.
   *
   * It is drawn from the selected season's `episode_count`, which is a thing
   * this component only learned in T98 — before it, `seasons` was a count and
   * the control had to be a free number field that could not search on change.
   * See the rows below for what that cost.
   *
   * Only meaningful with a season: the server answers 400 for an episode
   * without one, so there is no episode row until a season is picked.
   */
  episode?: number;
  onEpisode?: (episode: number) => void;
}) {
  const [dispatching, setDispatching] = useState<string | null>(null);
  const [dispatched, setDispatched] = useState<Record<string, Download>>({});
  const [dispatchError, setDispatchError] = useState<unknown>(null);

  const [downloadsConfigured, setDownloadsConfigured] = useState<boolean | null>(null);

  // Why dispatch is refused, in the API's own words rather than this file's
  // guess. The backend is a choice (docs/decisions.md D22), so the sentence that
  // fixes it differs per backend: `embedded` needs no credentials at all, and
  // telling that user to set QBIT_USER is an instruction that cannot work.
  const [downloadsDetail, setDownloadsDetail] = useState<string | null>(null);

  // Ask once whether dispatch is even possible, rather than letting the button
  // fail with a 503.
  useEffect(() => {
    let cancelled = false;
    api
      .settings()
      .then((s) => {
        if (cancelled) return;
        // `torrents`, which is what the API calls it — NOT `qbittorrent`.
        // This read `'qbittorrent'` until T79 and the name has been `torrents`
        // since D22 moved the engine into this binary, so `find` returned
        // undefined on every install and `?? false` turned that into a hard
        // "not configured": every Download button disabled, everywhere, for a
        // backend reporting itself ready. Dispatch worked over the API the
        // whole time, which is why it survived so long.
        const torrents = s.integrations.find((i) => i.name === 'torrents');
        // Missing means the contract changed, not that downloads are off. Fail
        // OPEN — leave it unknown, let the button work, and let a real dispatch
        // return a real error. A rename must never silently disable the product
        // again, which is exactly what the old `?? false` did.
        setDownloadsConfigured(torrents ? torrents.configured : null);
        setDownloadsDetail(torrents?.detail ?? null);
      })
      .catch(() => {
        if (cancelled) return;
        setDownloadsConfigured(null);
        setDownloadsDetail(null);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // A new search issues new release ids, so what was queued against the old ones
  // says nothing about these.
  useEffect(() => {
    setDispatched({});
    setDispatchError(null);
  }, [result]);

  async function dispatch(release: Release) {
    if (!film.year) return;
    setDispatching(release.id);
    setDispatchError(null);
    try {
      const saved = await api.dispatch({
        release_id: release.id,
        // The film, not the release. A release id says nothing about which movie
        // it is for — the caller picked it out of a search it made and is the
        // only party that knows.
        title: film.title,
        year: film.year,
        tmdb_id: film.tmdb_id,
        media_type: film.media,
        // The release's OWN season wins, and the answer's season is the
        // fallback — never the selector's current value. A season selector
        // fires a request per change, so the control can already be showing
        // season 3 while the row being clicked came out of the season 2 answer.
        // The name is the truest source there is: the importer files episodes
        // by what each FILE says, and this number is the record of what a
        // person believed they were grabbing.
        season: film.media === 'tv' ? (release.season ?? result?.season ?? 0) : undefined,
      });
      setDispatched((prev) => ({ ...prev, [release.id]: saved }));
    } catch (e) {
      setDispatchError(e);
    } finally {
      setDispatching(null);
    }
  }

  const expired = dispatchError instanceof ApiError && dispatchError.expiredSearch;

  return (
    <>
      {dispatchError !== null && (
        <Failure
          error={dispatchError}
          // A 410 is the one error whose correct fix is a user action: the ids
          // belong to a search that has aged out, so offer to run it again.
          onRetry={expired ? onSearchAgain : undefined}
        />
      )}

      {/* Television only, and above the results rather than beside the search
          button, because it is a filter on what is listed below it and changing
          it re-runs the search. Drawn before the first search too: picking the
          season you want and then pressing Find releases is one wait instead of
          two. */}
      {film.media === 'tv' && onSeason && (
        <SeasonPicker
          seasons={seasons}
          season={season}
          onSeason={onSeason}
          episode={episode}
          onEpisode={onEpisode}
          searching={searching}
        />
      )}

      {result && <Indexers result={result} />}

      {result && result.releases.length === 0 && empty && <Empty>{empty}</Empty>}

      {result && result.releases.length > 0 && (
        <div className="panel">
          <table>
            <thead>
              <tr>
                <th>Release</th>
                <th className="num">Seeders</th>
                <th>Quality</th>
                <th className="num">Size</th>
                <th>Source</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {result.releases.map((release, i) => {
                const saved = dispatched[release.id];
                // The first release that is not an exact match opens the
                // "contains it" group. Read off the server's own verdict rather
                // than recomputed here (see Release.match), and keyed on the
                // CHANGE rather than on the value so the divider is drawn once
                // instead of before every pack. Releases arrive ranked by tier,
                // so one transition is all there can be per group.
                const previous = i > 0 ? result.releases[i - 1].match : undefined;
                const opensGroup =
                  release.match !== undefined &&
                  release.match !== 'exact' &&
                  release.match !== previous;
                return (
                  <Fragment key={release.id}>
                    {opensGroup && (
                      <tr>
                        <td colSpan={6} className="small muted" style={{ paddingTop: '1rem' }}>
                          {release.match === 'pack'
                            ? `— season ${result.season} packs, which contain episode ${result.episode} —`
                            : '— releases whose name does not say which season —'}
                        </td>
                      </tr>
                    )}
                    <tr>
                    <td>{release.title}</td>
                    {/* Seeders lead, because they are the only column that
                        predicts whether the download finishes — which is the
                        question a manual picker is actually asking (D11). */}
                    <td className="num">{release.seeders.toLocaleString()}</td>
                    <td>
                      <span className="badge">{release.quality || '—'}</span>
                    </td>
                    <td className="num tight">{formatBytes(release.size_bytes)}</td>
                    <td className="small muted">{release.indexers.join(', ')}</td>
                    <td className="tight">
                      {saved ? (
                        <span className="badge ok">queued</span>
                      ) : (
                        <button
                          onClick={() => dispatch(release)}
                          disabled={
                            dispatching !== null ||
                            downloadsConfigured === false ||
                            searching ||
                            !film.year
                          }
                          title={
                            downloadsConfigured === false
                              ? downloadsDetail
                                ? `Downloads are not configured: ${downloadsDetail}`
                                : 'Downloads are not configured'
                              : !film.year
                                ? noYearReason
                                : undefined
                          }
                        >
                          {dispatching === release.id ? 'Sending…' : 'Download'}
                        </button>
                      )}
                    </td>
                    </tr>
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {downloadsConfigured === false && (
        <div className="banner warn" style={{ marginTop: '1rem' }}>
          <strong>Downloads are not configured</strong>
          <span>
            {downloadsDetail ?? 'No torrent backend can accept a release.'} Search works without it.
          </span>
        </div>
      )}
    </>
  );
}

/**
 * Season and episode, as two rows of buttons.
 *
 * ```
 * SEASON   [Every] [1] [2] [3]
 * EPISODE  [All] [1][2][3][4][5][6][7][8][9][10]
 * ```
 *
 * **Both rows show what exists**, which is the whole change. The season row was
 * a `<select>` built from TMDB's season COUNT, so Silo — which reports four
 * seasons and has aired three — offered a Season 4 that could only ever return
 * nothing. And an episode row was impossible at all: a count cannot say how
 * many episodes a season has, so the episode control was a free number field.
 * `season_list` carries `episode_count` per season, and both rows fall out of
 * it.
 *
 * **Clicking searches immediately, and that undoes a compromise.** The number
 * field could not: it fires per keystroke, and a cold television search costs
 * up to thirteen seconds behind minter, so typing "12" would have launched a
 * search for episode 1 and abandoned it — which is why it needed Enter, and why
 * Enter had to be explained in the caption. A button is a discrete choice that
 * fires once, exactly as the season select already did, so the explanation goes
 * away with the input.
 *
 * It is `.switch`, the same class the Movies|Shows toggle uses, rather than a
 * fourth way of drawing a group of buttons where one is active.
 */
function SeasonPicker({
  seasons,
  season,
  onSeason,
  episode,
  onEpisode,
  searching,
}: {
  seasons: Season[];
  season: number;
  onSeason: (season: number) => void;
  episode: number;
  onEpisode?: (episode: number) => void;
  searching?: boolean;
}) {
  /**
   * Which seasons get a button, and the two exclusions are excluded for
   * completely different reasons.
   *
   * `episode_count === 0` is a season TMDB has ANNOUNCED and nobody has aired.
   * There is nothing to search for, and offering it is what the count-based
   * control did wrong.
   *
   * `number === 0` is Specials, and it is dropped because it **cannot be
   * asked for**: `?season=0` is what the API reads an absent season as, so a
   * Specials button would silently search every season and return a list that
   * looks like an answer. That overload is a known gap rather than something
   * this control can paper over — see parseSeason in internal/api/search.go.
   * Omitting the button is what keeps the gap from becoming a wrong answer.
   */
  const pickable = seasons.filter((s) => s.number > 0 && s.episode_count > 0);
  if (pickable.length === 0) return null;

  // The selected season's own episode count. A season that is no longer in the
  // list — the show was refetched between a click and a render — leaves this 0,
  // which draws no episode row rather than a row of nothing.
  const episodes = pickable.find((s) => s.number === season)?.episode_count ?? 0;

  return (
    <div style={{ margin: '0 0 .9rem' }}>
      <div className="row" style={{ marginBottom: '.4rem' }}>
        <span className="small muted" style={{ minWidth: '4.2rem' }}>
          Season
        </span>
        <div className="switch" role="group" aria-label="Season">
          {/* 0 is what the API reads an absent season as: no constraint at the
              indexers, which is how a complete-series pack is found. */}
          <button
            type="button"
            data-active={season === 0}
            aria-pressed={season === 0}
            disabled={searching}
            onClick={() => onSeason(0)}
          >
            Every
          </button>
          {pickable.map((s) => (
            <button
              key={s.number}
              type="button"
              data-active={season === s.number}
              aria-pressed={season === s.number}
              disabled={searching}
              // TMDB's own label, so a show that names its seasons something
              // other than "Season N" is not renamed by this control.
              title={`${s.name} — ${s.episode_count} episodes`}
              onClick={() => onSeason(s.number)}
            >
              {s.number}
            </button>
          ))}
        </div>
      </div>

      {/* No season, no episode row. The server answers 400 for an episode
          without one, so an unreachable control is worse than no control:
          this is the same rule, drawn. */}
      {onEpisode && season > 0 && episodes > 0 && (
        <div className="row" style={{ marginBottom: '.4rem' }}>
          <span className="small muted" style={{ minWidth: '4.2rem' }}>
            Episode
          </span>
          <div className="switch" role="group" aria-label="Episode">
            <button
              type="button"
              data-active={episode === 0}
              aria-pressed={episode === 0}
              disabled={searching}
              onClick={() => onEpisode(0)}
            >
              All
            </button>
            {Array.from({ length: episodes }, (_, i) => i + 1).map((n) => (
              <button
                key={n}
                type="button"
                data-active={episode === n}
                aria-pressed={episode === n}
                disabled={searching}
                onClick={() => onEpisode(n)}
              >
                {n}
              </button>
            ))}
          </div>
        </div>
      )}

      <span className="small muted">
        {season === 0
          ? 'Season packs and single episodes, from every season TMDB knows about.'
          : episode > 0
            ? `Narrowed to season ${season} episode ${episode}. Packs covering it are listed under the episodes.`
            : `Narrowed to season ${season}. Both a pack and a single episode import. Pick an episode to go narrower.`}
      </span>
    </div>
  );
}

/**
 * Per-indexer status.
 *
 * A search with one source down is a SUCCESS carrying the other two's releases,
 * not an error page — but the failure has to be named. An aggregator that
 * silently hides a downed indexer is how somebody concludes a film does not
 * exist.
 *
 * **Three kinds of not-working, and they are separated here because they need
 * three different sentences.** An indexer that is *down* is something to retry
 * or wait out. An indexer that is *unconfigured* never started at all — the
 * companion service it needs is not running — and no amount of waiting fixes
 * it; one line in a terminal does. Telling somebody 1337x is "down" when minter
 * was never started sends them to look at the wrong thing entirely
 * (docs/tasks/T49-minter-on-demand.md).
 *
 * The third is *not applicable*, and it is the one where nothing is wrong: the
 * indexer was switched on, did not fail, and was never asked, because this
 * media type is not one it has. All three arrive as `ok: false`, so all three
 * have to be read off their own field — until T95 this file knew two of them
 * and painted the third red with no message under it, on every television
 * search YTS was configured for.
 */
function Indexers({ result }: { result: SearchResult }) {
  const broken = result.indexers.filter((i) => !i.ok);
  const notApplicable = broken.filter((i) => i.not_applicable);
  // not_applicable first, and excluded from both lists below: an indexer that
  // was never asked is not unconfigured and is emphatically not down.
  const unconfigured = broken.filter((i) => !i.not_applicable && i.unconfigured);
  const failed = broken.filter((i) => !i.not_applicable && !i.unconfigured);

  return (
    <>
      <p className="small muted" style={{ margin: '0 0 .75rem' }}>
        {result.releases.length} release{result.releases.length === 1 ? '' : 's'} for{' '}
        {/* Shown exactly as it will be written to the library, so a sloppy
            search term is visible before it becomes a folder name. */}
        <strong>
          {result.title}
          {result.year ? ` (${result.year})` : ''}
        </strong>{' '}
        ·{' '}
        {result.indexers.map((indexer, i) => (
          <span key={indexer.name}>
            {i > 0 && ' · '}
            {/* Red is reserved for something that went wrong. An indexer that
                has no listings of this kind did not go wrong, so it is a plain
                badge — the sentence below says the rest. */}
            <span className={indexer.ok ? '' : indexer.not_applicable ? 'badge' : 'badge bad'}>
              {indexer.name}{' '}
              {indexer.ok
                ? indexer.count
                : indexer.not_applicable
                  ? 'not searched'
                  : indexer.unconfigured
                    ? 'not set up'
                    : 'failed'}
            </span>
          </span>
        ))}
      </p>

      {/* Its own sentence, and deliberately NOT a banner. The two banners below
          are things to act on — wait for an indexer, or start a container — and
          this is a fact about the search with no action attached to it at all.
          A warn banner would make "YTS has no television" look like a problem
          somebody introduced. */}
      {notApplicable.length > 0 && (
        <p className="small muted" style={{ margin: '-.35rem 0 .75rem' }}>
          {notApplicable.map((i) => i.name).join(', ')}{' '}
          {notApplicable.length === 1 ? 'has no' : 'have no'}{' '}
          {result.media === 'tv' ? 'television' : 'releases of this kind'}, so{' '}
          {notApplicable.length === 1 ? 'it was' : 'they were'} not searched at all. Nothing is
          wrong and there is nothing to set up — these results come from the sources that carry it.
        </p>
      )}

      {unconfigured.length > 0 && (
        <div className="banner warn">
          <strong>
            {unconfigured.map((i) => i.name).join(', ')}{' '}
            {unconfigured.length === 1 ? 'is switched on but not set up' : 'are switched on but not set up'}{' '}
            — these results are partial
          </strong>
          <span>
            {unconfigured.length === 1 ? 'It needs' : 'They need'} a companion service that is not
            running. Settings → Indexers has the one line to start it, and says whether it came up.
          </span>
        </div>
      )}

      {failed.length > 0 && (
        <div className="banner warn">
          <strong>
            {failed.length === 1 ? 'One indexer is down' : `${failed.length} indexers are down`} —
            these results are partial
          </strong>
          {failed.map((indexer) => (
            <span key={indexer.name} className="small">
              <span className="mono">{indexer.name}</span>: {indexer.error ?? 'no reason given'}
              <br />
            </span>
          ))}
        </div>
      )}
    </>
  );
}
