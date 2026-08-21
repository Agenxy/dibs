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

	quiet := filepath.Join(t.TempDir(), "data")
	if err := configure([]string{"--non-interactive", quiet}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(quiet, "dibs.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("the non-interactive path wrote something other than defaultConfig:\n%q\nwant\n%q", got, body)
	}

	// And the WIZARD, driven the way a person drives it.
	//
	// The first version of this compared defaultConfig against the
	// non-interactive output only, so the interactive path could have diverged
	// while it stayed green: it named both paths and tested one. Raised by the
	// pre-release review. Enter through every question is the documented way to
	// get a correct single-machine setup, so that is what this presses.
	wizard := filepath.Join(t.TempDir(), "data")
	withStdin(t, "\n\n\n\n")
	if err := runWizard(wizard); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(filepath.Join(wizard, "dibs.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("pressing Enter through the wizard does not produce the defaults the "+
			"other path writes:\n%q\nwant\n%q", got, body)
	}
}

// withStdin points os.Stdin at a string for the duration of the test.
func withStdin(t *testing.T, in string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(in); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	saved := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = saved; _ = r.Close() })
}

// Every argument is read before anything is written.
//
// The rule this command already states twice, arrived at the hard way:
// `configure --service --help` wrote a LaunchAgent and exited 0, and `dibs stop
// --help` stopped the daemon. It happened a third time. Arguments after the
// directory were silently ignored, harmless while the wizard needed a terminal
// to do anything, and `--non-interactive` turned that into a silent write on
// the path built to run unattended. Found by the pre-release review, which
// reproduced it from the shipped command.
func TestConfigureReadsEveryArgumentBeforeWriting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := configure([]string{dir, "--non-interactive", "--help"}); err != nil {
		t.Fatalf("help is not an error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dibs.toml")); err == nil {
		t.Error("asking for help wrote the configuration: an argument was acted on " +
			"before the rest had been read")
	}

	// An argument it does not understand is a misunderstanding about what this
	// does, and it writes a file.
	if err := configure([]string{dir, "--wat"}); err == nil {
		t.Error("an unknown flag was accepted")
	}
	if err := configure([]string{dir, filepath.Join(t.TempDir(), "other")}); err == nil {
		t.Error("two data directories were accepted, so one of them was silently ignored")
	}
}
