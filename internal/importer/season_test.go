package importer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DulsaraNethmin/curator/internal/library"
	"github.com/DulsaraNethmin/curator/internal/store"
)

// Television, imported. The film half of this package is proven above and is
// deliberately untouched by any of it: what these tests pin is that the ROW
// decides which of the two shapes a download is read as, that a season pack
// becomes N hardlinks rather than one enormous "movie", and that D8 holds per
// file — same inode is success, a different file is a refusal, and a source is
// never moved, copied over or deleted.

// The season pack, which is the whole point of T93. Before it, FindFeature took
// the largest file, logged "content path held more than one video" and left the
// rest of the season in the download folder.
func TestImportFilesEveryEpisodeOfASeasonPack(t *testing.T) {
	h := newHarness(t)
	h.show()
	content := h.seasonPack("Severance.S01.1080p.WEB-DL",
		"Severance.S01E01.1080p.WEB-DL.mkv",
		"Severance.S01E02.1080p.WEB-DL.mkv",
		"Severance.S01E03.1080p.WEB-DL.mkv")

	before := h.snapshotDownloads()

	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err != nil {
		t.Fatalf("Import: %v", err)
	}

	showDir := filepath.Join(h.tv, "Severance (2022)")
	seasonDir := filepath.Join(showDir, "Season 01")

	// Jellyfin's own layout: the show folder, a zero-padded season folder, and
	// files a scanner reads without curator installed at all.
	if got := readNames(t, showDir); !equalStrings(got, []string{"Season 01"}) {
		t.Errorf("the show folder holds %v, want one season folder", got)
	}
	want := []string{
		"Severance (2022) - S01E01.mkv",
		"Severance (2022) - S01E02.mkv",
		"Severance (2022) - S01E03.mkv",
	}
	if got := readNames(t, seasonDir); !equalStrings(got, want) {
		t.Errorf("the season folder holds %v, want %v", got, want)
	}

	// D8, per file: one inode with two names, three times over.
	for i, name := range want {
		src := filepath.Join(content, fmt.Sprintf("Severance.S01E%02d.1080p.WEB-DL.mkv", i+1))
		dst := filepath.Join(seasonDir, name)
		if !os.SameFile(statOf(t, src), statOf(t, dst)) {
			t.Errorf("%s is a copy, not a hardlink", name)
		}
		if n := nlinkOf(t, dst); n != 2 {
			t.Errorf("%s: link count = %d, want 2", name, n)
		}
	}

	// The store is told the SHOW folder — not a season, not a file — because
	// that is the identity key every future scan matches on and what makes a
	// second season fold into this row (docs/decisions.md D48).
	if len(h.store.marked) != 1 {
		t.Fatalf("MarkImported called %d times, want 1", len(h.store.marked))
	}
	if got := h.store.marked[0].path; got != showDir {
		t.Errorf("library_path = %q, want the show folder %q", got, showDir)
	}
	if got, want := h.store.marked[0].size, int64(3*featureSize); got != want {
		t.Errorf("size = %d, want the three episodes summed, %d", got, want)
	}
	if !strings.Contains(h.logs.String(), "episodes=3") {
		t.Errorf("the import line does not say how many episodes landed:\n%s", h.logs.String())
	}

	h.assertDownloadsIntact(before)
}

// Two seasons, two downloads, one row and one folder — the accumulation D48
// argues for. And the size_bytes decision: the row carries the WHOLE show, not
// whichever season arrived last, so it never disagrees with what a scan of the
// same folder would write.
func TestASecondSeasonJoinsTheShowFolderAndTheSizeCountsBoth(t *testing.T) {
	h := newHarness(t)
	h.show()
	ctx := context.Background()

	first := h.seasonPack("Severance.S01", "Severance.S01E01.mkv", "Severance.S01E02.mkv")
	if _, err := h.importer.Import(ctx, completed(first), h.dl); err != nil {
		t.Fatalf("season 1: %v", err)
	}

	second := h.seasonPack("Severance.S02", "Severance.S02E01.mkv")
	if _, err := h.importer.Import(ctx, completed(second), h.dl); err != nil {
		t.Fatalf("season 2: %v", err)
	}

	showDir := filepath.Join(h.tv, "Severance (2022)")
	if got := readNames(t, showDir); !equalStrings(got, []string{"Season 01", "Season 02"}) {
		t.Errorf("the show folder holds %v, want both seasons", got)
	}
	if got := readNames(t, filepath.Join(showDir, "Season 02")); !equalStrings(got, []string{"Severance (2022) - S02E01.mkv"}) {
		t.Errorf("season 2 holds %v", got)
	}

	if len(h.store.marked) != 2 {
		t.Fatalf("MarkImported called %d times, want 2", len(h.store.marked))
	}
	for i, call := range h.store.marked {
		if call.path != showDir {
			t.Errorf("import %d recorded %q, want the show folder %q", i+1, call.path, showDir)
		}
	}
	// The clobber this decision exists to prevent: season 2 alone is one file.
	if got, want := h.store.marked[1].size, int64(3*featureSize); got != want {
		t.Errorf("after season 2 the row says %d bytes, want the whole show, %d", got, want)
	}
}

// The crashed-run retry path for a season: D14 tries again on every tick, and
// library.Link treats a destination that is already our own link as success. A
// re-import must therefore be a no-op on disk rather than an error or a second
// copy.
func TestReImportingASeasonIsIdempotent(t *testing.T) {
	h := newHarness(t)
	h.show()
	ctx := context.Background()
	content := h.seasonPack("Severance.S01", "Severance.S01E01.mkv", "Severance.S01E02.mkv")

	if _, err := h.importer.Import(ctx, completed(content), h.dl); err != nil {
		t.Fatalf("first Import: %v", err)
	}
	seasonDir := filepath.Join(h.tv, "Severance (2022)", "Season 01")
	inodes := map[string]uint64{}
	for _, name := range readNames(t, seasonDir) {
		inodes[name] = inodeOf(t, filepath.Join(seasonDir, name))
	}

	if _, err := h.importer.Import(ctx, completed(content), h.dl); err != nil {
		t.Fatalf("second Import: %v — this is the crashed-run retry path", err)
	}

	names := readNames(t, seasonDir)
	if len(names) != 2 {
		t.Fatalf("the season folder holds %v after re-importing, want the same two files", names)
	}
	for _, name := range names {
		path := filepath.Join(seasonDir, name)
		if inodeOf(t, path) != inodes[name] {
			t.Errorf("%s is a different inode after re-importing", name)
		}
		if n := nlinkOf(t, path); n != 2 {
			t.Errorf("%s: link count = %d after re-importing, want still 2", name, n)
		}
	}
}

// Phase 11's verification step 7, and the shape a manual grab actually takes: a
// single episode dropped into a season that is already there. content_path is
// the FILE for a single-file torrent, which FindEpisodes accepts for the reason
// FindFeature does.
func TestASingleEpisodeJoinsASeasonThatIsAlreadyThere(t *testing.T) {
	h := newHarness(t)
	h.show()
	ctx := context.Background()

	pack := h.seasonPack("Severance.S01", "Severance.S01E01.mkv", "Severance.S01E02.mkv")
	if _, err := h.importer.Import(ctx, completed(pack), h.dl); err != nil {
		t.Fatalf("the season pack: %v", err)
	}

	// A new episode, on its own, as its own torrent.
	single := sparseFile(t, filepath.Join(h.downloads, "complete", "curator", "Severance.S01E03.WEB.mkv"), featureSize)
	if _, err := h.importer.Import(ctx, completed(single), h.dl); err != nil {
		t.Fatalf("the single episode: %v", err)
	}

	seasonDir := filepath.Join(h.tv, "Severance (2022)", "Season 01")
	want := []string{
		"Severance (2022) - S01E01.mkv",
		"Severance (2022) - S01E02.mkv",
		"Severance (2022) - S01E03.mkv",
	}
	if got := readNames(t, seasonDir); !equalStrings(got, want) {
		t.Errorf("the season folder holds %v, want %v", got, want)
	}
	if !os.SameFile(statOf(t, single), statOf(t, filepath.Join(seasonDir, "Severance (2022) - S01E03.mkv"))) {
		t.Error("the single episode is a copy, not a hardlink")
	}
	if got, want := h.store.marked[1].size, int64(3*featureSize); got != want {
		t.Errorf("size = %d after the single episode, want the whole show, %d", got, want)
	}
}

// The same episode arriving twice — the re-grab. It is the SAME file on disk,
// so library.Link's os.SameFile path answers success and nothing is duplicated
// or refused.
func TestTheSameEpisodeGrabbedAgainIsSuccessAndNotADuplicate(t *testing.T) {
	h := newHarness(t)
	h.show()
	ctx := context.Background()

	pack := h.seasonPack("Severance.S01", "Severance.S01E01.mkv", "Severance.S01E02.mkv")
	if _, err := h.importer.Import(ctx, completed(pack), h.dl); err != nil {
		t.Fatalf("the season pack: %v", err)
	}

	again := filepath.Join(pack, "Severance.S01E02.mkv")
	if _, err := h.importer.Import(ctx, completed(again), h.dl); err != nil {
		t.Fatalf("re-grabbing one episode of a season already imported: %v", err)
	}

	seasonDir := filepath.Join(h.tv, "Severance (2022)", "Season 01")
	if got := readNames(t, seasonDir); len(got) != 2 {
		t.Errorf("the season folder holds %v, want the same two files", got)
	}
	if n := nlinkOf(t, filepath.Join(seasonDir, "Severance (2022) - S01E02.mkv")); n != 2 {
		t.Errorf("link count = %d, want still 2 — the re-grab must not add a name", n)
	}
}

// A DIFFERENT file at an episode's destination is refused and never
// overwritten: the thing already there might be the good copy. It is the one
// case library.Link exists to say no to, and per file since T93.
func TestADifferentFileAtAnEpisodesDestinationIsRefused(t *testing.T) {
	h := newHarness(t)
	h.show()

	seasonDir := filepath.Join(h.tv, "Severance (2022)", "Season 01")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	squatter := filepath.Join(seasonDir, "Severance (2022) - S01E02.mkv")
	if err := os.WriteFile(squatter, []byte("somebody else's release"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	content := h.seasonPack("Severance.S01",
		"Severance.S01E01.mkv", "Severance.S01E02.mkv", "Severance.S01E03.mkv")

	_, err := h.importer.Import(context.Background(), completed(content), h.dl)
	if err == nil {
		t.Fatal("the import succeeded over a file that was already there")
	}
	if !strings.Contains(err.Error(), "not ours to overwrite") {
		t.Errorf("err = %v, want library.Link's refusal", err)
	}

	// Untouched, byte for byte.
	body, err := os.ReadFile(squatter)
	if err != nil || string(body) != "somebody else's release" {
		t.Errorf("the file that was already there is now %q, %v", body, err)
	}
	// And the row is NOT recorded: the show is half filed, the next tick tries
	// again, and a scan would find whatever did land.
	if len(h.store.marked) != 0 {
		t.Error("the show was recorded as imported despite an episode being refused")
	}
}

// A repack beside the original: two files, one episode. FindEpisodes returns
// both on purpose and says the choice is the caller's, so the caller makes it —
// the larger wins, which is FindFeature's rule for the same question about a
// film, and the other is reported.
//
// Letting both through would link one and then have library.Link refuse the
// other as "a different file is already there", failing the whole pack over a
// conflict this import created in it.
func TestARepackBesideTheOriginalIsOneEpisodeAndTheLargerWins(t *testing.T) {
	h := newHarness(t)
	h.show()
	content := filepath.Join(h.downloads, "complete", "curator", "Severance.S01")
	original := sparseFile(t, filepath.Join(content, "Severance.S01E01.WEB.mkv"), featureSize)
	repack := sparseFile(t, filepath.Join(content, "Severance.S01E01.REPACK.WEB.mkv"), featureSize*2)

	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err != nil {
		t.Fatalf("Import: %v", err)
	}

	seasonDir := filepath.Join(h.tv, "Severance (2022)", "Season 01")
	dst := filepath.Join(seasonDir, "Severance (2022) - S01E01.mkv")
	if got := readNames(t, seasonDir); !equalStrings(got, []string{"Severance (2022) - S01E01.mkv"}) {
		t.Errorf("the season folder holds %v, want one file", got)
	}
	if !os.SameFile(statOf(t, repack), statOf(t, dst)) {
		t.Error("the smaller file won")
	}
	if os.SameFile(statOf(t, original), statOf(t, dst)) {
		t.Error("the original won; the larger repack should have")
	}
	if !strings.Contains(h.logs.String(), "same episode") {
		t.Errorf("the file that was passed over was not reported:\n%s", h.logs.String())
	}
	if !strings.Contains(h.logs.String(), "episodes=1") {
		t.Errorf("two files for one episode were counted as two:\n%s", h.logs.String())
	}
	// The size is one episode's, not both files' — it is the show folder that is
	// measured, and only one file is in it.
	if got, want := h.store.marked[0].size, int64(featureSize*2); got != want {
		t.Errorf("size = %d, want the one file that landed, %d", got, want)
	}
}

// ErrNoEpisode is the second sentinel beside ErrNoVideo and it is not a failed
// download either: the bytes are there and the NAMES are the problem, which a
// rename fixes and the next tick picks up.
func TestAShowWithNothingNamedLikeAnEpisodeIsErrNoEpisodeAndWritesNothing(t *testing.T) {
	h := newHarness(t)
	h.show()
	content := filepath.Join(h.downloads, "complete", "curator", "Severance.COMPLETE")
	sparseFile(t, filepath.Join(content, "part1.mkv"), featureSize)
	sparseFile(t, filepath.Join(content, "part2.mkv"), featureSize)

	_, err := h.importer.Import(context.Background(), completed(content), h.dl)
	if !errors.Is(err, library.ErrNoEpisode) {
		t.Fatalf("err = %v, want library.ErrNoEpisode", err)
	}
	if len(h.store.marked) != 0 {
		t.Error("the store was written to; the row must stay completed for the next tick")
	}
	if entries := readNames(t, h.tv); len(entries) != 0 {
		t.Errorf("the television library holds %v; a failed import must create nothing", entries)
	}
}

// The ordering rule, for a show: no folder — and no season folder — until a
// source file has been chosen AND named. A show whose title cannot be a folder
// name creates nothing at all.
func TestAShowCreatesNoFolderUntilThereIsSomethingToPutInIt(t *testing.T) {
	h := newHarness(t)
	h.store.movies[1] = store.Movie{ID: 1, Title: "Severance/Evil", Year: 2022, MediaType: store.MediaTypeTV}
	h.seasonPack("Severance.S01", "Severance.S01E01.mkv")

	content := filepath.Join(h.downloads, "complete", "curator", "Severance.S01")
	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err == nil {
		t.Fatal("a title with a path separator in it was accepted")
	}
	if entries := readNames(t, h.tv); len(entries) != 0 {
		t.Errorf("the television library holds %v", entries)
	}
	if entries := readNames(t, h.library); len(entries) != 0 {
		t.Errorf("the film library holds %v — a show must never land there", entries)
	}
}

// --- the subtitles ----------------------------------------------------------

// Per episode, named off the episode: "Show (Year) - S01E01.en.srt" beside
// "Show (Year) - S01E01.mkv", which is the convention Jellyfin, Plex and VLC
// read. Each subtitle goes with the episode ITS OWN NAME says it belongs to —
// giving every episode every subtitle would put episode 2's dialogue on episode
// 1.
func TestEachEpisodeGetsTheSubtitleThatNamesIt(t *testing.T) {
	h := newHarness(t)
	h.show()
	content := h.seasonPack("Severance.S01", "Severance.S01E01.mkv", "Severance.S01E02.mkv")
	first := writeSubtitle(t, content, "Severance.S01E01.en.srt")
	second := writeSubtitle(t, filepath.Join(content, "Subs"), "Severance.S01E02.eng.srt")
	// Named after no episode at all: it cannot be filed without guessing, so it
	// stays in the download.
	writeSubtitle(t, content, "readme.en.srt")

	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err != nil {
		t.Fatalf("Import: %v", err)
	}

	seasonDir := filepath.Join(h.tv, "Severance (2022)", "Season 01")
	want := []string{
		"Severance (2022) - S01E01.en.srt",
		"Severance (2022) - S01E01.mkv",
		"Severance (2022) - S01E02.en.srt",
		"Severance (2022) - S01E02.mkv",
	}
	if got := readNames(t, seasonDir); !equalStrings(got, want) {
		t.Errorf("the season folder holds %v, want %v", got, want)
	}
	for src, dst := range map[string]string{
		first:  filepath.Join(seasonDir, "Severance (2022) - S01E01.en.srt"),
		second: filepath.Join(seasonDir, "Severance (2022) - S01E02.en.srt"),
	} {
		if !os.SameFile(statOf(t, src), statOf(t, dst)) {
			t.Errorf("%s went to the wrong episode, or is a copy", filepath.Base(src))
		}
	}
	if !strings.Contains(h.logs.String(), "subtitles=2") {
		t.Errorf("the import line does not count the subtitles:\n%s", h.logs.String())
	}
	// The one that names no episode is reported rather than dropped in silence.
	if !strings.Contains(h.logs.String(), "name no episode") {
		t.Errorf("the unfilable subtitle was not reported:\n%s", h.logs.String())
	}
}

// One episode in the download is the film's case exactly: there is only one
// thing a subtitle can belong to, so it does not have to say so. This is what a
// single-episode release actually ships — Subs/2_English.srt, named after
// nothing.
func TestASingleEpisodeTakesTheSubtitlesThatNameNothing(t *testing.T) {
	h := newHarness(t)
	h.show()
	content := h.seasonPack("Severance.S01E05.1080p", "Severance.S01E05.1080p.mkv")
	writeSubtitle(t, filepath.Join(content, "Subs"), "2_English.srt")

	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err != nil {
		t.Fatalf("Import: %v", err)
	}

	seasonDir := filepath.Join(h.tv, "Severance (2022)", "Season 01")
	want := []string{"Severance (2022) - S01E05.en.srt", "Severance (2022) - S01E05.mkv"}
	if got := readNames(t, seasonDir); !equalStrings(got, want) {
		t.Errorf("the season folder holds %v, want %v", got, want)
	}
}

// --- the roots ---------------------------------------------------------------

// LIBRARY_TV unset is television off, and it must change NOTHING about films.
// The show is refused rather than filed under the movies, which is the failure
// that would otherwise be discovered by a scan deleting the row.
func TestWithNoTelevisionRootAShowIsRefusedAndFilmsAreUntouched(t *testing.T) {
	h := newHarnessWithTV(t, false)
	ctx := context.Background()

	// The film half, byte for byte what it is with television configured.
	film := h.download("Interstellar.2014.1080p", "Interstellar.2014.1080p.mkv")
	if _, err := h.importer.Import(ctx, completed(film), h.dl); err != nil {
		t.Fatalf("a film import failed with LIBRARY_TV unset: %v", err)
	}
	dst := filepath.Join(h.library, "Interstellar (2014)", "Interstellar (2014).mkv")
	if n := nlinkOf(t, dst); n != 2 {
		t.Errorf("link count = %d, want 2", n)
	}

	// The television half.
	h.show()
	pack := h.seasonPack("Severance.S01", "Severance.S01E01.mkv")
	_, err := h.importer.Import(ctx, completed(pack), h.dl)
	if !errors.Is(err, library.ErrOutsideRoot) {
		t.Fatalf("err = %v, want library.ErrOutsideRoot", err)
	}
	if !strings.Contains(err.Error(), "LIBRARY_TV") {
		t.Errorf("err = %v, want it to name the variable that is unset", err)
	}
	if got := readNames(t, h.tv); len(got) != 0 {
		t.Errorf("something was written to the television root that was never configured: %v", got)
	}
	if got := readNames(t, h.library); !equalStrings(got, []string{"Interstellar (2014)"}) {
		t.Errorf("the film library holds %v — a show must never be filed with the films", got)
	}
	if len(h.store.marked) != 1 {
		t.Errorf("MarkImported was called %d times, want only the film's", len(h.store.marked))
	}
}

// media_type is NOT NULL DEFAULT 'movie' and validated on every write, so an
// unrecognised value is a bug or a hand-edited database. Filing it with the
// films is the one answer that loses data quietly.
func TestAnUnrecognisedMediaTypeIsRefusedRatherThanFiledWithTheFilms(t *testing.T) {
	for _, mediaType := range []string{"", "show", "MOVIE", "series"} {
		h := newHarness(t)
		h.store.movies[1] = store.Movie{ID: 1, Title: "Interstellar", Year: 2014, MediaType: mediaType}
		content := h.download("release", "feature.mkv")

		_, err := h.importer.Import(context.Background(), completed(content), h.dl)
		if !errors.Is(err, library.ErrOutsideRoot) {
			t.Errorf("media_type %q: err = %v, want a refusal", mediaType, err)
		}
		if got := readNames(t, h.library); len(got) != 0 {
			t.Errorf("media_type %q: the film library holds %v", mediaType, got)
		}
		if len(h.store.marked) != 0 {
			t.Errorf("media_type %q: the store was written to", mediaType)
		}
	}
}

// --- fixtures ---------------------------------------------------------------

// seasonPack builds a release folder holding one sparse file per name and
// returns its path. The files clear the real 50 MiB floor, so the picker's
// sample rule is exercised rather than lowered.
func (h *harness) seasonPack(folder string, files ...string) string {
	h.t.Helper()
	content := filepath.Join(h.downloads, "complete", "curator", folder)
	for _, name := range files {
		sparseFile(h.t, filepath.Join(content, name), featureSize)
	}
	return content
}

// D19's delete path, which is the half T93 nearly shipped broken.
//
// RemoveFromLibrary took only a path and checked it against LIBRARY_MOVIES, so
// a show answered ErrOutsideRoot — and download.Service reads that sentinel as
// "there is nothing of ours in there to remove", takes the rows and leaves the
// disk alone. Rows gone, the whole show still on disk, and the only trace a Warn
// in a log nobody reads before clicking delete. The media type is what makes the
// answer depend on where the title actually lives.
func TestDeletingAShowRemovesItsFolderRatherThanOrphaningIt(t *testing.T) {
	h := newHarness(t)
	h.show()
	content := h.seasonPack("Severance.S01.1080p.WEB-DL",
		"Severance.S01E01.1080p.WEB-DL.mkv",
		"Severance.S01E02.1080p.WEB-DL.mkv")

	if _, err := h.importer.Import(context.Background(), completed(content), h.dl); err != nil {
		t.Fatalf("import: %v", err)
	}
	showDir := filepath.Join(h.tv, "Severance (2022)")
	if _, err := os.Stat(showDir); err != nil {
		t.Fatalf("the show folder is not there to delete: %v", err)
	}

	if err := h.importer.RemoveFromLibrary(store.MediaTypeTV, showDir); err != nil {
		t.Fatalf("RemoveFromLibrary for a show: %v — this is the ErrOutsideRoot that orphaned it", err)
	}
	if _, err := os.Stat(showDir); !os.IsNotExist(err) {
		t.Fatalf("the show folder survived the delete: %v", err)
	}

	// The download is untouched, because a hardlink is one of two names for an
	// inode and the other one is qBittorrent's to remove (D19).
	if _, err := os.Stat(filepath.Join(content, "Severance.S01E01.1080p.WEB-DL.mkv")); err != nil {
		t.Errorf("the source was removed with the library copy: %v", err)
	}
}

// The two refusals, which stay refusals: with no television root there is
// genuinely nothing of curator's to delete, so the caller may take the rows
// rather than leaving one permanently undeletable.
func TestDeletingAShowWithNoTelevisionRootRefusesRatherThanReachingIntoTheFilms(t *testing.T) {
	h := newHarnessWithTV(t, false)

	// A path that would be inside the FILMS root if the media type were ignored.
	// Nothing may be removed on the strength of a show's row.
	trap := filepath.Join(h.library, "Severance (2022)")
	if err := os.MkdirAll(trap, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	err := h.importer.RemoveFromLibrary(store.MediaTypeTV, trap)
	if !errors.Is(err, library.ErrOutsideRoot) {
		t.Fatalf("RemoveFromLibrary = %v, want ErrOutsideRoot so the caller takes the rows and leaves the disk", err)
	}
	if _, statErr := os.Stat(trap); statErr != nil {
		t.Errorf("a folder under LIBRARY_MOVIES was removed on a show's behalf: %v", statErr)
	}

	if err := h.importer.RemoveFromLibrary("gramophone", trap); !errors.Is(err, library.ErrOutsideRoot) {
		t.Errorf("an unrecognised media type = %v, want ErrOutsideRoot", err)
	}
	if _, statErr := os.Stat(trap); statErr != nil {
		t.Errorf("a folder was removed for an unrecognised media type: %v", statErr)
	}
}
