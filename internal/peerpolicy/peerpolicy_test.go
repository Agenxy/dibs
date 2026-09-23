package peerpolicy

import (
	"os"
	"path/filepath"
	"testing"
)

// The three states a hint has to tell apart, which is the whole reason this
// package exists. `dibs doctor` used to warn about the hold unconditionally,
// so an operator who had already fixed it kept being told to fix it, and the
// next real warning would have read like more of the same.
func TestTheThreeStatesADiagnosticHasToTellApart(t *testing.T) {
	user := Source{Name: "your own settings"}
	for _, c := range []struct {
		name string
		val  string
		want Verdict
	}{
		{"nothing set is the parity default, which holds a bypass session", "", Parity},
		{"accept opens the route for every session reading it", "accept", Accept},
		{"hold parks it whatever mode the session is in", "hold", Hold},
		{"refuse opts out of cross-session messages entirely", "refuse", Refuse},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, _ := decide([]Source{user}, []string{c.val})
			if got != c.want {
				t.Errorf("decide(%q) = %v, want %v", c.val, got, c.want)
			}
		})
	}
}

// A REPOSITORY MAY ONLY TIGHTEN, and this is the case that makes the rule
// worth encoding rather than assuming. Reading the sources in order and
// letting the last one win would have a checkout silently loosen what its
// user or their administrator decided, which is the opposite of the client's
// rule and would make Dibs report an open route on a machine that holds.
func TestACheckoutCanTightenAndCannotLoosen(t *testing.T) {
	user := Source{Name: "your own settings"}
	repo := Source{Name: "this checkout's settings", Tightening: true}

	t.Run("a checkout tightens a user's accept", func(t *testing.T) {
		got, where := decide([]Source{user, repo}, []string{"accept", "hold"})
		if got != Hold {
			t.Fatalf("got %v, want Hold: a repo may tighten", got)
		}
		if where != repo.Name {
			t.Errorf("decided by %q, want %q: an operator told to edit the wrong file "+
				"edits the wrong file", where, repo.Name)
		}
	})

	t.Run("and cannot loosen a user's hold", func(t *testing.T) {
		got, where := decide([]Source{user, repo}, []string{"hold", "accept"})
		if got != Hold {
			t.Fatalf("got %v, want Hold: a repo may not loosen", got)
		}
		if where != user.Name {
			t.Errorf("decided by %q, want %q", where, user.Name)
		}
	})

	// THE CASE THAT CAUGHT A REAL BUG IN THIS FUNCTION. Parity is not looser
	// than accept: the client compares a tightening source against "accept"
	// when nothing above it has decided, so a checkout asking for accept with
	// no user setting present changes nothing and the parity default stands.
	// The first version of decide() ranked parity below accept and reported
	// this as an open route.
	t.Run("and a checkout alone cannot turn the route on", func(t *testing.T) {
		got, where := decide([]Source{user, repo}, []string{"", "accept"})
		if got != Parity {
			t.Fatalf("got %v, want Parity: accept from a checkout alone opens nothing", got)
		}
		if where != "" {
			t.Errorf("decided by %q, want nothing to have decided", where)
		}
	})
}

// Managed policy wins outright, and the hint has to say so, because "set
// accept in your own settings" is an afternoon wasted on a machine where an
// administrator has already answered.
func TestManagedPolicyBeatsTheUser(t *testing.T) {
	managed := Source{Name: "your organization's managed settings"}
	user := Source{Name: "your own settings"}
	got, where := decide([]Source{managed, user}, []string{"refuse", "accept"})
	if got != Refuse || where != managed.Name {
		t.Errorf("decide = (%v, %q), want (Refuse, %q)", got, where, managed.Name)
	}
}

// A value this does not recognise is not a value to guess at: it is somebody
// else's schema, and a fourth state invented here would be a claim with
// nothing behind it. An unreadable file is the same answer.
func TestAnUnknownValueDecidesNothing(t *testing.T) {
	user := Source{Name: "your own settings"}
	for _, v := range []string{"yes", "true", "ACCEPT", "  accept", "0"} {
		if got, where := decide([]Source{user}, []string{v}); got != Parity || where != "" {
			t.Errorf("decide(%q) = (%v, %q), want the parity default and no decider", v, got, where)
		}
	}
}

// Read does the file half, and the cases worth covering are the ones that are
// not errors: no file at all is the common state on a fresh machine, and
// malformed json is what a half-written settings file looks like.
func TestReadTreatsAMissingOrBrokenFileAsNoDecision(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()

	if got, where := Read(home, cwd); got != Parity || where != "" {
		t.Errorf("with no settings anywhere: (%v, %q), want the parity default", got, where)
	}

	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(broken, []byte(`{"crossSessionInbound":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := Read(home, cwd); got != Parity {
		t.Errorf("with a half-written settings file: %v, want the parity default", got)
	}

	if err := os.WriteFile(broken, []byte(`{"crossSessionInbound":"accept"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, where := Read(home, cwd)
	if got != Accept || where != "your own settings" {
		t.Errorf("Read = (%v, %q), want (Accept, \"your own settings\")", got, where)
	}
}

// The administrator's file is per-platform, and the point of naming it is that
// a managed Mac is exactly the machine where the wrong path produces the wrong
// advice.
func TestTheManagedPathIsPerPlatform(t *testing.T) {
	was := osName
	t.Cleanup(func() { osName = was })
	for goos, want := range map[string]string{
		"darwin":  "/Library/Application Support/ClaudeCode/managed-settings.json",
		"linux":   "/etc/claude-code/managed-settings.json",
		"windows": `C:\ProgramData\ClaudeCode\managed-settings.json`,
	} {
		osName = func() string { return goos }
		if got := managedPath(); got != want {
			t.Errorf("managedPath() on %s = %q, want %q", goos, got, want)
		}
	}
}
