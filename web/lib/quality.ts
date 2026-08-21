import type { Release } from '@/lib/api';

/**
 * The selection meaning "do not filter".
 *
 * Empty rather than a word like `all`, because that is what the API already
 * reads as no constraint — `splitQuality` discards blanks and `FilterQuality`
 * returns its input for an empty `want` — so the one spelling means the same
 * thing on both sides if a screen ever sends it upstream.
 */
export const AnyQuality = '';

/**
 * What a release whose name carries no resolution is called.
 *
 * The server's own spelling (`indexer.QualityUnknown`,
 * internal/indexer/quality.go:7), not an em dash this file picked. It is also
 * exactly what the Quality column already draws for these rows, which is what
 * lets the chip and the badge be the same character — you filter for what you
 * saw.
 */
export const UnknownQuality = '—';

/** One chip in the filter row: what it selects, what it says, and how many rows it keeps. */
export type QualityOption = {
  value: string;
  label: string;
  count: number;
};

/**
 * Resolution order, best first — `internal/indexer/quality.go`'s table, mirrored.
 *
 * **This is a MENU order and it is emphatically not a section order.** Ordering
 * quality *sections* this way is D11 reversed and there is a live measurement
 * saying so: it leads Interstellar with a 262-seeder 2160p and buries the
 * 1194-seeder 1080p (D51, and `qualitySections` in ./sections.ts refuses it in
 * as many words). Do not reach for this table from there.
 *
 * The distinction is that a chip **buries nothing**. Section position decides
 * which release is read first; chip position decides where a control sits, and
 * the rows behind it stay seeders-ordered whichever chip is lit. So the two
 * orders differ on purpose, and the reason they may differ is that only one of
 * them is an implicit ranking of releases.
 *
 * What a menu needs instead is to hold still. Sections move between searches by
 * design — that is D51's accepted cost — and a filter whose buttons reshuffled
 * underneath a pointer would spend that cost a second time, for a control that
 * exists to be clicked rather than read.
 */
const resolutionOrder: Record<string, number> = {
  '2160p': 0,
  '1440p': 1,
  '1080p': 2,
  '720p': 3,
  '480p': 4,
  '360p': 5,
};

/**
 * Where a quality sorts in the chip row. Anything unrecognised lands with
 * `UnknownQuality` at the end, which is the residual bucket rather than a
 * judgement — YTS really does return `3D`, so an unranked spelling is a thing
 * that happens and not a parse failure.
 */
function resolutionIndex(quality: string): number {
  const rank = resolutionOrder[quality];
  return rank === undefined ? Object.keys(resolutionOrder).length : rank;
}

/**
 * Fold a release's quality to the one spelling everything here keys on.
 *
 * An empty string and the em dash are the same fact to a reader, so they must
 * not become two chips that each keep half the rows. `sectionKey` collapses the
 * pair the same way, and the two have to agree or a chip would select a section
 * that does not exist.
 */
export function qualityOf(release: Release): string {
  return release.quality || UnknownQuality;
}

/**
 * The chips to draw, derived from the releases actually in hand.
 *
 * **Always build these from the UNFILTERED list.** Deriving them from what is
 * currently shown is the standard way a filter row eats itself: pick 1080p, the
 * other chips vanish because no visible row carries them, and the only way back
 * to the full list is a new search. The caller therefore holds one list and
 * passes it here before narrowing it.
 *
 * A hard-coded set of chips would avoid that trap and introduce a worse one —
 * offering 2160p for a film that has none, which is a control whose only
 * outcome is an empty table. Every chip here keeps at least one row by
 * construction.
 */
export function qualityOptions(releases: Release[]): QualityOption[] {
  const counts = new Map<string, number>();
  for (const release of releases) {
    const quality = qualityOf(release);
    counts.set(quality, (counts.get(quality) ?? 0) + 1);
  }
  return [...counts.entries()]
    .map(([value, count]) => ({ value, label: value, count }))
    .sort((a, b) => {
      const byResolution = resolutionIndex(a.value) - resolutionIndex(b.value);
      // Ties are the unranked spellings sharing the last slot — `3D` beside the
      // em dash. Sorted by name so the row is deterministic rather than at the
      // mercy of which indexer answered first.
      return byResolution !== 0 ? byResolution : a.value.localeCompare(b.value);
    });
}

/**
 * The selection that is actually in effect, which is not always the one stored.
 *
 * A chosen quality **survives a new search**, because it is how somebody wants
 * to read a list and they are usually about to want the same thing again. But
 * the next film may not have it, and a 2160p selection against a film with no
 * 2160p would draw an empty table under a lit chip — a control silently
 * insisting on nothing.
 *
 * So the fallback is derived here rather than written back into the state. The
 * selection is kept as it was and this reports `AnyQuality` for the search that
 * cannot honour it; the moment a search does carry that quality again, it comes
 * back on its own. Resetting the state instead would make the preference
 * unrecoverable, and it is the reset — not the filter — that a person would
 * have to notice and undo.
 */
export function resolveQuality(options: QualityOption[], quality: string): string {
  if (quality === AnyQuality) return AnyQuality;
  return options.some((o) => o.value === quality) ? quality : AnyQuality;
}

/**
 * The releases a selection keeps, in the order they arrived.
 *
 * Order is never touched: the server ranked this list (D11) and a filter is a
 * narrowing, not a re-sort. That is what makes this compose with
 * `qualitySections` — sections partition whatever they are given, so filtering
 * first leaves the section rule reading an already-ranked list exactly as it
 * would have read the full one.
 *
 * Filtering happens here rather than through `?quality=` on the server for two
 * measured reasons. Quality is **not** in the search cache key
 * (`cacheKey`, internal/indexer/cache.go:55) and only 1337x is wrapped by the
 * cache at all, so every change of chip would re-fetch YTS, TPB and EZTV live —
 * 7.08 s for a film, up to 13 s for a cold television search, per click. And
 * the server cannot express this row's last chip: `FilterQuality` lowercases a
 * want and appends a `p` to anything lacking one, so `—` is compared as `—p`
 * and matches nothing, while an empty quality can never satisfy
 * `keep[r.Quality]` either. "No resolution in the name" is filterable here and
 * nowhere else.
 */
export function filterByQuality(releases: Release[], quality: string): Release[] {
  if (quality === AnyQuality) return releases;
  return releases.filter((release) => qualityOf(release) === quality);
}
