// Package hygiene holds repository-wide checks that belong to no other package.
//
// These enforce two standards that were previously enforced by remembering
// them. A standard nobody checks is a preference, and this repository has
// already paid for that once: 3,316 em dashes accumulated across 284 files
// before anyone counted, and removing them mechanically then damaged product
// strings, table cells and comments because there was no check to notice.
package hygiene

import (
	"bytes"
	"io"
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
		if target, ok := symlinkToShell(abs); ok && !shellAllowlist[rel] {
			t.Errorf("%s is a tracked symlink to %s. Committing a link to a shell puts a "+
				"shell in the tree under another name, and anything with a shebang naming "+
				"it becomes a shell script the OS runs and this check cannot see", rel, target)
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
	// The whole first line, not a fixed 64 bytes. A review padded a shebang past
	// the window with an environment assignment, `env -S PAD=xxxx... bash`, and
	// macOS ran it under Bash while the truncated read saw no interpreter at all.
	// A limit that silently changes the input is a limit that decides the answer.
	head := make([]byte, 8192)
	n, _ := f.Read(head)
	first, complete := "", false
	if i := strings.IndexByte(string(head[:n]), '\n'); i >= 0 {
		first, complete = string(head[:i]), true
	} else {
		first = string(head[:n])
	}
	// A first line longer than the buffer is not something to guess about.
	if !complete && n == len(head) {
		return true
	}
	if !strings.HasPrefix(first, "#!") {
		return false
	}
	// A NUL terminates the interpreter path as far as the kernel is concerned,
	// so `#!/bin/zsh<NUL>rest-of-line` executes zsh while a reader of the whole
	// line sees something else. Cut at the first NUL and judge what actually
	// runs. Found by a review that tracked exactly that file and watched the OS
	// run it.
	if i := strings.IndexByte(first, 0); i >= 0 {
		first = first[:i]
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
		if isShellName(frag) {
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

// symlinkToShell reports a tracked symlink whose target is a shell.
//
// Found by a review that committed `fixture-python` as a link to /bin/zsh and a
// second file whose shebang named it. The OS ran the second file under zsh; this
// check read the shebang, saw a basename that is not a shell, and passed it. The
// walker uses Stat and the reader uses Open, and both follow links, so nothing
// in the chain ever saw the link itself.
//
// Only the link is reported, not every file that might name it. The link is the
// thing that put a shell in the tree, and it is the smaller and more precise
// thing to remove.
func symlinkToShell(abs string) (string, bool) {
	info, err := os.Lstat(abs)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return "", false
	}
	target, err := os.Readlink(abs)
	if err != nil {
		return "", false
	}
	// The name it points at, and the name that name resolves to: a link to a
	// link to a shell is still a shell.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		if isShellName(filepath.Base(resolved)) {
			return target, true
		}
	}
	return target, isShellName(filepath.Base(target))
}

func isShellName(name string) bool {
	switch strings.ToLower(name) {
	case "sh", "bash", "zsh", "dash", "ksh", "ash", "csh", "tcsh", "fish":
		return true
	}
	return false
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
		if isShellName(base) {
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
	// A release tarball is not a checkout, and repository hygiene is not a
	// property a tarball can have. These assertions failed there with "listing
	// tracked files: exit status 128", which reads like a broken machine: an
	// operator building from a tarball hit exactly this and spent time telling
	// it apart from a real failure. Skipping is the honest outcome, and it is
	// distinct from git being absent, which is skipped above for its own reason.
	if err := exec.Command(git, "-C", root, "rev-parse", "--is-inside-work-tree").Run(); err != nil { // #nosec G204
		t.Skip("not a git work tree (a release tarball, most likely): repository " +
			"hygiene is only meaningful in a checkout")
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

// No compiled binary may be committed.
//
// An 11.6 MB Mach-O executable named `dibs` sat tracked at the repository root
// for two commits. It arrived the way these always do: a bare `go build
// ./cmd/dibs` writes ./dibs into the working directory, and the next `git add
// -A` swept it in with everything else.
//
// It was found by an operator auditing Dibs before trusting it with a fleet,
// who called it "the single strongest trust smell available": an unexplained
// committed executable is exactly the shape of a supply-chain compromise, and
// nothing in the tree lets a reader verify it. They deleted it and built from
// source. That is the correct response, and the cost of earning it back is much
// higher than the cost of this test. It also dominated the source tarball.
//
// Detected by CONTENT, not by name or extension: a binary committed as
// `helper`, `tool` or `dibs` has no extension to match on, and the whole point
// is to catch the file nobody meant to add.
func TestNoCompiledBinariesHaveEnteredTheTree(t *testing.T) {
	// The magic numbers for the executable formats a Go build can produce.
	magics := map[string][]byte{
		"ELF":               {0x7f, 'E', 'L', 'F'},
		"Mach-O 64":         {0xcf, 0xfa, 0xed, 0xfe},
		"Mach-O 64 (BE)":    {0xfe, 0xed, 0xfa, 0xcf},
		"Mach-O universal":  {0xca, 0xfe, 0xba, 0xbe},
		"PE (Windows .exe)": {'M', 'Z'},
	}

	root := repoRoot(t)
	walk(t, root, func(rel, abs string) {
		f, err := os.Open(abs) // #nosec G304 -- a tracked path under the repo root
		if err != nil {
			return
		}
		defer func() { _ = f.Close() }()
		head := make([]byte, 4)
		n, _ := io.ReadFull(f, head)
		if n < 2 {
			return
		}
		for format, magic := range magics {
			if len(magic) <= n && bytes.HasPrefix(head[:n], magic) {
				t.Errorf("%s is a committed %s executable.\n"+
					"  Nobody auditing this repository can verify it, and it is the exact\n"+
					"  shape of a supply-chain compromise. Releases ship signed binaries\n"+
					"  with SBOMs; the tree ships source. Untrack it and add it to\n"+
					"  .gitignore: a bare `go build ./cmd/dibs` writes ./dibs, which is how\n"+
					"  the last one arrived.", rel, format)
				return
			}
		}
	})
}
