package indexer

import "testing"

// The release names are the real ones apibay returned for q=severance&cat=205,208
// on 2026-08-20, which is also what testdata/tpb-search-severance-tv.json holds.
// The seeder counts are real too, and they are the whole reason the tier sits
// above seeders in rank: the season pack outseeds every single episode by
// roughly two to one, so any ordering that puts seeders first buries the episode
// somebody explicitly asked for.
const (
	severancePack   = "Severance - Season 2 - Mp4 x264 AC3 1080p"
	severanceS02E05 = "Severance S02E05 Trojans Horse 1080p ATVP WEB-DL DDP5 1 H 264-NTb"
)

func tvRelease(title string, season, episode, seeders int) Release {
	return Release{Title: title, Season: season, Episode: episode, Seeders: seeders}
}

// TestSeasonTier is the whole narrowing rule in one table.
//
// The case that earns its keep is "a pack against an episode query": it is the
// one that must NOT be TierWrong, because dropping it answers "no releases
// found" for an episode that has a 727-seeder source sitting right there.
func TestSeasonTier(t *testing.T) {
	for _, tt := range []struct {
		label   string
		release Release
		query   Query
		want    int
	}{
		{
			"a film is never tiered",
			Release{Title: "Interstellar 2014 1080p"},
			Query{Title: "Interstellar", Year: 2014, Media: MediaMovie},
			TierExact,
		},
		{
			"a show searched with no season constrains nothing",
			tvRelease(severancePack, 2, 0, 727),
			Query{Title: "Severance", Media: MediaTV},
			TierExact,
		},
		{
			"the season asked for, no episode asked for",
			tvRelease(severancePack, 2, 0, 727),
			Query{Title: "Severance", Media: MediaTV, Season: 2},
			TierExact,
		},
		{
			// A single episode is as much an answer to "season 2" as the pack
			// is, and neither outranks the other — which is what keeps a
			// season-only search ordered by seeders exactly as it always was.
			"a single episode also answers a season-only query",
			tvRelease(severanceS02E05, 2, 5, 381),
			Query{Title: "Severance", Media: MediaTV, Season: 2},
			TierExact,
		},
		{
			"the episode asked for",
			tvRelease(severanceS02E05, 2, 5, 381),
			Query{Title: "Severance", Media: MediaTV, Season: 2, Episode: 5},
			TierExact,
		},
		{
			// The case the whole design turns on.
			"a pack of the right season is kept when an episode was asked for",
			tvRelease(severancePack, 2, 0, 727),
			Query{Title: "Severance", Media: MediaTV, Season: 2, Episode: 5},
			TierPack,
		},
		{
			"a name claiming no season at all is kept, last",
			tvRelease("Severance COMPLETE 1080p WEB-DL", 0, 0, 90),
			Query{Title: "Severance", Media: MediaTV, Season: 2, Episode: 5},
			TierUnstated,
		},
		{
			"another season is dropped",
			tvRelease("Severance - Season 1 - Mp4 x264 AC3 1080p", 1, 0, 844),
			Query{Title: "Severance", Media: MediaTV, Season: 2},
			TierWrong,
		},
		{
			"the right season, the wrong episode, is dropped",
			tvRelease("Severance S02E06 1080p ATVP WEB-DL", 2, 6, 120),
			Query{Title: "Severance", Media: MediaTV, Season: 2, Episode: 5},
			TierWrong,
		},
		{
			// Query.Episode's documented contract, from the tier's side: with
			// no season there is no constraint at all, so nothing is narrowed.
			"an episode with no season constrains nothing",
			tvRelease("Severance - Season 1 - Mp4 x264 AC3 1080p", 1, 0, 844),
			Query{Title: "Severance", Media: MediaTV, Episode: 5},
			TierExact,
		},
	} {
		t.Run(tt.label, func(t *testing.T) {
			if got := SeasonTier(tt.release, tt.query); got != tt.want {
				t.Errorf("SeasonTier(%q s%de%d, season %d episode %d) = %d, want %d",
					tt.release.Title, tt.release.Season, tt.release.Episode,
					tt.query.Season, tt.query.Episode, got, tt.want)
			}
		})
	}
}

// TestNarrowDropsOnlyWhatTheNameContradicts pins the half of the rule that is a
// filter rather than an ordering.
//
// It also pins the thing that was broken before any of this: a season used to
// narrow ONLY 1337x, because the season went into that indexer's keyword string
// and nothing filtered a result anywhere. So TPB answered "season 2" with all
// four seasons, and the two backends answered different questions from one
// Query. The rows below are what one merged answer now looks like.
func TestNarrowDropsOnlyWhatTheNameContradicts(t *testing.T) {
	in := []Found{
		{Release: tvRelease(severancePack, 2, 0, 727)},
		{Release: tvRelease(severanceS02E05, 2, 5, 381)},
		{Release: tvRelease("Severance - Season 1 - Mp4 x264 AC3 1080p", 1, 0, 844)},
		{Release: tvRelease("Severance S02E06 1080p ATVP WEB-DL", 2, 6, 120)},
		{Release: tvRelease("Severance COMPLETE 1080p WEB-DL", 0, 0, 90)},
	}

	got := narrow(in, Query{Title: "Severance", Media: MediaTV, Season: 2, Episode: 5})

	var kept []string
	for _, f := range got {
		kept = append(kept, f.Title)
	}
	want := []string{severancePack, severanceS02E05, "Severance COMPLETE 1080p WEB-DL"}
	if len(kept) != len(want) {
		t.Fatalf("kept %d releases %v, want %d %v", len(kept), kept, len(want), want)
	}
	for i := range want {
		if kept[i] != want[i] {
			t.Errorf("kept[%d] = %q, want %q", i, kept[i], want[i])
		}
	}
}

// TestNarrowLeavesAFilmSearchAlone: every query that names no season has to come
// out of narrow byte for byte, because that is every film search this app has
// ever made.
func TestNarrowLeavesAFilmSearchAlone(t *testing.T) {
	in := []Found{
		{Release: Release{Title: "Interstellar 2014 1080p", Seeders: 500}},
		// A film whose name happens to parse as a season. "Ocean's 8" and "Dune
		// Part 2" are the real hazards here, and the guard is that no film query
		// is tiered at all rather than that the parser is clever.
		{Release: Release{Title: "Dune Part 2 2024 2160p", Season: 2, Seeders: 300}},
	}
	if got := narrow(in, Query{Title: "Interstellar", Year: 2014, Media: MediaMovie}); len(got) != 2 {
		t.Errorf("a film search kept %d of 2 releases", len(got))
	}
	// The same slice against a show searched without a season.
	if got := narrow(in, Query{Title: "Severance", Media: MediaTV}); len(got) != 2 {
		t.Errorf("a seasonless show search kept %d of 2 releases", len(got))
	}
}

// TestRankPutsTheAskedForEpisodeAboveABetterSeededPack is the measurement, as a
// test: 727 seeders against 381, and the 381 has to win because it is the thing
// that was asked for.
//
// If this ever fails with the pack first, the tier has been moved below seeders
// in rank — which is the ordering that looks reasonable in the diff and is
// wrong for the only question being asked.
func TestRankPutsTheAskedForEpisodeAboveABetterSeededPack(t *testing.T) {
	q := Query{Title: "Severance", Media: MediaTV, Season: 2, Episode: 5}
	in := []Found{
		{Release: tvRelease(severancePack, 2, 0, 727)},
		{Release: tvRelease("Severance COMPLETE 1080p WEB-DL", 0, 0, 90)},
		{Release: tvRelease(severanceS02E05, 2, 5, 381)},
	}

	got := rank(in, q)

	want := []string{severanceS02E05, severancePack, "Severance COMPLETE 1080p WEB-DL"}
	for i, title := range want {
		if got[i].Title != title {
			t.Errorf("rank[%d] = %q (%d seeders), want %q",
				i, got[i].Title, got[i].Seeders, title)
		}
	}
}

// TestRankIsSeedersFirstWhenNothingIsTiered: below the tier the old order is
// untouched, and for a film there is no tier at all. D11 is the reason — seeders
// predict whether a download finishes and resolution does not.
func TestRankIsSeedersFirstWhenNothingIsTiered(t *testing.T) {
	in := []Found{
		{Release: Release{Title: "low", Seeders: 1, Quality: "2160p"}},
		{Release: Release{Title: "high", Seeders: 500, Quality: "1080p"}},
	}
	got := rank(in, Query{Title: "Interstellar", Media: MediaMovie})
	if got[0].Title != "high" {
		t.Errorf("rank[0] = %q, want the 500-seeder even though it is the lower resolution", got[0].Title)
	}
}
