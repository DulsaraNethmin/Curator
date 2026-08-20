package library

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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

// --- ScanShows --------------------------------------------------------------

// tvFixtureRoot is the television half of testdata/library. Unlike the movie
// fixture it does not mirror a library that exists — D43 retired television on
// the Pi and the disk was emptied — so every folder in it is here to pin one
// behaviour, and tvShows/tvSkips below say which.
const tvFixtureRoot = "../../testdata/library/tv"

// tvFloor is how the committed fixture is scanned, and 1024 rather than 1 is
// load-bearing: "The Bear (2022)/the.bear.1x03.mkv" is 512 bytes and stands in
// for the 3-8 MB sample.mkv a release ships beside a 2 GB episode. At the
// PRODUCTION floor the whole fixture holds nothing, which is what
// TestScanShowsFixtureAtTheRealFloor asserts on purpose.
var tvFloor = FeatureOpts{MinBytes: 1024}

type showWant struct {
	title    string
	year     int
	episodes int
	size     int64
}

// The five folders that hold episodes, with the title exactly as it must come
// back — no hyphen turned into a colon, no colon invented, nothing collapsed.
var tvShows = map[string]showWant{
	// Two seasons, five episodes. S02E02E03 is one file filed under E02, and
	// poster.jpg is not a video: 4096+4096+2048+4096+4096.
	"Severance (2022)": {"Severance", 2022, 5, 18432},
	// One episode. sample/ and Extras/ each hold a file NAMED like an episode
	// and neither is walked, so a lost skip changes this count visibly.
	"Star Wars - Andor (2022)": {"Star Wars - Andor", 2022, 1, 2048},
	// 1x01 and S01E02 (an .mp4), and 1x03 at 512 bytes is under the floor.
	"The Bear (2022)": {"The Bear", 2022, 2, 4096},
	// A real hyphen and a substituted colon in one title.
	"X-Men - The Animated Series (1992)": {"X-Men - The Animated Series", 1992, 1, 2048},
	// Season 00: the specials folder is a season like any other.
	"Fleabag (2016)": {"Fleabag", 2016, 1, 2048},
}

// The three that are not shows, and whether each one REMOVES A ROW. NoMedia is
// the flag that does that (docs/decisions.md D33), so it is asserted per folder
// rather than counted.
var tvSkips = map[string]struct {
	noMedia bool
	reason  string
}{
	// Video that clears the floor, no code in the name: the bytes are here and
	// the NAME is the problem. Still not a show.
	"Chernobyl (2019)": {true, "video in it, but nothing named like an episode"},
	// Read fine, nothing in it.
	"The Leftovers (2014)": {true, "no episode in it"},
	// Not a library folder at all — and NOT NoMedia, because nothing was read.
	"Ted Lasso": {false, `does not match "Title (Year)"`},
}

func scanTVFixture(t *testing.T) ([]Show, []Skipped) {
	t.Helper()
	shows, skipped, err := ScanShows(tvFixtureRoot, tvFloor)
	if err != nil {
		t.Fatalf("ScanShows(%q): %v", tvFixtureRoot, err)
	}
	return shows, skipped
}

// The sentence the next reader needs before they "fix" a count somewhere: at
// the production floor the committed fixture holds no episode, because its
// largest file is 4096 bytes and the floor is 50 MiB. That is not a broken
// fixture, it is the floor working — and it is why every other test here passes
// tvFloor.
func TestScanShowsFixtureAtTheRealFloor(t *testing.T) {
	shows, skipped, err := ScanShows(tvFixtureRoot, FeatureOpts{})
	if err != nil {
		t.Fatalf("ScanShows: %v", err)
	}
	if len(shows) != 0 {
		t.Errorf("shows = %+v, want none — nothing in the fixture clears %d bytes", shows, DefaultMinFeatureBytes)
	}
	if want := len(tvShows) + len(tvSkips); len(skipped) != want {
		t.Fatalf("skipped %d folders, want %d", len(skipped), want)
	}
	for _, s := range skipped {
		// Every folder but the unparseable one reads fine and holds nothing
		// that clears the floor, which is a positive finding and removes a row.
		if want := s.Name != "Ted Lasso"; s.NoMedia != want {
			t.Errorf("%q: NoMedia = %v, want %v", s.Name, s.NoMedia, want)
		}
	}
}

func TestScanShowsFixture(t *testing.T) {
	shows, skipped := scanTVFixture(t)

	if len(shows) != len(tvShows) {
		t.Fatalf("scanned %d shows, want %d: %+v", len(shows), len(tvShows), shows)
	}
	if len(skipped) != len(tvSkips) {
		t.Fatalf("skipped %d folders, want %d: %+v", len(skipped), len(tvSkips), skipped)
	}

	// Every folder is accounted for exactly once across both slices. Nothing
	// may vanish from a scan without being named in one of them.
	seen := map[string]bool{}
	claim := func(base string) {
		t.Helper()
		if seen[base] {
			t.Errorf("folder %q returned twice", base)
		}
		seen[base] = true
	}

	for _, s := range shows {
		base := filepath.Base(s.LibraryPath)
		claim(base)

		want, ok := tvShows[base]
		if !ok {
			t.Errorf("unexpected show %q", base)
			continue
		}
		if s.Title != want.title {
			t.Errorf("%q: title = %q, want %q", base, s.Title, want.title)
		}
		if s.Year != want.year {
			t.Errorf("%q: year = %d, want %d", base, s.Year, want.year)
		}
		if s.Episodes != want.episodes {
			t.Errorf("%q: episodes = %d, want %d", base, s.Episodes, want.episodes)
		}
		if s.SizeBytes != want.size {
			t.Errorf("%q: size = %d, want %d", base, s.SizeBytes, want.size)
		}
		if s.Status != StatusImported {
			t.Errorf("%q: status = %q, want %q", base, s.Status, StatusImported)
		}
		if s.LibraryPath != filepath.Join(tvFixtureRoot, base) {
			t.Errorf("%q: library path = %q", base, s.LibraryPath)
		}
	}

	for _, s := range skipped {
		claim(s.Name)

		want, ok := tvSkips[s.Name]
		if !ok {
			t.Errorf("unexpected skip %q: %s", s.Name, s.Reason)
			continue
		}
		if s.NoMedia != want.noMedia {
			t.Errorf("%q: NoMedia = %v, want %v — that flag is what deletes a row", s.Name, s.NoMedia, want.noMedia)
		}
		if s.Reason != want.reason {
			t.Errorf("%q: reason = %q, want %q", s.Name, s.Reason, want.reason)
		}
	}

	if len(seen) != len(tvShows)+len(tvSkips) {
		t.Errorf("accounted for %d folders, want %d", len(seen), len(tvShows)+len(tvSkips))
	}
}

// The trap CLAUDE.md opens with, on the television side. A " - " in a folder
// name may be a colon that could not be written; a hyphen inside a word is a
// hyphen. The scanner hands the title back exactly as it is on disk and lets
// internal/tmdb do the guessing.
func TestScanShowsKeepsHyphenatedTitlesVerbatim(t *testing.T) {
	shows, _ := scanTVFixture(t)

	titles := map[string]bool{}
	for _, s := range shows {
		titles[s.Title] = true
	}
	for _, want := range []string{"Star Wars - Andor", "X-Men - The Animated Series"} {
		if !titles[want] {
			t.Errorf("%q is not among the scanned titles %v", want, titles)
		}
	}
	for _, wrong := range []string{
		"Star Wars: Andor", "Star Wars Andor",
		"X-Men: The Animated Series", "X Men - The Animated Series", "X:Men - The Animated Series",
	} {
		if titles[wrong] {
			t.Errorf("title was rewritten to %q", wrong)
		}
	}
}

// The episodes of one fixture show, in full, because the counts above would
// still pass if the wrong five files were found.
func TestTheFixturesSeasonsAreReadInOrder(t *testing.T) {
	episodes, err := FindEpisodes(filepath.Join(tvFixtureRoot, "Severance (2022)"), tvFloor)
	if err != nil {
		t.Fatalf("FindEpisodes: %v", err)
	}
	want := []string{"S01E01", "S01E02", "S01E03", "S02E01", "S02E02"}
	if got := codes(episodes); !slices.Equal(got, want) {
		t.Fatalf("codes = %v, want %v — S02E02E03 is one file, filed under the first number", got, want)
	}
}

// Two scans of an unchanged library are identical, which is what lets a caller
// diff this against the database instead of reconciling it.
func TestScanShowsTwiceIsIdentical(t *testing.T) {
	firstShows, firstSkipped := scanTVFixture(t)
	secondShows, secondSkipped := scanTVFixture(t)

	if !reflect.DeepEqual(firstShows, secondShows) {
		t.Errorf("two scans disagree about the shows:\n%+v\n%+v", firstShows, secondShows)
	}
	if !reflect.DeepEqual(firstSkipped, secondSkipped) {
		t.Errorf("two scans disagree about the skips:\n%+v\n%+v", firstSkipped, secondSkipped)
	}
}

// A repack beside the original is two files and one episode. The bytes are both
// files' — that is what the disk holds — and the count is not, because a season
// that reported one more episode than it has is a season the UI calls complete.
func TestScanShowsCountsDistinctEpisodesAndSumsEveryFile(t *testing.T) {
	root := t.TempDir()
	show := mkdir(t, root, "Severance (2022)")
	mkFile(t, filepath.Join(show, "Season 01", "Severance (2022) - S01E01.mkv"), 2048, 'a')
	mkFile(t, filepath.Join(show, "Season 01", "Severance (2022) - S01E01.REPACK.mkv"), 4096, 'b')
	mkFile(t, filepath.Join(show, "Season 01", "Severance (2022) - S01E02.mkv"), 1024, 'c')

	shows, _, err := ScanShows(root, tvFloor)
	if err != nil {
		t.Fatalf("ScanShows: %v", err)
	}
	if len(shows) != 1 {
		t.Fatalf("shows = %+v, want one", shows)
	}
	if shows[0].Episodes != 2 {
		t.Errorf("episodes = %d, want 2 — the repack is the same episode", shows[0].Episodes)
	}
	if shows[0].SizeBytes != 7168 {
		t.Errorf("size = %d, want 7168 — every file on disk counts toward the bytes", shows[0].SizeBytes)
	}
}

// An unreadable folder is "could not tell", never "no episode". Only a positive
// finding removes a row, and this is the branch that decides which one a folder
// gets — a directory that cannot be read is what a failing disk looks like.
func TestScanShowsUnreadableFolderIsSkippedNotFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not stop root reading the directory")
	}

	root := t.TempDir()
	good := mkdir(t, root, "Severance (2022)")
	mkFile(t, filepath.Join(good, "Severance (2022) - S01E01.mkv"), 2048, 'a')
	bad := mkdir(t, root, "The Bear (2022)")
	if err := os.Chmod(bad, 0o000); err != nil {
		t.Skipf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o755) })

	shows, skipped, err := ScanShows(root, tvFloor)
	if err != nil {
		t.Fatalf("ScanShows: %v — one unreadable folder must not abandon the others", err)
	}
	if len(shows) != 1 || shows[0].Title != "Severance" {
		t.Fatalf("shows = %+v, want Severance (2022)", shows)
	}
	if len(skipped) != 1 {
		t.Fatalf("skipped = %+v, want 1", skipped)
	}
	if skipped[0].NoMedia {
		t.Error("NoMedia = true for a folder that could not be read; that is the flag that removes a row")
	}
	if skipped[0].Path != bad {
		t.Errorf("path = %q, want %q", skipped[0].Path, bad)
	}
}

// A dangling symlink where a show folder should be is the same answer: a fact
// about the disk, not a season that is missing.
func TestScanShowsDanglingSymlinkIsNotNoMedia(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone"), filepath.Join(root, "Severance (2022)")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	shows, skipped, err := ScanShows(root, tvFloor)
	if err != nil {
		t.Fatalf("ScanShows: %v", err)
	}
	if len(shows) != 0 || len(skipped) != 1 {
		t.Fatalf("shows = %+v, skipped = %+v, want no show and one skip", shows, skipped)
	}
	if skipped[0].NoMedia {
		t.Error("NoMedia = true for a dangling symlink; nothing was read, so nothing is known")
	}
}

// A symlinked show folder is followed, because a library assembled out of links
// to other disks still has to scan. This is the pair to the test above and to
// Scan's own.
func TestScanShowsFollowsSymlinkedFolders(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	real := mkdir(t, elsewhere, "Fleabag (2016)")
	mkFile(t, filepath.Join(real, "Season 01", "Fleabag (2016) - S01E01.mkv"), 2048, 'a')

	if err := os.Symlink(real, filepath.Join(root, "Fleabag (2016)")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	shows, _, err := ScanShows(root, tvFloor)
	if err != nil {
		t.Fatalf("ScanShows: %v", err)
	}
	if len(shows) != 1 || shows[0].Title != "Fleabag" || shows[0].Episodes != 1 {
		t.Fatalf("shows = %+v, want one Fleabag with one episode", shows)
	}
}

func TestScanShowsSkipsHiddenAndNonDirectories(t *testing.T) {
	root := t.TempDir()
	hidden := mkdir(t, root, ".Severance (2022)")
	mkFile(t, filepath.Join(hidden, "Severance (2022) - S01E01.mkv"), 2048, 'a')
	writeFile(t, filepath.Join(root, ".DS_Store"), 64)
	writeFile(t, filepath.Join(root, "Severance (2022) - S01E01.mkv"), 2048)

	shows, skipped, err := ScanShows(root, tvFloor)
	if err != nil {
		t.Fatalf("ScanShows: %v", err)
	}
	if len(shows) != 0 || len(skipped) != 0 {
		t.Fatalf("shows = %+v, skipped = %+v, want neither — noise is not a library folder", shows, skipped)
	}
}

func TestScanShowsEmptyLibrary(t *testing.T) {
	shows, skipped, err := ScanShows(t.TempDir(), FeatureOpts{})
	if err != nil {
		t.Fatalf("ScanShows: %v", err)
	}
	if len(shows) != 0 || len(skipped) != 0 {
		t.Fatalf("shows = %+v, skipped = %+v, want none of either", shows, skipped)
	}
}

// A missing root is FATAL, and that asymmetry with the per-folder skips is the
// guard behind the whole prune: an unmounted library reads as zero folders, and
// a caller that pruned on that would delete the row for every show on it. This
// is the television half of D33's safety argument.
func TestScanShowsMissingRootErrorsWithContext(t *testing.T) {
	root := filepath.Join(t.TempDir(), "no-such-library")
	shows, skipped, err := ScanShows(root, FeatureOpts{})
	if err == nil {
		t.Fatalf("ScanShows(%q) = %+v / %+v, want an error", root, shows, skipped)
	}
	if shows != nil || skipped != nil {
		t.Errorf("shows = %+v, skipped = %+v, want both nil on the error path", shows, skipped)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want it to wrap fs.ErrNotExist", err)
	}
	if !strings.Contains(err.Error(), "scan "+root) {
		t.Errorf("err = %q, want it to carry the path as `scan %s: ...`", err, root)
	}
}
