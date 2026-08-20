package library

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
)

// Television, on disk. The library layer knew exactly one shape until phase 11 —
// a "Title (Year)" folder holding ONE feature file — and a show is the second:
// the same folder name, seasons under it, and many files that are each a
// numbered part of the same thing rather than candidates for the same slot.
//
// The posture is ParseFolder's, unchanged: a name is parsed or it is refused.
// Nothing here infers an episode number from a file's position in a sorted
// directory, from the enclosing folder, or from anything except the characters
// in the name itself, because the cost of being wrong is a hardlink filed under
// somebody else's episode and a row that says a season is complete when it is
// not.

// episodeSE matches the "S02E05" form, which is what all but a handful of
// releases use.
//
// Each bound is a real name that must NOT match, rather than a guess at how
// wide to be:
//
//   - The leading `(?:^|[^0-9A-Za-z])` is a word boundary written out. Without
//     it "Cosmos1x05" and worse would match on a fragment of a title. `\b` is
//     not the same thing: `\b` is satisfied by a preceding digit, which is
//     precisely the neighbour that turns a resolution into a season below.
//   - The separator is optional and narrow (`S01E05`, `S01.E05`, `S01 E05`),
//     because release groups differ there and nothing else in the name does.
//   - The season takes up to FOUR digits: daily shows are published as
//     "S2019E243", and an explicit S…E… marker makes a false positive there
//     very unlikely.
//   - The episode takes at most THREE, and the trailing `(?:[^0-9]|$)` is what
//     makes that a refusal instead of a truncation. "S01E1010" does not become
//     episode 101 — no alternative satisfies the trailing guard, so the whole
//     match fails and the caller is told the name is unparseable.
var episodeSE = regexp.MustCompile(`(?i)(?:^|[^0-9A-Za-z])s(\d{1,4})[ ._-]?e(\d{1,3})(?:[^0-9]|$)`)

// episodeX matches the "2x05" form, and it is the dangerous one: there is no
// marker in it, only two numbers around an x, and release names are full of
// numbers around an x.
//
// Two measured traps decide its bounds, and both are in ordinary file names:
//
//   - "Show.1920x1080.mkv" — a resolution. The season is capped at two digits
//     so "1920" cannot be one, and the leading boundary stops the engine from
//     re-trying at "20x1080" with a digit in front of it.
//   - "Show.DD5.1x264-GROUP.mkv" — a codec. The episode is EXACTLY two digits
//     and must be followed by a non-digit, so "x264" fails: "26" is followed by
//     "4". This is the reason it is not `\d{1,3}` like the form above.
//
// The cost of those bounds is that "1x100" is refused. That is the trade this
// package always makes: an unparseable name is surfaced, never guessed at, and
// a release that needs three digits can be named S01E100 like everything else.
var episodeX = regexp.MustCompile(`(?i)(?:^|[^0-9A-Za-z])(\d{1,2})x(\d{2})(?:[^0-9]|$)`)

// ParseEpisode reads a season and an episode number out of a file name.
//
// It accepts "S02E05", "s02e05", "2x05" and the multi-episode "S02E05E06", and
// ok is false for anything else — a season pack named "S01", a file called
// "E05" whose season is only in the folder above it, a name with no code in it
// at all. Those are REPORTED by the callers (FindEpisodes returns ErrNoEpisode,
// ScanShows returns a Skipped) rather than repaired here, which is ParseFolder's
// rule applied to the other half of a television library.
//
// **On a multi-episode file the FIRST number wins**, and that is a deliberate
// choice rather than a limitation nobody noticed. A file named "S02E05E06"
// holds two episodes and curator's row model has one (season, episode) per
// file, so something has to give. The first number is the only part every
// spelling of the range agrees on — "S02E05E06", "S02E05-E06" and "S02E05-06"
// all begin at 05 — and it is where the file actually starts playing, so a
// player that seeks to "episode 5" lands in the right place. Jellyfin and Plex
// both file such a release under the first episode too. What is NOT done is
// inventing a second row for E06: nothing on disk is a separate file, and a row
// pointing at a file that begins with somebody else's episode is worse than an
// episode that is visibly absent.
//
// The name is taken verbatim; the caller passes filepath.Base. Case folds
// because release groups disagree about it and nothing else here depends on it.
func ParseEpisode(name string) (season, episode int, ok bool) {
	// The explicit form first: when a name carries both, the code with an S and
	// an E in it is the one that cannot also be a resolution or a codec, so it
	// is the one that wins.
	for _, re := range []*regexp.Regexp{episodeSE, episodeX} {
		m := re.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		// strconv.Atoi is safe on these two, unlike on the year in ParseFolder:
		// the groups are `\d{1,4}` and `\d{1,3}`, so there is no sign to accept
		// and no length to be surprised by. An error here is impossible.
		s, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		e, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		return s, e, true
	}
	return 0, 0, false
}

// ErrNoEpisode reports that a folder holds video that clears the picker's
// floor, but nothing whose name says which episode it is.
//
// It is a SECOND sentinel beside ErrNoVideo rather than the same one because
// the two are different facts about the disk and a person can act on only one
// of them. ErrNoVideo means the download brought nothing playable — wait, or
// look at the torrent. This means the bytes are there and the NAME is the
// problem, which is a rename away from working. Collapsing them would put a
// season of files nobody can file behind the message "no episode in it", which
// is exactly the sentence that gets a folder ignored.
//
// Both are answers to a folder that was READ SUCCESSFULLY, so both make
// ScanShows report Skipped{NoMedia: true} — see that field's comment for why
// that distinction is the one that matters to a caller pruning rows.
var ErrNoEpisode = errors.New("no episode file to import")

// Episode is one video file whose name says where it belongs.
//
// Season and Episode are what ParseEpisode read out of the file NAME. They are
// not corrected against the folder above, against TMDB, or against the other
// files beside it: this type reports what the disk says, and anything that
// disagrees with TMDB is a fact the caller gets to see rather than one this
// package quietly resolves.
type Episode struct {
	Path    string
	Size    int64
	Season  int
	Episode int
}

// FindEpisodes returns every qualifying video under root, with the season and
// episode read out of each file's name.
//
// It is FindFeature's counterpart and it shares FindFeature's walk (videoWalk),
// which is the point rather than an implementation detail: the 50 MiB floor
// that keeps a sample out of the library, the sample/extras/featurettes/subs
// skip, the hidden-file skip that catches macOS "._Episode.mkv" AppleDouble
// forks, the depth cap, and the refusal to follow a symlink out of a stranger's
// torrent directory are ONE answer here, not two that agree today.
//
// root may be a FILE or a DIRECTORY, for the same reason FindFeature accepts
// both: qBittorrent's content_path is the file itself for a single-file torrent
// — one episode grabbed on its own — and the containing folder for a season
// pack, and the caller cannot know which it will get.
//
// The two empty answers are different and are reported as such: ErrNoVideo when
// nothing cleared the floor at all, ErrNoEpisode when video did but no name
// said which episode it was. Neither is a failed download, exactly as
// FindFeature's ErrNoVideo is not — the torrent fetched what it advertised.
//
// The result is sorted by season, then episode, then path, which does two
// things worth relying on: two calls over an unchanged tree return identical
// slices whatever the layout, and duplicates of one episode — a repack beside
// the original — land next to each other for the caller to notice. They are
// both RETURNED. Which of two files for one episode to keep is a decision about
// what is in the library, and it is not this function's to make silently.
func FindEpisodes(root string, opts FeatureOpts) ([]Episode, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("find episodes in %s: %w", root, err)
	}

	// videos counts everything that cleared the floor, named or not, so the two
	// empty answers below can be told apart.
	videos := 0
	var found []Episode
	collect := func(path string, size int64) {
		videos++
		season, episode, ok := ParseEpisode(filepath.Base(path))
		if !ok {
			return
		}
		found = append(found, Episode{Path: path, Size: size, Season: season, Episode: episode})
	}

	if info.IsDir() {
		w := &videoWalk{
			label:    "find episodes",
			minBytes: opts.minBytes(),
			maxDepth: opts.maxDepth(),
			visit:    collect,
		}
		if err := w.walk(root, 0); err != nil {
			return nil, err
		}
	} else {
		// The same three tests the walk applies to a file it meets inside a
		// directory, so a single-file torrent is judged identically to the same
		// file inside a folder.
		if !info.Mode().IsRegular() || !isVideo(info.Name()) || info.Size() < opts.minBytes() {
			return nil, fmt.Errorf("find episodes in %s: %w", root, ErrNoVideo)
		}
		collect(root, info.Size())
	}

	switch {
	case videos == 0:
		return nil, fmt.Errorf("find episodes in %s: %w", root, ErrNoVideo)
	case len(found) == 0:
		return nil, fmt.Errorf("find episodes in %s: %w", root, ErrNoEpisode)
	}

	slices.SortFunc(found, func(a, b Episode) int {
		if c := cmp.Compare(a.Season, b.Season); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Episode, b.Episode); c != 0 {
			return c
		}
		return cmp.Compare(a.Path, b.Path)
	})
	return found, nil
}
