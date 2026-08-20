package library

import (
	"fmt"
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
