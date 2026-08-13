package indexer

import "strings"

// NormaliseQuery turns a film's canonical title into the one the indexers
// actually answer.
//
// These are two different strings and conflating them is the bug this exists to
// prevent. The CANONICAL title — "Avengers: Endgame" — is what the API echoes,
// what dispatch stores, and what library.DestFolder turns into the folder
// "Avengers - Endgame (2019)". The QUERY title is only ever sent to an indexer.
//
// Measured against the live indexers 2026-08-13, which is why this is a decision
// (docs/decisions.md D20) rather than a tidy-up:
//
//	query                       1337x   yts   tpb
//	Avengers: Endgame               0     7   100
//	Avengers Endgame               20     7   100
//	Avengers: Infinity War          0     6   100
//	Avengers Infinity War          20     6   100
//	Spider-Man: No Way Home        20     6    55
//	Dune: Part Two                 20     6    73
//
// A colon silently loses 1337x on some films and not others, and it does it with
// NO error: the search reports ok:true, count:0, which is indistinguishable from
// a film nobody has uploaded. Stripping it never lost a result in any case
// measured; YTS and TPB are unaffected either way.
//
// So it strips the colon and NOTHING else.
//
//   - Not the hyphen. "Spider-Man" and "X-Men" contain real ones, and D9 exists
//     because that distinction has already cost this project once.
//   - Not the ampersand or the apostrophe. Unmeasured — and this function is a
//     record of a measurement, not a guess about what an indexer might prefer.
func NormaliseQuery(title string) string {
	if !strings.Contains(title, ":") {
		// The overwhelmingly common case, and worth not rebuilding the string for.
		return strings.TrimSpace(title)
	}
	// Fields collapses the run of whitespace the removed colon leaves behind, so
	// "Avengers:  Endgame" and "Avengers: Endgame" produce the same query — and
	// therefore the same cache key.
	return strings.Join(strings.Fields(strings.ReplaceAll(title, ":", " ")), " ")
}
