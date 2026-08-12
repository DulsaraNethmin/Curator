package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// StatusImported is the status of every movie Scan returns: the folder is on disk,
// so by definition it is imported.
const StatusImported = "imported"

// Movie is one library folder. The field names mirror the movies table columns that
// the scanner owns; everything else on a row (tmdb_id, overview, poster_path) is
// filled in later by metadata lookup.
type Movie struct {
	LibraryPath string `json:"library_path"`
	Title       string `json:"title"`
	Year        int    `json:"year"`
	SizeBytes   int64  `json:"size_bytes"`
	Status      string `json:"status"`
}

// Skipped is a directory Scan refused to interpret. It is surfaced rather than
// guessed at, so a folder that has drifted from "Title (Year)" is visible instead of
// silently absent from the library.
type Skipped struct {
	Name   string // entry name as it appears on disk
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

// Scan walks root one level deep and returns one Movie per movie folder, ordered by
// folder name. Directories whose names do not parse are omitted; use ScanWithSkipped
// to see them.
func Scan(root string) ([]Movie, error) {
	movies, _, err := ScanWithSkipped(root)
	return movies, err
}

// ScanWithSkipped is Scan plus the directories it could not interpret and why.
//
// Hidden entries (macOS scatters .DS_Store everywhere) and non-directories are
// ignored silently; they are noise, not library folders that need attention.
// LibraryPath is root joined with the folder name, so a stable root gives a stable
// identity key across rescans.
func ScanWithSkipped(root string) ([]Movie, []Skipped, error) {
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
			skipped = append(skipped, Skipped{Name: name, Reason: err.Error()})
			continue
		}
		if !isDir {
			continue
		}

		title, year, ok := ParseFolder(name)
		if !ok {
			skipped = append(skipped, Skipped{Name: name, Reason: `does not match "Title (Year)"`})
			continue
		}

		size, err := largestVideo(path)
		if err != nil {
			return nil, nil, err
		}

		movies = append(movies, Movie{
			LibraryPath: path,
			Title:       title,
			Year:        year,
			SizeBytes:   size,
			Status:      StatusImported,
		})
	}

	return movies, skipped, nil
}

// largestVideo returns the size of the biggest video file directly inside dir, or 0
// if there is none — an empty folder is not an error. The largest file wins rather
// than the folder total because samples, artwork and subtitles sit beside the
// feature, and the folder total would count them all. Subdirectories are not
// descended into: the library is flat.
func largestVideo(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("scan %s: %w", dir, err)
	}

	var largest int64
	for _, entry := range entries {
		name := entry.Name()
		// Hidden files include macOS "._Feature.mkv" AppleDouble forks, which carry
		// the extension of a file they are not.
		if entry.IsDir() || isHidden(name) || !isVideo(name) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // vanished between ReadDir and Info
			}
			return 0, fmt.Errorf("scan %s: %w", filepath.Join(dir, name), err)
		}
		if size := info.Size(); size > largest {
			largest = size
		}
	}
	return largest, nil
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
