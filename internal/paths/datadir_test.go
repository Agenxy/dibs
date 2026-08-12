package paths

import (
	"os"
	"path/filepath"
	"testing"
)

// A rename must not orphan a board.
//
// `~/.agents` was never a name Dibs chose: the 0.0.3 vocabulary rename turned
// every "lane" into an "agent" and took `~/.lanes` with it, so the product
// shipped writing to a generic noun in the user's home. Correcting it to
// `~/.dibs` is right, and correcting it by walking past an existing directory
// would be a data-loss bug wearing a naming fix as a disguise: the daemon would
// come up clean, create an empty ledger, and every claim and message from
// before the upgrade would simply be gone from the board.
//
// So this asserts the fallback, not the preference. The preference is the easy
// half and would pass with the fallback deleted.
func TestAnInheritedDataDirectoryIsStillFound(t *testing.T) {
	for _, name := range legacyDirs {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("DIBS_DIR", "")
			old := filepath.Join(home, name)
			if err := os.MkdirAll(old, 0o700); err != nil {
				t.Fatal(err)
			}

			dir, inherited := Resolve()
			if dir != old {
				t.Errorf("Resolve() = %q, want the existing %q: an upgrade that walks past\n"+
					"the old directory starts a fresh ledger and loses the board", dir, old)
			}
			if inherited != old {
				t.Errorf("inheritedFrom = %q, want %q: nothing can tell the user which\n"+
					"directory it opened if Resolve does not say", inherited, old)
			}
		})
	}
}

// Once a user has moved to the current name, an old directory left behind must
// not pull the daemon back. Two directories that both look live is how a
// history forks in half, and the halves are each individually valid, so nothing
// downstream can detect it.
func TestTheCurrentNameWinsOverAnInheritedOne(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DIBS_DIR", "")
	for _, name := range append([]string{".dibs"}, legacyDirs...) {
		if err := os.MkdirAll(filepath.Join(home, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	dir, inherited := Resolve()
	if want := filepath.Join(home, ".dibs"); dir != want {
		t.Errorf("Resolve() = %q, want %q", dir, want)
	}
	if inherited != "" {
		t.Errorf("reported an inherited directory (%q) while using the current one", inherited)
	}
}

// A fresh machine gets the product's own name, which is the whole point of the
// change. Asserting the literal, not a constant built from the same expression
// the code uses: a sweep that renames the product must come and edit this.
func TestAFreshInstallGetsTheProductName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DIBS_DIR", "")

	dir, inherited := Resolve()
	if want := filepath.Join(home, ".dibs"); dir != want {
		t.Errorf("a fresh install resolved to %q, want %q", dir, want)
	}
	if inherited != "" {
		t.Errorf("a fresh install claims to have inherited %q", inherited)
	}
}

// An explicit DIBS_DIR is an instruction, not a hint, and it must win even when
// a legacy directory exists. Development instances and second daemons are
// addressed this way, and a fallback that could override it would silently
// point a test run at the developer's real board.
func TestAnExplicitDirWinsOverEverything(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range append([]string{".dibs"}, legacyDirs...) {
		if err := os.MkdirAll(filepath.Join(home, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	explicit := filepath.Join(t.TempDir(), "elsewhere")
	t.Setenv("DIBS_DIR", explicit)

	if dir, _ := Resolve(); dir != explicit {
		t.Errorf("Resolve() = %q, want the explicit %q", dir, explicit)
	}
}

// A FILE named ~/.agents is not a data directory. Treating it as one reports a
// path the caller cannot open, and the resulting error names the wrong problem:
// the user reads "cannot open ~/.agents" and goes looking for a directory that
// was never there.
func TestAFileWithALegacyNameIsNotADataDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DIBS_DIR", "")
	if err := os.WriteFile(filepath.Join(home, legacyDirs[0]), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}

	dir, inherited := Resolve()
	if want := filepath.Join(home, ".dibs"); dir != want {
		t.Errorf("Resolve() = %q, want %q: a file is not a data directory", dir, want)
	}
	if inherited != "" {
		t.Errorf("reported inheriting %q, which is a file", inherited)
	}
}
