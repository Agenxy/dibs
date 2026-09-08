package hygiene

import (
	"strings"
	"testing"
)

// The no-shell guard removes quoted arguments before it reads a command,
// because prose in a help string is not control flow. A double-quoted
// argument that carries a command substitution is still executed by the
// shell, and removing it removed the second program from what the guard
// read: `go run ./tools/x "$(printf y)"` passed.
func TestTheNoShellGuardSeesASubstitutionInsideQuotes(t *testing.T) {
	// The probe: the stripping still does its job on prose.
	if got := shellBody(`echo "asked for Desktop access"`); strings.Contains(got, "for ") {
		t.Fatalf("setup: prose inside quotes is read as a loop: %q", got)
	}
	for _, cmd := range []string{
		`go run ./tools/x "$(printf y)"`,
		"go run ./tools/x \"`printf y`\"",
		`go run ./tools/x "${HOME#/}"`,
	} {
		got := shellBody(cmd)
		if !strings.Contains(got, "$(") && !strings.Contains(got, "`") && !strings.Contains(got, "${") {
			t.Errorf("%s: the substitution the shell would run is gone from what the guard reads: %q", cmd, got)
		}
	}
	// Single quotes expand nothing, so what is inside them is an argument.
	if got := shellBody(`echo '$(not run)'`); strings.Contains(got, "$(") {
		t.Errorf("a single-quoted argument is read as a substitution: %q", got)
	}
}
