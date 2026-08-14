package library

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// --- what a sidecar's own name says about it --------------------------------

// The naming convention this whole feature exists to produce, read from the
// forms release groups actually ship.
func TestSidecarNameIsBuiltOffTheFeature(t *testing.T) {
	cases := []struct {
		name    string
		sidecar string
		want    string
	}{
		// The plain conventions, both spellings of the code.
		{"a two-letter code", "Movie.en.srt", "Interstellar (2014).en.srt"},
		{"a three-letter code", "Movie.eng.srt", "Interstellar (2014).en.srt"},
		{"the language spelled out", "Movie.English.srt", "Interstellar (2014).en.srt"},

		// The Subs/ folder form, which is the reason the rename is worth doing
		// at all: nothing associates "2_English.srt" with the film until it is
		// named off it.
		{"a numbered track in a Subs folder", "2_English.srt", "Interstellar (2014).en.srt"},
		{"a numbered short code", "3_Fre.srt", "Interstellar (2014).fr.srt"},
		{"nothing but the language", "English.srt", "Interstellar (2014).en.srt"},

		// The flag is kept, because dropping it makes two genuinely different
		// tracks collide.
		{"forced", "Movie.en.forced.srt", "Interstellar (2014).en.forced.srt"},
		{"hearing impaired", "Movie.eng.sdh.srt", "Interstellar (2014).en.sdh.srt"},
		{"both flags", "Movie.en.sdh.cc.srt", "Interstellar (2014).en.sdh.cc.srt"},

		// A full release name in front of the language changes nothing: only the
		// tokens at the END are read.
		{"a whole release name in front", "Movie.2014.1080p.BluRay.x264.eng.srt", "Interstellar (2014).en.srt"},

		// No language curator recognises: the feature's stem and nothing else,
		// which is what a player reads as "the subtitle for this film".
		{"an unrecognised name", "Movie.2014.1080p.BluRay.x264-GROUP.srt", "Interstellar (2014).srt"},
		{"a bare stem", "Movie.srt", "Interstellar (2014).srt"},
		{"a flag with no language in front of it", "Movie.forced.srt", "Interstellar (2014).srt"},
		{"nothing but a flag", "forced.srt", "Interstellar (2014).srt"},

		// The other extensions are named the same way; only their SERVING
		// differs, and that is internal/api's business.
		{"an ass keeps its extension", "Movie.en.ass", "Interstellar (2014).en.ass"},
		{"a vtt keeps its extension", "Movie.en.vtt", "Interstellar (2014).en.vtt"},
		{"the extension is lower-cased", "Movie.EN.SRT", "Interstellar (2014).en.srt"},
	}

	for _, c := range cases {
		got, err := SidecarName("Interstellar (2014).mkv", filepath.Join("/downloads", c.sidecar))
		if err != nil {
			t.Errorf("%s: SidecarName(%q): %v", c.name, c.sidecar, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: SidecarName(%q) = %q, want %q", c.name, c.sidecar, got, c.want)
		}
	}
}

func TestSidecarNameRefusesSomethingThatIsNotASubtitle(t *testing.T) {
	for _, name := range []string{"Movie.mkv", "Movie.nfo", "Movie", "Movie.txt"} {
		if got, err := SidecarName("Interstellar (2014).mkv", name); err == nil {
			t.Errorf("SidecarName(%q) = %q, want an error", name, got)
		}
	}
}

// The property the importer's containment check is there to keep true: the
// destination's last component is the FEATURE's name plus tokens out of closed
// tables, so nothing a release group chose to call a file reaches the path.
func TestASidecarNameCanNeverCarryASeparatorOutOfTheSourceName(t *testing.T) {
	hostile := []string{
		"..srt",
		"...en.srt",
		"..%2F..%2Fetc%2Fpasswd.en.srt",
		"Movie..\\..\\windows.en.srt",
		"Movie.$(rm -rf ~).en.srt",
		"Movie.‮exe.en.srt",
	}
	for _, name := range hostile {
		got, err := SidecarName("Interstellar (2014).mkv", filepath.Join("/downloads", name))
		if err != nil {
			t.Errorf("SidecarName(%q): %v", name, err)
			continue
		}
		if got != "Interstellar (2014).en.srt" && got != "Interstellar (2014).srt" {
			t.Errorf("SidecarName(%q) = %q — something out of the source name reached the destination", name, got)
		}
	}
}

// The suffix is what a player reads to label a track, so it has to be a code and
// not a spelling.
func TestLanguageNameTakesACodeAndNothingElse(t *testing.T) {
	if got := LanguageName("en"); got != "English" {
		t.Errorf(`LanguageName("en") = %q, want "English"`, got)
	}
	if got := LanguageName("EN"); got != "English" {
		t.Errorf(`LanguageName("EN") = %q, want "English" — the code is compared case-folded`, got)
	}
	// An alias is how a NAME is recognised, not a code curator ever writes, and
	// answering to it would be a second looser spelling of one lookup.
	for _, alias := range []string{"eng", "english", "", "xx", "klingon"} {
		if got := LanguageName(alias); got != "" {
			t.Errorf("LanguageName(%q) = %q, want empty", alias, got)
		}
	}
}

// "hi" is both the hearing-impaired marker and Hindi's ISO 639-1 code, and the
// token in front of it is what settles which. Both spellings are real files.
func TestHIIsAFlagAfterALanguageAndHindiOtherwise(t *testing.T) {
	cases := []struct {
		sidecar string
		want    string
	}{
		// What the Backrooms release in this build's downloads folder actually
		// ships. `eng` in front of it makes the `HI` a marker, and it is written
		// as "sdh" because that is the spelling Jellyfin acts on.
		{"SDH.eng.HI.srt", "Interstellar (2014).en.sdh.srt"},
		{"Movie.en.hi.srt", "Interstellar (2014).en.sdh.srt"},
		// Nothing in front of it says otherwise, so it is the language.
		{"Movie.hi.srt", "Interstellar (2014).hi.srt"},
		{"hi.srt", "Interstellar (2014).hi.srt"},
		{"2_Hindi.srt", "Interstellar (2014).hi.srt"},
		// The unambiguous spelling behaves the same way from either side.
		{"Movie.en.sdh.srt", "Interstellar (2014).en.sdh.srt"},
		// Two spellings of one flag are one flag.
		{"Movie.en.sdh.hi.srt", "Interstellar (2014).en.sdh.srt"},
	}
	for _, c := range cases {
		got, err := SidecarName("Interstellar (2014).mkv", c.sidecar)
		if err != nil {
			t.Errorf("SidecarName(%q): %v", c.sidecar, err)
			continue
		}
		if got != c.want {
			t.Errorf("SidecarName(%q) = %q, want %q", c.sidecar, got, c.want)
		}
	}
}

// The four subtitles the Backrooms release actually ships, plus the one beside
// the feature, resolved as a set — because what matters is not any one name but
// that no two of them collide, which is what decides whether all five land.
func TestTheRealBackroomsSidecarsAllGetDistinctNames(t *testing.T) {
	sources := []string{
		"Backrooms.2026.1080p.WEBRip.x264.AAC5.1-[YTS.GG - YTS.BZ].srt",
		"Subs/English.srt",
		"Subs/Latin American.spa.srt",
		"Subs/Saudi Arabia.ara.srt",
		"Subs/SDH.eng.HI.srt",
	}
	want := []string{
		"Backrooms (2026).srt",
		"Backrooms (2026).en.srt",
		"Backrooms (2026).es.srt",
		"Backrooms (2026).ar.srt",
		"Backrooms (2026).en.sdh.srt",
	}

	seen := map[string]string{}
	for i, src := range sources {
		got, err := SidecarName("Backrooms (2026).mp4", src)
		if err != nil {
			t.Fatalf("SidecarName(%q): %v", src, err)
		}
		if got != want[i] {
			t.Errorf("SidecarName(%q) = %q, want %q", src, got, want[i])
		}
		if first, clash := seen[got]; clash {
			t.Errorf("%q and %q both want %q, so one of them would be skipped", first, src, got)
		}
		seen[got] = src
	}
}

// --- finding them in a download ---------------------------------------------

func TestFindSidecarsLooksBesideTheFeatureAndInSubs(t *testing.T) {
	root := t.TempDir()
	content := filepath.Join(root, "Interstellar.2014.1080p.BluRay.x264-GROUP")
	feature := touch(t, content, "Interstellar.2014.1080p.mkv")
	beside := touch(t, content, "Interstellar.2014.1080p.en.srt")
	inSubs := touch(t, filepath.Join(content, "Subs"), "2_English.srt")
	inSubtitles := touch(t, filepath.Join(content, "Subtitles"), "3_French.srt")

	// None of these is a subtitle, and one of them is the trap: a folder that
	// FindFeature skips and this one reads has to still refuse what is not a
	// sidecar.
	touch(t, content, "RARBG.txt")
	touch(t, content, "Interstellar.nfo")
	touch(t, filepath.Join(content, "Subs"), ".DS_Store")
	touch(t, filepath.Join(content, "Sample"), "sample.en.srt")

	// Two levels down, which is a folder nobody's convention uses.
	touch(t, filepath.Join(content, "Subs", "Extra"), "4_German.srt")

	got, err := FindSidecars(content, feature)
	if err != nil {
		t.Fatalf("FindSidecars: %v", err)
	}
	want := []string{beside, inSubs, inSubtitles}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("FindSidecars =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Sorted, because the importer keeps the FIRST of two colliding names and two
// directories were read to build the list — without an order, which subtitle
// survives an import would depend on the filesystem.
func TestFindSidecarsIsOrdered(t *testing.T) {
	root := t.TempDir()
	content := filepath.Join(root, "Release")
	feature := touch(t, content, "Movie.mkv")
	touch(t, content, "z.en.srt")
	touch(t, content, "a.en.srt")
	touch(t, filepath.Join(content, "Subs"), "m.en.srt")

	got, err := FindSidecars(content, feature)
	if err != nil {
		t.Fatalf("FindSidecars: %v", err)
	}
	if !slices.IsSorted(got) {
		t.Errorf("FindSidecars is not sorted:\n%s", strings.Join(got, "\n"))
	}
}

// content_path is the file itself for a single-file torrent, and its directory
// is the shared completed folder holding every other download. Reading it would
// sweep another film's subtitles into this one's library folder.
func TestASingleFileContentPathHasNoSidecars(t *testing.T) {
	completed := t.TempDir()
	feature := touch(t, completed, "Interstellar.2014.mkv")
	touch(t, completed, "Some.Other.Film.en.srt")

	got, err := FindSidecars(feature, feature)
	if err != nil {
		t.Fatalf("FindSidecars: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("FindSidecars = %v, want none — the neighbours belong to other downloads", got)
	}
}

// A torrent directory is written by a stranger, and a symlink out of it is not
// something to hardlink into the library. Same posture as FindFeature's.
func TestFindSidecarsDoesNotFollowASymlinkOutOfTheRelease(t *testing.T) {
	root := t.TempDir()
	elsewhere := filepath.Join(root, "elsewhere.srt")
	if err := os.WriteFile(elsewhere, []byte("1\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	content := filepath.Join(root, "Release")
	feature := touch(t, content, "Movie.mkv")
	if err := os.Symlink(elsewhere, filepath.Join(content, "Movie.en.srt")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	got, err := FindSidecars(content, feature)
	if err != nil {
		t.Fatalf("FindSidecars: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("FindSidecars = %v, want none — a symlink was followed out of the release folder", got)
	}
}

func TestFindSidecarsOnAMissingContentPathIsAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	if _, err := FindSidecars(missing, filepath.Join(missing, "Movie.mkv")); err == nil {
		t.Error("FindSidecars on a missing content path returned no error")
	}
}

// --- listing them in the library --------------------------------------------

func TestListSidecarsParsesWhatTheImporterWrote(t *testing.T) {
	folder := t.TempDir()
	touch(t, folder, "Interstellar (2014).mkv")
	touch(t, folder, "Interstellar (2014).srt")
	touch(t, folder, "Interstellar (2014).en.srt")
	touch(t, folder, "Interstellar (2014).en.forced.srt")
	touch(t, folder, "Interstellar (2014).fr.ass")
	touch(t, folder, ".hidden.en.srt")
	touch(t, filepath.Join(folder, "Subs"), "2_English.srt")

	got, err := ListSidecars(folder)
	if err != nil {
		t.Fatalf("ListSidecars: %v", err)
	}

	want := []Sidecar{
		{Name: "Interstellar (2014).en.forced.srt", Language: "en", Flags: []string{"forced"}},
		{Name: "Interstellar (2014).en.srt", Language: "en"},
		{Name: "Interstellar (2014).fr.ass", Language: "fr"},
		// The film's own year is the last token, and it is not a language.
		{Name: "Interstellar (2014).srt", Language: ""},
	}
	if len(got) != len(want) {
		t.Fatalf("ListSidecars returned %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Name != w.Name {
			t.Errorf("entry %d name = %q, want %q — ReadDir's order is what the track menu shows", i, got[i].Name, w.Name)
		}
		if got[i].Language != w.Language {
			t.Errorf("%s: language = %q, want %q", w.Name, got[i].Language, w.Language)
		}
		if !slices.Equal(got[i].Flags, w.Flags) {
			t.Errorf("%s: flags = %v, want %v", w.Name, got[i].Flags, w.Flags)
		}
		if got[i].Path != filepath.Join(folder, w.Name) {
			t.Errorf("%s: path = %q, want it under the folder that was read", w.Name, got[i].Path)
		}
	}
}

// The library is flat, because the importer writes it flat — the same rule the
// scanner's largestVideo follows. A Subs/ folder under a library folder is
// something a human put there, and half-adopting it would make the {name} the
// API matches ambiguous between two directories.
func TestListSidecarsDoesNotDescend(t *testing.T) {
	folder := t.TempDir()
	touch(t, filepath.Join(folder, "Subs"), "2_English.srt")

	got, err := ListSidecars(folder)
	if err != nil {
		t.Fatalf("ListSidecars: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListSidecars = %+v, want none", got)
	}
}

func TestListSidecarsOnAMissingFolderIsAnError(t *testing.T) {
	if _, err := ListSidecars(filepath.Join(t.TempDir(), "gone")); err == nil {
		t.Error("ListSidecars on a missing folder returned no error")
	}
}

// touch creates an empty file, making its directory on the way. Subtitles have
// no size floor — a 40-byte one is a real subtitle — so unlike the feature
// fixtures these are genuinely empty.
func touch(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}
