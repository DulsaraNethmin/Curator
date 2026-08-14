package importer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/library"
	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// featureSize clears library.DefaultMinFeatureBytes. The files are sparse, so
// this costs no disk and no time — and it means the tests exercise the real
// 50 MiB floor rather than a lowered one.
const featureSize = 60 << 20

const testHash = "AAAABBBBCCCCDDDDEEEEFFFF0000111122223333"

var importAt = time.Date(2026, 8, 12, 19, 30, 0, 0, time.UTC)

// --- the happy path ---------------------------------------------------------

func TestImportHardlinksTheFeatureIntoTheLibrary(t *testing.T) {
	h := newHarness(t)
	content := h.download("Interstellar.2014.1080p.BluRay.x264", "Interstellar.2014.1080p.mkv")

	before := h.snapshotDownloads()

	got, err := h.importer.Import(context.Background(), completed(content), h.dl)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	wantDir := filepath.Join(h.library, "Interstellar (2014)")
	wantFile := filepath.Join(wantDir, "Interstellar (2014).mkv")

	if _, err := os.Stat(wantFile); err != nil {
		t.Fatalf("the library entry is missing: %v", err)
	}

	// Proof one: one inode, two names.
	src := filepath.Join(content, "Interstellar.2014.1080p.mkv")
	if !os.SameFile(statOf(t, src), statOf(t, wantFile)) {
		t.Error("os.SameFile is false: the library entry is a copy, not a hardlink")
	}
	if n := nlinkOf(t, wantFile); n != 2 {
		t.Errorf("link count = %d, want 2", n)
	}

	// Proof two, independent of the first: bytes written through the source show
	// up through the destination.
	writeAt(t, src, "hardlinked, not copied")
	if got := readAt(t, wantFile, len("hardlinked, not copied")); got != "hardlinked, not copied" {
		t.Errorf("destination = %q after writing through the source", got)
	}

	// The store was told about the FOLDER, which is the scanner's identity key.
	if len(h.store.marked) != 1 {
		t.Fatalf("MarkImported called %d times, want 1", len(h.store.marked))
	}
	call := h.store.marked[0]
	if call.hash != testHash {
		t.Errorf("hash = %q, want %q", call.hash, testHash)
	}
	if call.path != wantDir {
		t.Errorf("library_path = %q, want the folder %q", call.path, wantDir)
	}
	if call.size != featureSize {
		t.Errorf("size = %d, want %d", call.size, featureSize)
	}
	if !call.at.Equal(importAt) {
		t.Errorf("at = %v, want the injected clock %v", call.at, importAt)
	}
	if got.Status != store.StatusImported {
		t.Errorf("returned status = %q, want %q", got.Status, store.StatusImported)
	}

	h.assertDownloadsIntact(before)
}

// content_path is the file itself for a single-file torrent, which is a shape
// the configuration cannot choose between in advance.
func TestImportAcceptsASingleFileContentPath(t *testing.T) {
	h := newHarness(t)
	file := filepath.Join(h.downloads, "complete", "curator", "Interstellar.2014.mkv")
	sparseFile(t, file, featureSize)

	if _, err := h.importer.Import(context.Background(), completed(file), h.dl); err != nil {
		t.Fatalf("Import: %v", err)
	}

	dst := filepath.Join(h.library, "Interstellar (2014)", "Interstellar (2014).mkv")
	if !os.SameFile(statOf(t, file), statOf(t, dst)) {
		t.Error("the single-file torrent was not hardlinked")
	}
}

func TestImportPrefersTheLargestAndReportsTheOthers(t *testing.T) {
	h := newHarness(t)
	content := filepath.Join(h.downloads, "complete", "curator", "Double Feature")
	sparseFile(t, filepath.Join(content, "part1.mkv"), featureSize)
	sparseFile(t, filepath.Join(content, "part2.mkv"), featureSize*2)
	sparseFile(t, filepath.Join(content, "sample.mkv"), 4<<20) // under the floor

	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err != nil {
		t.Fatalf("Import: %v", err)
	}

	dst := filepath.Join(h.library, "Interstellar (2014)", "Interstellar (2014).mkv")
	if !os.SameFile(statOf(t, filepath.Join(content, "part2.mkv")), statOf(t, dst)) {
		t.Error("the largest video did not win")
	}
	if h.store.marked[0].size != featureSize*2 {
		t.Errorf("size = %d, want the largest %d", h.store.marked[0].size, featureSize*2)
	}
	// A double feature must be visible rather than silently half-imported.
	if !strings.Contains(h.logs.String(), "more than one video") {
		t.Errorf("the extra video was not logged; log was:\n%s", h.logs.String())
	}
}

// --- the untrusted title ----------------------------------------------------

func TestImportRejectsATitleThatIsNotAName(t *testing.T) {
	for _, title := range []string{"../../etc/cron.d", "Movies/Evil", "..", ".", `back\slash`} {
		h := newHarness(t)
		h.store.movies[1] = store.Movie{ID: 1, Title: title, Year: 2014}
		content := h.download("release", "feature.mkv")

		if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err == nil {
			t.Errorf("title %q was accepted", title)
		}

		// Nothing may have been created, anywhere under the library root.
		if entries, err := os.ReadDir(h.library); err != nil {
			t.Fatalf("read library: %v", err)
		} else if len(entries) != 0 {
			t.Errorf("title %q: the library is not empty: %v", title, names(entries))
		}
		if len(h.store.marked) != 0 {
			t.Errorf("title %q: the store was written to", title)
		}
	}
}

// Belt and braces over DestFolder: whatever the naming rules become, a
// destination outside the library root creates nothing.
func TestAssertInsideLibraryRefusesAnEscape(t *testing.T) {
	h := newHarness(t)

	if err := library.AssertInside(h.library, filepath.Join(h.library, "Interstellar (2014)")); err != nil {
		t.Errorf("a normal destination was refused: %v", err)
	}
	for _, dest := range []string{
		filepath.Join(h.library, "..", "elsewhere"),
		filepath.Join(h.library, "..", "..", "etc"),
		"/etc/cron.d",
	} {
		if err := library.AssertInside(h.library, dest); err == nil {
			t.Errorf("%s was accepted as inside %s", dest, h.library)
		}
	}
}

// --- nothing to import ------------------------------------------------------

// ErrNoVideo is not a failed download: the torrent fetched what it advertised.
// The row must stay `completed` so the next tick retries, which means the store
// is not touched at all.
func TestImportWithNoVideoIsErrNoVideoAndWritesNothing(t *testing.T) {
	h := newHarness(t)
	content := filepath.Join(h.downloads, "complete", "curator", "release")
	sparseFile(t, filepath.Join(content, "readme.nfo"), 1024)
	sparseFile(t, filepath.Join(content, "sample.mkv"), 4<<20) // under the 50 MiB floor

	_, err := h.importer.Import(context.Background(), completed(content), h.dl)
	if !errors.Is(err, library.ErrNoVideo) {
		t.Fatalf("err = %v, want library.ErrNoVideo", err)
	}
	if len(h.store.marked) != 0 {
		t.Error("the store was written to; the row must stay completed for the next tick")
	}
}

// The ordering test. MkdirAll must come after the source file has been chosen,
// or a failure leaves an empty "Title (Year)/" that the scanner records as a
// zero-size movie — and 15 of the 29 real library folders are already empty, so
// that is not a hypothetical shape.
func TestImportLeavesNoEmptyFolderWhenThereIsNothingToLink(t *testing.T) {
	h := newHarness(t)
	content := filepath.Join(h.downloads, "complete", "curator", "release")
	if err := os.MkdirAll(content, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err == nil {
		t.Fatal("an empty content path imported successfully")
	}

	entries, err := os.ReadDir(h.library)
	if err != nil {
		t.Fatalf("read library: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the library holds %v; a failed import must create nothing", names(entries))
	}
}

// --- the subtitle sidecars --------------------------------------------------

// The two places release groups actually put them, in one import, named off the
// feature so that Jellyfin, Plex and VLC all read them without being told.
func TestImportLinksTheSidecarsBesideTheFeature(t *testing.T) {
	h := newHarness(t)
	content := h.download("Interstellar.2014.1080p.BluRay.x264-GROUP", "Interstellar.2014.1080p.mkv")
	beside := writeSubtitle(t, content, "Interstellar.2014.1080p.en.srt")
	inSubs := writeSubtitle(t, filepath.Join(content, "Subs"), "2_French.srt")
	// Neither of these is a subtitle, and neither may be linked.
	sparseFile(t, filepath.Join(content, "RARBG.txt"), 30)
	sparseFile(t, filepath.Join(content, "Interstellar.nfo"), 30)

	before := h.snapshotDownloads()

	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err != nil {
		t.Fatalf("Import: %v", err)
	}

	dir := filepath.Join(h.library, "Interstellar (2014)")
	want := []string{
		"Interstellar (2014).en.srt",
		"Interstellar (2014).fr.srt",
		"Interstellar (2014).mkv",
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the library folder: %v", err)
	}
	if got := names(entries); !equalStrings(got, want) {
		t.Errorf("the library folder holds %v, want %v", got, want)
	}

	// The same proof phase 4 gave for the feature, because it is the same
	// library.Link and D8 binds it identically: one inode, two names.
	for src, dst := range map[string]string{
		beside: filepath.Join(dir, "Interstellar (2014).en.srt"),
		inSubs: filepath.Join(dir, "Interstellar (2014).fr.srt"),
	} {
		if !os.SameFile(statOf(t, src), statOf(t, dst)) {
			t.Errorf("%s is a copy, not a hardlink", filepath.Base(dst))
		}
		if n := nlinkOf(t, dst); n != 2 {
			t.Errorf("%s: link count = %d, want 2", filepath.Base(dst), n)
		}
	}

	if !strings.Contains(h.logs.String(), "subtitles=2") {
		t.Errorf("the import line does not say how many subtitles came across:\n%s", h.logs.String())
	}
	h.assertDownloadsIntact(before)
}

// The task file's own fixture — Movie.en.srt beside Subs/2_English.srt — and it
// COLLIDES, because both name the same language and the naming rule is "off the
// feature". First wins, second warns, nothing is overwritten.
func TestTwoSubtitlesWantingOneNameKeepTheFirst(t *testing.T) {
	h := newHarness(t)
	content := h.download("release", "Movie.mkv")
	first := writeSubtitle(t, content, "Movie.en.srt")
	second := writeSubtitle(t, filepath.Join(content, "Subs"), "2_English.srt")

	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err != nil {
		t.Fatalf("Import: %v", err)
	}

	dst := filepath.Join(h.library, "Interstellar (2014)", "Interstellar (2014).en.srt")
	if !os.SameFile(statOf(t, first), statOf(t, dst)) {
		t.Error("the second subtitle overwrote the first")
	}
	if os.SameFile(statOf(t, second), statOf(t, dst)) {
		t.Error("the second subtitle is what landed; the first must win")
	}
	if !strings.Contains(h.logs.String(), "kept the first") {
		t.Errorf("the skipped subtitle was not reported:\n%s", h.logs.String())
	}
	if !strings.Contains(h.logs.String(), "subtitles=1") {
		t.Errorf("the count does not reflect the skip:\n%s", h.logs.String())
	}
}

// **The trade this whole path exists to avoid.** The feature is the point and
// the subtitle is a courtesy: an import that failed over a bad .srt would leave
// a film out of the library for a file nobody would miss.
func TestASubtitleThatCannotBeLinkedStillLeavesTheFilmImported(t *testing.T) {
	h := newHarness(t)
	content := h.download("release", "Movie.mkv")
	writeSubtitle(t, content, "Movie.en.srt")

	// A different file already at the destination is the one thing library.Link
	// refuses outright — it never overwrites, because the thing already there
	// might be the good copy.
	dir := filepath.Join(h.library, "Interstellar (2014)")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Interstellar (2014).en.srt"), []byte("someone else's"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := h.importer.Import(context.Background(), completed(content), h.dl)
	if err != nil {
		t.Fatalf("Import failed because of a subtitle: %v", err)
	}
	if got.Status != store.StatusImported {
		t.Errorf("status = %q, want the film imported", got.Status)
	}
	if len(h.store.marked) != 1 {
		t.Error("the film was not recorded")
	}
	if _, err := os.Stat(filepath.Join(dir, "Interstellar (2014).mkv")); err != nil {
		t.Errorf("the feature is not in the library: %v", err)
	}
	if !strings.Contains(h.logs.String(), "could not link a subtitle") {
		t.Errorf("the failure was not reported at all:\n%s", h.logs.String())
	}
	// And the file that was already there is untouched.
	body, err := os.ReadFile(filepath.Join(dir, "Interstellar (2014).en.srt"))
	if err != nil || string(body) != "someone else's" {
		t.Errorf("the existing file was overwritten: %q, %v", body, err)
	}
}

// A folder that cannot be read is the same trade: warn, and import the film.
func TestAnUnreadableSubsFolderStillLeavesTheFilmImported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 directory regardless")
	}
	h := newHarness(t)
	content := h.download("release", "Movie.mkv")
	subs := filepath.Join(content, "Subs")
	writeSubtitle(t, subs, "2_English.srt")
	if err := os.Chmod(subs, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(subs, 0o755) })

	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err != nil {
		t.Fatalf("Import failed because a Subs folder could not be read: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.library, "Interstellar (2014)", "Interstellar (2014).mkv")); err != nil {
		t.Errorf("the feature is not in the library: %v", err)
	}
	if !strings.Contains(h.logs.String(), "could not look for subtitles") {
		t.Errorf("the failure was not reported at all:\n%s", h.logs.String())
	}
}

// A single-file torrent's content path is the file, and its directory is the
// shared completed folder. Sweeping it would put another film's subtitles in
// this film's library folder.
func TestASingleFileImportDoesNotAdoptItsNeighboursSubtitles(t *testing.T) {
	h := newHarness(t)
	completedDir := filepath.Join(h.downloads, "complete", "curator")
	file := filepath.Join(completedDir, "Interstellar.2014.mkv")
	sparseFile(t, file, featureSize)
	writeSubtitle(t, completedDir, "Some.Other.Film.en.srt")

	if _, err := h.importer.Import(context.Background(), completed(file), h.dl); err != nil {
		t.Fatalf("Import: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(h.library, "Interstellar (2014)"))
	if err != nil {
		t.Fatalf("read the library folder: %v", err)
	}
	if got := names(entries); !equalStrings(got, []string{"Interstellar (2014).mkv"}) {
		t.Errorf("the library folder holds %v, want only the feature", got)
	}
}

// The property the AssertInside call in linkSidecars keeps true: a destination
// is the FEATURE's name plus tokens out of closed tables, so a filename a
// release group chose never reaches a path.
func TestAHostileSubtitleNameLandsUnderTheFilmsOwnName(t *testing.T) {
	h := newHarness(t)
	content := h.download("release", "Movie.mkv")
	// Not starting with a dot, on purpose: a name that did would be skipped as
	// hidden before any of this, which would prove nothing.
	writeSubtitle(t, content, "x..%2F..%2Fetc%2Fpasswd.en.srt")

	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err != nil {
		t.Fatalf("Import: %v", err)
	}

	dir := filepath.Join(h.library, "Interstellar (2014)")
	if got := readNames(t, dir); !equalStrings(got, []string{"Interstellar (2014).en.srt", "Interstellar (2014).mkv"}) {
		t.Errorf("the library folder holds %v", got)
	}
	// And nothing appeared beside the library root, which is where an escape
	// with one fewer ".." would have landed.
	if got := readNames(t, h.root); !equalStrings(got, []string{"downloads", "library"}) {
		t.Errorf("something was written outside the library: %v", got)
	}
}

// .ass and .ssa are LINKED. Whether a browser is offered them is internal/api's
// decision, and the library must not be shaped by the player's limits — Jellyfin
// and VLC render them properly.
func TestAStyledSubtitleIsStillLinked(t *testing.T) {
	h := newHarness(t)
	content := h.download("release", "Movie.mkv")
	writeSubtitle(t, content, "Movie.en.ass")
	writeSubtitle(t, content, "Movie.fr.ssa")
	writeSubtitle(t, content, "Movie.de.sub")
	writeSubtitle(t, content, "Movie.es.vtt")

	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err != nil {
		t.Fatalf("Import: %v", err)
	}

	want := []string{
		"Interstellar (2014).de.sub",
		"Interstellar (2014).en.ass",
		"Interstellar (2014).es.vtt",
		"Interstellar (2014).fr.ssa",
		"Interstellar (2014).mkv",
	}
	if got := readNames(t, filepath.Join(h.library, "Interstellar (2014)")); !equalStrings(got, want) {
		t.Errorf("the library folder holds %v, want %v", got, want)
	}
}

// The crashed-run retry path, for sidecars. D14 retries a failing import on
// every tick, and a second pass finds its own links: library.Link treats a
// destination that is already os.SameFile as success, so the count is the same
// and the link count does not climb.
func TestReImportingFindsItsOwnSidecarsAndDoesNotDuplicateThem(t *testing.T) {
	h := newHarness(t)
	content := h.download("release", "Movie.mkv")
	writeSubtitle(t, content, "Movie.en.srt")
	writeSubtitle(t, filepath.Join(content, "Subs"), "2_French.srt")
	ctx := context.Background()

	if _, err := h.importer.Import(ctx, completed(content), h.dl); err != nil {
		t.Fatalf("first Import: %v", err)
	}
	if _, err := h.importer.Import(ctx, completed(content), h.dl); err != nil {
		t.Fatalf("second Import: %v — this is the crashed-run retry path", err)
	}

	dir := filepath.Join(h.library, "Interstellar (2014)")
	want := []string{"Interstellar (2014).en.srt", "Interstellar (2014).fr.srt", "Interstellar (2014).mkv"}
	if got := readNames(t, dir); !equalStrings(got, want) {
		t.Errorf("the library folder holds %v, want %v", got, want)
	}
	if n := nlinkOf(t, filepath.Join(dir, "Interstellar (2014).en.srt")); n != 2 {
		t.Errorf("link count = %d after re-importing, want still 2", n)
	}
	if strings.Contains(h.logs.String(), "could not link a subtitle") {
		t.Errorf("re-linking onto its own link was reported as a failure:\n%s", h.logs.String())
	}
	if got := strings.Count(h.logs.String(), "subtitles=2"); got != 2 {
		t.Errorf("the count says %d imports linked two subtitles, want 2:\n%s", got, h.logs.String())
	}
}

// A film with no subtitles is the ordinary case and must cost nothing and say
// nothing.
func TestAnImportWithNoSubtitlesIsSilent(t *testing.T) {
	h := newHarness(t)
	content := h.download("release", "Movie.mkv")

	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if strings.Contains(h.logs.String(), "subtitle") && !strings.Contains(h.logs.String(), "subtitles=0") {
		t.Errorf("a film with no subtitles produced a subtitle line:\n%s", h.logs.String())
	}
}

// --- retry and failure ------------------------------------------------------

// A process that died between linking and recording finds its own link on the
// next tick. That has to be success, or the import is stuck for ever.
func TestImportOnItsOwnExistingLinkSucceedsAndStillRecords(t *testing.T) {
	h := newHarness(t)
	content := h.download("release", "feature.mkv")
	ctx := context.Background()

	if _, err := h.importer.Import(ctx, completed(content), h.dl); err != nil {
		t.Fatalf("first Import: %v", err)
	}
	if _, err := h.importer.Import(ctx, completed(content), h.dl); err != nil {
		t.Fatalf("second Import: %v — this is the crashed-run retry path", err)
	}

	if len(h.store.marked) != 2 {
		t.Errorf("MarkImported called %d times, want 2 — the retry must still record", len(h.store.marked))
	}
	dst := filepath.Join(h.library, "Interstellar (2014)", "Interstellar (2014).mkv")
	if n := nlinkOf(t, dst); n != 2 {
		t.Errorf("link count = %d after re-importing, want still 2", n)
	}
}

// The link is a fact on disk. Removing it because the database write failed
// would delete the library's only copy if the write actually landed, and the
// next tick re-links onto it harmlessly anyway.
func TestImportKeepsTheLinkWhenTheStoreFails(t *testing.T) {
	h := newHarness(t)
	h.store.markErr = errors.New("database is locked")
	content := h.download("release", "feature.mkv")

	before := h.snapshotDownloads()

	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err == nil {
		t.Fatal("Import succeeded with a failing store")
	}

	dst := filepath.Join(h.library, "Interstellar (2014)", "Interstellar (2014).mkv")
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("the link was removed after the store failed: %v", err)
	}
	h.assertDownloadsIntact(before)
}

func TestImportReportsAMissingMovieRow(t *testing.T) {
	h := newHarness(t)
	h.store.getErr = store.ErrNotFound
	content := h.download("release", "feature.mkv")

	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want store.ErrNotFound", err)
	}
	if entries, _ := os.ReadDir(h.library); len(entries) != 0 {
		t.Error("a folder was created for a movie row that does not exist")
	}
}

// --- TryImport --------------------------------------------------------------

// D14 retries a failing import on every tick, by design. What is suppressed is
// the log, not the retry — a permanently broken import must not write a warning
// every ten seconds for ever.
func TestTryImportSuppressesARepeatedIdenticalFailure(t *testing.T) {
	h := newHarness(t)
	content := filepath.Join(h.downloads, "complete", "curator", "release")
	if err := os.MkdirAll(content, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ctx := context.Background()

	h.importer.TryImport(ctx, completed(content), h.dl)
	h.importer.TryImport(ctx, completed(content), h.dl)
	h.importer.TryImport(ctx, completed(content), h.dl)

	if n := strings.Count(h.logs.String(), "import failed"); n != 1 {
		t.Errorf("logged %d failures for three identical ticks, want 1:\n%s", n, h.logs.String())
	}

	// A different failure for the same torrent is news and is logged.
	h.store.getErr = errors.New("database is locked")
	h.importer.TryImport(ctx, completed(content), h.dl)
	if n := strings.Count(h.logs.String(), "import failed"); n != 2 {
		t.Errorf("a different error logged %d times in total, want 2:\n%s", n, h.logs.String())
	}

	// A success clears the suppression, so a later relapse is reported again.
	h.store.getErr = nil
	sparseFile(t, filepath.Join(content, "feature.mkv"), featureSize)
	h.importer.TryImport(ctx, completed(content), h.dl)
	if !strings.Contains(h.logs.String(), "\"imported\"") && !strings.Contains(h.logs.String(), "msg=imported") {
		t.Errorf("the success was not logged:\n%s", h.logs.String())
	}

	h.store.getErr = errors.New("database is locked")
	h.importer.TryImport(ctx, completed(content), h.dl)
	if n := strings.Count(h.logs.String(), "import failed"); n != 3 {
		t.Errorf("after a success the relapse logged %d in total, want 3:\n%s", n, h.logs.String())
	}
}

// TryImport returns nothing at all, which is how "an import cannot fail a tick"
// is a fact about the type rather than a rule someone has to remember.
func TestTryImportNeverPanics(t *testing.T) {
	h := newHarness(t)
	h.store.getErr = errors.New("boom")

	h.importer.TryImport(context.Background(), torrent.Torrent{}, h.dl)
	h.importer.TryImport(context.Background(), completed("/does/not/exist"), store.Download{})
}

// --- Refresh ----------------------------------------------------------------

func TestRefreshIsOncePerCallAndOnlyAfterAnImport(t *testing.T) {
	h := newHarness(t)
	refresher := &fakeRefresher{}
	h.importer.refresher = refresher
	ctx := context.Background()

	// Nothing imported yet: a tick must not queue a whole-library scan.
	h.importer.Refresh(ctx)
	if refresher.calls != 0 {
		t.Fatalf("refreshed %d times with nothing imported, want 0", refresher.calls)
	}

	// Three imports in one tick, then one Refresh: one scan, not three.
	for i, name := range []string{"a", "b", "c"} {
		h.store.movies[int64(i+1)] = store.Movie{ID: int64(i + 1), Title: "Film " + name, Year: 2014}
		content := h.download("release-"+name, "feature.mkv")
		if _, err := h.importer.Import(ctx, completed(content), store.Download{MovieID: int64(i + 1), TorrentHash: testHash}); err != nil {
			t.Fatalf("Import %s: %v", name, err)
		}
	}
	h.importer.Refresh(ctx)
	if refresher.calls != 1 {
		t.Errorf("refreshed %d times after three imports in one tick, want 1", refresher.calls)
	}

	// A second tick with nothing new must not refresh again.
	h.importer.Refresh(ctx)
	if refresher.calls != 1 {
		t.Errorf("refreshed %d times in total, want 1 — an idle tick must cost nothing", refresher.calls)
	}
}

func TestRefreshWithNoRefresherIsANoOp(t *testing.T) {
	h := newHarness(t) // built with a nil refresher: JELLYFIN_API_KEY unset
	content := h.download("release", "feature.mkv")
	ctx := context.Background()

	if _, err := h.importer.Import(ctx, completed(content), h.dl); err != nil {
		t.Fatalf("Import: %v", err)
	}
	h.importer.Refresh(ctx) // must not panic
}

// D15: whether a media server has noticed is softer than whether the file is in
// the library, so a failure changes nothing observable and does not re-arm.
func TestRefreshFailureChangesNothing(t *testing.T) {
	h := newHarness(t)
	refresher := &fakeRefresher{err: errors.New("jellyfin is down")}
	h.importer.refresher = refresher
	ctx := context.Background()

	content := h.download("release", "feature.mkv")
	if _, err := h.importer.Import(ctx, completed(content), h.dl); err != nil {
		t.Fatalf("Import: %v", err)
	}

	h.importer.Refresh(ctx)
	h.importer.Refresh(ctx)
	if refresher.calls != 1 {
		t.Errorf("refreshed %d times, want 1 — a failure must not re-arm and warn every tick", refresher.calls)
	}
	if !strings.Contains(h.logs.String(), "jellyfin refresh failed") {
		t.Error("the failure was not logged at all")
	}
	// The import stands.
	dst := filepath.Join(h.library, "Interstellar (2014)", "Interstellar (2014).mkv")
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("the import was undone by a failed refresh: %v", err)
	}
}

// --- harness ----------------------------------------------------------------

type harness struct {
	t         *testing.T
	root      string
	downloads string
	library   string
	store     *fakeStore
	importer  *Importer
	logs      *bytes.Buffer
	dl        store.Download
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	downloads := filepath.Join(root, "downloads")
	lib := filepath.Join(root, "library", "movies")
	for _, dir := range []string{downloads, lib} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	st := &fakeStore{movies: map[int64]store.Movie{
		1: {ID: 1, Title: "Interstellar", Year: 2014},
	}}
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Paths.Curator is empty: the fixtures are real paths on this machine, which
	// is the verbatim case. Translation has its own table test.
	im := New(st, lib, nil, log)
	im.now = func() time.Time { return importAt }

	return &harness{
		t: t, root: root, downloads: downloads, library: lib,
		store: st, importer: im, logs: logs,
		dl: store.Download{MovieID: 1, TorrentHash: testHash},
	}
}

// download builds a release folder holding one feature file and returns its path.
func (h *harness) download(folder, file string) string {
	h.t.Helper()
	content := filepath.Join(h.downloads, "complete", "curator", folder)
	sparseFile(h.t, filepath.Join(content, file), featureSize)
	return content
}

// snapshotDownloads records every file under the downloads root by relative
// path, size and inode. Paired with assertDownloadsIntact it is D8 — no source
// file is ever deleted, moved or truncated — as an assertion rather than a
// promise. The link count is deliberately not part of it: it legitimately goes
// from 1 to 2, which is the whole point.
func (h *harness) snapshotDownloads() map[string]string {
	h.t.Helper()
	snapshot := map[string]string{}

	err := filepath.WalkDir(h.downloads, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(h.downloads, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		snapshot[rel] = fmt.Sprintf("%d:%d", info.Size(), inodeOf(h.t, p))
		return nil
	})
	if err != nil {
		h.t.Fatalf("snapshot downloads: %v", err)
	}
	return snapshot
}

func (h *harness) assertDownloadsIntact(before map[string]string) {
	h.t.Helper()
	after := h.snapshotDownloads()

	for rel, want := range before {
		got, ok := after[rel]
		if !ok {
			h.t.Errorf("%s is gone from the download directory; D8 says a source file is never deleted", rel)
			continue
		}
		if got != want {
			h.t.Errorf("%s changed: %s -> %s", rel, want, got)
		}
	}
	for rel := range after {
		if _, ok := before[rel]; !ok {
			h.t.Errorf("%s appeared in the download directory; nothing should be written there", rel)
		}
	}
}

// completed is a finished torrent as a backend hands one over: curator's own
// state vocabulary and its upper-case hash, whichever client produced it.
func completed(contentPath string) torrent.Torrent {
	return torrent.Torrent{
		Hash:        testHash,
		Name:        filepath.Base(contentPath),
		State:       torrent.StateCompleted,
		Progress:    1,
		ContentPath: contentPath,
		Category:    "curator",
	}
}

// --- fakes ------------------------------------------------------------------

type markCall struct {
	hash string
	path string
	size int64
	at   time.Time
}

type fakeStore struct {
	movies  map[int64]store.Movie
	getErr  error
	markErr error
	marked  []markCall
}

func (f *fakeStore) GetMovie(_ context.Context, id int64) (store.Movie, error) {
	if f.getErr != nil {
		return store.Movie{}, f.getErr
	}
	m, ok := f.movies[id]
	if !ok {
		return store.Movie{}, store.ErrNotFound
	}
	return m, nil
}

func (f *fakeStore) MarkImported(_ context.Context, hash, libraryPath string, size int64, at time.Time) (store.Movie, error) {
	if f.markErr != nil {
		return store.Movie{}, f.markErr
	}
	f.marked = append(f.marked, markCall{hash: hash, path: libraryPath, size: size, at: at})
	return store.Movie{ID: 1, Title: "Interstellar", Year: 2014, Status: store.StatusImported, LibraryPath: &libraryPath}, nil
}

type fakeRefresher struct {
	calls int
	err   error
}

func (f *fakeRefresher) RefreshLibrary(context.Context) error {
	f.calls++
	return f.err
}

// --- small helpers ----------------------------------------------------------

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

// sparseFile creates a file of the given apparent size without writing its
// blocks, so the real 50 MiB feature floor is exercised at no cost in disk or
// time.
func sparseFile(t *testing.T, path string, size int64) string {
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

// nlinkOf and inodeOf convert explicitly, because syscall.Stat_t.Nlink is
// uint16 on darwin and uint64 on linux — an inline comparison compiles on a
// laptop and breaks GOOS=linux GOARCH=arm64 go vet, which is how this ships.
func nlinkOf(t *testing.T, path string) uint64 {
	t.Helper()
	st, ok := statOf(t, path).Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no syscall.Stat_t on this platform")
	}
	return uint64(st.Nlink)
}

func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	st, ok := statOf(t, path).Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no syscall.Stat_t on this platform")
	}
	return uint64(st.Ino)
}

func writeAt(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteAt([]byte(text), 0); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readAt(t *testing.T, path string, n int) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(buf)
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// readNames is names over a directory that has to be there. os.ReadDir sorts, so
// the result is comparable against a written-out list.
func readNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	return names(entries)
}

func equalStrings(got, want []string) bool { return slices.Equal(got, want) }

// writeSubtitle creates a real (tiny) subtitle file, making its directory on the
// way. Unlike the feature fixtures these are not sparse: subtitles have no size
// floor, and a hardlink assertion wants an inode with something in it.
func writeSubtitle(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	body := "1\n00:00:01,000 --> 00:00:02,000\n" + name + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}
