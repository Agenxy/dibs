package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The permission hint is offered for the permission failure, not for the folder.
//
// It was chosen by PATH alone, so a directory under ~/Desktop that simply had no
// .git was told to move the checkout out of Desktop or grant the daemon Full
// Disk Access, while "not a git repository" sat in the error text underneath.
// Both remedies are heavier than the real fix and one of them moves a working
// tree for nothing. An agent followed it and reported that back.
//
// The distinguishing symptom is already written in that function's own comment:
// TCC makes git BLOCK, so the failure is a deadline. A clean, fast answer from
// git means git ran, which means permission was not the problem.
func TestTheProtectedFolderHintIsOfferedOnlyForABlockedCall(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory to build a protected path from")
	}
	protected := filepath.Join(home, "Desktop", "not-a-checkout")
	plain := filepath.Join(os.TempDir(), "not-a-checkout")

	notARepo := errors.New("exit status 128: fatal: not a git repository")
	blocked := context.DeadlineExceeded

	// The case that was wrong: a protected path, and git answered.
	if got := tccHint(protected, notARepo); strings.Contains(got, "Full Disk Access") {
		t.Errorf("a path under ~/Desktop with no .git is told:\n  %s\n\n"+
			"git ANSWERED, so permission was not the problem. The advice moves a "+
			"checkout or grants a coordination daemon Full Disk Access, and the real "+
			"fix is in the error it was printed beside", got)
	}
	// The case it exists for: a protected path, and git never answered.
	if runtime.GOOS == "darwin" {
		if got := tccHint(protected, blocked); !strings.Contains(got, "Full Disk Access") {
			t.Errorf("a blocked call under ~/Desktop is told %q, which no longer "+
				"explains the one failure this hint was written for", got)
		}
	}
	// And an ordinary path never gets it, however it failed.
	for _, e := range []error{notARepo, blocked} {
		if got := tccHint(plain, e); strings.Contains(got, "Full Disk Access") {
			t.Errorf("a path outside the protected folders is told %q", got)
		}
	}
}
