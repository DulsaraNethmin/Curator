package library

import (
	"errors"
	"path/filepath"
	"testing"
)

// The cases the importer's own test has asserted since phase 4, kept here now
// that the check lives here — plus the two the importer's harness could not
// express, because it only ever asked about a folder it was about to create.
func TestAssertInside(t *testing.T) {
	const root = "/library/movies"

	inside := []struct {
		name string
		path string
	}{
		{"a movie folder", root + "/Interstellar (2014)"},
		{"the file inside it", root + "/Interstellar (2014)/Interstellar (2014).mkv"},
		{"a folder whose name starts with ..", root + "/...And Justice for All (1979)"},
		// "." from filepath.Rel. Not relied on by any caller and not refused by
		// this one: a library_path that IS the root is a strange row, not an
		// escape, and the endpoint that opens it has its own answer for a folder
		// with no feature in it.
		{"the root itself", root},
	}
	for _, c := range inside {
		if err := AssertInside(root, c.path); err != nil {
			t.Errorf("%s: %s was refused: %v", c.name, c.path, err)
		}
	}

	outside := []struct {
		name string
		path string
	}{
		{"a sibling", filepath.Join(root, "..", "elsewhere")},
		{"two levels out", filepath.Join(root, "..", "..", "etc")},
		{"an absolute path elsewhere", "/etc/cron.d"},
		// The reason this is filepath.Rel and not strings.HasPrefix. A prefix
		// test calls this one inside; Rel answers "../movies-old", and a
		// database restored beside last year's library is exactly the row this
		// check exists to catch.
		{"a sibling sharing the root's prefix", "/library/movies-old/Interstellar (2014)"},
		// Unresolved traversal, which is what a hand-edited row looks like.
		{"traversal that has not been cleaned", root + "/Interstellar (2014)/../../../etc/passwd"},
	}
	for _, c := range outside {
		err := AssertInside(root, c.path)
		if err == nil {
			t.Errorf("%s: %s was accepted as inside %s", c.name, c.path, root)
			continue
		}
		// The same sentinel RemoveMovieFolder wraps, so one errors.Is answers
		// "is this path ours?" whichever of the two was asked.
		if !errors.Is(err, ErrOutsideRoot) {
			t.Errorf("%s: err = %v, want it to wrap ErrOutsideRoot", c.name, err)
		}
	}
}

// A relative root is resolved against the working directory, on both sides, so
// LIBRARY_MOVIES=./movies is not a hole. Both arguments go through
// filepath.Abs, which is why this holds.
func TestAssertInsideResolvesARelativeRoot(t *testing.T) {
	if err := AssertInside("testdata", filepath.Join("testdata", "library")); err != nil {
		t.Errorf("a relative root refused its own child: %v", err)
	}
	if err := AssertInside("testdata", "/etc"); err == nil {
		t.Error("a relative root accepted /etc")
	}
}
