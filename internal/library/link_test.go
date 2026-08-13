package library

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"testing"
)

// --- naming -----------------------------------------------------------------

// The test this task exists for. Every folder in the real library must survive
// ParseFolder -> DestFolder unchanged, or an import writes a second folder for a
// film that is already there and the next scan records it as a second movie.
//
// Running it over the fixture rather than a hand-written list is the point: it
// meets the eight " - " titles, Spider-Man, X-Men Origins, Deadpool & Wolverine
// and Tom Clancy's Jack Ryan - Ghost War without anyone having to remember that
// those are the dangerous ones.
func TestDestFolderRoundTripsEveryFixtureFolder(t *testing.T) {
	entries, err := os.ReadDir(fixtureRoot)
	if err != nil {
		t.Fatalf("read %s: %v", fixtureRoot, err)
	}

	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || isHidden(name) {
			continue
		}

		title, year, ok := ParseFolder(name)
		if !ok {
			t.Errorf("ParseFolder(%q) did not parse; the fixture is all Title (Year)", name)
			continue
		}

		got, err := DestFolder(title, year)
		if err != nil {
			t.Errorf("DestFolder(%q, %d): %v", title, year, err)
			continue
		}
		if got != name {
			t.Errorf("round trip: %q -> DestFolder(%q, %d) = %q, want the original name back", name, title, year, got)
		}
		checked++
	}

	if checked != 29 {
		t.Fatalf("round-tripped %d folders, want 29 — the fixture is T7's and must not have changed", checked)
	}
}

// The inverse of D9, which the round trip above cannot show because no folder on
// disk contains a colon: a real title carries one, and the library spells it " - ".
func TestDestFolderSpellsOutTheColon(t *testing.T) {
	cases := []struct {
		title string
		year  int
		want  string
	}{
		{"Avengers: Infinity War", 2018, "Avengers - Infinity War (2018)"},
		{"Spider-Man: No Way Home", 2021, "Spider-Man - No Way Home (2021)"},
		{"Tom Clancy's Jack Ryan: Ghost War", 2026, "Tom Clancy's Jack Ryan - Ghost War (2026)"},
		// A real hyphen is left alone. This is the half of D9 that a naive
		// "- means :" rewrite gets wrong in the other direction.
		{"Spider-Man", 2002, "Spider-Man (2002)"},
		{"Deadpool & Wolverine", 2024, "Deadpool & Wolverine (2024)"},
		{"  Interstellar  ", 2014, "Interstellar (2014)"},
	}

	for _, c := range cases {
		got, err := DestFolder(c.title, c.year)
		if err != nil {
			t.Errorf("DestFolder(%q, %d): %v", c.title, c.year, err)
			continue
		}
		if got != c.want {
			t.Errorf("DestFolder(%q, %d) = %q, want %q", c.title, c.year, got, c.want)
		}
	}
}

// movies.title arrives from a client through POST /api/downloads, and this is the
// first code that turns it into a path. Each of these is rejected outright rather
// than sanitised into something plausible.
func TestDestFolderRejectsATitleThatIsNotAName(t *testing.T) {
	cases := map[string]string{
		"a slash":          "Movies/Evil",
		"a leading slash":  "/etc/cron.d",
		"a traversal":      "../../etc/cron.d",
		"a backslash":      `Movies\Evil`,
		"a NUL":            "Movie\x00.mkv",
		"a dot":            ".",
		"a double dot":     "..",
		"empty":            "",
		"only whitespace":  "   ",
		"only a colon":     ":",
		"colons and space": " : ",
	}

	for name, title := range cases {
		got, err := DestFolder(title, 2014)
		if err == nil {
			t.Errorf("%s: DestFolder(%q, 2014) = %q, want an error", name, title, got)
			continue
		}
		// The API turns this into a 422: it is the client's title, and no amount
		// of retrying will fix it.
		if !errors.Is(err, ErrBadTitle) {
			t.Errorf("%s: err = %v, want ErrBadTitle", name, err)
		}
	}
}

// A year that is not four digits would produce a folder curator writes and then
// cannot read back — ParseFolder accepts exactly four.
func TestDestFolderRejectsAYearThatWouldNotParseBack(t *testing.T) {
	for _, year := range []int{-1, 0, 1, 999, 10000, 99999} {
		got, err := DestFolder("Interstellar", year)
		if err == nil {
			t.Errorf("DestFolder(_, %d) = %q, want an error", year, got)
			continue
		}
		if !errors.Is(err, ErrBadTitle) {
			t.Errorf("year %d: err = %v, want ErrBadTitle", year, err)
		}
	}
}

func TestDestName(t *testing.T) {
	got, err := DestName("Interstellar", 2014, ".mkv")
	if err != nil {
		t.Fatalf("DestName: %v", err)
	}
	if want := "Interstellar (2014).mkv"; got != want {
		t.Errorf("DestName = %q, want %q", got, want)
	}

	// A source spelled .MKV must not produce a library entry spelled unlike
	// every other one.
	if got, err := DestName("Interstellar", 2014, ".MKV"); err != nil || got != "Interstellar (2014).mkv" {
		t.Errorf("DestName(.MKV) = %q, %v; want %q and no error", got, err, "Interstellar (2014).mkv")
	}

	for _, ext := range []string{"", ".", ".srt", ".nfo", "mkv"} {
		if got, err := DestName("Interstellar", 2014, ext); err == nil {
			t.Errorf("DestName(_, _, %q) = %q, want an error", ext, got)
		}
	}

	// The title rules are DestFolder's, reached through this function too.
	if _, err := DestName("../evil", 2014, ".mkv"); err == nil {
		t.Error("DestName accepted a traversing title")
	}
}

// --- FindFeature ------------------------------------------------------------

// smallFloor keeps the fixtures in these tests to a few kilobytes.
// TestFindFeatureAppliesTheDefaultFloor is what proves the real 50 MiB value.
var smallFloor = FeatureOpts{MinBytes: 1024, MaxDepth: 3}

// content_path is the file itself for a single-file torrent, so a bare file has
// to work as well as a directory.
func TestFindFeatureAcceptsAFilePath(t *testing.T) {
	dir := t.TempDir()
	file := mkFile(t, filepath.Join(dir, "Interstellar.2014.mkv"), 4096, 'a')

	got, err := FindFeature(file, smallFloor)
	if err != nil {
		t.Fatalf("FindFeature: %v", err)
	}
	if got.Path != file || got.Size != 4096 || got.Others != 0 {
		t.Errorf("FindFeature = %+v, want the file itself at 4096 bytes with no others", got)
	}

	// A file that is not a video, and a video under the floor, are both
	// ErrNoVideo rather than a confusing success.
	for _, name := range []string{"notes.txt", "tiny.mkv"} {
		size := 4096
		if name == "tiny.mkv" {
			size = 10
		}
		p := mkFile(t, filepath.Join(dir, name), size, 'b')
		if _, err := FindFeature(p, smallFloor); !errors.Is(err, ErrNoVideo) {
			t.Errorf("FindFeature(%s) err = %v, want ErrNoVideo", name, err)
		}
	}
}

func TestFindFeatureTakesTheLargestVideoAndCountsTheRest(t *testing.T) {
	root := t.TempDir()
	mkFile(t, filepath.Join(root, "part1.mkv"), 2048, 'a')
	want := mkFile(t, filepath.Join(root, "nested", "feature.mkv"), 8192, 'b')
	mkFile(t, filepath.Join(root, "nested", "deeper", "part2.mp4"), 4096, 'c')
	mkFile(t, filepath.Join(root, "readme.nfo"), 9999, 'd')
	mkFile(t, filepath.Join(root, "subs.srt"), 9999, 'e')

	got, err := FindFeature(root, smallFloor)
	if err != nil {
		t.Fatalf("FindFeature: %v", err)
	}
	if got.Path != want {
		t.Errorf("Path = %q, want the largest video %q", got.Path, want)
	}
	if got.Size != 8192 {
		t.Errorf("Size = %d, want 8192", got.Size)
	}
	// The .nfo and .srt are larger than one of the videos and must not be
	// counted: Others is "other videos I passed over", not "other files".
	if got.Others != 2 {
		t.Errorf("Others = %d, want 2 — a double feature has to be visible in the log", got.Others)
	}
}

// The floor is why a 4 MB sample.mkv beside a missing feature cannot be
// imported as the film. This is the only test that writes 50 MiB-scale files,
// and it does so sparsely.
func TestFindFeatureAppliesTheDefaultFloor(t *testing.T) {
	root := t.TempDir()
	sparse(t, filepath.Join(root, "sample.mkv"), 4<<20)

	if _, err := FindFeature(root, FeatureOpts{}); !errors.Is(err, ErrNoVideo) {
		t.Fatalf("a lone 4 MB sample: err = %v, want ErrNoVideo", err)
	}

	feature := filepath.Join(root, "Interstellar.mkv")
	sparse(t, feature, 60<<20)

	got, err := FindFeature(root, FeatureOpts{})
	if err != nil {
		t.Fatalf("FindFeature: %v", err)
	}
	if got.Path != feature {
		t.Errorf("Path = %q, want %q", got.Path, feature)
	}
	// The sample is below the floor, so it is not even an "other".
	if got.Others != 0 {
		t.Errorf("Others = %d, want 0 — the sample is under the floor", got.Others)
	}
}

// Each of these directories gets a LARGER video than the feature, so a test that
// passed by accident would fail here.
func TestFindFeatureDoesNotDescendIntoExtras(t *testing.T) {
	for _, name := range []string{"Sample", "sample", "Extras", "FEATURETTES", "Subs", ".hidden"} {
		root := t.TempDir()
		feature := mkFile(t, filepath.Join(root, "feature.mkv"), 2048, 'a')
		mkFile(t, filepath.Join(root, name, "bigger.mkv"), 65536, 'b')

		got, err := FindFeature(root, smallFloor)
		if err != nil {
			t.Fatalf("%s: FindFeature: %v", name, err)
		}
		if got.Path != feature {
			t.Errorf("%s: Path = %q, want the feature %q — %s must not be descended into", name, got.Path, feature, name)
		}
		if got.Others != 0 {
			t.Errorf("%s: Others = %d, want 0", name, got.Others)
		}
	}
}

func TestFindFeatureIgnoresHiddenFiles(t *testing.T) {
	root := t.TempDir()
	feature := mkFile(t, filepath.Join(root, "feature.mkv"), 2048, 'a')
	// macOS scatters AppleDouble forks that carry the extension of a file they
	// are not, and they sort before the real one.
	mkFile(t, filepath.Join(root, "._feature.mkv"), 65536, 'b')

	got, err := FindFeature(root, smallFloor)
	if err != nil {
		t.Fatalf("FindFeature: %v", err)
	}
	if got.Path != feature {
		t.Errorf("Path = %q, want %q", got.Path, feature)
	}
}

func TestFindFeatureEmptyOrNonVideoIsErrNoVideo(t *testing.T) {
	empty := t.TempDir()
	if _, err := FindFeature(empty, smallFloor); !errors.Is(err, ErrNoVideo) {
		t.Errorf("empty directory: err = %v, want ErrNoVideo", err)
	}

	// 15 of the 29 folders in the real library are empty — a folder does not
	// imply a file, here or in the download directory.
	junk := t.TempDir()
	mkFile(t, filepath.Join(junk, "movie.nfo"), 4096, 'a')
	mkFile(t, filepath.Join(junk, "movie.en.srt"), 4096, 'b')
	mkFile(t, filepath.Join(junk, "poster.jpg"), 4096, 'c')
	if _, err := FindFeature(junk, smallFloor); !errors.Is(err, ErrNoVideo) {
		t.Errorf("no video: err = %v, want ErrNoVideo", err)
	}

	if _, err := FindFeature(filepath.Join(empty, "nope"), smallFloor); err == nil {
		t.Error("a missing content path returned no error")
	}
}

func TestFindFeatureStopsAtTheDepthCap(t *testing.T) {
	root := t.TempDir()
	mkFile(t, filepath.Join(root, "a", "b", "deep.mkv"), 4096, 'a')

	if _, err := FindFeature(root, FeatureOpts{MinBytes: 1024, MaxDepth: 1}); !errors.Is(err, ErrNoVideo) {
		t.Errorf("MaxDepth 1: err = %v, want ErrNoVideo — a/b is two levels down", err)
	}
	if _, err := FindFeature(root, FeatureOpts{MinBytes: 1024, MaxDepth: 2}); err != nil {
		t.Errorf("MaxDepth 2: %v, want the file to be found", err)
	}
}

// A crafted torrent must not be able to point the import at an arbitrary file on
// the disk and have it hardlinked into the library.
func TestFindFeatureDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mkFile(t, filepath.Join(outside, "secret.mkv"), 65536, 'x')
	feature := mkFile(t, filepath.Join(root, "feature.mkv"), 2048, 'a')

	if err := os.Symlink(outside, filepath.Join(root, "elsewhere")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.mkv"), filepath.Join(root, "linked.mkv")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := FindFeature(root, smallFloor)
	if err != nil {
		t.Fatalf("FindFeature: %v", err)
	}
	if got.Path != feature {
		t.Errorf("Path = %q, want the real file %q — symlinks are not followed out of a torrent directory", got.Path, feature)
	}
	if got.Others != 0 {
		t.Errorf("Others = %d, want 0", got.Others)
	}
}

// --- Link -------------------------------------------------------------------

// The proof, done twice and independently. os.SameFile plus a link count of 2
// says the two names share an inode; writing through the source and reading the
// destination says it is the SAME BYTES, not a copy taken at link time. Either
// check alone is satisfied by something that is not a hardlink.
func TestLinkCreatesASecondNameForOneFile(t *testing.T) {
	dir := t.TempDir()
	srcDir := mkdir(t, dir, "src")
	dstDir := mkdir(t, dir, "dst")
	src := mkFile(t, filepath.Join(srcDir, "feature.mkv"), 4096, 'a')
	dst := filepath.Join(dstDir, "Interstellar (2014).mkv")

	before := treeSnapshot(t, srcDir)

	if err := Link(src, dst); err != nil {
		t.Fatalf("Link: %v", err)
	}

	srcInfo, dstInfo := statOf(t, src), statOf(t, dst)
	if !os.SameFile(srcInfo, dstInfo) {
		t.Error("os.SameFile is false: the destination is a copy, not a link")
	}
	if n := nlinkOf(t, dst); n != 2 {
		t.Errorf("link count = %d, want 2", n)
	}
	if srcInfo.Size() != dstInfo.Size() {
		t.Errorf("sizes differ: %d and %d", srcInfo.Size(), dstInfo.Size())
	}

	// The independent half: change the source's bytes afterwards.
	if err := os.WriteFile(src, []byte("changed through the source"), 0o644); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != "changed through the source" {
		t.Errorf("destination = %q after writing through the source; one inode would have shown the change", got)
	}

	// D8: the source is never deleted. Its content legitimately changed above,
	// so the snapshot is re-taken from the point that matters — the file is
	// still there, still one entry.
	if names := entryNames(t, srcDir); len(names) != 1 || names[0] != "feature.mkv" {
		t.Errorf("source directory = %v, want just feature.mkv; %v was there before", names, keysOf(before))
	}
}

// The crashed-run retry. A process that died between linking and writing the row
// finds its own link on the next tick, and that has to be success or the import
// is stuck for ever.
func TestLinkOntoItsOwnLinkIsSuccess(t *testing.T) {
	dir := t.TempDir()
	src := mkFile(t, filepath.Join(dir, "src", "feature.mkv"), 4096, 'a')
	dst := filepath.Join(mkdir(t, dir, "dst"), "Interstellar (2014).mkv")

	if err := Link(src, dst); err != nil {
		t.Fatalf("first Link: %v", err)
	}
	firstInfo := statOf(t, dst)

	if err := Link(src, dst); err != nil {
		t.Fatalf("second Link: %v, want success — this is the retry path", err)
	}
	if !os.SameFile(firstInfo, statOf(t, dst)) {
		t.Error("the destination was replaced; it should have been left exactly as it was")
	}
	if n := nlinkOf(t, dst); n != 2 {
		t.Errorf("link count = %d after re-linking, want still 2", n)
	}
}

func TestLinkRefusesToOverwriteADifferentFile(t *testing.T) {
	dir := t.TempDir()
	srcDir := mkdir(t, dir, "src")
	dstDir := mkdir(t, dir, "dst")
	src := mkFile(t, filepath.Join(srcDir, "feature.mkv"), 4096, 'a')
	dst := mkFile(t, filepath.Join(dstDir, "Interstellar (2014).mkv"), 2048, 'z')

	before := treeSnapshot(t, dir)

	if err := Link(src, dst); err == nil {
		t.Fatal("Link overwrote a different file")
	}

	// Nothing moved on either side: the file that was already there might be the
	// good copy, and the source is never touched.
	assertTreeUnchanged(t, dir, before)
	if n := nlinkOf(t, dst); n != 1 {
		t.Errorf("destination link count = %d, want 1 — it must not have been linked", n)
	}
}

func TestLinkFallsBackToACopyOnEXDEV(t *testing.T) {
	dir := t.TempDir()
	srcDir := mkdir(t, dir, "src")
	dstDir := mkdir(t, dir, "dst")
	src := mkFile(t, filepath.Join(srcDir, "feature.mkv"), 4096, 'a')
	dst := filepath.Join(dstDir, "Interstellar (2014).mkv")

	srcBefore := treeSnapshot(t, srcDir)

	calls := 0
	crossDevice := func(string, string) error {
		calls++
		return &os.LinkError{Op: "link", Old: src, New: dst, Err: syscall.EXDEV}
	}
	if err := LinkWith(crossDevice, src, dst); err != nil {
		t.Fatalf("LinkWith(EXDEV): %v", err)
	}
	if calls != 1 {
		t.Errorf("the link seam was called %d times, want 1", calls)
	}

	// A copy is a different inode by definition, which is how the fallback is
	// told apart from the hardlink it replaced.
	if os.SameFile(statOf(t, src), statOf(t, dst)) {
		t.Error("os.SameFile is true after the copy fallback; it should be a distinct file")
	}
	if n := nlinkOf(t, dst); n != 1 {
		t.Errorf("copy link count = %d, want 1", n)
	}
	if !sameContents(t, src, dst) {
		t.Error("the copy is not byte-identical to the source")
	}
	if _, err := os.Stat(dst + tmpSuffix); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s was left behind after a successful copy", dst+tmpSuffix)
	}
	assertTreeUnchanged(t, srcDir, srcBefore)
}

// A crash mid-copy must leave a .curator-tmp, not a half-written .mkv that
// satisfies every existence check. A copy that FAILS must leave neither.
func TestLinkLeavesNothingBehindWhenTheCopyFails(t *testing.T) {
	dir := t.TempDir()
	srcDir := mkdir(t, dir, "src")
	src := mkFile(t, filepath.Join(srcDir, "feature.mkv"), 4096, 'a')

	// The destination directory does not exist, so opening the temp file fails —
	// standing in for a full disk without needing one.
	dst := filepath.Join(dir, "missing", "Interstellar (2014).mkv")

	srcBefore := treeSnapshot(t, srcDir)

	crossDevice := func(string, string) error {
		return &os.LinkError{Op: "link", Old: src, New: dst, Err: syscall.EXDEV}
	}
	if err := LinkWith(crossDevice, src, dst); err == nil {
		t.Fatal("the copy fallback reported success with no destination directory")
	}

	for _, path := range []string{dst, dst + tmpSuffix} {
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s exists after a failed copy", path)
		}
	}
	assertTreeUnchanged(t, srcDir, srcBefore)
}

func TestLinkRejectsASourceThatIsNotAFile(t *testing.T) {
	dir := t.TempDir()
	dstDir := mkdir(t, dir, "dst")
	srcDir := mkdir(t, dir, "src")

	before := treeSnapshot(t, dir)

	if err := Link(filepath.Join(dir, "nope.mkv"), filepath.Join(dstDir, "x.mkv")); err == nil {
		t.Error("Link accepted a missing source")
	}
	if err := Link(srcDir, filepath.Join(dstDir, "y.mkv")); err == nil {
		t.Error("Link accepted a directory as the source")
	}

	assertTreeUnchanged(t, dir, before)
}

// The caller creates the destination folder only once it has chosen a source
// file. Link creating it would mean a failed import leaves an empty
// "Title (Year)/" that the scanner records as a zero-size movie.
func TestLinkDoesNotCreateTheDestinationDirectory(t *testing.T) {
	dir := t.TempDir()
	src := mkFile(t, filepath.Join(dir, "src", "feature.mkv"), 4096, 'a')
	dst := filepath.Join(dir, "library", "Interstellar (2014)", "Interstellar (2014).mkv")

	if err := Link(src, dst); err == nil {
		t.Fatal("Link created the destination directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "library")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("Link created the library directory tree")
	}
}

// --- helpers ----------------------------------------------------------------

// mkFile writes size bytes of fill at path, creating parent directories. The
// fill byte differs per file so that "the contents are unchanged" is a real
// assertion rather than a size comparison.
func mkFile(t *testing.T, path string, size int, fill byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	body := make([]byte, size)
	for i := range body {
		body[i] = fill
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// sparse creates a file of the given apparent size without writing its blocks,
// so the 50 MiB floor can be exercised without 50 MiB of disk or of time.
func sparse(t *testing.T, path string, size int64) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatalf("truncate %s: %v", path, err)
	}
	return path
}

func statOf(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}

// nlinkOf reads the hard link count.
//
// It exists as a helper for one reason: syscall.Stat_t.Nlink is uint16 on
// darwin and uint64 on linux, so a comparison written inline compiles on a
// laptop and breaks GOOS=linux GOARCH=arm64 go vet — which is how this project
// ships. The conversion is the whole point of the function.
func nlinkOf(t *testing.T, path string) uint64 {
	t.Helper()
	sys := statOf(t, path).Sys()
	st, ok := sys.(*syscall.Stat_t)
	if !ok {
		t.Skipf("no syscall.Stat_t on this platform (%T)", sys)
	}
	return uint64(st.Nlink)
}

func sameContents(t *testing.T, a, b string) bool {
	t.Helper()
	return fileDigest(t, a) == fileDigest(t, b)
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// treeSnapshot records every regular file under root by relative path, size and
// content digest. Paired with assertTreeUnchanged it is decisions.md D8 — "the
// source is never deleted" — turned into an assertion instead of a promise, and
// it catches a truncation or an in-place rewrite as well as a delete.
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		snapshot[rel] = fmt.Sprintf("%s:%d", fileDigest(t, path), info.Size())
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return snapshot
}

func assertTreeUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	after := treeSnapshot(t, root)

	for path, want := range before {
		got, ok := after[path]
		if !ok {
			t.Errorf("%s is gone; D8 says a source file is never deleted", path)
			continue
		}
		if got != want {
			t.Errorf("%s changed: %s -> %s", path, want, got)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("%s appeared where nothing should have been written", path)
		}
	}
}

func entryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- RemoveMovieFolder ------------------------------------------------------

func TestRemoveMovieFolderDeletesOnlyInsideTheLibrary(t *testing.T) {
	dir := t.TempDir()
	root := mkdir(t, dir, "movies")
	outside := mkdir(t, dir, "elsewhere")
	mkFile(t, filepath.Join(outside, "precious.mkv"), 1024, 'p')

	folder := mkdir(t, root, "Interstellar (2014)")
	mkFile(t, filepath.Join(folder, "Interstellar (2014).mkv"), 2048, 'a')

	if err := RemoveMovieFolder(root, folder); err != nil {
		t.Fatalf("RemoveMovieFolder: %v", err)
	}
	if _, err := os.Stat(folder); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the folder is still there")
	}
	// The root survives, and so does everything beside it.
	if _, err := os.Stat(root); err != nil {
		t.Errorf("the library root was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "precious.mkv")); err != nil {
		t.Errorf("a file outside the library was removed: %v", err)
	}
}

// The containment check is the whole safety argument: a library_path that had
// drifted, or been crafted, must not turn this into rm -rf with a friendly name.
func TestRemoveMovieFolderRefusesAnythingOutsideOrTheRootItself(t *testing.T) {
	dir := t.TempDir()
	root := mkdir(t, dir, "movies")
	outside := mkdir(t, dir, "elsewhere")
	mkFile(t, filepath.Join(outside, "precious.mkv"), 1024, 'p')

	for name, folder := range map[string]string{
		"the root itself":    root,
		"a parent":           dir,
		"a sibling":          outside,
		"a traversal":        filepath.Join(root, "..", "elsewhere"),
		"an absolute escape": "/tmp",
	} {
		if err := RemoveMovieFolder(root, folder); err == nil {
			t.Errorf("%s: %q was accepted", name, folder)
		}
	}

	for _, path := range []string{root, outside, filepath.Join(outside, "precious.mkv")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was removed: %v", path, err)
		}
	}
}

// A symlink at the library path would otherwise have RemoveAll follow it out of
// the library and delete whatever it names.
func TestRemoveMovieFolderRefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	root := mkdir(t, dir, "movies")
	outside := mkdir(t, dir, "elsewhere")
	mkFile(t, filepath.Join(outside, "precious.mkv"), 1024, 'p')

	link := filepath.Join(root, "Interstellar (2014)")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := RemoveMovieFolder(root, link); err == nil {
		t.Error("a symlinked folder was removed, following it out of the library")
	}
	if _, err := os.Stat(filepath.Join(outside, "precious.mkv")); err != nil {
		t.Errorf("the symlink target's contents were removed: %v", err)
	}
}

func TestRemoveMovieFolderIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	root := mkdir(t, dir, "movies")
	folder := filepath.Join(root, "Never Existed (2014)")

	if err := RemoveMovieFolder(root, folder); err != nil {
		t.Errorf("removing a folder that is not there: %v, want success", err)
	}
}
