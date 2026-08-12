package library

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrNoVideo reports that a download's content path holds nothing that looks
// like a feature file.
//
// It is a named error because the caller must not treat it as a failed
// download: the torrent fetched exactly what it advertised, and it simply is
// not a film we can place. The download row stays `completed` so the next poll
// tick tries again, and it is never marked `failed`.
var ErrNoVideo = errors.New("no video file to import")

// DefaultMinFeatureBytes is the floor a file must clear to be considered the
// feature. Release folders routinely ship a 3-8 MB `sample.mkv` beside the
// film, and without a floor a torrent whose feature failed to download would
// import the sample instead — a movie that plays for thirty seconds.
const DefaultMinFeatureBytes = 50 << 20 // 50 MiB

// DefaultMaxDepth bounds how far below the content path FindFeature will look.
// Two levels covers every real layout (`Movie/`, `Movie/Movie.mkv`,
// `Movie/BDMV/STREAM/…`); the cap exists so a pathological torrent full of
// symlinked or deeply nested directories cannot turn one import into a walk of
// the filesystem.
const DefaultMaxDepth = 3

// tmpSuffix names the partial file the EXDEV copy fallback writes before it
// renames. The suffix is deliberately not a video extension: a crash mid-copy
// must leave something the scanner ignores and a human recognises, not a
// half-written .mkv that satisfies every existence check and then stops playing
// forty minutes in.
const tmpSuffix = ".curator-tmp"

// skipDirs are the directories inside a release folder that hold something
// other than the feature. Matched case-insensitively, because release groups
// disagree about capitalisation and nothing else here depends on it.
var skipDirs = map[string]bool{
	"sample":      true,
	"extras":      true,
	"featurettes": true,
	"subs":        true,
}

// Feature is the file chosen out of a download's content path.
//
// Others counts the other qualifying videos that were passed over. It is
// reported rather than discarded so a double feature, or a season pack that
// arrived in the movie category, shows up in a log line instead of being
// silently half-imported.
type Feature struct {
	Path   string
	Size   int64
	Others int
}

// FeatureOpts tunes the search. The zero value is the production configuration;
// the fields exist so tests can lower the floor instead of writing 50 MiB.
type FeatureOpts struct {
	MinBytes int64 // 0 → DefaultMinFeatureBytes
	MaxDepth int   // 0 → DefaultMaxDepth
}

func (o FeatureOpts) minBytes() int64 {
	if o.MinBytes <= 0 {
		return DefaultMinFeatureBytes
	}
	return o.MinBytes
}

func (o FeatureOpts) maxDepth() int {
	if o.MaxDepth <= 0 {
		return DefaultMaxDepth
	}
	return o.MaxDepth
}

// DestFolder builds the library folder name for a movie: "Title (Year)".
//
// It is the exact inverse of ParseFolder, and the colon substitution is the
// half that matters. Colons are illegal in filenames, so the library replaced
// them with " - " in 8 of its 29 folders (decisions.md D9). An importer that
// skipped the substitution would write "Avengers: Infinity War (2018)" beside
// the "Avengers - Infinity War (2018)" that is already there, and the next scan
// would record a second movie for one film.
//
// The title is REJECTED rather than repaired when it cannot be a folder name.
// movies.title reaches the database from a client via POST /api/downloads, and
// this is the first code that turns it into a path: a title containing "/" or
// ".." writes outside the library. A failed import is recoverable; a file
// written into /etc is not.
func DestFolder(title string, year int) (string, error) {
	name, err := destTitle(title)
	if err != nil {
		return "", err
	}
	// Four digits, because ParseFolder accepts exactly four and nothing else.
	// A year of 0 would produce "Title (0)", which the scanner then skips as
	// unparseable — a folder curator wrote and cannot read back.
	if year < 1000 || year > 9999 {
		return "", fmt.Errorf("destination folder for %q: year %d is not four digits, so %q would not parse back", title, year, fmt.Sprintf("%s (%d)", name, year))
	}
	return fmt.Sprintf("%s (%d)", name, year), nil
}

// DestName builds the file name inside that folder: "Title (Year).ext". It is
// built on DestFolder so the two can never disagree about the title, which is
// what would leave "Title (Year)/Other Title (Year).mkv".
//
// ext arrives as filepath.Ext gives it, leading dot included, and is
// lower-cased: a source named ".MKV" must not produce a library entry spelled
// differently from every other one.
func DestName(title string, year int, ext string) (string, error) {
	folder, err := DestFolder(title, year)
	if err != nil {
		return "", err
	}
	ext = strings.ToLower(strings.TrimSpace(ext))
	if !videoExtensions[ext] {
		return "", fmt.Errorf("destination file for %q: %q is not a video extension", title, ext)
	}
	return folder + ext, nil
}

// destTitle validates a title as a path component and applies D9's colon rule.
func destTitle(title string) (string, error) {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return "", errors.New("destination folder: the title is empty")
	}
	// Checked before the substitution, which cannot introduce any of them.
	// Backslash is rejected as well as "/" because this has to stay correct if
	// curator is ever run on Windows, and because a backslash in a folder name
	// is a lie waiting to be told to some other tool.
	if strings.ContainsAny(trimmed, `/\`+"\x00") {
		return "", fmt.Errorf("destination folder for %q: a title containing a path separator or NUL would write outside the library", title)
	}
	if trimmed == "." || trimmed == ".." {
		return "", fmt.Errorf("destination folder for %q: a title of %q is a directory reference, not a name", title, trimmed)
	}

	// The inverse of D9: the library's own folders spell an illegal colon " - ".
	//
	// The whitespace already beside the colon is absorbed rather than kept, so
	// "Avengers: Infinity War" becomes "Avengers - Infinity War" and not
	// "Avengers -  Infinity War" — a double space no folder on the Pi has, which
	// would produce a second directory for a film that is already there. That is
	// the entire failure this function exists to prevent, so it is done by
	// splitting on the colon and trimming the pieces rather than by a
	// substitution that cannot see its own neighbours.
	//
	// Note this is NOT reversible in general — "Spider-Man" contains a real
	// hyphen — which is exactly why ParseFolder does not try to undo it.
	parts := make([]string, 0, 2)
	for _, part := range strings.Split(trimmed, ":") {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("destination folder for %q: nothing is left of the title once the colons are spelled out", title)
	}
	return strings.Join(parts, " - "), nil
}

// FindFeature picks the file to import out of a download's content path.
//
// root may be a FILE or a DIRECTORY: qBittorrent's content_path is the file
// itself for a single-file torrent and the containing folder for a multi-file
// one, and the caller has no way to know which it will get. torrents/files is
// deliberately not implemented in internal/qbit, so the walk happens here.
//
// The largest qualifying video wins. Nothing qualifying is ErrNoVideo, which
// the caller turns into "leave the row completed and try again next tick"
// rather than into a failed download.
func FindFeature(root string, opts FeatureOpts) (Feature, error) {
	info, err := os.Stat(root)
	if err != nil {
		return Feature{}, fmt.Errorf("find feature in %s: %w", root, err)
	}

	if !info.IsDir() {
		if !info.Mode().IsRegular() || !isVideo(info.Name()) || info.Size() < opts.minBytes() {
			return Feature{}, fmt.Errorf("find feature in %s: %w", root, ErrNoVideo)
		}
		return Feature{Path: root, Size: info.Size()}, nil
	}

	f := &featureSearch{minBytes: opts.minBytes(), maxDepth: opts.maxDepth()}
	if err := f.walk(root, 0); err != nil {
		return Feature{}, err
	}
	if f.found == 0 {
		return Feature{}, fmt.Errorf("find feature in %s: %w", root, ErrNoVideo)
	}
	return Feature{Path: f.best, Size: f.bestSize, Others: f.found - 1}, nil
}

type featureSearch struct {
	minBytes int64
	maxDepth int
	best     string
	bestSize int64
	found    int
}

func (f *featureSearch) walk(dir string, depth int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("find feature in %s: %w", dir, err)
	}

	for _, entry := range entries {
		name := entry.Name()
		// Hidden entries include macOS "._Feature.mkv" AppleDouble forks, which
		// carry the extension of a file they are not — the same trap scan.go
		// documents.
		if isHidden(name) {
			continue
		}

		switch {
		case entry.IsDir():
			if depth+1 > f.maxDepth || skipDirs[strings.ToLower(name)] {
				continue
			}
			if err := f.walk(filepath.Join(dir, name), depth+1); err != nil {
				return err
			}
		case entry.Type().IsRegular():
			f.consider(dir, entry)
		default:
			// Symlinks, sockets and devices. A torrent directory is written by a
			// stranger and is not a place to follow links out of: descending one
			// would let a crafted torrent point the import at any file on the
			// disk, and hardlinking that into the library is not a mistake worth
			// leaving available.
			continue
		}
	}
	return nil
}

func (f *featureSearch) consider(dir string, entry fs.DirEntry) {
	name := entry.Name()
	if !isVideo(name) {
		return
	}
	info, err := entry.Info()
	if err != nil {
		return // vanished between ReadDir and Info; it was not going to be linkable
	}
	if info.Size() < f.minBytes {
		return
	}

	f.found++
	if info.Size() > f.bestSize {
		f.best, f.bestSize = filepath.Join(dir, name), info.Size()
	}
}

// Linker is the seam os.Link sits behind, so the EXDEV fallback can be tested
// without a second filesystem to hand.
type Linker func(oldname, newname string) error

// Link gives src a second name at dst, falling back to a copy across
// filesystems. It is the whole of decisions.md D8 in one function.
func Link(src, dst string) error { return LinkWith(os.Link, src, dst) }

// LinkWith is Link with the link call injected.
//
// Three behaviours are load-bearing and none of them is the obvious one:
//
//   - A destination that is already os.SameFile as the source is SUCCESS. That
//     is a previous run's own link, left behind when the process died between
//     linking and recording, and treating it as success is what lets a retry
//     converge instead of failing for ever.
//   - A destination that exists and is a DIFFERENT file is an error. Never an
//     overwrite, never a remove — the thing already there might be the good copy.
//   - The destination DIRECTORY is not created here. The caller creates it only
//     once it has chosen a source file, because a folder created earlier and
//     then abandoned is one the scanner faithfully records as a zero-size movie.
//
// The source file is never modified, moved or deleted on any path, including
// every error path.
func LinkWith(link Linker, src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("link %s: %w", src, err)
	}
	if !srcInfo.Mode().IsRegular() {
		return fmt.Errorf("link %s: not a regular file", src)
	}

	switch same, err := sameDestination(srcInfo, dst); {
	case err != nil:
		return err
	case same:
		return nil
	}

	err = link(src, dst)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, syscall.EXDEV):
		// Downloads and library are on one ext4 filesystem on the Pi — both
		// report device 2049 — so this is the path that should never run there.
		// It exists for a library mounted from somewhere else, where a copy is
		// the only honest answer.
		return copyFile(srcInfo, src, dst)
	case errors.Is(err, fs.ErrExist):
		// Something created dst between the check above and here. If it is our
		// own link, that is still success.
		if same, serr := sameDestination(srcInfo, dst); serr != nil {
			return serr
		} else if same {
			return nil
		}
		return fmt.Errorf("link %s -> %s: %w", src, dst, err)
	default:
		return fmt.Errorf("link %s -> %s: %w", src, dst, err)
	}
}

// sameDestination reports whether dst is already another name for the same
// inode as src. A different file there is an error, not a permission to
// overwrite.
//
// Lstat, not Stat: a symlink at dst is judged as itself rather than as whatever
// it points at, so the answer is "a different file is there" and the import
// stops, instead of the link being silently followed out of the library.
func sameDestination(srcInfo fs.FileInfo, dst string) (bool, error) {
	dstInfo, err := os.Lstat(dst)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("link to %s: %w", dst, err)
	}
	if os.SameFile(srcInfo, dstInfo) {
		return true, nil
	}
	return false, fmt.Errorf("link to %s: a different file is already there, and it is not ours to overwrite", dst)
}

// copyFile is the EXDEV fallback: write a temp file beside the destination,
// flush it, then rename. The rename is atomic within a directory, so the
// destination either does not exist or is complete — never a plausible-looking
// truncated video.
//
// The source's permission bits are reproduced, because a hardlink would have
// carried them: qBittorrent writes 0644 on the Pi, which is exactly what the
// library's existing files are, so neither path needs a chmod.
func copyFile(srcInfo fs.FileInfo, src, dst string) error {
	tmp := dst + tmpSuffix

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}
	defer in.Close()

	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, srcInfo.Mode().Perm())
	if err != nil {
		return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}

	// Every failure below removes the temp file: a leftover partial is only
	// useful as evidence of a crash, and this is not one.
	fail := func(err error) error {
		out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}

	if _, err := io.Copy(out, in); err != nil {
		return fail(err)
	}
	if err := out.Sync(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}
	return nil
}
