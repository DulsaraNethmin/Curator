package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// The headline, over the committed fixture: a row for a folder that holds no film
// loses its ROW, and the folder stays exactly where it is.
//
// The os.Stat at the end is not belt-and-braces. Leaving the directory alone is
// the promise D33 makes — at the phase 10 cutover those directories belong to
// Radarr — and it is the one assertion that would catch a "tidy up while we are
// here" change.
func TestScanRemovesARowForAFolderWithNoFilm(t *testing.T) {
	st := newFakeStore()
	empty := filepath.Join(fixtureRoot, "Gladiator (2000)")
	stale := st.seedOnDisk(empty, "Gladiator", 2000)
	h := newTestServer(t, st, nil)

	got := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	if got.Removed != 1 {
		t.Errorf("removed = %d, want 1", got.Removed)
	}
	if got.Missing != 0 {
		t.Errorf("missing = %d, want 0 — the folder was read, and it holds no film", got.Missing)
	}
	if _, ok := st.byID[stale.ID]; ok {
		t.Error("the row survived")
	}

	info, err := os.Stat(empty)
	if err != nil {
		t.Fatalf("the folder was removed from disk: %v — the scan removes rows only (D33)", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is no longer a directory", empty)
	}
}

// The most important test here: a library that is not mounted must cost nothing.
//
// An unmounted disk is not a read failure — the mount point is an empty directory
// that reads fine. Every row falls through to "this scan could not account for it",
// which is a reason to keep a row and never a reason to delete one.
func TestScanOfAnEmptyRootRemovesNothing(t *testing.T) {
	root := t.TempDir()
	st := newFakeStore()
	for _, title := range []string{"Gladiator", "Jaws", "Troy", "Babylon", "Deadpool"} {
		st.seedOnDisk(filepath.Join(root, title+" (2000)"), title, 2000)
	}
	h := newTestServerAt(t, st, nil, root)

	got := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	if got.Removed != 0 {
		t.Fatalf("removed = %d, want 0 — an empty library root is what an unplugged disk looks like", got.Removed)
	}
	if got.Missing != 5 {
		t.Errorf("missing = %d, want 5", got.Missing)
	}
	if len(st.order) != 5 {
		t.Errorf("%d rows survived, want 5", len(st.order))
	}
}

// A row pointing outside LIBRARY_MOVIES can never be served — the stream endpoint
// refuses it with the same containment check — so it is dead weight. Its directory
// is somebody else's and is left alone.
func TestScanRemovesARowOutsideTheLibraryRoot(t *testing.T) {
	elsewhere := t.TempDir()
	outside := filepath.Join(elsewhere, "Gladiator (2000)")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// With a real film in it, so the removal cannot be mistaken for "no media".
	writeTestFile(t, filepath.Join(outside, "Gladiator (2000).mkv"), 4096)

	st := newFakeStore()
	stale := st.seedOnDisk(outside, "Gladiator", 2000)
	h := newTestServer(t, st, nil) // rooted at the fixture, so this row is outside it

	got := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	if got.Removed != 1 {
		t.Errorf("removed = %d, want 1", got.Removed)
	}
	if _, ok := st.byID[stale.ID]; ok {
		t.Error("the row survived")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("a folder outside the library root was touched: %v", err)
	}
}

// A wanted film has library_path NULL. "There is no film in its folder" is not a
// statement about it — it has no folder — so it is not a candidate at all.
func TestScanNeverTouchesAWantedRow(t *testing.T) {
	st := newFakeStore()
	wanted := st.seedWanted("Dune Part Three", 2026)
	h := newTestServer(t, st, nil)

	got := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	if got.Removed != 0 || got.Missing != 0 {
		t.Errorf("removed/missing = %d/%d, want 0/0", got.Removed, got.Missing)
	}
	if _, ok := st.byID[wanted.ID]; !ok {
		t.Fatal("a wanted row was pruned")
	}
}

// The window this guards is real: the importer creates the destination folder and
// only then hardlinks into it, so a film mid-import legitimately has an empty
// folder. Deleting then would take its downloads rows with it.
func TestScanNeverPrunesARowWithADownloadInFlight(t *testing.T) {
	st := newFakeStore()
	empty := filepath.Join(fixtureRoot, "Gladiator (2000)")
	row := st.seedOnDisk(empty, "Gladiator", 2000)
	st.downloading[row.ID] = true
	h := newTestServer(t, st, nil)

	got := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	if got.Removed != 0 {
		t.Fatalf("removed = %d, want 0 — a film being fetched is never pruned", got.Removed)
	}
	// Not counted as missing either: nothing about it is unaccounted for.
	if got.Missing != 0 {
		t.Errorf("missing = %d, want 0", got.Missing)
	}
	if _, ok := st.byID[row.ID]; !ok {
		t.Fatal("a row with a download in flight was pruned")
	}
}

// "Could not read it" is not "there is no film in it", and only the second removes
// a row. This is the branch a failing disk lands on.
func TestScanKeepsARowWhoseFolderCannotBeRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not stop root reading the directory")
	}

	root := t.TempDir()
	unreadable := filepath.Join(root, "Gladiator (2000)")
	if err := os.Mkdir(unreadable, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Skipf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o755) })

	st := newFakeStore()
	row := st.seedOnDisk(unreadable, "Gladiator", 2000)
	h := newTestServerAt(t, st, nil, root)

	got := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	if got.Removed != 0 {
		t.Fatalf("removed = %d, want 0", got.Removed)
	}
	if got.Missing != 1 {
		t.Errorf("missing = %d, want 1", got.Missing)
	}
	if _, ok := st.byID[row.ID]; !ok {
		t.Fatal("a row whose folder could not be read was pruned")
	}
}

// A folder whose name has drifted away from "Title (Year)" says nothing about
// what is inside it. The film may well still be there, so the row stays and the
// count says a human should look.
func TestScanKeepsARowWhoseFolderNameNoLongerParses(t *testing.T) {
	root := t.TempDir()
	drifted := filepath.Join(root, "Gladiator 2000")
	if err := os.Mkdir(drifted, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestFile(t, filepath.Join(drifted, "Gladiator.mkv"), 4096)

	st := newFakeStore()
	row := st.seedOnDisk(drifted, "Gladiator", 2000)
	h := newTestServerAt(t, st, nil, root)

	got := decodeScan(t, do(t, h, http.MethodPost, "/api/scan"))
	if got.Removed != 0 {
		t.Fatalf("removed = %d, want 0", got.Removed)
	}
	if got.Missing != 1 {
		t.Errorf("missing = %d, want 1", got.Missing)
	}
	if _, ok := st.byID[row.ID]; !ok {
		t.Fatal("a row whose folder name no longer parses was pruned")
	}
}

// A failing prune is a 500, not a 200 carrying a cleanup that did not happen —
// the same answer a failing upsert gets, for the same reason.
func TestScanPruneFailureIs500(t *testing.T) {
	st := newFakeStore()
	st.seedOnDisk(filepath.Join(fixtureRoot, "Gladiator (2000)"), "Gladiator", 2000)
	st.deleteErr = errors.New("database is locked")
	h := newTestServer(t, st, nil)

	rec := do(t, h, http.MethodPost, "/api/scan")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	assertErrorBody(t, rec)
}

func TestScanListingCandidatesFailureIs500(t *testing.T) {
	st := newFakeStore()
	st.onDiskErr = errors.New("database is locked")
	h := newTestServer(t, st, nil)

	rec := do(t, h, http.MethodPost, "/api/scan")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	assertErrorBody(t, rec)
}

// The three counts are a screen, so they have to be in the JSON under the names
// the screen reads.
func TestScanResponseCarriesTheNewCounts(t *testing.T) {
	st := newFakeStore()
	st.seedOnDisk(filepath.Join(fixtureRoot, "Gladiator (2000)"), "Gladiator", 2000)
	h := newTestServer(t, st, nil)

	rec := do(t, h, http.MethodPost, "/api/scan")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	for key, want := range map[string]float64{"empty": 27, "removed": 1, "missing": 0, "scanned": 2} {
		got, present := body[key]
		if !present {
			t.Errorf("%q missing from the response: %v", key, body)
			continue
		}
		if got != want {
			t.Errorf("%q = %v, want %v", key, got, want)
		}
	}
}

func writeTestFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
