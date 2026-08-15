package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// StatusImported is the status of every movie Scan returns: the folder is on disk
// and there is a film in it, so by definition it is imported.
const StatusImported = "imported"

// Movie is one library folder that holds a film. The field names mirror the movies
// table columns that the scanner owns; everything else on a row (tmdb_id, overview,
// poster_path) is filled in later by metadata lookup.
//
// SizeBytes carries an invariant worth naming, because the rest of the system now
// leans on it: it is the FEATURE FILE's size and it always clears the picker's
// floor, so `size_bytes > 0` means *playable* rather than *a folder exists*. That
// is what spares a "playable" column, a migration, and a disk read on every list
// request (docs/decisions.md D33).
type Movie struct {
	LibraryPath string `json:"library_path"`
	Title       string `json:"title"`
	Year        int    `json:"year"`
	SizeBytes   int64  `json:"size_bytes"`
	Status      string `json:"status"`
}

// Skipped is a directory Scan did not record as a movie. It is surfaced rather
// than guessed at, so a folder that has drifted from "Title (Year)" is visible
// instead of silently absent from the library.
type Skipped struct {
	Name string // entry name as it appears on disk
	Path string // root joined with Name — the key a caller joins database rows on

	// NoMedia is true when the folder was READ SUCCESSFULLY and holds no feature
	// file. It is a field rather than a substring of Reason because it is the one
	// skip that removes a row (docs/decisions.md D33), and a caller deciding that
	// on a prose match is a caller one reworded sentence away from deleting the
	// wrong thing — or nothing at all.
	//
	// False for every other skip, and "could not tell" is emphatically not "no
	// film": an unreadable folder is what an unplugged disk looks like.
	NoMedia bool

	Reason string
}

// videoExtensions are the containers the feature file can be in. Lowercase; lookups
// fold case.
var videoExtensions = map[string]bool{
	".mkv": true,
	".mp4": true,
	".avi": true,
	".m4v": true,
}

// Scan walks root and returns one Movie per folder that holds a film, plus every
// folder it did not record and why, both ordered by folder name.
//
// A folder with nothing playable in it is NOT a movie (docs/decisions.md D33). It
// used to be recorded with size 0, and the row that produced was the bug: `status`
// read `imported`, so the UI drew a Play button for a film that is not there.
func Scan(root string) ([]Movie, []Skipped, error) { return ScanWith(root, FeatureOpts{}) }

// ScanWith is Scan with the feature picker's options supplied.
//
// The options exist for the reason FeatureOpts itself exists: a test can lower the
// 50 MiB floor instead of writing 50 MiB. **Production passes the zero value and
// must.** The floor, the depth and the sample/extras/subs skip are what make this
// and the stream endpoint agree about which file is the film, and a scanner that
// disagreed would now delete the row for a film that plays.
//
// Hidden entries (macOS scatters .DS_Store everywhere) and non-directories are
// ignored silently; they are noise, not library folders that need attention.
// LibraryPath is root joined with the folder name, so a stable root gives a stable
// identity key across rescans.
//
// A failing ReadDir of ROOT is fatal, unlike a failing read of one folder inside
// it. That asymmetry is load-bearing: an unmounted library reads as zero folders,
// and the caller prunes a row per folder it did not see. The error is what stops a
// loose cable emptying the database (D33).
func ScanWith(root string, opts FeatureOpts) ([]Movie, []Skipped, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, fmt.Errorf("scan %s: %w", root, err)
	}

	movies := make([]Movie, 0, len(entries))
	var skipped []Skipped

	// os.ReadDir sorts by filename, so two scans of an unchanged library produce
	// identical slices.
	for _, entry := range entries {
		name := entry.Name()
		if isHidden(name) {
			continue
		}

		path := filepath.Join(root, name)
		isDir, err := isDirectory(root, entry)
		if err != nil {
			// A dangling symlink is a fact about the disk, not a reason to abandon
			// the other 28 folders.
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

		// The same picker the importer used to put the file there and the stream
		// endpoint uses to serve it. One answer to "which file is the film", by
		// construction rather than by three implementations agreeing for now.
		feature, err := FindFeature(path, opts)
		switch {
		case err == nil:
			movies = append(movies, Movie{
				LibraryPath: path,
				Title:       title,
				Year:        year,
				SizeBytes:   feature.Size,
				Status:      StatusImported,
			})
		case errors.Is(err, ErrNoVideo):
			skipped = append(skipped, Skipped{
				Name: name, Path: path, NoMedia: true, Reason: "no film in it",
			})
		default:
			// NOT "no film" — "could not tell". Only a positive finding removes a
			// row, and the whole safety argument for pruning is that these two are
			// never confused (D33). Continuing rather than aborting is the same
			// posture the dangling symlink above gets, for the same reason.
			skipped = append(skipped, Skipped{Name: name, Path: path, Reason: err.Error()})
		}
	}

	return movies, skipped, nil
}

// isDirectory resolves symlinks, so a library assembled out of links to other disks
// still scans.
func isDirectory(root string, entry fs.DirEntry) (bool, error) {
	if entry.Type()&fs.ModeSymlink == 0 {
		return entry.IsDir(), nil
	}
	path := filepath.Join(root, entry.Name())
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("scan %s: %w", path, err)
	}
	return info.IsDir(), nil
}

func isHidden(name string) bool {
	return strings.HasPrefix(name, ".")
}

func isVideo(name string) bool {
	return videoExtensions[strings.ToLower(filepath.Ext(name))]
}
