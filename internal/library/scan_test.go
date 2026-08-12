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

func scanFixture(t *testing.T) []Movie {
	t.Helper()
	movies, skipped, err := ScanWithSkipped(fixtureRoot)
	if err != nil {
		t.Fatalf("ScanWithSkipped(%q): %v", fixtureRoot, err)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped %v, want none — every fixture folder is Title (Year)", skipped)
	}
	return movies
}

func TestScanFixture(t *testing.T) {
	movies := scanFixture(t)

	if len(movies) != 29 {
		t.Fatalf("scanned %d folders, want 29", len(movies))
	}

	seen := make(map[string]bool, len(movies))
	for _, m := range movies {
		base := filepath.Base(m.LibraryPath)
		w, ok := fixture[base]
		if !ok {
			t.Errorf("unexpected folder %q", base)
			continue
		}
		if seen[base] {
			t.Errorf("folder %q returned twice", base)
		}
		seen[base] = true

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
	for name := range fixture {
		if !seen[name] {
			t.Errorf("folder %q missing from scan", name)
		}
	}
}

// The one that matters. 8 of 29 titles contain " - " where a colon was illegal in a
// filename, but Spider-Man and X-Men contain real hyphens and
// "Spider-Man - No Way Home" contains one of each. The scanner returns what is on
// disk; see docs/decisions.md D9.
func TestScanKeepsHyphenatedTitlesVerbatim(t *testing.T) {
	movies := scanFixture(t)

	byPath := make(map[string]Movie, len(movies))
	for _, m := range movies {
		byPath[filepath.Base(m.LibraryPath)] = m
	}

	hyphenated := []struct{ folder, title string }{
		{"Avengers - Infinity War (2018)", "Avengers - Infinity War"},
		{"Captain America - The First Avenger (2011)", "Captain America - The First Avenger"},
		{"Predator - Badlands (2025)", "Predator - Badlands"},
		{"Spider-Man - Across the Spider-Verse (2023)", "Spider-Man - Across the Spider-Verse"},
		{"Spider-Man - Into the Spider-Verse (2018)", "Spider-Man - Into the Spider-Verse"},
		{"Spider-Man - No Way Home (2021)", "Spider-Man - No Way Home"},
		{"Tom Clancy's Jack Ryan - Ghost War (2026)", "Tom Clancy's Jack Ryan - Ghost War"},
		{"X-Men Origins - Wolverine (2009)", "X-Men Origins - Wolverine"},
	}
	if len(hyphenated) != 8 {
		t.Fatalf("test lists %d hyphenated titles, want 8", len(hyphenated))
	}

	for _, h := range hyphenated {
		m, ok := byPath[h.folder]
		if !ok {
			t.Errorf("folder %q not scanned", h.folder)
			continue
		}
		if m.Title != h.title {
			t.Errorf("title = %q, want %q verbatim", m.Title, h.title)
		}
	}

	// Explicitly the two failure modes: a collapsed hyphen and a substituted colon.
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

	// Exactly the 8 known titles carry " - ".
	var got int
	for _, m := range movies {
		if strings.Contains(m.Title, " - ") {
			got++
		}
	}
	if got != len(hyphenated) {
		t.Errorf("%d titles contain \" - \", want %d", got, len(hyphenated))
	}
}

// The 2026 releases parse like any other year — these are the ones where a
// confident-but-wrong TMDB match is plausible later, so the year must be right.
func TestScanParses2026Releases(t *testing.T) {
	movies := scanFixture(t)

	byPath := make(map[string]Movie, len(movies))
	for _, m := range movies {
		byPath[filepath.Base(m.LibraryPath)] = m
	}

	for folder, w := range fixture {
		if w.year != 2026 {
			continue
		}
		m, ok := byPath[folder]
		if !ok {
			t.Errorf("folder %q not scanned", folder)
			continue
		}
		if m.Year != 2026 {
			t.Errorf("%q: year = %d, want 2026", folder, m.Year)
		}
	}
}

func TestScanSizeIsLargestVideoFile(t *testing.T) {
	movies := scanFixture(t)

	sizes := make(map[string]int64, len(movies))
	for _, m := range movies {
		sizes[filepath.Base(m.LibraryPath)] = m.SizeBytes
	}

	// Interstellar holds a 4096 B feature, a 512 B sample.mkv and a 128 B
	// poster.jpg. The feature wins: not the sample, and not the 4736 B total.
	if got := sizes["Interstellar (2014)"]; got != 4096 {
		t.Errorf("Interstellar (2014) size = %d, want 4096", got)
	}
	// A different container extension is still a video.
	if got := sizes["Spider-Man - No Way Home (2021)"]; got != 2048 {
		t.Errorf("Spider-Man - No Way Home (2021) size = %d, want 2048", got)
	}

	// Every other fixture folder holds only a .gitkeep: no video, size 0, no error.
	empty := 0
	for folder, size := range sizes {
		if folder == "Interstellar (2014)" || folder == "Spider-Man - No Way Home (2021)" {
			continue
		}
		if size != 0 {
			t.Errorf("%q: size = %d, want 0 (no video files)", folder, size)
		}
		empty++
	}
	if empty != 27 {
		t.Errorf("%d folders without video files, want 27", empty)
	}
}

func TestScanTwiceIsIdentical(t *testing.T) {
	first, err := Scan(fixtureRoot)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	second, err := Scan(fixtureRoot)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("rescan differs:\nfirst  = %+v\nsecond = %+v", first, second)
	}
}

func TestScanSkipsHiddenAndNonDirectories(t *testing.T) {
	root := t.TempDir()
	mkdir(t, root, "Gladiator (2000)")
	mkdir(t, root, ".hidden movie (2001)") // hidden directory
	mkdir(t, root, ".Trashes")
	writeFile(t, filepath.Join(root, ".DS_Store"), 6148) // macOS puts it everywhere
	writeFile(t, filepath.Join(root, "notes.txt"), 10)
	writeFile(t, filepath.Join(root, "Loose Movie (1999).mkv"), 100)

	movies, skipped, err := ScanWithSkipped(root)
	if err != nil {
		t.Fatalf("ScanWithSkipped: %v", err)
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
	mkdir(t, root, "Gladiator (2000)")
	mkdir(t, root, "Some Movie")           // no year
	mkdir(t, root, "Another Movie [2011]") // wrong brackets

	movies, skipped, err := ScanWithSkipped(root)
	if err != nil {
		t.Fatalf("ScanWithSkipped: %v", err)
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
	}

	// Scan drops them silently; only ScanWithSkipped surfaces them.
	plain, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(plain) != 1 {
		t.Fatalf("Scan returned %d movies, want 1", len(plain))
	}
}

func TestScanDoesNotRecurse(t *testing.T) {
	root := t.TempDir()
	dir := mkdir(t, root, "Gladiator (2000)")
	writeFile(t, filepath.Join(dir, "Gladiator (2000).mkv"), 1024)
	// A nested folder that both looks like a movie and holds a bigger file. The
	// library is flat: neither may show up.
	nested := mkdir(t, dir, "Extras (2000)")
	writeFile(t, filepath.Join(nested, "behind the scenes.mkv"), 99999)

	movies, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(movies) != 1 {
		t.Fatalf("movies = %+v, want 1", movies)
	}
	if movies[0].SizeBytes != 1024 {
		t.Errorf("size = %d, want 1024 (nested files must not count)", movies[0].SizeBytes)
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

	movies, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
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
		got, err := Scan(other)
		if err != nil {
			t.Fatalf("Scan(%s): %v", ext, err)
		}
		if got[0].SizeBytes != 42 {
			t.Errorf("%s: size = %d, want 42", ext, got[0].SizeBytes)
		}
	}
}

func TestScanEmptyLibrary(t *testing.T) {
	movies, err := Scan(t.TempDir())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(movies) != 0 {
		t.Fatalf("movies = %+v, want none", movies)
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

	movies, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(movies) != 1 || movies[0].Title != "Jaws" || movies[0].SizeBytes != 777 {
		t.Fatalf("movies = %+v, want Jaws (1975) at 777 bytes", movies)
	}
}

func TestScanMissingRootErrorsWithContext(t *testing.T) {
	root := filepath.Join(t.TempDir(), "no-such-library")
	movies, err := Scan(root)
	if err == nil {
		t.Fatalf("Scan(%q) = %+v, want an error", root, movies)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want it to wrap fs.ErrNotExist", err)
	}
	if !strings.Contains(err.Error(), "scan "+root) {
		t.Errorf("err = %q, want it to carry the path as `scan %s: ...`", err, root)
	}
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
