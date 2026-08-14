package library

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Subtitles, on disk: what a release shipped beside the film, what it is called
// once it is in the library, and what its name says about it.
//
// The naming is the whole point of this file and it is not curator's invention.
// `Title (Year).en.srt` beside `Title (Year).mkv` is the convention Jellyfin,
// Plex and VLC already read, so one rename at import time makes the file work in
// three players that are not curator — which is worth more than anything curator
// could do with it on its own.

// subtitleExtensions are the sidecar formats an import carries into the library.
//
// All five are LINKED; only some of them are offered to a browser, and that
// decision belongs to internal/api rather than here — a `.ass` is a real
// subtitle file that Jellyfin and VLC both render, and refusing to place it
// because a `<track>` element cannot use it would be the player's opinion
// deciding what the library contains.
var subtitleExtensions = map[string]bool{
	".srt": true,
	".ass": true,
	".ssa": true,
	".sub": true,
	".vtt": true,
}

// sidecarDirs are the folders release groups put subtitles in.
//
// "subs" is also in skipDirs, and the two are the same fact read from opposite
// ends: FindFeature must not descend into it because there is no film in there,
// and this must, because that is exactly where the subtitles are.
var sidecarDirs = map[string]bool{
	"subs":      true,
	"subtitles": true,
}

// sidecarFlags maps the modifiers that follow a language to the one spelling
// curator writes.
//
// They are kept rather than dropped for one concrete reason: a film with
// foreign-language dialogue routinely ships `English.srt` AND `English.forced.srt`,
// and a naming rule that carried only the language would normalise both to
// `Title (Year).en.srt`, at which point one of two genuinely different tracks is
// skipped as a collision. Keeping the flag keeps both files.
//
// **"hi" is in this table AND is Hindi's ISO 639-1 code**, which is a genuine
// ambiguity and not a table that could be written more carefully. It is
// resolved in sidecarTags by what comes in front of it, and both halves are real
// files: the Backrooms release in this build's own downloads folder ships
// `Subs/SDH.eng.HI.srt`, and `Movie.hi.srt` is how a Hindi subtitle is named.
// It normalises to "sdh" because that is the spelling Jellyfin acts on.
var sidecarFlags = map[string]string{
	"forced": "forced",
	"sdh":    "sdh",
	"hi":     "sdh",
	"cc":     "cc",
}

// subtitleLanguage is one entry in the table below.
type subtitleLanguage struct {
	// Code is ISO 639-1, which is what <track srclang> wants and what Jellyfin
	// reads out of a file name. It is the only spelling curator ever writes.
	Code string
	// Name is what a human reads in a player's track menu.
	Name string
	// Aliases are the ISO 639-2 codes and the spellings release groups actually
	// use. The Code and the lower-cased Name are indexed too and are not
	// repeated here.
	Aliases []string
}

// subtitleLanguages is the table a sidecar's language is recognised from.
//
// **It is a closed set, and that is a security property rather than a
// convenience.** The destination name for a sidecar is the feature's own stem
// plus a Code from this table plus a flag from the one above plus a known
// extension — so no part of a filename a release group chose ever reaches a
// path. The AssertInside call at the import site is the guard that keeps that
// true rather than assumed.
//
// It is deliberately short. These are the languages that turn up in releases;
// a full ISO 639 table would be four hundred entries earning nothing, and every
// entry is a chance for a two-letter English word to be read as a language.
// Which is the known cost of this table, stated: `no`, `it` and `id` are also
// words, so a sidecar whose name happens to END in one gets a wrong suffix.
// That mislabels a track in a menu; it never loses a file and never moves one,
// because the suffix is the only thing derived from it.
var subtitleLanguages = []subtitleLanguage{
	{Code: "en", Name: "English", Aliases: []string{"eng"}},
	{Code: "es", Name: "Spanish", Aliases: []string{"spa", "espanol", "castellano"}},
	{Code: "fr", Name: "French", Aliases: []string{"fre", "fra", "francais"}},
	{Code: "de", Name: "German", Aliases: []string{"ger", "deu", "deutsch"}},
	{Code: "it", Name: "Italian", Aliases: []string{"ita"}},
	{Code: "pt", Name: "Portuguese", Aliases: []string{"por"}},
	{Code: "nl", Name: "Dutch", Aliases: []string{"dut", "nld"}},
	{Code: "sv", Name: "Swedish", Aliases: []string{"swe"}},
	{Code: "no", Name: "Norwegian", Aliases: []string{"nor"}},
	{Code: "da", Name: "Danish", Aliases: []string{"dan"}},
	{Code: "fi", Name: "Finnish", Aliases: []string{"fin"}},
	{Code: "pl", Name: "Polish", Aliases: []string{"pol"}},
	{Code: "ru", Name: "Russian", Aliases: []string{"rus"}},
	{Code: "uk", Name: "Ukrainian", Aliases: []string{"ukr"}},
	{Code: "cs", Name: "Czech", Aliases: []string{"cze", "ces"}},
	{Code: "sk", Name: "Slovak", Aliases: []string{"slo", "slk"}},
	{Code: "hu", Name: "Hungarian", Aliases: []string{"hun"}},
	{Code: "ro", Name: "Romanian", Aliases: []string{"rum", "ron"}},
	{Code: "bg", Name: "Bulgarian", Aliases: []string{"bul"}},
	{Code: "hr", Name: "Croatian", Aliases: []string{"hrv"}},
	{Code: "sr", Name: "Serbian", Aliases: []string{"srp"}},
	{Code: "el", Name: "Greek", Aliases: []string{"gre", "ell"}},
	{Code: "tr", Name: "Turkish", Aliases: []string{"tur"}},
	{Code: "ar", Name: "Arabic", Aliases: []string{"ara"}},
	{Code: "he", Name: "Hebrew", Aliases: []string{"heb"}},
	{Code: "fa", Name: "Persian", Aliases: []string{"per", "fas", "farsi"}},
	{Code: "hi", Name: "Hindi", Aliases: []string{"hin"}},
	{Code: "ta", Name: "Tamil", Aliases: []string{"tam"}},
	{Code: "si", Name: "Sinhala", Aliases: []string{"sin", "sinhalese"}},
	{Code: "th", Name: "Thai", Aliases: []string{"tha"}},
	{Code: "vi", Name: "Vietnamese", Aliases: []string{"vie"}},
	{Code: "id", Name: "Indonesian", Aliases: []string{"ind"}},
	{Code: "ms", Name: "Malay", Aliases: []string{"may", "msa"}},
	{Code: "ja", Name: "Japanese", Aliases: []string{"jpn"}},
	{Code: "ko", Name: "Korean", Aliases: []string{"kor"}},
	{Code: "zh", Name: "Chinese", Aliases: []string{"chi", "zho", "chs", "cht"}},
}

// languageByToken indexes the table by every spelling that resolves to it.
var languageByToken = func() map[string]subtitleLanguage {
	index := make(map[string]subtitleLanguage, len(subtitleLanguages)*3)
	for _, language := range subtitleLanguages {
		index[language.Code] = language
		index[strings.ToLower(language.Name)] = language
		for _, alias := range language.Aliases {
			index[alias] = language
		}
	}
	return index
}()

// LanguageName turns an ISO 639-1 code back into something a person reads in a
// track menu, or "" when curator's table does not hold it.
//
// It takes a CODE and not any of the aliases the table is indexed by, because
// the only thing that ever reaches it is a Sidecar.Language, which this package
// wrote — and a function that answered "English" to "eng" would be a second,
// looser spelling of the same lookup for no caller that needs one.
func LanguageName(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if language, ok := languageByToken[code]; ok && language.Code == code {
		return language.Name
	}
	return ""
}

// Sidecar is one subtitle file beside a film in the library.
type Sidecar struct {
	// Name is the file name on disk, and it is also the last segment of the URL
	// that serves it. **Matched against this list rather than joined onto a
	// path** — that is the only reason {name} out of a request can be trusted
	// at all.
	Name string

	// Path is the file. It is built here from a directory this package read,
	// never from anything a caller supplied.
	Path string

	// Language is an ISO 639-1 code, or "" when the name carries nothing this
	// package recognises.
	Language string

	// Flags are the modifiers after the language — "forced", "sdh", "cc" — in
	// the order they appear in the name. Empty is the ordinary case.
	Flags []string
}

// FindSidecars lists the subtitle files a completed download shipped with the
// film, for the importer to link beside it.
//
// contentPath is the torrent's content path and featurePath is the file
// FindFeature chose out of it. Two places are looked in, which is where release
// groups actually put them: beside the feature, and one level down in a
// `Subs`/`Subtitles` folder.
//
// **A content path that is a FILE has no sidecars, by definition.** That is not
// a shortcut, it is the only safe reading: for a single-file torrent
// content_path is the file itself, and its directory is the shared completed
// folder holding every other download. Scanning that would sweep another film's
// subtitles into this one's library folder.
//
// Symlinks are not followed and hidden entries are skipped, exactly as
// FindFeature does and for the same reason — a torrent directory is written by a
// stranger, and a link out of it is not a thing to hardlink into the library.
func FindSidecars(contentPath, featurePath string) ([]string, error) {
	info, err := os.Stat(contentPath)
	if err != nil {
		return nil, fmt.Errorf("find subtitles in %s: %w", contentPath, err)
	}
	if !info.IsDir() {
		return nil, nil
	}

	var found []string
	seen := map[string]bool{}

	collect := func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("find subtitles in %s: %w", dir, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if isHidden(name) || !entry.Type().IsRegular() || !isSubtitle(name) {
				continue
			}
			path := filepath.Join(dir, name)
			if !seen[path] {
				seen[path] = true
				found = append(found, path)
			}
		}
		return nil
	}

	// Beside the feature. For every ordinary layout that IS the content path
	// itself; it differs only when the film sits deeper, as in a BDMV tree.
	if err := collect(filepath.Dir(featurePath)); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(contentPath)
	if err != nil {
		return nil, fmt.Errorf("find subtitles in %s: %w", contentPath, err)
	}
	for _, entry := range entries {
		if entry.IsDir() && sidecarDirs[strings.ToLower(entry.Name())] {
			if err := collect(filepath.Join(contentPath, entry.Name())); err != nil {
				return nil, err
			}
		}
	}

	// Two directories were read, so ReadDir's own ordering is not enough: the
	// importer walks this list and the FIRST of two colliding names wins, which
	// would otherwise depend on which folder was read first.
	slices.Sort(found)
	return found, nil
}

// ListSidecars reports the subtitle files in a movie's library folder, parsed.
//
// **One level, no descent**, which is the same rule the scanner's largestVideo
// follows and for the same reason: the library is flat, because the importer
// writes it flat. A `Subs/` folder under a library folder is something a human
// put there and is not what curator produced, so it is left alone rather than
// half-adopted.
//
// A missing folder is an error and not an empty list: the caller has a row that
// says the film is on disk, so a folder that is not there is a fact worth
// surfacing rather than a film that quietly has no subtitles.
func ListSidecars(folder string) ([]Sidecar, error) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil, fmt.Errorf("list subtitles in %s: %w", folder, err)
	}

	sidecars := make([]Sidecar, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if isHidden(name) || entry.IsDir() || !isSubtitle(name) {
			continue
		}
		language, flags := sidecarTags(stem(name))
		sidecars = append(sidecars, Sidecar{
			Name:     name,
			Path:     filepath.Join(folder, name),
			Language: language,
			Flags:    flags,
		})
	}

	// os.ReadDir already sorts, and the slice above preserves that order. Said
	// out loud because the API hands this list to a <track> menu, and a menu
	// that reorders itself between two loads of the same page is a bug nobody
	// would think to look for here.
	return sidecars, nil
}

// SidecarName builds the name a sidecar takes in the library: the feature's own
// stem, the language it declares, and the extension it arrived with.
//
// featureName is the file name the feature is being linked as — DestName's
// answer, "Title (Year).mkv" — so the two can never disagree about the title,
// which is what leaves a subtitle that no player associates with the film.
//
// A sidecar whose name carries no language curator recognises gets the stem and
// nothing else: `Title (Year).srt`, which is exactly what Jellyfin, Plex and VLC
// read as "the subtitle for this film, language unstated".
func SidecarName(featureName, sidecarPath string) (string, error) {
	ext := strings.ToLower(filepath.Ext(sidecarPath))
	if !subtitleExtensions[ext] {
		return "", fmt.Errorf("subtitle %s: %q is not a subtitle extension", sidecarPath, ext)
	}
	base := stem(featureName)
	if base == "" {
		return "", fmt.Errorf("subtitle %s: the feature has no name to hang it off", sidecarPath)
	}

	language, flags := sidecarTags(stem(filepath.Base(sidecarPath)))
	if language == "" {
		return base + ext, nil
	}
	return base + "." + strings.Join(append([]string{language}, flags...), ".") + ext, nil
}

// sidecarTags reads what a subtitle's own name says about it, walking the
// tokens from the END.
//
// From the end, because that is where the convention puts them and because the
// front is the film's title, which is full of words. `2_English`, `Movie.en`,
// `Movie.2014.1080p.eng.forced` all answer the same way, and a title that
// happens to contain "German" does not.
//
// Flags are only kept when a language was found in front of them. A bare
// `forced.srt` says nothing about which language it is, and writing
// `Title (Year).forced.srt` would have every player read "forced" as the
// language — a worse answer than no suffix at all.
//
// **"hi" is both a flag and a language, and what settles it is the token in
// front of it.** `SDH.eng.HI` has a language there, so the `hi` is the
// hearing-impaired marker on an English track; `Movie.hi` does not, so the `hi`
// IS the track's language. Both spellings are real and neither can be dropped:
// the first is what the Backrooms release in this build's downloads folder
// actually ships, and the second is how a Hindi subtitle is named everywhere.
func sidecarTags(name string) (string, []string) {
	tokens := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r == '.' || r == '_' || r == '-' || r == ' '
	})

	var flags []string
	i := len(tokens) - 1
	for i >= 0 {
		flag, isFlag := sidecarFlags[tokens[i]]
		if !isFlag {
			break
		}
		if _, alsoLanguage := languageByToken[tokens[i]]; alsoLanguage && !precededByLanguage(tokens, i) {
			// Ambiguous, and nothing in front of it says otherwise: this is the
			// language and not a marker on one.
			break
		}
		// Deduplicated because two spellings normalise to one flag, and
		// "Title (Year).en.sdh.sdh.srt" is not a name.
		if !slices.Contains(flags, flag) {
			flags = append(flags, flag)
		}
		i--
	}
	if i < 0 {
		return "", nil
	}
	language, ok := languageByToken[tokens[i]]
	if !ok {
		return "", nil
	}
	// Collected back to front; the name reads front to back.
	slices.Reverse(flags)
	return language.Code, flags
}

// precededByLanguage reports whether the first non-flag token in front of i
// names a language.
//
// The flags in between are skipped rather than stopping the search, because
// `Movie.en.sdh.hi` puts one there — and the token that decides whether that
// trailing `hi` is Hindi or a marker is the `en` behind it, not the `sdh`. The
// search stops at the first token that is not a flag, so a title whose last word
// happens to be a language does not reach back and change the answer.
func precededByLanguage(tokens []string, i int) bool {
	for j := i - 1; j >= 0; j-- {
		if _, isFlag := sidecarFlags[tokens[j]]; isFlag {
			continue
		}
		_, isLanguage := languageByToken[tokens[j]]
		return isLanguage
	}
	return false
}

// stem is a file name without its extension.
func stem(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

func isSubtitle(name string) bool {
	return subtitleExtensions[strings.ToLower(filepath.Ext(name))]
}
