package library

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// --- naming -----------------------------------------------------------------

// The full path curator writes for one episode, assembled from the three
// functions the way a caller does it. This is the layout claim in one
// assertion: Jellyfin, Plex and Kodi all read this tree without curator.
func TestTheEpisodePathIsJellyfinsLayout(t *testing.T) {
	folder, err := ShowFolder("Severance", 2022)
	if err != nil {
		t.Fatalf("ShowFolder: %v", err)
	}
	name, err := EpisodeName("Severance", 2022, 1, 1, ".mkv")
	if err != nil {
		t.Fatalf("EpisodeName: %v", err)
	}

	got := filepath.Join(folder, SeasonFolder(1), name)
	want := filepath.Join("Severance (2022)", "Season 01", "Severance (2022) - S01E01.mkv")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

// D9 reaches television through destTitle, which ShowFolder gets by calling
// DestFolder rather than by carrying a second copy of the rule. The colon is
// spelled " - " and a REAL hyphen is left alone — the half of D9 that a naive
// "- means :" rewrite gets wrong in the other direction, and the reason the
// library's own "Spider-Man - No Way Home (2021)" survives a round trip.
func TestShowFolderSpellsTheColonAndKeepsRealHyphens(t *testing.T) {
	cases := []struct {
		title string
		year  int
		want  string
	}{
		{"Star Wars: Andor", 2022, "Star Wars - Andor (2022)"},
		{"Marvel's Daredevil: Born Again", 2025, "Marvel's Daredevil - Born Again (2025)"},
		// The traps CLAUDE.md names. Neither hyphen is a colon and neither may
		// be touched.
		{"Spider-Man", 2002, "Spider-Man (2002)"},
		{"X-Men: The Animated Series", 1992, "X-Men - The Animated Series (1992)"},
		{"Spider-Man: No Way Home", 2021, "Spider-Man - No Way Home (2021)"},
		// Already spelled the library's way: idempotent, not doubled.
		{"Star Wars - Andor", 2022, "Star Wars - Andor (2022)"},
		{"  Severance  ", 2022, "Severance (2022)"},
	}

	for _, c := range cases {
		got, err := ShowFolder(c.title, c.year)
		if err != nil {
			t.Errorf("ShowFolder(%q, %d): %v", c.title, c.year, err)
			continue
		}
		if got != c.want {
			t.Errorf("ShowFolder(%q, %d) = %q, want %q", c.title, c.year, got, c.want)
		}
	}
}

// A show's title reaches curator from a client exactly as a film's does, and
// this is the first code that turns it into a path. The refusals are
// DestFolder's, reached through ShowFolder — which is the point of it being a
// call rather than a copy.
func TestShowFolderAndEpisodeNameRefuseATitleThatIsNotAName(t *testing.T) {
	for what, title := range map[string]string{
		"a slash":         "Shows/Evil",
		"a traversal":     "../../etc/cron.d",
		"a backslash":     `Shows\Evil`,
		"a NUL":           "Show\x00.mkv",
		"a dot":           ".",
		"a double dot":    "..",
		"empty":           "",
		"only whitespace": "   ",
		"only a colon":    ":",
	} {
		t.Run(what, func(t *testing.T) {
			if got, err := ShowFolder(title, 2022); err == nil {
				t.Errorf("ShowFolder(%q) = %q, want an error", title, got)
			} else if !errors.Is(err, ErrBadTitle) {
				t.Errorf("err = %v, want ErrBadTitle", err)
			}
			if got, err := EpisodeName(title, 2022, 1, 1, ".mkv"); err == nil {
				t.Errorf("EpisodeName(%q) = %q, want an error", title, got)
			}
		})
	}

	// And the year rule, which is ParseFolder's: a folder curator writes must
	// be one it can read back.
	for _, year := range []int{-1, 0, 1, 999, 10000} {
		if got, err := ShowFolder("Severance", year); err == nil {
			t.Errorf("ShowFolder(_, %d) = %q, want an error", year, got)
		}
	}
}

func TestSeasonFolderPadsToTwoDigits(t *testing.T) {
	cases := map[int]string{
		0:   "Season 00", // specials, and a real folder rather than a degenerate one
		1:   "Season 01",
		9:   "Season 09",
		10:  "Season 10",
		99:  "Season 99",
		100: "Season 100", // the padding is a minimum width, not a truncation
	}
	for season, want := range cases {
		if got := SeasonFolder(season); got != want {
			t.Errorf("SeasonFolder(%d) = %q, want %q", season, got, want)
		}
	}
}

func TestEpisodeName(t *testing.T) {
	got, err := EpisodeName("Severance", 2022, 2, 10, ".mkv")
	if err != nil {
		t.Fatalf("EpisodeName: %v", err)
	}
	if want := "Severance (2022) - S02E10.mkv"; got != want {
		t.Errorf("EpisodeName = %q, want %q", got, want)
	}

	// A source spelled .MKV must not produce a library entry spelled unlike
	// every other one.
	if got, err := EpisodeName("Severance", 2022, 2, 10, ".MKV"); err != nil || got != "Severance (2022) - S02E10.mkv" {
		t.Errorf("EpisodeName(.MKV) = %q, %v", got, err)
	}

	for _, ext := range []string{"", ".", ".srt", ".nfo", "mkv"} {
		if got, err := EpisodeName("Severance", 2022, 1, 1, ext); err == nil {
			t.Errorf("EpisodeName(_, _, _, _, %q) = %q, want an error", ext, got)
		}
	}

	// A negative number cannot come out of ParseEpisode, so it came from a bug
	// or from outside. Refused rather than rendered into a folder name.
	for _, c := range []struct{ season, episode int }{{-1, 1}, {1, -1}, {-1, -1}} {
		if got, err := EpisodeName("Severance", 2022, c.season, c.episode, ".mkv"); err == nil {
			t.Errorf("EpisodeName(_, _, %d, %d, _) = %q, want an error", c.season, c.episode, got)
		} else if !errors.Is(err, ErrBadTitle) {
			t.Errorf("err = %v, want ErrBadTitle", err)
		}
	}
}

// The round trip that keeps the library readable by the thing that wrote it. A
// name curator writes and then cannot parse is a row that vanishes on the next
// scan, so both halves are asserted together: the folder through ParseFolder,
// the file through ParseEpisode.
func TestEpisodeNameRoundTripsBackThroughBothParsers(t *testing.T) {
	cases := []struct {
		title           string
		year            int
		season, episode int
		wantTitle       string
	}{
		{"Severance", 2022, 1, 1, "Severance"},
		{"Star Wars: Andor", 2022, 2, 12, "Star Wars - Andor"},
		{"X-Men: The Animated Series", 1992, 5, 9, "X-Men - The Animated Series"},
		{"Spider-Man", 2002, 0, 1, "Spider-Man"},
		{"Tom Clancy's Jack Ryan", 2018, 3, 7, "Tom Clancy's Jack Ryan"},
		{"9-1-1", 2018, 8, 18, "9-1-1"},
		{"Fleabag", 2016, 0, 1, "Fleabag"},
	}

	for _, c := range cases {
		folder, err := ShowFolder(c.title, c.year)
		if err != nil {
			t.Errorf("ShowFolder(%q, %d): %v", c.title, c.year, err)
			continue
		}
		title, year, ok := ParseFolder(folder)
		if !ok {
			t.Errorf("ParseFolder(%q) failed on a folder curator writes", folder)
			continue
		}
		if title != c.wantTitle || year != c.year {
			t.Errorf("ParseFolder(%q) = (%q, %d), want (%q, %d)", folder, title, year, c.wantTitle, c.year)
		}

		name, err := EpisodeName(c.title, c.year, c.season, c.episode, ".mkv")
		if err != nil {
			t.Errorf("EpisodeName(%q, ...): %v", c.title, err)
			continue
		}
		season, episode, ok := ParseEpisode(name)
		if !ok {
			t.Errorf("ParseEpisode(%q) failed on a name curator writes", name)
			continue
		}
		if season != c.season || episode != c.episode {
			t.Errorf("ParseEpisode(%q) = (%d, %d), want (%d, %d)", name, season, episode, c.season, c.episode)
		}
	}
}

// Every folder in the movie fixture is a legal SHOW folder too, because the two
// are the same shape. Running the show namer over the 29 real names is how the
// eight " - " titles, Spider-Man, X-Men Origins and Tom Clancy's Jack Ryan get
// met without anyone remembering that those are the dangerous ones.
func TestShowFolderRoundTripsEveryMovieFixtureFolder(t *testing.T) {
	entries, err := readLibraryDirs(fixtureRoot)
	if err != nil {
		t.Fatalf("read %s: %v", fixtureRoot, err)
	}

	checked := 0
	for _, name := range entries {
		title, year, ok := ParseFolder(name)
		if !ok {
			t.Errorf("ParseFolder(%q) did not parse; the fixture is all Title (Year)", name)
			continue
		}
		got, err := ShowFolder(title, year)
		if err != nil {
			t.Errorf("ShowFolder(%q, %d): %v", title, year, err)
			continue
		}
		if got != name {
			t.Errorf("round trip: %q -> ShowFolder(%q, %d) = %q, want the original name back", name, title, year, got)
		}
		checked++
	}

	if checked != 29 {
		t.Fatalf("round-tripped %d folders, want 29 — the movie fixture is T7's and must not have changed", checked)
	}
}

// readLibraryDirs lists the visible directories in a library root — the set
// both scanners consider, and nothing else.
func readLibraryDirs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && !isHidden(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}
