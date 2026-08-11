// Package hygiene holds repository-wide checks that belong to no other package.
//
// These enforce two standards that were previously enforced by remembering
// them. A standard nobody checks is a preference, and this repository has
// already paid for that once: 3,316 em dashes accumulated across 284 files
// before anyone counted, and removing them mechanically then damaged product
// strings, table cells and comments because there was no check to notice.
package hygiene

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// Written as escapes, not as the characters themselves. Spelling them literally
// would put an em dash in this file, and this file is inside the tree it checks:
// the test would fail on its own source, which is a confusing way to learn that
// the check works.
const (
	emDash = '\u2014'
	enDash = '\u2013'
)

// shellAllowlist is repository-relative paths that may be shell.
//
// Empty, and that is the point: an entry here is a decision, reviewed like one.
// The only cases that qualify are the ones where nothing else can run yet. A
// bootstrap script that obtains the toolchain cannot be written in the
// toolchain; a zsh completion is shell by definition. "Shell would be easier"
// is not on the list.
var shellAllowlist = map[string]bool{}

// Shell is untyped, continues past failures unless every script remembers to
// ask it not to, quotes wrongly under whitespace, and cannot be tested or type
// checked. The rule is worth having mechanically because the pressure to add
// "just one" script arrives when somebody is in a hurry, which is exactly when
// nobody is reading the contributing guide.
func TestNoShellScriptsHaveEnteredTheTree(t *testing.T) {
	root := repoRoot(t)
	walk(t, root, func(rel, abs string) {
		if strings.HasSuffix(rel, ".sh") || strings.HasSuffix(rel, ".bash") {
			if !shellAllowlist[rel] {
				t.Errorf("%s is a shell script. Write it in the project's runner, or in "+
					"Python with a uv shebang. If this is a bootstrap that runs before any "+
					"toolchain exists, add it to shellAllowlist with the reason", rel)
			}
			return
		}
		if shebangIsShell(abs) && !shellAllowlist[rel] {
			t.Errorf("%s has a shell shebang, which makes it a shell script whatever it "+
				"is called", rel)
		}
	})
}

// An em dash is the strongest single tell that a machine wrote a document, and
// this project's credibility rests on the care visible in its reasoning: a
// reader who spots the pattern stops reading the argument and starts assessing
// the author. The measured density here was roughly one per 40 words against a
// human baseline nearer one per 500.
//
// En dashes are checked only where they are doing an em dash's job, which is
// with a space on each side. Tight between characters it is a range, and
// "51-66%" or "§2-§10" is correct typography that this must not break.
func TestProseCarriesNoEmDashes(t *testing.T) {
	root := repoRoot(t)
	walk(t, root, func(rel, abs string) {
		if !checkedForProse(rel) {
			return
		}
		body, err := os.ReadFile(abs) // #nosec G304 -- walking this repository
		if err != nil || !utf8.Valid(body) {
			return
		}
		for i, line := range strings.Split(string(body), "\n") {
			if strings.ContainsRune(line, emDash) {
				t.Errorf("%s:%d contains an em dash. Replace it with the job it was doing: "+
					"a colon when what follows explains what came before, a semicolon or a "+
					"full stop when both halves stand alone, commas or parentheses for an "+
					"aside. Never a hyphen, which reads as a typo.\n  %s", rel, i+1, strings.TrimSpace(line))
			}
			if spacedEnDash(line) {
				t.Errorf("%s:%d uses an en dash as punctuation. In a range it is correct and "+
					"stays; between spaces it is an em dash wearing a disguise.\n  %s",
					rel, i+1, strings.TrimSpace(line))
			}
		}
	})
}

// spacedEnDash reports an en dash with whitespace on either side, which is the
// punctuation use rather than the range use.
func spacedEnDash(line string) bool {
	runes := []rune(line)
	for i, r := range runes {
		if r != enDash {
			continue
		}
		before := i > 0 && unicode.IsSpace(runes[i-1])
		after := i+1 < len(runes) && unicode.IsSpace(runes[i+1])
		if before || after {
			return true
		}
	}
	return false
}

func checkedForProse(rel string) bool {
	switch filepath.Ext(rel) {
	case ".md", ".go", ".js", ".ts", ".html", ".css", ".yml", ".yaml", ".toml", ".json":
		return true
	}
	return false
}

// shebangIsShell reads the first line only. A file is not required to be
// executable to count: a shell script committed without the bit is still a
// shell script, and chmod is one command away.
func shebangIsShell(abs string) bool {
	f, err := os.Open(abs) // #nosec G304 -- walking this repository
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 64)
	n, _ := f.Read(head)
	first, _, _ := strings.Cut(string(head[:n]), "\n")
	if !strings.HasPrefix(first, "#!") {
		return false
	}
	for _, sh := range []string{"/sh", "/bash", "/zsh", "/dash", "/ksh", "env sh", "env bash", "env zsh"} {
		if strings.Contains(first, sh) {
			return true
		}
	}
	return false
}

// walk visits every file Git tracks, and nothing else.
//
// Asking Git rather than walking the filesystem, because "the tree" in these
// checks means the tracked tree and nothing else. Walking found a local Python
// virtualenv under contrib and reported third-party README files and the
// `activate` scripts every venv ships: all true, none of them ours, and none of
// them something a contributor could fix. A rule that fires on other people's
// code teaches people to ignore it.
func walk(t *testing.T, root string, visit func(rel, abs string)) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable, so the tracked file list cannot be established")
	}
	out, err := exec.Command(git, "-C", root, "ls-files", "-z").Output() // #nosec G204
	if err != nil {
		t.Fatalf("listing tracked files: %v", err)
	}
	files := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if len(files) < 2 {
		t.Fatalf("git reported %d tracked files, which cannot be right: the check would "+
			"pass by examining nothing", len(files))
	}
	for _, rel := range files {
		if rel == "" {
			continue
		}
		abs := filepath.Join(root, rel)
		if st, err := os.Stat(abs); err != nil || st.IsDir() {
			continue // deleted but still staged, or a submodule
		}
		visit(rel, abs)
	}
}

// repoRoot walks up for go.mod, so the checks work wherever the test is run
// from and do not encode this package's depth.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory: cannot find the repository root")
		}
		dir = parent
	}
}
