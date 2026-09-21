package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A portable path is contained with the portable separator.
//
// Both operands are made portable before the comparison, and the
// separator joining them was filepath.Separator: `\\` on Windows, so
// `C:/work/repo/pkg` was not beneath `C:/work/repo`. A Windows hub
// answered 403 to an index shipped from a subdirectory, and eviction did
// not count an agent in one as keeping the index alive.
//
// This test cannot fail on a unix host, where both separators are `/`:
// it pins the rule for the platform that has two of them, and the defect
// was found by reading rather than by running. Round forty-five of the
// pre-release review.
func TestPortablePathsAreContainedWithThePortableSeparator(t *testing.T) {
	for _, c := range []struct {
		p, dir string
		want   bool
	}{
		{`C:/work/repo/pkg`, `C:/work/repo`, true},
		{`C:/work/repo`, `C:/work/repo`, true},
		{`C:/work/repo-two/pkg`, `C:/work/repo`, false},
		{`//share/team/repo/pkg`, `//share/team/repo`, true},
		{`/w/repo/pkg`, `/w/repo`, true},
		{`/w/other/pkg`, `/w/repo`, false},
	} {
		if got := underDir(c.p, c.dir); got != c.want {
			t.Errorf("underDir(%q, %q) = %v, want %v", c.p, c.dir, got, c.want)
		}
	}
}

// A shipped index is not calibrated against this machine's disk.
//
// Calibration samples commits and builds a held-out index by running
// git in the root, and a shipped index's root is a path on the
// SHIPPER's machine. If an unrelated checkout happens to sit at that
// path here, the threshold deciding which suggestions an agent sees was
// measured on somebody else's history; if nothing sits there, this
// retries the very access the shipment exists to work around.
//
// Measured by whether git RAN, not by the number that came back: a
// calibration that fails also returns the configured threshold, so the
// value alone cannot tell the two apart. The stand-in git records that
// it was called and exits non-zero, which is what an unreadable tree
// looks like anyway. Round forty-nine of the pre-release review.
func TestAShippedIndexIsNotCalibratedAgainstThisDisk(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(bin, "called")
	// A stand-in for git, compiled rather than scripted: this repository
	// does not ship shell, not even into a temporary directory.
	src := filepath.Join(t.TempDir(), "git.go")
	prog := "package main\n\nimport \"os\"\n\nfunc main() {\n" +
		"\tf, _ := os.OpenFile(`" + marker + "`, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)\n" +
		"\tif f != nil {\n\t\t_, _ = f.WriteString(\"x\")\n\t\t_ = f.Close()\n\t}\n" +
		"\tos.Exit(1)\n}\n"
	if err := os.WriteFile(src, []byte(prog), 0o600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(bin, "git"), src) // #nosec G204 -- paths this test created
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build the stand-in git here: %v\n%s", err, out)
	}
	t.Setenv("PATH", bin)

	root := t.TempDir()
	// notify 0 is the case that CALIBRATES: a configured threshold
	// returns before any of this, so a fixture that sets one measures
	// nothing. The first version of this test did exactly that and
	// passed against the unfixed code.
	f := &scorerFlags{notify: 0}

	// A shipped index: nothing on this disk is consulted.
	if got := f.notifyFor(t.Context(), root, nil, nil, false); got != 0 {
		t.Fatalf("a shipped index answered %v, want the configured 0", got)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("git ran for a shipped index: that root is a path on the shipper's machine, " +
			"so whatever is here is either somebody else's checkout or the access that " +
			"failed in the first place")
	}

	// A tree the daemon mined itself: calibration is attempted, which is
	// what makes the check above mean anything.
	_ = f.notifyFor(t.Context(), root, nil, nil, true)
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("git did not run for a local tree either: this test cannot tell the two " +
			"cases apart, and proves nothing about the one above")
	}

	// An operator's own threshold is honoured either way, unmeasured.
	f.notify = 0.42
	if got := f.notifyFor(t.Context(), root, nil, nil, false); got != 0.42 {
		t.Fatalf("the configured notify threshold became %v", got)
	}
}
