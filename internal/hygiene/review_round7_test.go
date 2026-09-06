package hygiene

import (
	"os"
	"path/filepath"
	"testing"
)

// A tracked path the walk cannot examine is a failure, not a skip: only a
// path that is not there at all is "deleted but still staged".
func TestATrackedPathThatCannotBeExaminedIsAFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	if skip, problem := classifyTracked(filepath.Join(dir, "dangling")); problem == nil || skip {
		t.Errorf("a symlink to nothing classified as skip=%v problem=%v: it is tracked, "+
			"present, and unreadable, and used to pass as deleted", skip, problem)
	}
	if skip, problem := classifyTracked(filepath.Join(dir, "gone")); !skip || problem != nil {
		t.Errorf("a path that is not there classified as skip=%v problem=%v: that one IS "+
			"deleted but still staged", skip, problem)
	}
	if skip, problem := classifyTracked(dir); !skip || problem != nil {
		t.Errorf("a directory classified as skip=%v problem=%v: submodules are skipped", skip, problem)
	}
	if os.Geteuid() == 0 {
		t.Skip("root enters any directory, so the last case cannot be staged")
	}
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if skip, problem := classifyTracked(filepath.Join(locked, "f")); problem == nil || skip {
		t.Errorf("a file beneath a directory that cannot be entered classified as skip=%v "+
			"problem=%v: it used to pass as deleted", skip, problem)
	}
}
