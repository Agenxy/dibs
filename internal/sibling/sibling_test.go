package sibling

import (
	"os"
	"path/filepath"
	"testing"
)

// A sibling installed where a service's PATH does not reach is still found:
// the daemon under launchd sees a few system directories and nothing of the
// user's, and fell back to its own identity while doctor saw Supgang.
func TestASiblingOffThePathIsFoundInItsInstallDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir()) // nothing on it
	if got := Find("supgang-under-test"); got != "" {
		t.Fatalf("found %q with nothing installed", got)
	}
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(bin, "supgang-under-test")
	if err := os.WriteFile(p, []byte("#!/bin/false\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Find("supgang-under-test"); got != p {
		t.Errorf("Find = %q, want the binary in ~/.local/bin, where the installer put it", got)
	}
	// Not executable is not installed.
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Find("supgang-under-test"); got != "" {
		t.Errorf("a non-executable file was found as a sibling: %q", got)
	}
}
