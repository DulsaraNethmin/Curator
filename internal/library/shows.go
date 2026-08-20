package library

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A show on disk: the folder it lives in, the season folders under it, and the
// name each episode file is given.
//
// The layout is Jellyfin's own rather than curator's invention:
//
//	Severance (2022)/Season 01/Severance (2022) - S01E01.mkv
//
// which is worth more than any layout curator could prefer, because it means
// these files are read correctly by Jellyfin, Plex and Kodi with curator not
// installed at all. The show folder is spelled exactly like a movie folder, so
// ParseFolder reads it and D9's colon rule applies to it unchanged.

// ShowFolder builds the library folder name for a show: "Title (Year)".
//
// It CALLS DestFolder rather than copying it, and that is the whole of this
// function. A show folder and a movie folder are the same shape, so the colon
// rule (docs/decisions.md D9), the path-separator refusal and the four-digit
// year check have to be one implementation: a copy would let the television
// half of the library spell "Star Wars: Andor" differently from how the film
// half spells "Avengers: Infinity War", and two conventions in one tree is how
// a rescan ends up with a second folder for something already there.
//
// It exists as its own name because the CALLERS are different — the importer
// asks this about a show and DestFolder about a film — and a television caller
// reaching for something called DestFolder is a caller one refactor away from
// being given the movie rule on purpose.
func ShowFolder(title string, year int) (string, error) { return DestFolder(title, year) }

// SeasonFolder builds the folder one season lives in: "Season 01".
//
// Zero-padded to two digits because that is what Jellyfin's scanner expects,
// and because "Season 2" sorts after "Season 10" in every file browser a person
// will ever open this library in. A season past 99 keeps all its digits — the
// padding is a minimum width, not a truncation.
//
// Season 0 is "Season 00", which is not a degenerate case: it is where the
// specials go, and Jellyfin reads it as exactly that.
//
// There is no error to return, deliberately. A season number reaches this from
// ParseEpisode or from TMDB, both of which produce a non-negative integer, and
// %d cannot emit a path separator whatever it is handed — so no value here can
// write outside the library. AssertInside is what says that, rather than this
// signature.
func SeasonFolder(season int) string { return fmt.Sprintf("Season %02d", season) }

// EpisodeName builds the file name inside that season folder:
// "Title (Year) - S01E01.mkv".
//
// Built on ShowFolder for DestName's reason: the show folder and the files
// inside it can then never disagree about the title. The failure that prevents
// is "Severance (2022)/Season 01/Severence (2022) - S01E01.mkv" — a library
// that scans as one show whose episodes are named after another, and a set of
// sidecar subtitles that no longer match their video.
//
// The code is written "S01E01" because that is the spelling ParseEpisode reads
// back, and TestParseEpisodeReadsBackTheCodeCuratorWrites is the assertion that
// the two halves stay one convention: a name curator writes and cannot then
// read is a row that disappears on the next scan.
//
// A negative season or episode is REFUSED rather than rendered. It cannot come
// out of ParseEpisode, so it can only come from a caller with a bug or from
// data that reached curator from outside — the same provenance as a title with
// a slash in it, and the same answer: ErrBadTitle, which the API turns into a
// 422 because no retry will fix it.
//
// ext arrives as filepath.Ext gives it, leading dot included, and is
// lower-cased, exactly as DestName does it: a source named ".MKV" must not
// produce a library entry spelled differently from every other one.
func EpisodeName(title string, year, season, episode int, ext string) (string, error) {
	folder, err := ShowFolder(title, year)
	if err != nil {
		return "", err
	}
	if season < 0 || episode < 0 {
		return "", BadTitle{Title: title, Reason: fmt.Sprintf(
			"S%dE%d is not an episode number", season, episode)}
	}
	ext = strings.ToLower(strings.TrimSpace(ext))
	if !videoExtensions[ext] {
		return "", BadTitle{Title: title, Reason: fmt.Sprintf("%q is not a video extension", ext)}
	}
	return fmt.Sprintf("%s - S%02dE%02d%s", folder, season, episode, ext), nil
}

// Show is one library folder that holds episodes. It is Movie's counterpart and
// the field names mirror it deliberately, because since T88 both become a row
// in the same table with its media type stated.
//
// SizeBytes carries Movie's invariant unchanged: it is the size of files that
// CLEARED the picker's floor, so `size_bytes > 0` means *something here plays*
// rather than *a folder exists* (docs/decisions.md D33).
//
// Episodes counts DISTINCT (season, episode) pairs while SizeBytes sums every
// file, and the asymmetry is what a repack does to a library: a season holding
// both "S01E01.mkv" and the repack beside it really does occupy both files'
// bytes, but it is still one episode, and a count that said two would tell a
// person a season is more complete than it is.
type Show struct {
	LibraryPath string `json:"library_path"`
	Title       string `json:"title"`
	Year        int    `json:"year"`
	SizeBytes   int64  `json:"size_bytes"`
	Episodes    int    `json:"episodes"`
	Status      string `json:"status"`
}

// ScanShows walks a television library root and returns one Show per folder
// that holds at least one episode, plus every folder it did not record and why,
// both ordered by folder name.
//
// It is ScanWith for the other media type, and every rule is the same rule for
// the same reason:
//
//   - A folder with no episode in it is NOT a show (docs/decisions.md D33). The
//     row goes; the folder is left exactly where it is.
//   - Hidden entries and non-directories are ignored silently — macOS scatters
//     .DS_Store everywhere and it is noise, not a folder needing attention.
//     Symlinked show folders are followed, a dangling one is reported.
//   - A failing ReadDir of ROOT is FATAL, unlike a failing read of one folder
//     inside it. That asymmetry is load-bearing: an unmounted library reads as
//     zero folders and the caller prunes a row per folder it did not see, so
//     this error is what stops a loose cable emptying the television half of
//     the database.
//
// opts is taken rather than defaulted for FeatureOpts' own reason — a test
// lowers the floor instead of writing 50 MiB — and PRODUCTION PASSES THE ZERO
// VALUE. The 50 MiB floor is inherited unchanged from the film picker, which is
// what keeps a release's 3-8 MB sample out of a season, and is worth knowing
// about at the bottom end: an episode genuinely smaller than 50 MiB is not
// recorded, and would be reported as a folder with nothing in it.
func ScanShows(root string, opts FeatureOpts) ([]Show, []Skipped, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, fmt.Errorf("scan %s: %w", root, err)
	}

	shows := make([]Show, 0, len(entries))
	var skipped []Skipped

	// os.ReadDir sorts by filename, so two scans of an unchanged library
	// produce identical slices.
	for _, entry := range entries {
		name := entry.Name()
		if isHidden(name) {
			continue
		}

		path := filepath.Join(root, name)
		isDir, err := isDirectory(root, entry)
		if err != nil {
			// A dangling symlink is a fact about the disk, not a reason to
			// abandon the rest of the library.
			skipped = append(skipped, Skipped{Name: name, Path: path, Reason: err.Error()})
			continue
		}
		if !isDir {
			continue
		}

		title, year, ok := ParseFolder(name)
		if !ok {
			skipped = append(skipped, Skipped{
				Name: name, Path: path, Reason: `does not match "Title (Year)"`,
			})
			continue
		}

		episodes, err := FindEpisodes(path, opts)
		switch {
		case err == nil:
			shows = append(shows, newShow(path, title, year, episodes))
		case errors.Is(err, ErrNoVideo):
			skipped = append(skipped, Skipped{
				Name: name, Path: path, NoMedia: true, Reason: "no episode in it",
			})
		case errors.Is(err, ErrNoEpisode):
			// Still NoMedia, because the folder WAS read and what is in it is
			// not an episode of anything: the row goes. The reason says which
			// of the two emptinesses it was, because "there is video here that
			// nothing can file" is a sentence somebody can act on and "no
			// episode in it" is not.
			skipped = append(skipped, Skipped{
				Name: name, Path: path, NoMedia: true,
				Reason: "video in it, but nothing named like an episode",
			})
		default:
			// NOT "no episode" — "could not tell". Only a positive finding
			// removes a row, and the whole safety argument for pruning is that
			// these two are never confused (D33): an unreadable folder is what
			// an unplugged disk looks like. Continuing rather than aborting is
			// the same posture the dangling symlink above gets.
			skipped = append(skipped, Skipped{Name: name, Path: path, Reason: err.Error()})
		}
	}

	return shows, skipped, nil
}

// newShow sums what the picker found. See Show for why the bytes and the count
// are taken over different things.
func newShow(path, title string, year int, episodes []Episode) Show {
	var size int64
	distinct := make(map[[2]int]struct{}, len(episodes))
	for _, e := range episodes {
		size += e.Size
		distinct[[2]int{e.Season, e.Episode}] = struct{}{}
	}
	return Show{
		LibraryPath: path,
		Title:       title,
		Year:        year,
		SizeBytes:   size,
		Episodes:    len(distinct),
		Status:      StatusImported,
	}
}
