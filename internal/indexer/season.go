package indexer

import (
	"regexp"
	"strconv"
)

// The two shapes a television release names itself, and both of them are shapes
// a search has to survive rather than a preference. Measured against apibay on
// 2026-08-20, q=severance&cat=205,208 answered 100 rows carrying both at once:
//
//	Severance - Season 1 - Mp4 x264 AC3 1080p                      844 seeders
//	Severance S02E05 Trojans Horse 1080p ATVP WEB-DL DDP5 1 H 264-NTb  381
//
// The episode pattern is tried first, because "S02E05" also matches the season
// pattern and a single episode must not be read as a whole season.
var (
	// seasonEpisodePattern matches the scene convention: S02E05, s02.e05,
	// S1E1. The separator is optional because both spellings are in the wild.
	seasonEpisodePattern = regexp.MustCompile(`(?i)\bs(\d{1,2})[ ._-]?e(\d{1,3})\b`)

	// seasonPattern matches a season with no episode: S02, Season 2, Season.2.
	//
	// The bare "s" alternative has no optional separator on purpose. Allowing one
	// would make "Ocean's 8" a season and "Dune Part 2" nearly so, and a wrong
	// season is worse than a missing one — it is a claim about a release that the
	// name never made.
	seasonPattern = regexp.MustCompile(`(?i)\b(?:s|season[ ._-]?)(\d{1,2})\b`)
)

// parseSeasonEpisode reads the season and episode a release name states, or 0
// for whichever the name does not say.
//
// It is deliberately a read of the name and nothing else. A television search is
// answered by TPB and 1337x with the name they publish, so this is the only
// place either of them says which season a row is; guessing from the query
// instead would stamp the season that was ASKED FOR onto a row that is not it.
func parseSeasonEpisode(name string) (season, episode int) {
	if m := seasonEpisodePattern.FindStringSubmatch(name); m != nil {
		return atoiOrZero(m[1]), atoiOrZero(m[2])
	}
	if m := seasonPattern.FindStringSubmatch(name); m != nil {
		return atoiOrZero(m[1]), 0
	}
	return 0, 0
}

// How directly a release answers a television query that names a season, and
// the order they are offered in. The zero value is the one that needs no
// explanation — a film search, a show search naming no season, and an exact hit
// are all TierExact, so a query with no constraint sorts exactly as it did
// before any of this existed.
const (
	// TierExact is the thing that was asked for: the named episode, or any
	// release of the named season when no episode was named.
	TierExact = iota

	// TierPack is a season pack when a single episode was asked for. It is not
	// what was asked for and it does contain it, which is why it is kept and
	// why it is kept BELOW.
	//
	// Dropping it was the obvious alternative and it is wrong here, measured
	// rather than argued: apibay's best Severance result by a factor of two is
	// "Severance - Season 2 - Mp4 x264 AC3 1080p" at 727 seeders, and it names
	// no episode. A strict filter answers "no releases found" for an episode
	// that has a 727-seeder source sitting right there.
	TierPack

	// TierUnstated is a release whose name says no season at all — a
	// complete-series pack, or simply a name that does not follow the
	// convention. parseSeasonEpisode reads names and never guesses, so 0 here
	// means "the name does not say" and not "season 0".
	//
	// Kept, and last. This is the posture the year already takes and that TPB's
	// query takes: narrowing happens where "ambiguous" can mean "keep", because
	// the alternative is dropping a release on a claim its name never made.
	TierUnstated

	// TierWrong states a season or an episode that is not the one asked for.
	// This is the only tier that is dropped, and it is dropped on the name's own
	// evidence rather than on its silence.
	TierWrong
)

// SeasonTier says how directly r answers q's season and episode constraint.
//
// It is the single definition of that rule. The API echoes it per release so the
// screen can group on it, rather than each surface re-deriving "a pack is one
// whose episode is 0" and drifting from this the first time the rule gains a
// case.
func SeasonTier(r Release, q Query) int {
	// No constraint to answer. A film search, or a show searched without naming
	// a season — which is how a complete-series pack is found.
	if !q.IsTV() || q.Season == 0 {
		return TierExact
	}
	if r.Season == 0 {
		return TierUnstated
	}
	if r.Season != q.Season {
		return TierWrong
	}

	switch {
	case q.Episode == 0:
		// A whole season was asked for, so a pack and a single episode of that
		// season are equally the answer and neither outranks the other. This is
		// what keeps a season-only search ordered by seeders exactly as it was
		// before tiers existed.
		return TierExact
	case r.Episode == 0:
		return TierPack
	case r.Episode == q.Episode:
		return TierExact
	default:
		return TierWrong
	}
}

// atoiOrZero converts a matched run of digits, and cannot really fail: the
// patterns above match at most three of them. A value we somehow cannot read
// yields 0, which is this package's "the name does not say".
func atoiOrZero(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
