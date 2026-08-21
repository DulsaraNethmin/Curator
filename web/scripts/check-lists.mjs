// The ranked-list rules, checked against captured answers.
//
// Everything deciding what the release table shows lives in TypeScript —
// `qualitySections` orders the sections, `qualityOptions` orders the filter
// chips — and until T100 nothing here ran TypeScript at all. Both rules are one
// tidy-up away from being reversed with no test to notice: T99's own task file
// says so, and its guard was a comment. This is the guard.
//
// It imports the SHIPPED modules rather than restating them. A transliterated
// copy would pass while the real thing broke, which is the only failure mode
// worth designing against here.
//
// No test framework and no dependency: node's `--experimental-strip-types` runs
// the .ts files directly, and this is a .mjs so tsc never typechecks it (the
// fixtures carry only the fields the rules read, so they are not whole
// `Release`s and would not typecheck as such).
//
//   make check           # runs this
//   node --experimental-strip-types web/scripts/check-lists.mjs
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

import {
  AnyQuality,
  filterByQuality,
  qualityOf,
  qualityOptions,
  resolveQuality,
} from '../lib/quality.ts';
import { qualitySections } from '../lib/sections.ts';

const here = dirname(fileURLToPath(import.meta.url));
const fixtures = join(here, '..', '..', 'testdata', 'search');

let failed = 0;
function check(label, ok, detail = '') {
  if (ok) return true;
  failed++;
  console.log(`  FAIL  ${label}${detail ? `  — ${detail}` : ''}`);
  return false;
}

/**
 * Resolution order, restated on purpose.
 *
 * This is the one place a second copy is correct: quality.ts is what is being
 * checked, so importing its table would make the assertion pass by definition.
 */
const RESOLUTION = ['2160p', '1440p', '1080p', '720p', '480p', '360p'];
const resolutionIndex = (q) => (RESOLUTION.indexOf(q) === -1 ? RESOLUTION.length : RESOLUTION.indexOf(q));

const names = readdirSync(fixtures).filter((f) => f.endsWith('.json')).sort();
if (names.length === 0) throw new Error(`no fixtures in ${fixtures}`);

for (const file of names) {
  const answer = JSON.parse(readFileSync(join(fixtures, file), 'utf8'));
  const releases = answer.releases;
  const options = qualityOptions(releases);
  console.log(
    `\n${answer.search}  —  ${releases.length} releases, ` +
      `chips [All ${releases.length}] ${options.map((o) => `${o.label} ${o.count}`).join(' · ')}`,
  );

  // The chips partition the answer: every release in exactly one, none left over.
  check('chips partition the answer',
    options.reduce((n, o) => n + o.count, 0) === releases.length);
  for (const o of options) {
    check('a chip keeps exactly its count', filterByQuality(releases, o.value).length === o.count, o.value);
  }

  // Every chip keeps at least one row, so "your filter matched nothing" is
  // unreachable rather than handled. Follows from the counts above, asserted
  // anyway because it is the property the component relies on to draw no empty
  // state at all.
  check('no chip empties the table', options.every((o) => o.count > 0));

  // THE TRAP a filter row dies of: chips derived from the visible rows delete
  // themselves on the first click, and the only way back is a new search. Prove
  // both halves — that the real chips survive a selection, and that the naive
  // derivation would not.
  for (const o of options) {
    const narrowed = filterByQuality(releases, o.value);
    check('chips survive a selection', qualityOptions(releases).length === options.length, o.value);
    if (options.length > 1) {
      check('...and deriving them from the narrowed rows would not',
        qualityOptions(narrowed).length === 1, o.value);
    }
  }

  for (const o of options) {
    const narrowed = filterByQuality(releases, o.value);

    // A filter narrows; it never re-sorts. The kept rows keep the server's order.
    const expected = releases.filter((r) => qualityOf(r) === o.value);
    check('order is the server\'s, untouched',
      narrowed.every((r, i) => r.id === expected[i].id), o.value);

    // Seeders descend WITHIN A TIER — not globally. The tier is rank's primary
    // key (D49), so a season pack sits below an exact episode whatever its
    // seeders. Measured on the Severance episode fixture: filtered to 1080p it
    // opens on a 386-seeder exact match with a 716-seeder pack below it. An
    // assertion about the global maximum fails there, and the code is right.
    const tiers = new Map();
    for (const r of narrowed) {
      const tier = r.match ?? 'exact';
      if (!tiers.has(tier)) tiers.set(tier, []);
      tiers.get(tier).push(r);
    }
    for (const [tier, rows] of tiers) {
      check('seeders descend within a tier',
        rows.every((r, i) => i === 0 || rows[i - 1].seeders >= r.seeders), `${o.value}/${tier}`);
    }

    // Composes with the sections: filtering first leaves the section rule
    // reading an already-ranked list, and the result opens on the same release
    // as that quality's first section did unfiltered.
    const sections = qualitySections(narrowed);
    check('every section is that one quality',
      sections.every((s) => s.releases.every((r) => qualityOf(r) === o.value)), o.value);
    check('sections still partition',
      sections.reduce((n, s) => n + s.releases.length, 0) === o.count, o.value);
    const first = qualitySections(releases).find((s) => qualityOf(s.releases[0]) === o.value);
    check('opens on the same release as its first section',
      narrowed[0].id === first.releases[0].id, o.value);
  }

  // D51, the half of it that lives here: grouping is a stable partition, so the
  // top row is the same grouped or flat. T99 verified this on live answers and
  // nothing has re-checked it since.
  const sections = qualitySections(releases);
  check('grouped and flat open on the same release',
    sections[0].releases[0].id === releases[0].id);
  check('grouping loses no rows',
    sections.reduce((n, s) => n + s.releases.length, 0) === releases.length);

  // The chips are resolution-ordered; the sections are emphatically not.
  const chipOrder = options.map((o) => o.value);
  check('chips are resolution-ordered, unrecognised last',
    chipOrder.every((q, i) => i === 0 || resolutionIndex(chipOrder[i - 1]) <= resolutionIndex(q)),
    chipOrder.join(' '));

  // A selection stands down for an answer that cannot honour it, and is kept by
  // one that can — checked against every OTHER fixture, so the pair is real
  // data on both sides.
  for (const other of names) {
    const theirs = qualityOptions(JSON.parse(readFileSync(join(fixtures, other), 'utf8')).releases);
    for (const o of options) {
      const carried = resolveQuality(theirs, o.value);
      const present = theirs.some((t) => t.value === o.value);
      check('a carried selection is kept or stands down',
        carried === (present ? o.value : AnyQuality), `${o.value} → ${other}`);
    }
  }
  check('AnyQuality is always itself', resolveQuality(options, AnyQuality) === AnyQuality);
  check('AnyQuality keeps everything', filterByQuality(releases, AnyQuality).length === releases.length);
}

/**
 * D51's measurement, as an assertion rather than a comment.
 *
 * The chip order and the section order DIFFER on the same answer, and that is
 * the whole distinction T100 rests on: ordering sections by resolution buries
 * Interstellar's 1194-seeder 1080p under a 262-seeder 2160p, which is what D11
 * refuses; ordering chips by resolution buries nothing. If somebody sorts
 * `qualitySections` by `resolutionOrder` to make the headers predictable, this
 * is what fails.
 */
const interstellar = JSON.parse(readFileSync(join(fixtures, 'interstellar.json'), 'utf8')).releases;
const chips = qualityOptions(interstellar).map((o) => o.value);
const sectionOrder = qualitySections(interstellar).map((s) => qualityOf(s.releases[0]));
console.log(`\nInterstellar chips    ${chips.join(' · ')}`);
console.log(`Interstellar sections ${sectionOrder.join(' · ')}`);
check('the section order is NOT the chip order — D51 against the menu',
  JSON.stringify(chips) !== JSON.stringify(sectionOrder));
check('sections lead with the best-seeded release, not the best resolution',
  qualityOf(qualitySections(interstellar)[0].releases[0]) === '1080p',
  'a 262-seeder 2160p must not lead a 1194-seeder 1080p');

console.log(failed === 0 ? '\n  ok — the list rules hold' : `\n  ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
