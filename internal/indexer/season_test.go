package indexer

import "testing"

// The names are real ones. Everything with "Severance" in it came off apibay on
// 2026-08-20 (q=severance&cat=205,208, the fixture in
// testdata/tpb-search-severance-tv.json); the films are from the Interstellar
// fixture and from this repo's own library folders.
//
// The half of this table that matters most is the bottom half: a season this
// parser invents is a claim about a release the name never made, and it would be
// stamped onto a film.
func TestParseSeasonEpisode(t *testing.T) {
	for _, tt := range []struct {
		name        string
		wantSeason  int
		wantEpisode int
	}{
		// A single episode, in the two spellings apibay actually carries.
		{"Severance S02E05 Trojans Horse 1080p ATVP WEB-DL DDP5 1 H 264-NTb", 2, 5},
		{"Severance 2022 S02E02 MULTI 1080p WEB H264-HiggsBoson", 2, 2},
		{"Severance.S02.E05.1080p.WEB.H264", 2, 5},
		{"severance s02e05 1080p", 2, 5},
		{"The Wire S01E01 1080p BluRay", 1, 1},

		// A season pack: a season, and no episode. Both spellings again, and the
		// second is the 844-seeder row a television search must not lose.
		{"Severance.S02.1080p.WEBRip.10bit.DDP5.1.x265-HODL", 2, 0},
		{"Severance - Season 1 - Mp4 x264 AC3 1080p", 1, 0},
		{"Severance.Season.2.COMPLETE.720p.ATVP.WEB-DL.x264.S02.Full-MIKE", 2, 0},

		// More than one season is not one season. 0 is "the name does not say",
		// which is the honest answer for a box set.
		{"Severance 2022 Seasons 1 and 2 Complete 1080p WEB x264 [i_c]", 0, 0},

		// Films state neither, and must not be made to. The last three are the
		// false positives that made the bare "s" alternative refuse a separator.
		{"Interstellar (2014) (2014) 1080p BrRip x264 - YIFY", 0, 0},
		{"Interstellar.2014.2160p.UHD.BluRay.x265.10bit.HDR.DTS-HD.MA.5.1-", 0, 0},
		{"Ocean's 8 (2018) 1080p WEBRip x264", 0, 0},
		{"Dune Part Two 2024 2160p WEB-DL DDP5 1 Atmos", 0, 0},
		{"Se7en 1995 1080p BluRay x265", 0, 0},
		{"", 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			season, episode := parseSeasonEpisode(tt.name)
			if season != tt.wantSeason || episode != tt.wantEpisode {
				t.Errorf("parseSeasonEpisode(%q) = season %d episode %d, want %d and %d",
					tt.name, season, episode, tt.wantSeason, tt.wantEpisode)
			}
		})
	}
}

// The year rule, at the one place it is decided. Both of the things it protects
// are asserted in tpb_test.go and x1337_test.go against real names; this pins the
// rule itself, which is one line and easy to "simplify" away.
func TestATelevisionQueryIsSearchedWithNoYear(t *testing.T) {
	for _, tt := range []struct {
		label string
		query Query
		want  int
	}{
		{"a film keeps its year", Query{Title: "Interstellar", Year: 2014}, 2014},
		{"a film spelled out keeps its year", Query{Title: "Interstellar", Year: 2014, Media: MediaMovie}, 2014},
		{"a film with no year still has none", Query{Title: "Interstellar"}, 0},
		{"a show's first-air year is dropped", Query{Title: "Severance", Year: 2022, Media: MediaTV}, 0},
		{"a show with a season too", Query{Title: "Severance", Year: 2022, Media: MediaTV, Season: 2}, 0},
	} {
		t.Run(tt.label, func(t *testing.T) {
			if got := tt.query.searchYear(); got != tt.want {
				t.Errorf("searchYear = %d, want %d", got, tt.want)
			}
		})
	}
}

// The zero value is a film, because every search made before Query existed was
// one. A Query{Title, Year} must keep meaning exactly what it meant.
func TestAQueryWithNoMediaIsAFilm(t *testing.T) {
	if got := (Query{Title: "Interstellar", Year: 2014}).mediaType(); got != MediaMovie {
		t.Errorf("mediaType = %q, want %q", got, MediaMovie)
	}
	if (Query{Title: "Interstellar"}).IsTV() {
		t.Error("a query with no media type reports itself as television")
	}
	if !(Query{Title: "Severance", Media: MediaTV}).IsTV() {
		t.Error("a television query does not report itself as one")
	}
}
