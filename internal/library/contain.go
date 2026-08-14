package library

import (
	"fmt"
	"path/filepath"
	"strings"
)

// AssertInside refuses a path that has escaped root.
//
// It was `(*Importer).assertInsideLibrary` until phase 8 gave it a second
// caller: the importer asks before it creates a folder, and the stream endpoint
// asks before it opens a file. Copying it into internal/api is how one question
// grows two answers that drift, and this package is where the library root's
// meaning already lives — it is what turns a title into a folder name and
// refuses one that would escape (DestFolder).
//
// **Be exact about what it buys, because it looks like more than it is.** In
// the importer the last path component came from a title a client supplied; in
// the stream endpoint the id is parsed with strconv.ParseInt, so nothing
// traverses in from the request at all. What this catches there is a *row*
// pointing outside the library — written when LIBRARY_MOVIES pointed somewhere
// else, restored beside a different library, or edited by hand. That is a real
// failure and it is the operator's, which is why the caller logs both paths
// (docs/phase-8.md, "The containment check defends a row, not a URL").
//
// filepath.Rel rather than strings.HasPrefix, which is the trap this shape
// exists to avoid: "/library/movies-old" has "/library/movies" as a prefix and
// is not inside it. Rel answers "../movies-old" and it is rejected.
//
// root itself is inside root — Rel gives "." — which no caller relies on and
// none is refused for: the importer only ever asks about root/folder, and a row
// whose library_path IS the root is a strange row rather than an escape.
func AssertInside(root, path string) error {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absoluteRoot, absolute)
	if err != nil {
		return fmt.Errorf("destination %s is not under the library root %s", path, root)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("destination %s escapes the library root %s", path, root)
	}
	return nil
}
