package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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

// --- FindEpisodes -----------------------------------------------------------

// codes renders what a scan found in the form a person compares by eye, and it
// is what every assertion below is written against: the ORDER is part of the
// contract, not an artefact of how the directories happened to be walked.
func codes(episodes []Episode) []string {
	out := make([]string, 0, len(episodes))
	for _, e := range episodes {
		out = append(out, fmt.Sprintf("S%02dE%02d", e.Season, e.Episode))
	}
	return out
}

func findEpisodes(t *testing.T, root string) []Episode {
	t.Helper()
	episodes, err := FindEpisodes(root, smallFloor)
	if err != nil {
		t.Fatalf("FindEpisodes(%q): %v", root, err)
	}
	return episodes
}

// A season pack, in the layout the library itself is written in, and the answer
// sorted by season and episode rather than by where the walk happened to go.
func TestFindEpisodesReadsASeasonPack(t *testing.T) {
	root := t.TempDir()
	mkFile(t, filepath.Join(root, "Season 02", "Severance (2022) - S02E01.mkv"), 4096, 'a')
	mkFile(t, filepath.Join(root, "Season 01", "Severance (2022) - S01E02.mkv"), 2048, 'b')
	mkFile(t, filepath.Join(root, "Season 01", "Severance (2022) - S01E01.mkv"), 3072, 'c')

	episodes := findEpisodes(t, root)
	if got, want := codes(episodes), []string{"S01E01", "S01E02", "S02E01"}; !slices.Equal(got, want) {
		t.Fatalf("codes = %v, want %v", got, want)
	}
	if episodes[0].Size != 3072 {
		t.Errorf("S01E01 size = %d, want 3072", episodes[0].Size)
	}
	if base := filepath.Base(episodes[2].Path); base != "Severance (2022) - S02E01.mkv" {
		t.Errorf("S02E01 path = %q, want the file itself", episodes[2].Path)
	}
}

// A flat show folder is as real as a foldered one, and the codes in it are
// written in every case and both forms.
func TestFindEpisodesReadsAFlatFolder(t *testing.T) {
	root := t.TempDir()
	mkFile(t, filepath.Join(root, "the.bear.1x01.mkv"), 2048, 'a')
	mkFile(t, filepath.Join(root, "The.Bear.S01E02.mkv"), 2048, 'b')
	mkFile(t, filepath.Join(root, "the.bear.s01e03.mkv"), 2048, 'c')

	if got, want := codes(findEpisodes(t, root)), []string{"S01E01", "S01E02", "S01E03"}; !slices.Equal(got, want) {
		t.Fatalf("codes = %v, want %v", got, want)
	}
}

// content_path is the file itself for a single-file torrent — one episode
// grabbed on its own — so a bare file has to work as well as a directory.
func TestFindEpisodesAcceptsAFilePath(t *testing.T) {
	file := mkFile(t, filepath.Join(t.TempDir(), "Severance (2022) - S01E04.mkv"), 4096, 'a')

	episodes := findEpisodes(t, file)
	if len(episodes) != 1 {
		t.Fatalf("episodes = %+v, want one", episodes)
	}
	if episodes[0].Path != file || episodes[0].Season != 1 || episodes[0].Episode != 4 {
		t.Errorf("episode = %+v, want %q at S01E04", episodes[0], file)
	}

	// The same file under the floor is ErrNoVideo, judged exactly as it would
	// be inside a folder.
	small := mkFile(t, filepath.Join(t.TempDir(), "Severance (2022) - S01E04.mkv"), 16, 'a')
	if _, err := FindEpisodes(small, smallFloor); !errors.Is(err, ErrNoVideo) {
		t.Errorf("err = %v, want ErrNoVideo for a file under the floor", err)
	}
}

// The rules that keep a sample out of the library are the film picker's, and
// they are SHARED rather than reimplemented — this is the test that says so.
// Every entry here is one FindFeature already refuses.
func TestFindEpisodesAppliesTheFeaturePickersRules(t *testing.T) {
	root := t.TempDir()
	real := mkFile(t, filepath.Join(root, "Season 01", "Show - S01E01.mkv"), 4096, 'a')

	mkFile(t, filepath.Join(root, "Season 01", "sample", "Show - S01E02.mkv"), 8192, 'b')
	mkFile(t, filepath.Join(root, "Extras", "Show - S01E03.mkv"), 8192, 'c')
	mkFile(t, filepath.Join(root, "Featurettes", "Show - S01E04.mkv"), 8192, 'd')
	mkFile(t, filepath.Join(root, "Subs", "Show - S01E05.mkv"), 8192, 'e')
	// The macOS AppleDouble fork: a hidden file carrying the extension of a
	// file it is not.
	mkFile(t, filepath.Join(root, "Season 01", "._Show - S01E06.mkv"), 8192, 'f')
	// Under the floor, which at the real 50 MiB is what a 3-8 MB sample.mkv is.
	mkFile(t, filepath.Join(root, "Season 01", "Show - S01E07.mkv"), 64, 'g')
	// Not a video at all.
	mkFile(t, filepath.Join(root, "Season 01", "Show - S01E08.nfo"), 4096, 'h')

	episodes := findEpisodes(t, root)
	if got, want := codes(episodes), []string{"S01E01"}; !slices.Equal(got, want) {
		t.Fatalf("codes = %v, want %v — every other file here is one the film picker refuses", got, want)
	}
	if episodes[0].Path != real {
		t.Errorf("path = %q, want %q", episodes[0].Path, real)
	}
}

// A crafted torrent must not be able to point an import at an arbitrary file on
// the disk, and a season pack is no different from a film in that respect.
func TestFindEpisodesDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mkFile(t, filepath.Join(outside, "Show - S09E09.mkv"), 65536, 'x')
	real := mkFile(t, filepath.Join(root, "Show - S01E01.mkv"), 2048, 'a')

	if err := os.Symlink(outside, filepath.Join(root, "elsewhere")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "Show - S09E09.mkv"), filepath.Join(root, "Show - S02E02.mkv")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	episodes := findEpisodes(t, root)
	if got, want := codes(episodes), []string{"S01E01"}; !slices.Equal(got, want) {
		t.Fatalf("codes = %v, want %v — neither the linked directory nor the linked file is ours to follow", got, want)
	}
	if episodes[0].Path != real {
		t.Errorf("path = %q, want the real file %q", episodes[0].Path, real)
	}
}

func TestFindEpisodesStopsAtTheDepthCap(t *testing.T) {
	root := t.TempDir()
	mkFile(t, filepath.Join(root, "a", "b", "Show - S01E01.mkv"), 4096, 'a')

	if _, err := FindEpisodes(root, FeatureOpts{MinBytes: 1024, MaxDepth: 1}); !errors.Is(err, ErrNoVideo) {
		t.Errorf("MaxDepth 1: err = %v, want ErrNoVideo — a/b is two levels down", err)
	}
	if _, err := FindEpisodes(root, FeatureOpts{MinBytes: 1024, MaxDepth: 2}); err != nil {
		t.Errorf("MaxDepth 2: %v, want the file to be found", err)
	}
}

// The two empty answers, which are two different facts about the disk. Only one
// of them is fixed by renaming a file, and a caller that could not tell them
// apart would report the wrong one to a person.
func TestFindEpisodesTellsNoVideoFromNoEpisode(t *testing.T) {
	empty := t.TempDir()
	writeFile(t, filepath.Join(empty, "readme.txt"), 4096)
	if _, err := FindEpisodes(empty, smallFloor); !errors.Is(err, ErrNoVideo) {
		t.Errorf("nothing playable: err = %v, want ErrNoVideo", err)
	}

	unnamed := t.TempDir()
	mkFile(t, filepath.Join(unnamed, "Chernobyl (2019) - Behind the Scenes.mkv"), 4096, 'a')
	err := findEpisodesErr(t, unnamed)
	if !errors.Is(err, ErrNoEpisode) {
		t.Errorf("video with no code: err = %v, want ErrNoEpisode", err)
	}
	// And emphatically NOT the other one, because the bytes are there.
	if errors.Is(err, ErrNoVideo) {
		t.Error("err is ErrNoVideo as well; the two sentinels have to be distinguishable")
	}
	if !strings.Contains(err.Error(), unnamed) {
		t.Errorf("err = %q, want the path in it", err)
	}
}

// findEpisodesErr is the failure half of findEpisodes: it fails the test if the
// call SUCCEEDS, which is the mistake worth catching in a test about refusals.
func findEpisodesErr(t *testing.T, root string) error {
	t.Helper()
	episodes, err := FindEpisodes(root, smallFloor)
	if err == nil {
		t.Fatalf("FindEpisodes(%q) = %+v, want an error", root, episodes)
	}
	return err
}

// Two files for one episode — a repack beside the original — are BOTH returned,
// and the sort puts them next to each other. Choosing between them is a decision
// about what the library contains, and this function does not make it silently.
func TestFindEpisodesReturnsBothFilesForOneEpisode(t *testing.T) {
	root := t.TempDir()
	mkFile(t, filepath.Join(root, "Show - S01E01.mkv"), 2048, 'a')
	mkFile(t, filepath.Join(root, "Show - S01E01.REPACK.mkv"), 4096, 'b')
	mkFile(t, filepath.Join(root, "Show - S01E02.mkv"), 2048, 'c')

	episodes := findEpisodes(t, root)
	if got, want := codes(episodes), []string{"S01E01", "S01E01", "S01E02"}; !slices.Equal(got, want) {
		t.Fatalf("codes = %v, want %v", got, want)
	}
	if episodes[0].Path == episodes[1].Path {
		t.Error("the two S01E01 entries are the same file")
	}
}

// Two calls over an unchanged tree are identical, whatever order the walk found
// things in. Scan promises the same thing for the same reason: a caller diffs
// this against the database.
func TestFindEpisodesTwiceIsIdentical(t *testing.T) {
	root := t.TempDir()
	mkFile(t, filepath.Join(root, "Season 02", "Show - S02E10.mkv"), 2048, 'a')
	mkFile(t, filepath.Join(root, "Season 02", "Show - S02E02.mkv"), 2048, 'b')
	mkFile(t, filepath.Join(root, "Season 01", "Show - S01E01.mkv"), 2048, 'c')

	first, second := findEpisodes(t, root), findEpisodes(t, root)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("two scans differ:\n%+v\n%+v", first, second)
	}
	if got, want := codes(first), []string{"S01E01", "S02E02", "S02E10"}; !slices.Equal(got, want) {
		t.Errorf("codes = %v, want %v — S02E10 sorts after S02E02, not by string", got, want)
	}
}

// The walk is one implementation, so the set of files it offers has to be the
// same set for both questions. This is the assertion that would fail first if
// someone forked it: FindFeature counts what qualifies (its pick plus Others),
// and every one of those files here is named as an episode.
func TestFindEpisodesQualifiesTheSameFilesAsFindFeature(t *testing.T) {
	root := t.TempDir()
	mkFile(t, filepath.Join(root, "Season 01", "Show - S01E01.mkv"), 4096, 'a')
	mkFile(t, filepath.Join(root, "Season 01", "Show - S01E02.mkv"), 8192, 'b')
	mkFile(t, filepath.Join(root, "Season 01", "sample", "Show - S01E03.mkv"), 8192, 'c')
	mkFile(t, filepath.Join(root, "Season 01", "Show - S01E04.mkv"), 64, 'd')

	feature, err := FindFeature(root, smallFloor)
	if err != nil {
		t.Fatalf("FindFeature: %v", err)
	}
	episodes := findEpisodes(t, root)
	if got, want := len(episodes), feature.Others+1; got != want {
		t.Fatalf("FindEpisodes returned %d files and FindFeature qualified %d; the walk is supposed to be shared", got, want)
	}
	// And the film picker's own answer — the largest — is among them.
	if !slices.ContainsFunc(episodes, func(e Episode) bool { return e.Path == feature.Path }) {
		t.Errorf("the feature %q is not one of the episodes %v", feature.Path, episodes)
	}
}

func TestFindEpisodesMissingRootErrorsWithContext(t *testing.T) {
	root := filepath.Join(t.TempDir(), "no-such-show")
	err := findEpisodesErr(t, root)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want it to wrap fs.ErrNotExist", err)
	}
	if !strings.Contains(err.Error(), "find episodes in "+root) {
		t.Errorf("err = %q, want it to carry the path as `find episodes in %s: ...`", err, root)
	}
}
