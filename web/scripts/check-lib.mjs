// The pure modules the screens depend on, against an injected clock.
//
// A sibling of check-lists.mjs rather than a section inside it, and the split is
// by subject: that file is "the ranked-list rules, checked against captured
// answers" and every assertion in it reads testdata/search/*.json. These have no
// fixtures and need a clock instead, so folding them in would give that file two
// modes and a name that is only half true — and its name is quoted in four
// records (docs/progress.md, decisions.md D52, T100, T102).
//
// It imports the SHIPPED modules, exactly as its sibling does. A transliterated
// copy would pass while the real thing broke, which is the only failure mode
// worth designing against here.
//
//   make units           # runs this
//   node --experimental-strip-types web/scripts/check-lib.mjs
import { ago, parseGoDuration } from '../lib/duration.ts';
import { forget, recall, releaseKey, remember } from '../lib/recent.ts';

let failed = 0;
function check(label, ok, detail = '') {
  if (ok) return true;
  failed++;
  console.log(`  FAIL  ${label}${detail ? `  — ${detail}` : ''}`);
  return false;
}

// ---------------------------------------------------------------------------
// parseGoDuration — what time.Duration.String() actually writes.
//
// The three spellings are the ones GET /api/settings sends today:
// download_poll "5s", search_cache_ttl "1h0m0s", search_timeout "30s".
{
  check('an hour', parseGoDuration('1h0m0s') === 3_600_000, String(parseGoDuration('1h0m0s')));
  check('seconds', parseGoDuration('10s') === 10_000);
  check('a compound', parseGoDuration('1h30m0s') === 5_400_000);
  check('a fraction', parseGoDuration('1.5s') === 1500);

  // **The floor is not this function's.** It lived here until T108 and it is a
  // polling rule — Activity keeps it at its own call site. A cache TTL clamped
  // to a second would be harmless today and wrong in principle, and this is the
  // assertion that stops somebody moving it back in.
  check('sub-second durations survive', parseGoDuration('500ms') === 500,
    'the 1000ms floor belongs to Activity, not to duration parsing');

  check('nothing is null', parseGoDuration(undefined) === null);
  check('empty is null', parseGoDuration('') === null);
  check('prose is null', parseGoDuration('nonsense') === null);
}

// ---------------------------------------------------------------------------
// ago — three cases, because its one caller shows a list that cannot outlive
// SEARCH_CACHE_TTL. Anything at or past the hour says so in words rather than
// in a number: past the TTL the honest statement is that it is old.
{
  const t = 10_000_000;
  check('under a minute', ago(t, t + 30_000) === 'moments ago');
  check('exactly one minute is singular', ago(t, t + 60_000) === '1 minute ago');
  check('minutes are plural', ago(t, t + 240_000) === '4 minutes ago');
  check('just under an hour', ago(t, t + 59 * 60_000) === '59 minutes ago');
  check('an hour and over', ago(t, t + 3_600_000) === 'over an hour ago');
  check('a clock that went backwards does not say -3', ago(t, t - 5000) === 'moments ago');
}

// ---------------------------------------------------------------------------
// releaseKey — what counts as the same search.
{
  check('a film keys on its id', releaseKey('movie', 157336) === 'movie:157336:0:0');

  // The two id spaces overlap: 95396 is Severance's tv id AND some film's movie
  // id (D48). A key without the media type would serve one title's releases for
  // the other.
  check('the media type is part of the key',
    releaseKey('movie', 95396) !== releaseKey('tv', 95396));

  // /show/ searches per season, and 1337x answers ok:true count:0 rather than
  // erroring — so a season-2 list served to a season-3 question looks fine on
  // screen and is wrong.
  check('a season is part of the key',
    releaseKey('tv', 95396, 2) !== releaseKey('tv', 95396, 3));
  check('an episode is part of the key',
    releaseKey('tv', 95396, 2, 5) !== releaseKey('tv', 95396, 2, 0));

  // No id, no key — which is how /search/'s release-name mode is excluded by
  // construction rather than by a flag every caller has to remember.
  check('no id is no key', releaseKey('movie', null) === null);
  check('undefined is no key', releaseKey('movie', undefined) === null);
}

// ---------------------------------------------------------------------------
// remember / recall / forget — one entry, an absolute expiry, stable identity.
{
  const answer = { title: 'Interstellar', year: 2014, releases: [{ id: 'a' }] };
  const key = releaseKey('movie', 157336);
  const t0 = 1_000_000;
  const hour = 3_600_000;

  forget();
  check('nothing is held to begin with', recall(key, t0) === null);

  remember(key, answer, t0 + hour, t0);
  check('it comes back', recall(key, t0 + 1)?.result === answer);

  // Identity, not equality. <Releases> resets its dispatch state on
  // useEffect(…, [result]), so a fresh object per recall would clear every
  // "queued" mark on every render.
  check('the SAME object comes back', recall(key, t0 + 1)?.result === recall(key, t0 + 2)?.result,
    'a new object per recall would reset dispatch state in <Releases>');

  check('it remembers when', recall(key, t0 + 1)?.at === t0);

  check('another key misses', recall(releaseKey('movie', 999), t0 + 1) === null);
  check('the same id in the other space misses',
    recall(releaseKey('tv', 157336), t0 + 1) === null);

  // The expiry is absolute and exclusive at the boundary.
  check('it survives up to the deadline', recall(key, t0 + hour - 1) !== null);
  check('it is gone at the deadline', recall(key, t0 + hour) === null);
  check('and stays gone', recall(key, t0 + 1) === null,
    'an expired entry must be dropped, not merely hidden');

  // One entry: a second remember evicts the first. This is the contract the
  // user asked for — the most recent film or show, and only that one.
  const other = { title: 'Dune', year: 2021, releases: [] };
  remember(key, answer, t0 + hour, t0);
  remember(releaseKey('movie', 438631), other, t0 + hour, t0);
  check('a second entry evicts the first', recall(key, t0 + 1) === null);
  check('and the second is held', recall(releaseKey('movie', 438631), t0 + 1)?.result === other);

  // forget() is what the 410 handler calls: the ids are dead, and it does not
  // need to know which key issued them.
  forget();
  check('forget empties it', recall(releaseKey('movie', 438631), t0 + 1) === null);
}

console.log(failed === 0 ? '\n  ok — the shared lib rules hold' : `\n  ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
