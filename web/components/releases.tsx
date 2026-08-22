'use client';

import { Fragment, useEffect, useMemo, useState } from 'react';
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
import { parseGoDuration } from '@/lib/duration';
import { forget, releaseKey, remember } from '@/lib/recent';
import { qualitySections, type Section } from '@/lib/sections';
import {
  AnyQuality,
  filterByQuality,
  qualityOptions,
  resolveQuality,
  UnknownQuality,
} from '@/lib/quality';

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

  /**
   * Whether to draw the list in quality sections, and it is deliberately NOT
   * reset when a new search arrives: it is how somebody wants to read a list,
   * not a fact about the list they are reading.
   *
   * Nothing writes it to storage either. The repo remembers no view preference
   * anywhere — the Movies|Shows switch rides in the query string precisely so
   * that the URL is the only memory — and a toggle that quietly persisted would
   * be the first, for a control that is one click away from either state.
   */
  const [groupSections, setGroupSections] = useState(true);

  /**
   * The quality the list is narrowed to, and `AnyQuality` for all of them.
   *
   * Kept across a new search for the same reason the toggle above is: somebody
   * browsing at 1080p is usually about to want 1080p again. What makes that
   * safe is that it is never read directly — `activeQuality` below decides what
   * a stored selection means for the list actually in hand.
   */
  const [quality, setQuality] = useState(AnyQuality);

  // Built from the WHOLE answer, before any narrowing. Chips derived from the
  // visible rows would delete themselves on the first click and strand the list
  // at one quality until the next search — see qualityOptions.
  const options = useMemo(() => qualityOptions(result?.releases ?? []), [result]);

  // What the stored selection amounts to here, which is `AnyQuality` when this
  // search has no such release. Derived rather than written back, so the choice
  // returns by itself on the next search that can honour it.
  const activeQuality = resolveQuality(options, quality);

  // Asked for a quality this answer does not have. Worth one sentence, because
  // the chip moving on its own is otherwise unexplained.
  const droppedQuality = quality !== AnyQuality && activeQuality === AnyQuality;

  /**
   * The releases the filter keeps — the list every other derivation below reads.
   *
   * It can only be empty when the search itself was, which is a property rather
   * than a coincidence: `activeQuality` is either `AnyQuality` or a value taken
   * from `options`, and every option was built by counting at least one release.
   * There is no "your filter matched nothing" state to draw because the two
   * halves of this design make it unreachable.
   */
  const visible = useMemo(
    () => filterByQuality(result?.releases ?? [], activeQuality),
    [result, activeQuality],
  );

  const sections = useMemo(() => qualitySections(visible), [visible]);

  // One section is the whole list, so grouping it changes nothing — no headers,
  // and no control offering a choice that has no second outcome.
  const grouped = groupSections && sections.length > 1;

  /**
   * The rows in the order they are drawn, each carrying the section it belongs
   * to so the header can be drawn on the change rather than recomputed.
   *
   * Grouped is a stable partition of the ranked list and never a re-sort: the
   * sections come out in the order their first release appears, and inside one
   * the server's order is untouched. That is what makes the two modes safe to
   * toggle between — the first row on screen is the same release either way,
   * and no two rows in one section ever swap.
   */
  const ordered = useMemo(() => {
    if (grouped) {
      return sections.flatMap((section) => section.releases.map((release) => ({ release, section })));
    }
    const flat: { release: Release; section?: Section }[] = [];
    for (const release of visible) flat.push({ release, section: undefined });
    return flat;
  }, [grouped, sections, visible]);

  // Why dispatch is refused, in the API's own words rather than this file's
  // guess. The backend is a choice (docs/decisions.md D22), so the sentence that
  // fixes it differs per backend: `embedded` needs no credentials at all, and
  // telling that user to set QBIT_USER is an instruction that cannot work.
  const [downloadsDetail, setDownloadsDetail] = useState<string | null>(null);

  /**
   * How long curator keeps a release id resolvable, read from the server rather
   * than guessed — `intervals.search_cache_ttl` is `SEARCH_CACHE_TTL` as
   * `time.Duration.String()` wrote it, and `Aggregator.issue` stamps every id
   * with exactly that. A client TTL longer than the server's would hold a list
   * whose every Download button answers 410.
   *
   * The fallback is the Go default (`defaultSearchCacheTTL`, one hour), which is
   * the same shape `FALLBACK_POLL_MS` has on the Activity screen. It is only
   * ever an optimisation: the 410 below is what makes a stale list safe.
   */
  const [ttlMs, setTtlMs] = useState(3_600_000);

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
        setTtlMs(parseGoDuration(s.intervals?.search_cache_ttl) ?? 3_600_000);
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

  /**
   * Hold the answer, so leaving this screen and coming back does not cost the
   * search again — 7.08 s for a film, up to 13 s for television.
   *
   * **This component is the writer and the pages are the readers**, because it
   * is the only one in the tree that has read `/api/settings` and therefore the
   * only one holding the TTL. It stamps an absolute deadline once, so
   * `recall` needs no notion of how long anything lasts.
   *
   * `releaseKey` returns null for the release-name search on /search/, which has
   * no stable identity to key on — so that mode is excluded here by
   * construction rather than by a prop this component would have to be told.
   */
  useEffect(() => {
    if (!result) return;
    const key = releaseKey(film.media, film.tmdb_id, result.season ?? 0, result.episode ?? 0);
    if (key) remember(key, result, Date.now() + ttlMs);
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
      // A 410 means these ids are dead — which is precisely what the cache is
      // holding, so it goes. Dropped HERE rather than through a callback the
      // pages pass down: this is the one implementation of dispatch, and a page
      // that forgot to wire the prop would serve the dead list back on the next
      // visit. The list itself stays on screen with its "Search again"; only the
      // memory of it goes.
      if (e instanceof ApiError && e.expiredSearch) forget();
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

      {result && <Indexers result={result} shown={visible.length} />}

      {result && result.releases.length === 0 && empty && <Empty>{empty}</Empty>}

      {result && (options.length > 1 || sections.length > 1) && (
        <div style={{ margin: '0 0 .7rem' }}>
          {/* One quality is no choice at all, so no row — the same rule the
              layout toggle below has always used. */}
          {options.length > 1 && (
            <div className="row" style={{ marginBottom: '.4rem' }}>
              <span className="small muted" style={{ minWidth: '4.2rem' }}>
                Quality
              </span>
              <div className="switch" role="group" aria-label="Quality">
                <button
                  type="button"
                  data-active={activeQuality === AnyQuality}
                  aria-pressed={activeQuality === AnyQuality}
                  onClick={() => setQuality(AnyQuality)}
                >
                  All {result.releases.length}
                </button>
                {options.map((option) => (
                  <button
                    key={option.value}
                    type="button"
                    data-active={activeQuality === option.value}
                    aria-pressed={activeQuality === option.value}
                    // NOT disabled while a search runs, and that is the whole
                    // visible difference from the season row above it. That one
                    // fires a request per click and has to wait; this one
                    // re-reads a list already in memory, so there is nothing to
                    // wait for and disabling it would only invent a delay.
                    title={
                      option.value === UnknownQuality
                        ? `${option.count} with no resolution in the name`
                        : `${option.count} at ${option.value}`
                    }
                    onClick={() => setQuality(option.value)}
                  >
                    {option.label} {option.count}
                  </button>
                ))}
              </div>
              {droppedQuality && (
                <span className="small muted">
                  Nothing here at {quality} — showing all of them. It comes back on a search that
                  has it.
                </span>
              )}
            </div>
          )}

          {sections.length > 1 && (
            <div className="row">
              {/* "Layout", because the row above it took the word Quality and
                  meant it. This said Quality until T100, when a control that
                  actually filters on quality landed directly above a control
                  that only re-arranges. */}
              <span className="small muted" style={{ minWidth: '4.2rem' }}>
                Layout
              </span>
              <div className="switch" role="group" aria-label="Quality grouping">
                <button
                  type="button"
                  data-active={groupSections}
                  aria-pressed={groupSections}
                  onClick={() => setGroupSections(true)}
                >
                  Grouped
                </button>
                <button
                  type="button"
                  data-active={!groupSections}
                  aria-pressed={!groupSections}
                  onClick={() => setGroupSections(false)}
                >
                  Flat
                </button>
              </div>
              {/* Says the property rather than the mechanism, because the property
                  is the reason it is safe to leave on: the sections re-arrange the
                  middle of the list and never the top of it. */}
              <span className="small muted">
                {grouped
                  ? 'Sections in the order of their best-seeded release — the top row is the same either way.'
                  : 'One list, best-seeded first.'}
              </span>
            </div>
          )}
        </div>
      )}

      {result && visible.length > 0 && (
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
              {ordered.map(({ release, section }, i) => {
                const saved = dispatched[release.id];
                // The first release that is not an exact match opens the
                // "contains it" group. Read off the server's own verdict rather
                // than recomputed here (see Release.match), and keyed on the
                // CHANGE rather than on the value so the divider is drawn once
                // instead of before every pack. Releases arrive ranked by tier,
                // so one transition is all there can be per group — and the
                // grouped order preserves that, because a tier is contiguous in
                // the ranked list and the tier is part of a section's key.
                // TestRankKeepsATierContiguous is what pins the half of that
                // sentence living in Go.
                const previous = i > 0 ? ordered[i - 1] : undefined;
                const opensGroup =
                  release.match !== undefined &&
                  release.match !== 'exact' &&
                  release.match !== previous?.release.match;
                // Identity, not the key: two sections cannot share an object,
                // and comparing objects means the label is never re-derived.
                const opensSection = section !== undefined && section !== previous?.section;
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
                    {opensSection && (
                      <tr>
                        <td
                          colSpan={6}
                          className="small"
                          // Less top padding than the tier divider above, and
                          // that is the nesting: a section sits INSIDE a tier,
                          // and the two are adjacent whenever a tier's first
                          // section opens it.
                          style={{ paddingTop: opensGroup ? '.2rem' : '.8rem', fontWeight: 550 }}
                        >
                          {section.label}
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
function Indexers({ result, shown }: { result: SearchResult; shown: number }) {
  const broken = result.indexers.filter((i) => !i.ok);
  const notApplicable = broken.filter((i) => i.not_applicable);
  // not_applicable first, and excluded from both lists below: an indexer that
  // was never asked is not unconfigured and is emphatically not down.
  const unconfigured = broken.filter((i) => !i.not_applicable && i.unconfigured);
  const failed = broken.filter((i) => !i.not_applicable && !i.unconfigured);

  return (
    <>
      <p className="small muted" style={{ margin: '0 0 .75rem' }}>
        {/* "14 of 91" only while a filter is narrowing, so the unfiltered case
            reads exactly as it did before T100.

            The per-indexer counts beside it are deliberately NOT recounted
            against the filter. They report what each source returned for this
            search, which a client-side view control does not change — and they
            were never a count of the rows below in the first place: Count is
            len(s.releases) at internal/indexer/aggregate.go:266, taken before
            dedupe, narrow and rank. Recounting them here would leave the row
            looking the same while meaning something else. */}
        {shown < result.releases.length && `${shown} of `}
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
