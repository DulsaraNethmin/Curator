package library

import (
	"fmt"
	"testing"
)

func TestParseEpisode(t *testing.T) {
	cases := []struct {
		name    string
		season  int
		episode int
	}{
		// The three forms, bare.
		{"S02E05.mkv", 2, 5},
		{"s02e05.mkv", 2, 5},
		{"2x05.mkv", 2, 5},

		// The three forms as release groups actually write them.
		{"Severance.S01E07.1080p.WEB.H264-SuccessfulCrab.mkv", 1, 7},
		{"severance.s01e07.1080p.web.h264.mkv", 1, 7},
		{"The.Bear.1x03.HDTV.x264-GROUP.mkv", 1, 3},
		{"The Bear - 2x10.mkv", 2, 10},
		{"Severance (2022) - S01E01.mkv", 1, 1},

		// The separator between the two halves varies and nothing else does.
		{"Show.S01.E05.mkv", 1, 5},
		{"Show S01 E05.mkv", 1, 5},
		{"Show-S01-E05.mkv", 1, 5},

		// Single digits, because "S1E1" is a spelling in the wild.
		{"Show.S1E1.mkv", 1, 1},
		{"Show.1x01.mkv", 1, 1},

		// Season 00 is Jellyfin's specials folder, and a real one.
		{"Fleabag (2016) - S00E01.mkv", 0, 1},

		// Three-digit episodes, which anime and long-running dailies both use.
		{"Show.S01E101.mkv", 1, 101},
		{"Show.S2019E243.mkv", 2019, 243},

		// The multi-episode file: the FIRST number wins, in every spelling of
		// the range. See ParseEpisode's doc comment for why it is not two rows.
		{"Show.S02E05E06.mkv", 2, 5},
		{"Show.S02E05-E06.mkv", 2, 5},
		{"Show.S02E05E06E07.mkv", 2, 5},
		{"Show.s02e05e06.720p.mkv", 2, 5},

		// The code does not have to be at the end, and the noise after it is
		// noise.
		{"S03E09.Show.Name.2160p.DV.HDR.mkv", 3, 9},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			season, episode, ok := ParseEpisode(tc.name)
			if !ok {
				t.Fatalf("ParseEpisode(%q) = ok false, want S%02dE%02d", tc.name, tc.season, tc.episode)
			}
			if season != tc.season || episode != tc.episode {
				t.Errorf("ParseEpisode(%q) = (%d, %d), want (%d, %d)",
					tc.name, season, episode, tc.season, tc.episode)
			}
		})
	}
}

// Everything that is not an episode code, refused rather than approximated.
// Each of these has a plausible wrong answer, which is the reason it is here.
func TestParseEpisodeRefusesRatherThanGuesses(t *testing.T) {
	cases := map[string]string{
		"nothing at all": "",
		"a film":         "Interstellar (2014).mkv",
		"a season pack":  "Show.S01.1080p.WEB.mkv",

		// The season for these three is in the folder above, not in the name,
		// and reading it from there is exactly the inference this refuses. A
		// later task can add folder-aware parsing deliberately; it must not
		// arrive as a default that files everything under season 1.
		"an episode with no season": "Show.E05.mkv",
		"a bare number":             "05.mkv",
		"a padded number":           "Show - 05.mkv",

		"words":             "Season 2 Episode 5.mkv",
		"a four-digit code": "Show.S01E1010.mkv",
		"no digits":         "Show.SxxExx.mkv",
		"a year":            "Show (2022).mkv",
		"a date":            "Show.2022.03.14.mkv",
	}

	for what, name := range cases {
		t.Run(what, func(t *testing.T) {
			season, episode, ok := ParseEpisode(name)
			if ok {
				t.Errorf("ParseEpisode(%q) = (%d, %d, true), want ok false", name, season, episode)
			}
		})
	}
}

// The two names that made the "2x05" form dangerous enough to bound. Both are
// ordinary things to find in a release name, and both parse as a season and an
// episode if the pattern is written the obvious way.
func TestParseEpisodeIsNotFooledByAResolutionOrACodec(t *testing.T) {
	names := []string{
		"Show.Name.1920x1080.BluRay.mkv",
		"Show.Name.1280x720.mkv",
		"Show.Name.720x480.mkv",
		"Show.Name.DD5.1x264-GROUP.mkv",
		"Show.Name.AAC2.0.x264.mkv",
		"Show.Name.10bit.x265.mkv",
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			season, episode, ok := ParseEpisode(name)
			if ok {
				t.Errorf("ParseEpisode(%q) = S%02dE%02d — a resolution or a codec was read as an episode",
					name, season, episode)
			}
		})
	}
}

// A title's own hyphens and digits must not become a code. This is the same
// family as CLAUDE.md's title-parsing trap: the characters that look like a
// pattern are frequently part of the name.
func TestParseEpisodeDoesNotReadATitleAsACode(t *testing.T) {
	names := []string{
		"Spider-Man - No Way Home (2021).mkv",
		"X-Men Origins - Wolverine (2009).mkv",
		"F1 (2025).mkv",
		"Crime 101 (2026).mkv",
		"Se7en (1995).mkv",
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			if season, episode, ok := ParseEpisode(name); ok {
				t.Errorf("ParseEpisode(%q) = S%02dE%02d, want ok false", name, season, episode)
			}
		})
	}
}

// A code glued to a word is not a code. The word boundary is written out in the
// pattern rather than taken from \b, because \b is happy with a digit in front.
func TestParseEpisodeWantsABoundaryInFrontOfTheCode(t *testing.T) {
	for _, name := range []string{"Coloss01e05.mkv", "1080S01E05.mkv", "1x1x05.mkv"} {
		t.Run(name, func(t *testing.T) {
			if season, episode, ok := ParseEpisode(name); ok {
				t.Errorf("ParseEpisode(%q) = S%02dE%02d, want ok false", name, season, episode)
			}
		})
	}
}

// Every code this package WRITES must parse back, or curator names a file it
// cannot read on the next scan. The naming half is asserted against EpisodeName
// in shows_test.go; this is the pattern's own end of the round trip.
func TestParseEpisodeReadsBackTheCodeCuratorWrites(t *testing.T) {
	for _, c := range []struct{ season, episode int }{
		{0, 1}, {1, 1}, {1, 9}, {1, 10}, {2, 5}, {9, 99}, {10, 1}, {12, 24},
	} {
		name := fmt.Sprintf("Severance (2022) - S%02dE%02d.mkv", c.season, c.episode)
		season, episode, ok := ParseEpisode(name)
		if !ok {
			t.Errorf("ParseEpisode(%q) = ok false — curator wrote that name", name)
			continue
		}
		if season != c.season || episode != c.episode {
			t.Errorf("ParseEpisode(%q) = (%d, %d), want (%d, %d)", name, season, episode, c.season, c.episode)
		}
	}
}
