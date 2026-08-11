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
		// Lowercased, because a case-sensitive check is not a check: smuggle.SH
		// sailed past this and macOS would run it identically.
		switch strings.ToLower(filepath.Ext(rel)) {
		case ".sh", ".bash", ".zsh", ".ksh", ".fish", ".ps1":
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
	switch strings.ToLower(filepath.Ext(rel)) {
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
	return interpreterIsAShell(first) || mentionsAShell(first)
}

// mentionsAShell is the conservative second opinion: any fragment of the line
// whose basename names a shell counts, however the line is quoted or escaped.
//
// The parser above reads a shebang the way env does, and there are forms where
// that is CLEVERER than the kernel, which simply takes the first word. Rather
// than model every disagreement, anything that cannot be shown to be
// shell-free is treated as shell. A false positive here costs a rename or an
// allowlist entry with a reason; a false negative is a shell script in the tree,
// which is the thing this exists to prevent.
func mentionsAShell(shebang string) bool {
	for _, frag := range strings.FieldsFunc(shebang, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\'' || r == '"' || r == '\\' || r == '=' || r == '/'
	}) {
		switch strings.ToLower(frag) {
		case "sh", "bash", "zsh", "dash", "ksh", "ash", "csh", "tcsh", "fish":
			return true
		}
	}
	return false
}

// splitShebang splits on whitespace the way `env -S` does, honouring quotes and
// backslash escapes.
//
// strings.Fields was not enough. `#!/usr/bin/env -S 'bash'` runs Bash, and the
// parser saw a token `'bash'` whose basename is not a shell, so an executable
// shell script passed the check. Anything that decides what a line MEANS has to
// read it the way the thing executing it does.
func splitShebang(line string) []string {
	var (
		out   []string
		cur   strings.Builder
		quote rune
		esc   bool
		open  bool
	)
	flush := func() {
		if open {
			out = append(out, cur.String())
			cur.Reset()
			open = false
		}
	}
	for _, r := range line {
		switch {
		case esc:
			cur.WriteRune(r)
			open, esc = true, false
		case r == '\\' && quote != '\'':
			esc, open = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
			open = true
		case r == '\'' || r == '"':
			quote, open = r, true
		case r == ' ' || r == '\t':
			flush()
		default:
			cur.WriteRune(r)
			open = true
		}
	}
	flush()
	return out
}

// interpreterIsAShell parses a shebang into the program it will actually run.
//
// Substring matching was tried and defeated immediately: `#!/usr/bin/env -S bash`
// contains neither "env bash" nor "/bash", so the smuggled file passed. The line
// has to be read the way the kernel and env read it, which means walking past
// env, its flags, and any VAR=VALUE assignments to reach the real interpreter.
func interpreterIsAShell(shebang string) bool {
	fields := splitShebang(strings.TrimPrefix(shebang, "#!"))
	for len(fields) > 0 {
		word := fields[0]
		fields = fields[1:]
		base := strings.ToLower(filepath.Base(word))
		if base == "env" {
			continue // the interpreter is further along
		}
		// env's own flags, and the assignments it accepts before the command.
		if strings.HasPrefix(word, "-") {
			if rest, ok := strings.CutPrefix(word, "--split-string="); ok {
				fields = append(splitShebang(rest), fields...)
			}
			continue
		}
		if strings.Contains(word, "=") && !strings.Contains(word, "/") {
			continue
		}
		switch base {
		case "sh", "bash", "zsh", "dash", "ksh", "ash", "csh", "tcsh", "fish":
			return true
		}
		return false // the first real interpreter is something else
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
	visited := 0
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		abs := filepath.Join(root, rel)
		if st, err := os.Stat(abs); err != nil || st.IsDir() {
			continue // deleted but still staged, or a submodule
		}
		visit(rel, abs)
		visited++
	}
	// Counted AFTER the walk, not before it. The guard used to test how many
	// lines `ls-files` printed, and an index holding two nonexistent gitlinks
	// satisfied it while the loop examined zero real files: the check passed by
	// looking at nothing, which is the exact failure the guard was added to
	// prevent. Count what was actually opened.
	if visited < minimumTrackedFiles {
		t.Fatalf("only %d tracked files were examined, which cannot be right for this "+
			"repository: the check would pass by looking at nothing", visited)
	}
}

// minimumTrackedFiles is a floor far below the real count and far above zero.
// It exists to catch a broken invocation, not to track the repository's size.
const minimumTrackedFiles = 50

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
