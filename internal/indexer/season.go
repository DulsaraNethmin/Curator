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
