package library

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixtureRoot is the 29-folder stand-in for the real library on the Pi. It is task
// T7's, read-only from here.
const fixtureRoot = "../../testdata/library/movies"

// tinyFloor is how the committed fixture is scanned.
//
// The fixture's videos are 4096, 2048 and 512 bytes — real files in git, where a
// 50 MiB blob does not belong (docs/phase-4.md, and T17 says the same). At the
// PRODUCTION floor the whole fixture holds no film at all, which is what
// TestScanFixtureAtTheRealFloor asserts on purpose; every other test over it lowers
// the floor rather than pretending the files are bigger.
//
// The real 50 MiB floor is exercised further down, over SPARSE files in
// t.TempDir(), which is the shape internal/api/stream_test.go already uses.
var tinyFloor = FeatureOpts{MinBytes: 1}

type want struct {
	title string
	year  int
}

// Every folder in the fixture, with the title exactly as it must come back — no
// hyphen turned into a colon, no colon invented, nothing collapsed.
var fixture = map[string]want{
	"Avengers - Infinity War (2018)":              {"Avengers - Infinity War", 2018},
	"Babylon (2022)":                              {"Babylon", 2022},
	"Backrooms (2026)":                            {"Backrooms", 2026},
	"Captain America - The First Avenger (2011)":  {"Captain America - The First Avenger", 2011},
	"Crime 101 (2026)":                            {"Crime 101", 2026},
	"Deadpool (2016)":                             {"Deadpool", 2016},
	"Deadpool & Wolverine (2024)":                 {"Deadpool & Wolverine", 2024},
	"F1 (2025)":                                   {"F1", 2025},
	"Gladiator (2000)":                            {"Gladiator", 2000},
	"Gone Girl (2014)":                            {"Gone Girl", 2014},
	"In the Grey (2026)":                          {"In the Grey", 2026},
	"Interstellar (2014)":                         {"Interstellar", 2014},
	"Iron Man (2008)":                             {"Iron Man", 2008},
	"Iron Man 2 (2010)":                           {"Iron Man 2", 2010},
	"Jaws (1975)":                                 {"Jaws", 1975},
	"Man of Steel (2013)":                         {"Man of Steel", 2013},
	"Michael (2026)":                              {"Michael", 2026},
	"Mortal Kombat II (2026)":                     {"Mortal Kombat II", 2026},
	"Predator - Badlands (2025)":                  {"Predator - Badlands", 2025},
	"Pulp Fiction (1994)":                         {"Pulp Fiction", 1994},
	"Spider-Man - Across the Spider-Verse (2023)": {"Spider-Man - Across the Spider-Verse", 2023},
	"Spider-Man - Into the Spider-Verse (2018)":   {"Spider-Man - Into the Spider-Verse", 2018},
	"Spider-Man - No Way Home (2021)":             {"Spider-Man - No Way Home", 2021},
	"Supergirl (2026)":                            {"Supergirl", 2026},
	"The Avengers (2012)":                         {"The Avengers", 2012},
	"The Martian (2015)":                          {"The Martian", 2015},
	"Tom Clancy's Jack Ryan - Ghost War (2026)":   {"Tom Clancy's Jack Ryan - Ghost War", 2026},
	"Troy (2004)":                                 {"Troy", 2004},
	"X-Men Origins - Wolverine (2009)":            {"X-Men Origins - Wolverine", 2009},
}

// withFilm are the only two fixture folders that hold a video, and what the
// scanner must report for each. Interstellar also holds a 512 B sample.mkv and a
// 128 B poster.jpg: the feature wins, not the sample and not the 4736 B total.
var withFilm = map[string]int64{
	"Interstellar (2014)":             4096,
	"Spider-Man - No Way Home (2021)": 2048,
}

func scanFixture(t *testing.T) ([]Movie, []Skipped) {
	t.Helper()
	movies, skipped, err := ScanWith(fixtureRoot, tinyFloor)
	if err != nil {
		t.Fatalf("ScanWith(%q): %v", fixtureRoot, err)
	}
	return movies, skipped
}

// TestScanFixtureAtTheRealFloor is the sentence the next reader needs before they
// "fix" a count somewhere. At the production floor the committed fixture holds no
// film: its largest video is 4096 bytes and the floor is 50 MiB. That is not a
// broken fixture, it is the floor working — and it is why every other test here
// passes tinyFloor.
func TestScanFixtureAtTheRealFloor(t *testing.T) {
	movies, skipped, err := Scan(fixtureRoot)
	if err != nil {
		t.Fatalf("Scan(%q): %v", fixtureRoot, err)
	}
	if len(movies) != 0 {
		t.Errorf("movies = %+v, want none — nothing in the fixture clears %d bytes",
			movies, DefaultMinFeatureBytes)
	}
	if len(skipped) != len(fixture) {
		t.Fatalf("skipped %d folders, want %d", len(skipped), len(fixture))
	}
	for _, s := range skipped {
		if !s.NoMedia {
			t.Errorf("%q: NoMedia = false, want true — the folder reads fine, there is just no film in it", s.Name)
		}
	}
}

func TestScanFixture(t *testing.T) {
	movies, skipped := scanFixture(t)

	if len(movies) != len(withFilm) {
		t.Fatalf("scanned %d films, want %d — only %v hold a video", len(movies), len(withFilm), keys(withFilm))
	}
	if len(skipped) != len(fixture)-len(withFilm) {
		t.Fatalf("skipped %d folders, want %d", len(skipped), len(fixture)-len(withFilm))
	}

	// Every folder is accounted for exactly once, across both slices. Nothing may
	// vanish from a scan without being named in one of them.
	seen := make(map[string]bool, len(fixture))
	claim := func(base string) {
		t.Helper()
		if _, ok := fixture[base]; !ok {
			t.Errorf("unexpected folder %q", base)
			return
		}
		if seen[base] {
			t.Errorf("folder %q returned twice", base)
		}
		seen[base] = true
	}

	for _, m := range movies {
		base := filepath.Base(m.LibraryPath)
		claim(base)

		w := fixture[base]
		if m.Title != w.title {
			t.Errorf("%q: title = %q, want %q", base, m.Title, w.title)
		}
		if m.Year != w.year {
			t.Errorf("%q: year = %d, want %d", base, m.Year, w.year)
		}
		if m.Status != StatusImported {
			t.Errorf("%q: status = %q, want %q", base, m.Status, StatusImported)
		}
		if got, want := m.LibraryPath, filepath.Join(fixtureRoot, base); got != want {
			t.Errorf("%q: library path = %q, want %q", base, got, want)
		}
	}

	for _, s := range skipped {
		claim(s.Name)
		if !s.NoMedia {
			t.Errorf("%q: NoMedia = false, want true", s.Name)
		}
		// Path is what the caller joins database rows on, so it has to be the
		// same string LibraryPath would have been.
		if got, want := s.Path, filepath.Join(fixtureRoot, s.Name); got != want {
			t.Errorf("%q: path = %q, want %q", s.Name, got, want)
		}
		if s.Reason == "" {
			t.Errorf("%q: no reason", s.Name)
		}
	}

	for name := range fixture {
		if !seen[name] {
			t.Errorf("folder %q missing from the scan entirely", name)
		}
	}
}

// The one that matters. 8 of 29 titles contain " - " where a colon was illegal in a
// filename, but Spider-Man and X-Men contain real hyphens and
// "Spider-Man - No Way Home" contains one of each. The scanner returns what is on
// disk; see docs/decisions.md D9.
//
// Only two folders hold a film now, so this asserts over the NAMES from both
// slices — every folder comes back byte-for-byte whether or not there is a film in
// it. ParseFolder's own handling of all 8 is TestParseFolder's, over the same
// strings.
func TestScanKeepsHyphenatedTitlesVerbatim(t *testing.T) {
	movies, skipped := scanFixture(t)

	hyphenated := []string{
		"Avengers - Infinity War (2018)",
		"Captain America - The First Avenger (2011)",
		"Predator - Badlands (2025)",
		"Spider-Man - Across the Spider-Verse (2023)",
		"Spider-Man - Into the Spider-Verse (2018)",
		"Spider-Man - No Way Home (2021)",
		"Tom Clancy's Jack Ryan - Ghost War (2026)",
		"X-Men Origins - Wolverine (2009)",
	}
	if len(hyphenated) != 8 {
		t.Fatalf("test lists %d hyphenated folders, want 8", len(hyphenated))
	}

	names := map[string]bool{}
	for _, m := range movies {
		names[filepath.Base(m.LibraryPath)] = true
	}
	for _, s := range skipped {
		names[s.Name] = true
	}
	for _, folder := range hyphenated {
		if !names[folder] {
			t.Errorf("folder %q did not come back verbatim", folder)
		}
	}

	// Explicitly the two failure modes, on the one folder that holds both a real
	// hyphen and a substituted colon and is also scanned as a film.
	byPath := map[string]Movie{}
	for _, m := range movies {
		byPath[filepath.Base(m.LibraryPath)] = m
	}
	spidey := byPath["Spider-Man - No Way Home (2021)"].Title
	if spidey == "Spider Man - No Way Home" || spidey == "Spider Man: No Way Home" {
		t.Fatalf("hyphen was eaten: %q", spidey)
	}
	if spidey == "Spider-Man: No Way Home" {
		t.Fatalf(`" - " was rewritten to ":": %q`, spidey)
	}

	// And no scanned title anywhere may contain a colon: none of the folders do,
	// so a colon could only have been invented here.
	for _, m := range movies {
		if strings.Contains(m.Title, ":") {
			t.Errorf("title %q contains a colon; the scanner must not rewrite", m.Title)
		}
	}
}

// The 2026 releases parse like any other year — these are the ones where a
// confident-but-wrong TMDB match is plausible later, so the year must be right.
//
// Over a temp root rather than the fixture: all seven 2026 folders are empty in
// git, so none of them is a Movie any more and only a Movie carries a year.
func TestScanParses2026Releases(t *testing.T) {
	root := t.TempDir()
	var expected int
	for folder, w := range fixture {
		if w.year != 2026 {
			continue
		}
		expected++
		dir := mkdir(t, root, folder)
		writeFile(t, filepath.Join(dir, "feature.mkv"), 1024)
	}
	if expected != 7 {
		t.Fatalf("fixture holds %d 2026 releases, want 7", expected)
	}

	movies, _, err := ScanWith(root, tinyFloor)
	if err != nil {
		t.Fatalf("ScanWith: %v", err)
	}
	if len(movies) != expected {
		t.Fatalf("scanned %d, want %d", len(movies), expected)
	}
	for _, m := range movies {
		base := filepath.Base(m.LibraryPath)
		if m.Year != 2026 {
			t.Errorf("%q: year = %d, want 2026", base, m.Year)
		}
		if want := fixture[base].title; m.Title != want {
			t.Errorf("%q: title = %q, want %q", base, m.Title, want)
		}
	}
}

// The size is the FEATURE file's, chosen by the same picker the importer and the
// stream endpoint use — not the largest file, not the folder total, and not a
// sample.
func TestScanSizeIsTheFeatureFile(t *testing.T) {
	movies, skipped := scanFixture(t)

	sizes := make(map[string]int64, len(movies))
	for _, m := range movies {
		sizes[filepath.Base(m.LibraryPath)] = m.SizeBytes
	}
	for folder, want := range withFilm {
		if got := sizes[folder]; got != want {
			t.Errorf("%q size = %d, want %d", folder, got, want)
		}
	}

	// The 27 folders holding only a .gitkeep are SKIPS now, not zero-size movies.
	// That reversal is the whole task: a row saying `imported` for a folder with
	// no film in it is what drew a Play button for a film that is not there.
	if len(skipped) != 27 {
		t.Errorf("%d folders without a film, want 27", len(skipped))
	}
	for _, m := range movies {
		if m.SizeBytes == 0 {
			t.Errorf("%q: size 0 — the scanner must not return a movie with no file", m.LibraryPath)
		}
	}
}

// The property the whole change buys: a Movie the scanner returns is a film that
// can actually be played, and the picker that will be asked to serve it agrees
// about which file that is.
func TestScanAgreesWithFindFeature(t *testing.T) {
	for _, opts := range []FeatureOpts{tinyFloor, {}} {
		movies, _, err := ScanWith(fixtureRoot, opts)
		if err != nil {
			t.Fatalf("ScanWith(%+v): %v", opts, err)
		}
		for _, m := range movies {
			if m.SizeBytes < opts.minBytes() {
				t.Errorf("%q: size %d is under the floor of %d", m.LibraryPath, m.SizeBytes, opts.minBytes())
			}
			feature, err := FindFeature(m.LibraryPath, opts)
			if err != nil {
				t.Errorf("%q: scanned as a movie but FindFeature says %v", m.LibraryPath, err)
				continue
			}
			if feature.Size != m.SizeBytes {
				t.Errorf("%q: scan says %d, FindFeature says %d — two answers to which file is the film",
					m.LibraryPath, m.SizeBytes, feature.Size)
			}
		}
	}
}

func TestScanTwiceIsIdentical(t *testing.T) {
	firstMovies, firstSkipped, err := ScanWith(fixtureRoot, tinyFloor)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	secondMovies, secondSkipped, err := ScanWith(fixtureRoot, tinyFloor)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if !reflect.DeepEqual(firstMovies, secondMovies) {
		t.Fatalf("rescan differs:\nfirst  = %+v\nsecond = %+v", firstMovies, secondMovies)
	}
	if !reflect.DeepEqual(firstSkipped, secondSkipped) {
		t.Fatalf("rescan skips differ:\nfirst  = %+v\nsecond = %+v", firstSkipped, secondSkipped)
	}
}

func TestScanSkipsHiddenAndNonDirectories(t *testing.T) {
	root := t.TempDir()
	dir := mkdir(t, root, "Gladiator (2000)")
	writeFile(t, filepath.Join(dir, "Gladiator (2000).mkv"), 1024)
	mkdir(t, root, ".hidden movie (2001)") // hidden directory
	mkdir(t, root, ".Trashes")
	writeFile(t, filepath.Join(root, ".DS_Store"), 6148) // macOS puts it everywhere
	writeFile(t, filepath.Join(root, "notes.txt"), 10)
	writeFile(t, filepath.Join(root, "Loose Movie (1999).mkv"), 100)

	movies, skipped, err := ScanWith(root, tinyFloor)
	if err != nil {
		t.Fatalf("ScanWith: %v", err)
	}
	if len(movies) != 1 || movies[0].Title != "Gladiator" {
		t.Fatalf("movies = %+v, want only Gladiator (2000)", movies)
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %+v, want none — hidden entries and files are noise, not folders needing attention", skipped)
	}
}

func TestScanReportsUnparseableFolders(t *testing.T) {
	root := t.TempDir()
	good := mkdir(t, root, "Gladiator (2000)")
	writeFile(t, filepath.Join(good, "Gladiator (2000).mkv"), 1024)
	mkdir(t, root, "Some Movie")           // no year
	mkdir(t, root, "Another Movie [2011]") // wrong brackets

	movies, skipped, err := ScanWith(root, tinyFloor)
	if err != nil {
		t.Fatalf("ScanWith: %v", err)
	}
	if len(movies) != 1 {
		t.Fatalf("movies = %+v, want 1", movies)
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped = %+v, want 2", skipped)
	}
	for _, s := range skipped {
		if s.Name != "Some Movie" && s.Name != "Another Movie [2011]" {
			t.Errorf("unexpected skip %+v", s)
		}
		if s.Reason == "" {
			t.Errorf("skip %q has no reason", s.Name)
		}
		// A name that no longer parses says nothing about what is inside the
		// folder, so this skip must never be the one that removes a row.
		if s.NoMedia {
			t.Errorf("skip %q: NoMedia = true, want false — the name is the problem, not the contents", s.Name)
		}
		if got, want := s.Path, filepath.Join(root, s.Name); got != want {
			t.Errorf("skip %q: path = %q, want %q", s.Name, got, want)
		}
	}
}

// This REPLACES TestScanDoesNotRecurse, and the reversal is deliberate.
//
// The scanner used to be flat because a wrong answer only meant a wrong size.
// Now it decides whether a row survives a scan, so it has to look exactly where
// the stream endpoint looks: a film one level down streams, and a scanner that
// missed it would prune the row for a film that plays (docs/decisions.md D33).
func TestScanFindsAFeatureOneLevelDown(t *testing.T) {
	root := t.TempDir()
	dir := mkdir(t, root, "Gladiator (2000)")
	nested := mkdir(t, dir, "Gladiator.2000.1080p-GROUP")
	writeFile(t, filepath.Join(nested, "gladiator.mkv"), 4096)

	movies, skipped, err := ScanWith(root, tinyFloor)
	if err != nil {
		t.Fatalf("ScanWith: %v", err)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped = %+v, want none", skipped)
	}
	if len(movies) != 1 {
		t.Fatalf("movies = %+v, want 1", movies)
	}
	if movies[0].SizeBytes != 4096 {
		t.Errorf("size = %d, want 4096 — the film is one level down and must still be found", movies[0].SizeBytes)
	}

	// The skip list still applies at every depth: a release folder named for one
	// of them holds something other than the film.
	other := t.TempDir()
	extras := mkdir(t, mkdir(t, other, "Gladiator (2000)"), "Extras")
	writeFile(t, filepath.Join(extras, "behind the scenes.mkv"), 99999)

	movies, skipped, err = ScanWith(other, tinyFloor)
	if err != nil {
		t.Fatalf("ScanWith: %v", err)
	}
	if len(movies) != 0 || len(skipped) != 1 || !skipped[0].NoMedia {
		t.Fatalf("movies = %+v, skipped = %+v, want no movie and one NoMedia skip", movies, skipped)
	}
}

func TestScanSizeIgnoresNonVideoHiddenAndCase(t *testing.T) {
	root := t.TempDir()
	dir := mkdir(t, root, "Troy (2004)")
	writeFile(t, filepath.Join(dir, "Troy (2004).MKV"), 3000) // extension case folds
	writeFile(t, filepath.Join(dir, "sample.mp4"), 500)
	writeFile(t, filepath.Join(dir, "fanart.jpg"), 9000)        // not a video
	writeFile(t, filepath.Join(dir, "Troy (2004).srt"), 8000)   // not a video
	writeFile(t, filepath.Join(dir, "._Troy (2004).mkv"), 7000) // macOS AppleDouble fork
	writeFile(t, filepath.Join(dir, "Troy (2004).nfo"), 6000)   // not a video

	movies, _, err := ScanWith(root, tinyFloor)
	if err != nil {
		t.Fatalf("ScanWith: %v", err)
	}
	if len(movies) != 1 {
		t.Fatalf("movies = %+v, want 1", movies)
	}
	if movies[0].SizeBytes != 3000 {
		t.Errorf("size = %d, want 3000", movies[0].SizeBytes)
	}

	for _, ext := range []string{".mkv", ".mp4", ".avi", ".m4v"} {
		other := t.TempDir()
		d := mkdir(t, other, "Troy (2004)")
		writeFile(t, filepath.Join(d, "feature"+ext), 42)
		got, _, err := ScanWith(other, tinyFloor)
		if err != nil {
			t.Fatalf("ScanWith(%s): %v", ext, err)
		}
		if len(got) != 1 || got[0].SizeBytes != 42 {
			t.Errorf("%s: movies = %+v, want one at 42 bytes", ext, got)
		}
	}
}

// The real 50 MiB floor, over sparse files so it costs no disk and no time — the
// shape internal/api/stream_test.go already uses for the same reason. This is the
// case the floor exists for: a release group's sample.mkv sitting beside the film,
// and a folder where the sample is all that arrived.
func TestScanAtTheRealFloor(t *testing.T) {
	const feature = 60 << 20 // clears DefaultMinFeatureBytes
	const sample = 6 << 20   // what a release group's sample.mkv actually weighs

	root := t.TempDir()
	both := mkdir(t, root, "Gladiator (2000)")
	writeSparse(t, filepath.Join(both, "Gladiator (2000).mkv"), feature)
	writeSparse(t, filepath.Join(both, "sample.mkv"), sample)

	onlySample := mkdir(t, root, "Troy (2004)")
	writeSparse(t, filepath.Join(onlySample, "sample.mkv"), sample)

	inExtras := mkdir(t, root, "Jaws (1975)")
	writeSparse(t, filepath.Join(mkdir(t, inExtras, "Extras"), "featurette.mkv"), feature)

	movies, skipped, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(movies) != 1 || movies[0].Title != "Gladiator" || movies[0].SizeBytes != feature {
		t.Fatalf("movies = %+v, want only Gladiator (2000) at %d bytes", movies, feature)
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped = %+v, want 2", skipped)
	}
	for _, s := range skipped {
		if !s.NoMedia {
			t.Errorf("%q: NoMedia = false — a folder whose only video is under the floor holds no film", s.Name)
		}
	}
}

// An unreadable folder is "could not tell", never "no film". Only a positive
// finding removes a row, and this is the branch that decides which one a folder
// gets — a directory that cannot be read is what a failing disk looks like.
func TestScanUnreadableFolderIsSkippedNotFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not stop root reading the directory")
	}

	root := t.TempDir()
	good := mkdir(t, root, "Gladiator (2000)")
	writeFile(t, filepath.Join(good, "Gladiator (2000).mkv"), 1024)
	bad := mkdir(t, root, "Troy (2004)")
	if err := os.Chmod(bad, 0o000); err != nil {
		t.Skipf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o755) })

	movies, skipped, err := ScanWith(root, tinyFloor)
	if err != nil {
		t.Fatalf("ScanWith: %v — one unreadable folder must not abandon the others", err)
	}
	if len(movies) != 1 || movies[0].Title != "Gladiator" {
		t.Fatalf("movies = %+v, want Gladiator (2000) — the folder beside the broken one still scans", movies)
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

// A dangling symlink where a movie folder should be is the same answer: a fact
// about the disk, not a film that is missing.
func TestScanDanglingSymlinkIsNotNoMedia(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone"), filepath.Join(root, "Jaws (1975)")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	movies, skipped, err := ScanWith(root, tinyFloor)
	if err != nil {
		t.Fatalf("ScanWith: %v", err)
	}
	if len(movies) != 0 || len(skipped) != 1 {
		t.Fatalf("movies = %+v, skipped = %+v, want no movie and one skip", movies, skipped)
	}
	if skipped[0].NoMedia {
		t.Error("NoMedia = true for a dangling symlink; nothing was read, so nothing is known")
	}
}

func TestScanEmptyLibrary(t *testing.T) {
	movies, skipped, err := Scan(t.TempDir())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(movies) != 0 || len(skipped) != 0 {
		t.Fatalf("movies = %+v, skipped = %+v, want none of either", movies, skipped)
	}
}

func TestScanFollowsSymlinkedFolders(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	real := mkdir(t, elsewhere, "Jaws (1975)")
	writeFile(t, filepath.Join(real, "Jaws (1975).mkv"), 777)

	if err := os.Symlink(real, filepath.Join(root, "Jaws (1975)")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	movies, _, err := ScanWith(root, tinyFloor)
	if err != nil {
		t.Fatalf("ScanWith: %v", err)
	}
	if len(movies) != 1 || movies[0].Title != "Jaws" || movies[0].SizeBytes != 777 {
		t.Fatalf("movies = %+v, want Jaws (1975) at 777 bytes", movies)
	}
}

// A symlinked FEATURE inside a library folder is not a film, and that changed
// with this task: largestVideo lstat'd it and recorded the link's own few bytes,
// while the shared picker skips every non-regular entry on an argument written
// for torrent folders. Named here so it is a decision with a test rather than a
// row that quietly disappears (docs/decisions.md D33).
func TestScanIgnoresASymlinkedFeatureFile(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	target := filepath.Join(elsewhere, "somewhere else.mkv")
	writeFile(t, target, 4096)

	dir := mkdir(t, root, "Jaws (1975)")
	if err := os.Symlink(target, filepath.Join(dir, "Jaws (1975).mkv")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	movies, skipped, err := ScanWith(root, tinyFloor)
	if err != nil {
		t.Fatalf("ScanWith: %v", err)
	}
	if len(movies) != 0 {
		t.Fatalf("movies = %+v, want none", movies)
	}
	if len(skipped) != 1 || !skipped[0].NoMedia {
		t.Fatalf("skipped = %+v, want one NoMedia skip", skipped)
	}
}

// A missing root is fatal, and that asymmetry with the per-folder skips above is
// the guard behind the whole prune: an unmounted library reads as zero folders,
// and a caller that pruned on that would delete the row for every film on it.
func TestScanMissingRootErrorsWithContext(t *testing.T) {
	root := filepath.Join(t.TempDir(), "no-such-library")
	movies, skipped, err := Scan(root)
	if err == nil {
		t.Fatalf("Scan(%q) = %+v / %+v, want an error", root, movies, skipped)
	}
	if movies != nil || skipped != nil {
		t.Errorf("movies = %+v, skipped = %+v, want both nil on the error path", movies, skipped)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want it to wrap fs.ErrNotExist", err)
	}
	if !strings.Contains(err.Error(), "scan "+root) {
		t.Errorf("err = %q, want it to carry the path as `scan %s: ...`", err, root)
	}
}

func keys(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mkdir(t *testing.T, parent, name string) string {
	t.Helper()
	path := filepath.Join(parent, name)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	return path
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// writeSparse makes a file that reports size bytes and occupies none of them, so
// a test can meet the real 50 MiB floor without writing 50 MiB.
func writeSparse(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		t.Fatalf("truncate %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}
