package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The wizard assumes a TTY, and the machines that most need configuring are
// headless and reached by `ssh host command`, which has none. A second machine
// in a fleet hits this on its first command. Reported against v0.0.6.
func TestConfigureCanRunWithoutATerminal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := configure([]string{"--non-interactive", dir}); err != nil {
		t.Fatalf("non-interactive configure failed: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "dibs.toml"))
	if err != nil {
		t.Fatalf("no config written: %v", err)
	}
	if !strings.Contains(string(b), `addr = "127.0.0.1:4777"`) {
		t.Errorf("defaults not written: %q", b)
	}

	// A scripted re-run must not silently replace a hand-edited config: there
	// is no prompt on this path to catch it, which is the whole point of the
	// path.
	err = configure([]string{"--non-interactive", dir})
	if err == nil {
		t.Fatal("a second run overwrote the existing config without asking")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("refusal does not say why: %v", err)
	}
}

// Both paths must agree about what "the defaults" are. They were two separate
// string builders, which is how one of them ends up a release behind.
func TestBothConfigurePathsWriteTheSameDefaults(t *testing.T) {
	body := defaultConfig("127.0.0.1:4777")
	dir := filepath.Join(t.TempDir(), "data")
	if err := configure([]string{"--non-interactive", dir}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "dibs.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != body {
		t.Errorf("the non-interactive path wrote something other than defaultConfig:\n%q\nwant\n%q", b, body)
	}
}
