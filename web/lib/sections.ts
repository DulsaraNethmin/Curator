import type { Release } from '@/lib/api';

/**
 * One quality section: the releases in it, and the header drawn above them.
 *
 * The releases are held rather than counted because grouping is a re-ordering.
 * Inside one tier the ranked list is seeders-descending, so its qualities
 * INTERLEAVE — measured, Interstellar's 91 releases alternate 1080p and 2160p
 * the whole way down. Drawing a header wherever the quality changes would have
 * opened a section every few rows; the rows have to actually move.
 */
export type Section = {
  key: string;
  label: string;
  releases: Release[];
};

/**
 * Which section a release belongs to: its tier and its quality, together.
 *
 * **Together is the whole point.** The tier is the outer grouping (D49) and
 * quality nests inside it, so a bare "1080p" would appear once under the
 * episodes and again under the packs, twenty rows apart, meaning two different
 * things. Making the pair the key means the two are different sections, and
 * `sectionLabel` says which is which.
 *
 * An empty `quality` and the server's QualityUnknown em dash are the same fact
 * to a reader, so they collapse here rather than heading two sections the same
 * way.
 */
export function sectionKey(release: Release): string {
  return `${release.match ?? 'exact'}|${release.quality || '—'}`;
}

/**
 * The header for a section, built from any one of its releases and its size.
 *
 * The tier is named only when it is not `exact`, and that keeps the common case
 * clean: a film search and a season-only search are entirely exact — measured,
 * Interstellar and Silo are 100% of one tier — so those screens get bare
 * resolution headers with no qualifier at all. The qualifier appears exactly
 * where it is load-bearing, which is the screen that has more than one tier on
 * it.
 */
export function sectionLabel(release: Release, count: number): string {
  const quality = release.quality || '—';
  const parts = [quality === '—' ? 'no resolution in the name' : quality];
  if (release.match === 'pack') parts.push('season packs');
  else if (release.match === 'unstated') parts.push('season not stated');
  parts.push(`${count} release${count === 1 ? '' : 's'}`);
  return parts.join(' · ');
}

/**
 * The ranked list, partitioned into quality sections in the order they are
 * drawn. It is a **stable partition** and never a sort: sections come out in
 * the order their first release appeared, and within one the server's order
 * survives untouched.
 *
 * **The section order is D11 applied one level up.** Seeders order the rows,
 * and the best row orders the sections — so the section holding the best-seeded
 * release is first, which is the same release the flat list leads with. The
 * obvious alternative is ordering sections best-resolution-first, and it is
 * D11's own rejected sentence with numbers on it. Measured live 2026-08-21:
 *
 *   Interstellar    2160p best 262 seeders  ·  1080p best 1194
 *   Dune Part Two   2160p best 518          ·  1080p best 1142
 *
 * A resolution-ordered list of sections leads with the 262 and buries the 1194,
 * which is exactly "a 1-seeder 2160p above a 500-seeder 1080p" — the ordering
 * D11 exists to refuse. It is also why "unknown sorts last" does not hold here:
 * Silo's nine no-resolution releases outseed its ten 480p ones, 6 to 4, and
 * they sort above them on that evidence.
 *
 * Do not reorder these by QualityRank to make the headers appear in a
 * predictable place. The predictability is real and it is worth less than the
 * top row.
 *
 * **Since T100 that table exists in TypeScript too** — `resolutionOrder` in
 * ./quality.ts — and it is one import away from here. It orders the FILTER
 * CHIPS, where holding still is the whole point and no release is buried by a
 * button's position. Reaching for it from this function is the exact mistake
 * the paragraph above measures; its own comment says so from the other side.
 *
 * It lives in `lib` rather than beside the component because it is the only
 * part of the sections that can be checked without a browser, and it was —
 * see docs/tasks/T99-quality-sections.md for the run against live data.
 */
export function qualitySections(releases: Release[]): Section[] {
  const byKey = new Map<string, Section>();
  const out: Section[] = [];
  for (const release of releases) {
    const key = sectionKey(release);
    let section = byKey.get(key);
    if (!section) {
      section = { key, label: '', releases: [] };
      byKey.set(key, section);
      out.push(section);
    }
    section.releases.push(release);
  }
  // Labelled once the sizes are known, off the first release in each — every
  // release in a section shares the two facts the label is built from.
  for (const section of out) {
    section.label = sectionLabel(section.releases[0], section.releases.length);
  }
  return out;
}
