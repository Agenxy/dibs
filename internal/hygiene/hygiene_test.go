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
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/agenxy/dibs/internal/release"
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

// A release must reach the people who install it.
//
// Every release to v0.0.4 was a DRAFT. A draft's artifacts are visible only to
// accounts with push access, while the tag it names is public, so the Homebrew
// cask pointed at `releases/download/v0.0.4/...` and that URL answered 404 for
// everyone except the owner. The first command in the README had never worked
// for anybody else, and nothing failed: the tag existed, the workflow was
// green, the cask was updated, and the artifacts were unreachable.
//
// That is the shape this repository keeps finding: a thing that is true
// everywhere except where somebody stands. So it is asserted, in the file that
// decides it, rather than left to be noticed by a stranger who cannot install.
func TestReleasesArePublishedAndReachTheTap(t *testing.T) {
	blob, err := os.ReadFile(filepath.Join(repoRoot(t), ".goreleaser.yml"))
	if err != nil {
		t.Skip("no .goreleaser.yml here")
	}
	cfg := string(blob)

	if strings.Contains(cfg, "draft: true") {
		t.Error("releases are drafted, so their artifacts are unreachable to everyone " +
			"without push access while the tag they name is public. `brew install` and " +
			"every download URL 404 for the people the release is for")
	}
	// The cask is how a release becomes installable; a release nothing publishes
	// to is the same invisibility by another route.
	if !strings.Contains(cfg, "homebrew_casks:") {
		t.Error("nothing publishes a Homebrew cask, so `brew install agenxy/tap/dibs` " +
			"cannot track releases")
	}
	if !strings.Contains(cfg, "name: homebrew-tap") {
		t.Error("the cask does not name the tap repository it must be pushed to")
	}
}

// server.json must satisfy the registry's constraints BEFORE a tag reaches it.
//
// The first publish failed at the registry on `expected length <= 100` for
// description, after authenticating, installing the publisher and stamping the
// version. Every one of those steps worked; the payload was wrong, and the only
// thing that said so was a 422 from a remote service at the end of a release.
//
// The constraints are read from the SCHEMA the manifest declares, not copied
// into this test, so tightening one upstream fails here rather than in the
// registry. Offline, the schema cannot be fetched and the check skips: a
// release must not depend on a network fetch to be gated, and the registry
// itself is the backstop for the case this cannot see.
func TestTheRegistryManifestWouldBeAccepted(t *testing.T) {
	blob, err := os.ReadFile(filepath.Join(repoRoot(t), "server.json"))
	if err != nil {
		t.Skip("no server.json here")
	}
	var doc map[string]any
	if err := json.Unmarshal(blob, &doc); err != nil {
		t.Fatalf("server.json is not JSON: %v", err)
	}
	schemaURL, _ := doc["$schema"].(string)
	if schemaURL == "" {
		t.Fatal("server.json declares no $schema, so nothing can check it")
	}

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(schemaURL) // #nosec G107 -- the URL the manifest itself declares
	if err != nil {
		t.Skipf("cannot reach the schema (offline?): %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var schema struct {
		Definitions struct {
			ServerDetail struct {
				Required   []string `json:"required"`
				Properties map[string]struct {
					MaxLength int    `json:"maxLength"`
					MinLength int    `json:"minLength"`
					Pattern   string `json:"pattern"`
				} `json:"properties"`
			} `json:"ServerDetail"`
		} `json:"definitions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&schema); err != nil {
		t.Skipf("schema did not decode: %v", err)
	}
	sd := schema.Definitions.ServerDetail
	if len(sd.Properties) == 0 {
		t.Skip("schema shape changed; the registry remains the backstop")
	}

	for _, req := range sd.Required {
		if _, ok := doc[req]; !ok {
			t.Errorf("server.json is missing %q, which the registry requires", req)
		}
	}
	for key, val := range doc {
		if key == "$schema" {
			continue
		}
		rule, known := sd.Properties[key]
		if !known {
			t.Errorf("server.json carries %q, which the schema does not define", key)
			continue
		}
		s, isString := val.(string)
		if !isString {
			continue
		}
		if rule.MaxLength > 0 && len(s) > rule.MaxLength {
			t.Errorf("%s is %d characters; the registry accepts at most %d. "+
				"It fails at publish time, after a tag is already cut",
				key, len(s), rule.MaxLength)
		}
		if rule.MinLength > 0 && len(s) < rule.MinLength {
			t.Errorf("%s is shorter than the registry's minimum of %d", key, rule.MinLength)
		}
		if rule.Pattern != "" {
			ok, err := regexp.MatchString(rule.Pattern, s)
			if err == nil && !ok {
				t.Errorf("%s = %q does not match the registry's pattern %s", key, s, rule.Pattern)
			}
		}
	}
}

// The published version must be the same everywhere it is written down.
//
// The Claude Code plugin manifest said 0.0.0 while the project was at 0.0.5, so
// anyone installing it, or reviewing it for the official plugin directory, saw a
// version that had not existed since the first commit. Nothing failed: a stale
// version is valid JSON, passes `claude plugin validate`, and installs fine.
//
// The owner's standing requirement is that public sources track main. That is
// enforceable for the ones a release writes (the cask, the registry entry) and
// was not for the ones a human edits, which is precisely where it drifted.
func TestTheVersionIsTheSameInEveryManifest(t *testing.T) {
	root := repoRoot(t)
	want, err := release.Current(root)
	if err != nil {
		t.Skip(err)
	}
	for _, rel := range release.Manifests {
		blob, err := os.ReadFile(filepath.Join(root, rel)) // #nosec G304 -- a manifest named in release.Manifests
		if err != nil {
			continue // not every manifest exists in every checkout
		}
		var doc struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(blob, &doc) != nil || doc.Version == "" {
			continue
		}
		if doc.Version != want {
			t.Errorf("%s says version %q; the changelog's newest release is %q. "+
				"`task release VERSION=%s` writes both, and doing it by hand is how "+
				"a manifest sat at 0.0.0 through five releases",
				rel, doc.Version, want, want)
		}
	}
}

func TestNoDocumentReadsLikeARenameRanOverIt(t *testing.T) {
	// Each entry is a fragment plus what it means when it appears.
	broken := []struct{ frag, why string }{
		{"agents agents", "a plural noun doubled: one of these was `spaces`"},
		{"agent agent", "a noun doubled: one of these was `space`"},
		{"spaces spaces", "a plural noun doubled: one of these was `agents`"},
		{"space space", "a noun doubled: one of these was `agent`"},
		// No article. These read "an agent's agent" for as long as the guard has
		// existed, and the damage in the tree read "a dead agent's agent", which
		// the literal article walked straight past: four sites in SPEC.md and the
		// pi plugin survived every run. A possessive takes any determiner, so
		// pinning one spells out three quarters of the cases you are not checking.
		{"agent's agent", "an agent does not have an agent; it has a space"},
		{"space's space", "a space does not have a space"},
		{"a agent", "the article was left behind by a rename from a consonant word"},
		{"an space", "the article was left behind by a rename from a vowel word"},
		{"a announcement", "the article does not match the noun"},
		{"an lane", "the article does not match the noun"},
	}
	// Whole words only. Without the boundaries "metadata agents" matches
	// "a agent" across the join, which is the kind of false positive that gets
	// a guard deleted rather than fixed.
	pats := make([]*regexp.Regexp, len(broken))
	for i, b := range broken {
		pats[i] = regexp.MustCompile(`\b` + regexp.QuoteMeta(b.frag) + `\b`)
	}
	root := repoRoot(t)
	walk(t, root, func(rel, abs string) {
		// This file spells every fragment out in order to look for them.
		if !checkedForProse(rel) || rel == "internal/hygiene/hygiene_test.go" {
			return
		}
		body, err := os.ReadFile(abs) // #nosec G304 -- walking this repository
		if err != nil || !utf8.Valid(body) {
			return
		}
		for i, line := range strings.Split(string(body), "\n") {
			low := strings.ToLower(line)
			for j, b := range broken {
				if !pats[j].MatchString(low) {
					continue
				}
				t.Errorf("%s:%d reads %q: %s. A sweep cannot tell the participant "+
					"from the work it joins, so re-read the sentence and pick the one "+
					"the paragraph is about.\n  %s",
					rel, i+1, b.frag, b.why, strings.TrimSpace(line))
			}
		}
	})
}

// A list of things to keep in sync is itself a thing that falls out of sync.
//
// release.Manifests is what `task release` stamps AND what the test above
// asserts, so those two cannot disagree. What neither can see is a manifest
// nobody put on the list: `plugins/claude-desktop/manifest.json` sat at 0.0.0
// while the project shipped 0.0.5, invisible to every check, because it had
// never been anybody's to remember.
//
// So the list is not trusted. This goes looking for the thing it describes: any
// JSON in the tree with a top-level "version" is a file that states a release
// number, and either it is stamped or it is deliberately not ours.
func TestNoVersionedManifestEscapesTheStamp(t *testing.T) {
	root := repoRoot(t)
	stamped := map[string]bool{}
	for _, rel := range release.Manifests {
		stamped[rel] = true
	}
	// Files that carry a version of something that is not Dibs. Each one is a
	// decision, spelled out, rather than a pattern that could quietly widen.
	exempt := map[string]string{
		"package.json":      "a JS toolchain's own manifest, versioned by its ecosystem",
		"package-lock.json": "generated by the package manager",
	}
	walk(t, root, func(rel, abs string) {
		if filepath.Ext(rel) != ".json" || stamped[rel] {
			return
		}
		if why, ok := exempt[filepath.Base(rel)]; ok {
			t.Logf("%s: exempt (%s)", rel, why)
			return
		}
		blob, err := os.ReadFile(abs) // #nosec G304 -- walking this repository
		if err != nil {
			return
		}
		var doc struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(blob, &doc) != nil || doc.Version == "" {
			return
		}
		t.Errorf("%s states version %q and is not in release.Manifests, so no release "+
			"stamps it and no test checks it. Add it there, or add it to this test's "+
			"exempt list with the reason it is not ours to version", rel, doc.Version)
	})
}

// The tag is what the world receives, and nothing compared anything to it.
//
// The checks above hold the manifests to the changelog, which leaves the
// changelog itself unverified: forget to claim the Unreleased section and
// `git tag v0.0.6` publishes a release whose every manifest still says 0.0.5,
// with a green gate, because the manifests and the changelog agree with each
// other about the wrong number.
//
// The release workflow runs this gate against the TAGGED commit before it
// publishes anything, so the check belongs here and it fails the release rather
// than the developer. Silent on an ordinary checkout, where there is no tag and
// nothing to disagree with.
func TestATaggedCommitAgreesWithItsChangelog(t *testing.T) {
	root := repoRoot(t)
	out, err := exec.Command("git", "-C", root, "tag", "--points-at", "HEAD").Output()
	if err != nil {
		t.Skip("no git tags readable here")
	}
	var tags []string
	for _, line := range strings.Fields(string(out)) {
		if strings.HasPrefix(line, "v") {
			tags = append(tags, line)
		}
	}
	if len(tags) == 0 {
		t.Skip("HEAD is not a release commit")
	}
	want, err := release.Current(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		if strings.TrimPrefix(tag, "v") == want {
			return
		}
	}
	t.Errorf("HEAD is tagged %v but the changelog's newest release is %q: this commit "+
		"would publish artifacts, a Homebrew cask and a registry entry under a version "+
		"that no file in it names. Run `task release VERSION=…` and move the tag onto "+
		"the commit it produces", tags, want)
}

// The helper the daemon looks for must be the one the build produces.
//
// It was not, for the whole life of the rename. `humanauth.helperName` said
// "agents-presence" while the Taskfile compiled and installed "dibs-presence",
// so findHelper looked for a file that has never existed on any machine and
// every presence check answered Unavailable. Touch ID is the one assertion in
// Dibs that must not be forgeable by software, and it was silently off.
//
// Nothing could catch it: the Go tests never exec the helper, the Taskfile
// never reads the constant, and the product's own message for the failure
// ("this build ships without the presence helper") reads as a packaging
// decision rather than a typo, so nobody went looking.
//
// Two files that must agree and no compiler between them is precisely what this
// package is for.
func TestThePresenceHelperIsTheOneThatGetsBuilt(t *testing.T) {
	root := repoRoot(t)

	src, err := os.ReadFile(filepath.Join(root, "internal/humanauth/presence.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`helperName = "([^"]+)"`).FindSubmatch(src)
	if m == nil {
		t.Fatal("presence.go no longer declares helperName; this guard cannot see what " +
			"the daemon looks for")
	}
	want := string(m[1])

	task, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// Every place the Taskfile names a presence binary: the compile output and
	// the install/remove lines. All of them have to be the same word.
	names := regexp.MustCompile(`[\w./{}-]*/([a-z0-9]+-presence)\b`).FindAllStringSubmatch(string(task), -1)
	if len(names) == 0 {
		t.Fatal("the Taskfile no longer builds a presence helper; this guard cannot see " +
			"what ships")
	}
	seen := map[string]bool{}
	for _, n := range names {
		seen[n[1]] = true
	}
	for name := range seen {
		if name != want {
			t.Errorf("the daemon execs %q but the build produces/installs %q. findHelper "+
				"would look for a file that is never created, every presence check would "+
				"answer Unavailable, and the human identity check would be silently off",
				want, name)
		}
	}

	// AND IT MUST NAME ITS TARGET.
	//
	// The Swift helpers are compiled on the release runner and copied into the
	// archive, so with no -target the artifact is a property of whatever machine
	// ran the job rather than of this repository. That is how the notifier
	// shipped arm64-only into the Intel tarball, silently: no usable helper
	// means the password path, and Touch ID disappears from a release that
	// advertises it. The name checks below cannot see that, which is the point
	// of this one: they verify the word, not the artifact.
	//
	// AND THE MAC INTEL TARGET STAYS GONE. Apple is ending Intel support, so
	// Dibs does not ship that build: carrying it costs a second Swift slice for
	// each helper, lipo for both, and an archive nobody installs. This is
	// checked rather than remembered because `goarch: [amd64, arm64]` reads as
	// an obvious pair and the `ignore` line under it is easy to drop in a tidy-
	// up, which would restore the target and every cost with it, quietly.
	relRaw, err := os.ReadFile(filepath.Join(root, ".goreleaser.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// BOTH Swift artifacts, and in the RELEASE rather than in the tool.
	//
	// The notifier's target was briefly hardcoded in tools/appbundle, which
	// fixed the release and broke building from source on an Intel Mac: the
	// documented escape hatch for the platform the release just dropped. A local
	// build is for the machine doing the building; only the release is for
	// somewhere else, so only the release states a target, and this checks the
	// place that has to.
	for _, what := range []string{
		"-target arm64-apple-macos", // the presence helper's own swiftc line
		"appbundle",                 // and the notifier, via the bundler
	} {
		if !strings.Contains(string(relRaw), what) {
			t.Errorf("the release does not build %s at all", what)
		}
	}
	for _, line := range strings.Split(string(relRaw), "\n") {
		if strings.Contains(line, "tools/appbundle") && !strings.Contains(line, "-target ") {
			t.Error("the release builds Dibs.app without -target, so the notifier is " +
				"whatever architecture the runner happened to be. It shipped arm64-only " +
				"into the Intel archive exactly that way, and the failure is silent: " +
				"the runtime finds an executable at the expected path and runs it " +
				"rather than falling back")
		}
	}
	if !strings.Contains(string(relRaw), "{goos: darwin, goarch: amd64}") {
		t.Error("the release no longer ignores darwin/amd64, so it builds a Mac Intel " +
			"archive again. Both Swift helpers are single-slice and would be wrong " +
			"in it. Either restore the ignore, or bring back the lipo builds and the " +
			"second slice of each helper deliberately")
	}
	if strings.Contains(string(relRaw), "x86_64-apple-macos") {
		t.Error("a Swift helper is compiled for Mac Intel while the release ignores " +
			"that target: one of the two is wrong, and the build is paying for a " +
			"slice nothing ships")
	}

	// AND THE RELEASE, which is what almost everybody installs.
	//
	// This compared the runtime name with the Taskfile only, so it was green
	// while GoReleaser built dibd and dibs and nothing else: the helper existed
	// for anyone who built from source and was absent from every published
	// archive and every brew install, where the runtime looks for it beside the
	// executable and quietly falls back to the password. A guard that watches
	// the developer's path and not the shipped one watches the case that was
	// never going to break. Found by the pre-release review.
	rel, err := os.ReadFile(filepath.Join(root, ".goreleaser.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// It has to be BUILT and it has to be PACKAGED: the before hook produces it
	// and the archive carries it. Either one alone ships nothing usable.
	//
	// AN EXACT OUTPUT NAME, not a prefix. `-o dibs-presence` is a prefix of `-o
	// dibs-presence-arm64`, and while the release built per-architecture slices
	// and joined them, deleting the join left those compiles matching a
	// substring needle and every assertion here green with no helper to carry.
	// The join is gone now (one Mac target, one slice), so the needle is the
	// output name bounded at both ends, which is true of either arrangement and
	// false of a half-finished one.
	if !regexp.MustCompile(`-o(?:utput)?\s+` + regexp.QuoteMeta(want) + `(\s|$)`).
		Match(rel) {
		t.Errorf("the release does not build %q: no swiftc or lipo line produces that "+
			"exact name, so an installed Dibs finds no helper beside its executable "+
			"and falls back to the admin password, while the docs describe Touch ID "+
			"as the default", want)
	}
	for _, place := range []struct{ what, needle string }{
		{"carried in the archive", "src: " + want},
		{"linked by the Homebrew cask", "- " + want},
	} {
		if !strings.Contains(string(rel), place.needle) {
			t.Errorf("the release is not %s (%q not found in .goreleaser.yml): an "+
				"installed Dibs would find no helper beside its executable and fall "+
				"back to the admin password, while the docs describe Touch ID as the "+
				"default", place.what, place.needle)
		}
	}
}

// Every setting the daemon accepts must be documented, and nothing may be
// documented that it does not accept.
//
// The daemon refuses an unknown key and stops, so a setting an operator reads
// about and cannot use is not a small documentation slip: it is a daemon that
// will not start, blamed on the manual that suggested it. And a key with no
// entry is one nobody can discover, which for a knob whose whole reason to
// exist is a case the default gets wrong means it may as well not be there.
//
// Twenty-four keys were spread across five documents when this was written,
// with no reference anywhere. The reference is docs/CONFIGURATION.md; this is
// what keeps it true.
func TestEverySettingIsDocumentedAndEveryDocumentedSettingExists(t *testing.T) {
	root := repoRoot(t)
	doc, err := os.ReadFile(filepath.Join(root, "docs/CONFIGURATION.md"))
	if err != nil {
		t.Fatal(err)
	}
	reference := string(doc)

	// The keys the daemon really reads, taken from the struct tags rather than
	// a list somebody maintains: a list would be one more thing to fall out of
	// sync, which is the failure this test exists for.
	tag := regexp.MustCompile("`toml:\"([a-z_]+)\"`")
	declared := map[string]string{}
	// Every file that declares settings, not just the one named config.go: the
	// first version of this guard missed cmd/dibd/roles.go and reported the
	// [roles] keys as undocumented inventions, which they are not.
	// The settings themselves moved to internal/boardconfig, because `dibs
	// mcp-config` has to read the same file and reach the same verdict. This
	// guard caught the move by finding seven settings where it expects ten,
	// which is what the floor below is for.
	for _, rel := range []string{
		"internal/boardconfig/boardconfig.go",
		"cmd/dibd/config.go",
		"cmd/dibd/roles.go",
		"internal/liveness/settings.go",
	} {
		src, err := os.ReadFile(filepath.Join(root, rel)) // #nosec G304 -- a fixed path in this repository
		if err != nil {
			continue
		}
		for _, m := range tag.FindAllStringSubmatch(string(src), -1) {
			declared[m[1]] = rel
		}
	}
	if len(declared) < 10 {
		t.Fatalf("found only %d settings; this guard is looking in the wrong place and "+
			"would pass whatever the reference said", len(declared))
	}
	for key, where := range declared {
		// The table names are headings, not settings: they are documented as
		// the sections they introduce.
		switch key {
		case "match", "limits", "supervise", "roles", "wake":
			continue
		}
		if !strings.Contains(reference, "`"+key+"`") {
			t.Errorf("%s accepts %q and docs/CONFIGURATION.md does not mention it: an "+
				"operator cannot discover a knob that exists for a case the default gets "+
				"wrong", where, key)
		}
	}
	// And the other direction: a documented key the daemon refuses stops the
	// daemon, blamed on the manual that suggested it.
	inDoc := regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\|")
	for _, m := range inDoc.FindAllStringSubmatch(reference, -1) {
		if _, ok := declared[m[1]]; !ok {
			t.Errorf("docs/CONFIGURATION.md documents %q, which the daemon does not "+
				"accept: writing it into dibs.toml stops the daemon", m[1])
		}
	}
}

// Both binaries have a manual, and both manuals render.
//
// dibd had none. It is what an operator installs as a service, points at a
// listen address and configures with a file, and `man dibd` found nothing: the
// CLI is discoverable by typing `dibs help`, a daemon under launchd is
// discoverable only by reading about it. The wrong way round.
//
// Rendered rather than merely generated, because an mdoc page that does not
// parse is a page nobody can read, and the failure is invisible until somebody
// types `man`.
func TestBothBinariesShipAManualThatRenders(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the generators")
	}
	root := repoRoot(t)
	for _, tc := range []struct{ pkg, flag, section string }{
		{"./cmd/dibs", "man", "1"},
		{"./cmd/dibd", "-man", "8"},
	} {
		gen := exec.Command("go", "run", tc.pkg, tc.flag)
		// From the repository root: `go run ./cmd/...` is relative, and this
		// test runs in its own package directory.
		gen.Dir = root
		out, err := gen.Output()
		if err != nil {
			t.Errorf("%s %s: %v", tc.pkg, tc.flag, err)
			continue
		}
		page := string(out)
		if !strings.Contains(page, ".Dt ") || !strings.Contains(page, ".Sh NAME") {
			t.Errorf("%s produced something that is not an mdoc page:\n%.200s", tc.pkg, page)
			continue
		}
		if !strings.Contains(page, " "+tc.section+"\n") {
			t.Errorf("%s is not in section %s: a daemon in section 1 or a command in "+
				"section 8 is filed where nobody looks", tc.pkg, tc.section)
		}
		// mandoc where it exists. Not required, because a contributor without
		// it should still be able to run the suite, but the CI host has it and
		// a page that stops linting should fail there.
		if _, err := exec.LookPath("mandoc"); err != nil {
			continue
		}
		// Named with its section: mandoc infers the section from the filename,
		// and a page written to "page" lints against the wrong conventions.
		f := filepath.Join(t.TempDir(), "dibs."+tc.section)
		if err := os.WriteFile(f, out, 0o600); err != nil {
			t.Fatal(err)
		}
		lint, _ := exec.Command("mandoc", "-Tlint", f).CombinedOutput()
		// "referenced manual not found" is a fact about what is INSTALLED on
		// this machine, not about the page: dibd(8) correctly cross-references
		// dibs(1), and dibs(1) is only on the system after a release installs
		// it. Keeping it would make the suite pass or fail on whether somebody
		// had run brew install.
		var real []string
		for _, line := range strings.Split(strings.TrimSpace(string(lint)), "\n") {
			if line == "" || strings.Contains(line, "referenced manual not found") {
				continue
			}
			real = append(real, line)
		}
		lint = []byte(strings.Join(real, "\n"))
		if len(real) > 0 {
			t.Errorf("%s does not lint clean:\n%s\n(page written to %s)", tc.pkg, lint, f)
		}
	}
}

// A workflow's `run:` block is a shell script that happens to live in YAML.
//
// The no-shell rule is about what shell IS, not about where the bytes sit: an
// embedded block is the same untyped string handling, the same silent
// continuation past failure unless somebody remembered `set -euo pipefail`, the
// same quoting hazards, and it cannot be built, vetted, or run locally. The
// guard above looked for extensions, shebangs and symlinks, so every one of
// these was invisible to it, and three had accumulated: a brace group writing a
// job summary, a curl-into-sha256sum-into-tar install, and a three-branch
// version selector interpolating `${{ }}` template values straight into shell
// words. The pre-release review found the newest by reading.
//
// One COMMAND is not a script. `run: go test ./...` is how a workflow invokes
// anything at all, and forbidding it would forbid workflows. What is refused is
// shell LOGIC: multiple statements, conditionals, pipelines, redirections,
// substitutions, and parameter expansion.
func TestWorkflowsDoNotEmbedShellScripts(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	// Shell constructs that make a `run:` a program rather than an invocation.
	//
	// THE LIST WAS SHORTER THAN THE RULE IT CLAIMED. The prose above says
	// multiple statements, pipelines, redirections, substitutions and control
	// flow are all forbidden, and this checked nine substrings: `cmd1; cmd2`
	// passed, so did an unspaced `a|b`, a single `>`, a backtick, a `while`
	// loop, and a block of two ordinary commands on separate lines. A guard that
	// states a property and enforces a subset of it is the kind this repository
	// keeps finding, and it found this one in itself.
	constructs := []struct{ token, why string }{
		{"${", "parameter expansion (${VAR#prefix} and friends) is shell string surgery"},
		{"$(", "command substitution runs a second program to build the first one's arguments"},
		{"`", "backtick substitution is command substitution wearing older syntax"},
		{"&&", "a conditional sequence is control flow"},
		{"||", "a fallback is control flow, and the silent kind"},
		{"|", "a pipeline hides the exit status of everything but the last stage"},
		{";", "two statements on one line is a script"},
		{">", "a redirection is the script deciding where output goes"},
		{"<", "a redirection is the script deciding where input comes from"},
		{"if ", "a conditional is control flow"},
		{"for ", "a loop is control flow"},
		{"while ", "a loop is control flow"},
		{"case ", "a conditional is control flow"},
		{"set -e", "needing this is the admission that shell continues past failures"},
	}
	// AND MORE THAN ONE COMMAND, which no substring can see. A folded block of
	// two ordinary lines is a script by the definition at the top of this test,
	// and every token above would miss it.
	multiline := func(body string) int {
		n := 0
		for _, l := range strings.Split(body, "\n") {
			if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") {
				n++
			}
		}
		return n
	}
	checked := 0
	for _, e := range entries {
		if e.IsDir() || (filepath.Ext(e.Name()) != ".yml" && filepath.Ext(e.Name()) != ".yaml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- this repo's own workflows
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, block := range runBlocks(string(raw)) {
			if n := multiline(block.body); n > 1 && !block.folded {
				t.Errorf("%s line %d: this `run:` holds %d commands, which is a script "+
					"whatever it contains. Write it as a Go program under tools/ and "+
					"invoke that, or split it into one step per command so a failure "+
					"names itself.\n  %s",
					e.Name(), block.line, n, strings.TrimSpace(firstLine(block.body)))
				continue
			}
			for _, c := range constructs {
				if !strings.Contains(block.body, c.token) {
					continue
				}
				t.Errorf("%s line %d: this `run:` is a shell script, not a command: %s\n"+
					"  %s\n"+
					"Write it as a Go program under tools/ and invoke that. See "+
					"tools/casknote, tools/fetchpinned and tools/stampserver, each of "+
					"which replaced one of these",
					e.Name(), block.line, c.why, strings.TrimSpace(firstLine(block.body)))
				break
			}
		}
	}
	if checked == 0 {
		t.Fatal("no workflow files were read, so this check verified nothing")
	}
}

type runBlock struct {
	line int
	body string
	// folded is a `>` block, whose lines join into ONE command. A `|` block
	// keeps them separate, and that is the difference between an invocation
	// spread over four readable lines and a four-line script.
	folded bool
}

// runBlocks returns every `run:` value in a workflow, folded blocks included.
func runBlocks(s string) []runBlock {
	var out []runBlock
	lines := strings.Split(s, "\n")
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trimmed, "run:") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "run:"))
		// A single-line command.
		if rest != "" && rest != "|" && rest != ">" && rest != ">-" && rest != "|-" {
			out = append(out, runBlock{i + 1, rest, false})
			continue
		}
		// FOLDED (`>`) joins its lines into ONE command; literal (`|`) keeps
		// them as separate ones. The difference decides whether several lines
		// are several commands, so it has to travel with the body.
		folded := strings.HasPrefix(rest, ">")
		// A block scalar: everything indented under it.
		indent := len(lines[i]) - len(strings.TrimLeft(lines[i], " "))
		var body []string
		for j := i + 1; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == "" {
				body = append(body, "")
				continue
			}
			if len(lines[j])-len(strings.TrimLeft(lines[j], " ")) <= indent {
				break
			}
			body = append(body, strings.TrimSpace(lines[j]))
		}
		out = append(out, runBlock{i + 1, strings.Join(body, "\n"), folded})
	}
	return out
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// Every tool has an ignore entry, so building one cannot commit it.
//
// `go build ./tools/<name>` writes the binary into the working directory, and
// `git add -A` then stages it. Three went in on one commit exactly that way,
// and the gate that forbids committed binaries did not stop them: it reads
// TRACKED files, and it had run before the add. So the gate was green, the
// binaries were staged afterwards, and nothing looked again.
//
// A list of names in .gitignore is the fix, and a list of names is also this
// repository's most reliable way to go stale: the entry it will be missing is
// the one for the tool somebody adds next, which is the one nobody has learned
// to expect yet. So the list is checked against the directory rather than
// remembered, and the check names the line to add.
func TestEveryToolBinaryIsIgnored(t *testing.T) {
	root := repoRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, "tools"))
	if err != nil {
		t.Fatalf("reading tools/: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	ignored := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "/") && !strings.HasSuffix(line, "/") {
			ignored[strings.TrimPrefix(line, "/")] = true
		}
	}
	seen := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		seen++
		if !ignored[e.Name()] {
			t.Errorf("tools/%s has no anchored .gitignore entry. `go build "+
				"./tools/%s` leaves ./%s in the working directory, and the next "+
				"`git add -A` commits a binary. Add this line:\n\n    /%s\n\n"+
				"Anchored: unanchored, it would also match the tools/%s DIRECTORY "+
				"and the source would stop being committed at all",
				e.Name(), e.Name(), e.Name(), e.Name(), e.Name())
		}
	}
	if seen == 0 {
		t.Fatal("no tool directories found, so this check verified nothing")
	}
}

// The notifier bundle ships in the release, not only in `task install`.
//
// internal/notify resolves Dibs.app/Contents/MacOS/dibs-notify beside the
// executable, and Reach reports notifications unavailable when it is absent.
// Only the Taskfile built it, so source builds had the branded, actionable
// notification the README lists as a required artifact and every published
// archive and brew install did not. CI therefore exercised a different
// installation surface from the one people download.
//
// This is the SECOND component to go missing the same way: the Touch ID helper
// had the identical hole one review round earlier, and its guard watched only
// itself. A guard written for one artifact is a guard for that artifact.
//
// WHAT THIS CANNOT SEE, and what does. Everything below reads
// `.goreleaser.yml`, so it can only answer whether the configuration NAMES the
// artifact. It was green while the notifier bundle shipped as
// `Dibs.app/MacOS/dibs-notify`, because `src: Dibs.app/**/*` contains the
// substring `src: Dibs.app` and the glob quietly ate the `Contents` level the
// runtime resolves through. The archive had the file, under a path that is not
// a macOS bundle and is not where internal/notify looks, and `dibs doctor` run
// from an extracted archive said the notifier was not installed. `task
// test:archive` builds the archives and tools/archivecheck opens one; this
// stays because a deleted entry is worth catching in a second, without a build.
func TestEveryBundledHelperShipsInTheRelease(t *testing.T) {
	root := repoRoot(t)
	rel, err := os.ReadFile(filepath.Join(root, ".goreleaser.yml"))
	if err != nil {
		t.Fatal(err)
	}
	release := string(rel)

	// What the RUNTIME looks for beside the executable. Read from the source
	// rather than listed here, so a third helper is covered on the day it is
	// added rather than on the day somebody remembers this test.
	helpers := map[string]string{
		"dibs-presence": "internal/humanauth",
		"Dibs.app":      "internal/notify",
	}
	for name, where := range helpers {
		found := false
		if werr := filepath.WalkDir(filepath.Join(root, where), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || filepath.Ext(p) != ".go" {
				return nil
			}
			b, rerr := os.ReadFile(p) // #nosec G304 -- this repository
			if rerr != nil {
				return rerr
			}
			if strings.Contains(string(b), name) {
				found = true
			}
			return nil
		}); werr != nil {
			t.Fatalf("walking %s: %v", where, werr)
		}
		if !found {
			t.Errorf("%s no longer names %q, so this check is guarding an artifact "+
				"the runtime does not look for", where, name)
			continue
		}
		if !strings.Contains(release, "src: "+name) {
			t.Errorf("%s is resolved at runtime by %s and is not carried in the "+
				"archive (%q missing from .goreleaser.yml). Every published archive "+
				"would be without it, while a source build has it and CI passes",
				name, where, "src: "+name)
		}
		// THE CASK SECTION, in whatever stanza carries it.
		//
		// The first version looked for the list form `- Dibs.app` and the valid
		// configuration turned out to be a `custom_block` emitting Homebrew's
		// own `app` stanza, so the guard failed on a correct file. A check that
		// knows one spelling finds the artifact only where somebody already put
		// it in that spelling; what matters is that the cask mentions it at all.
		at := strings.Index(release, "homebrew_casks:")
		if at < 0 {
			t.Fatal(".goreleaser.yml has no homebrew_casks section, so this check " +
				"cannot say whether the cask installs anything")
		}
		if !strings.Contains(release[at:], name) {
			t.Errorf("%s is resolved at runtime by %s and the Homebrew cask never "+
				"mentions it, so every brew install would be without it", name, where)
		}
	}
}

// Every workflow pins the same toolchain version, and pins one at all.
//
// The action commit being pinned is not the same as the tool being pinned:
// publish-mcp.yml pinned jdx/mise-action to a SHA and left the mise version
// unset, so a job holding `id-token: write` downloaded whatever `latest` meant
// that morning. That is the exact shape the same file refuses three lines lower
// for mcp-publisher, in a comment, added by the change that introduced this.
//
// And one version across all of them, because three copies of a pin is three
// places for it to drift and this repository has paid for that shape before.
func TestEveryWorkflowPinsTheSameToolchain(t *testing.T) {
	dir := filepath.Join(repoRoot(t), ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string][]string{}
	for _, e := range entries {
		if e.IsDir() || (filepath.Ext(e.Name()) != ".yml" && filepath.Ext(e.Name()) != ".yaml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- this repository
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		uses := strings.Count(body, "jdx/mise-action@")
		if uses == 0 {
			continue
		}
		m := regexp.MustCompile(`(?m)^\s*version:\s*([0-9][\w.\-]*)`).FindAllStringSubmatch(body, -1)
		if len(m) < uses {
			t.Errorf("%s uses mise-action %d time(s) and pins %d version(s). A pinned "+
				"ACTION with an unpinned tool downloads whatever latest means today, "+
				"and these jobs hold id-token: write", e.Name(), uses, len(m))
			continue
		}
		for _, hit := range m {
			seen[hit[1]] = append(seen[hit[1]], e.Name())
		}
	}
	if len(seen) == 0 {
		t.Fatal("no workflow pins a toolchain version, so this check verified nothing")
	}
	if len(seen) > 1 {
		t.Errorf("workflows pin different toolchain versions: %v. One of them is the "+
			"gate and one of them is the release, and they have to be the same "+
			"toolchain or the gate is not testing what ships", seen)
	}
}

// Every release before-hook output has an ignore entry.
//
// The hooks build the man pages, the Touch ID helper's two slices, the
// universal binary made from them, and the notifier bundle, all into the
// working directory. Until the gate built a release nothing produced any of
// that outside the release job, so `.gitignore` listed exactly one of them and
// nobody noticed: `task test:archive` went into `task ci` to prove the archive
// carries what the runtime resolves, and the first local run left four
// untracked artifacts including a whole signed .app bundle, which `git add -A`
// staged.
//
// That is the same failure as the tool binaries above, arriving from a
// different direction, and it has the same answer: derive the list rather than
// remember it. The entry a hand-written list will be missing is the one for the
// hook somebody adds next.
func TestEveryReleaseHookOutputIsIgnored(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".goreleaser.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// The hooks section only. A `-o` further down the file belongs to something
	// that is not run before the build and does not land in this directory.
	hooks := regexp.MustCompile(`(?ms)^before:\n(.*?)^\S`).FindStringSubmatch(string(raw))
	if hooks == nil {
		t.Fatal(".goreleaser.yml has no `before:` section, so this check cannot see what " +
			"the release builds into the working directory before it starts")
	}
	// -o, -out and -output: three tools, three spellings, and a guard that knew
	// one of them would pass over the other two. Bare `-o` last so the longer
	// flags are not read as it.
	outputs := map[string]bool{}
	for _, m := range regexp.MustCompile(`-(?:output|out|o)\s+(\S+)`).
		FindAllStringSubmatch(hooks[1], -1) {
		outputs[m[1]] = true
	}
	if len(outputs) == 0 {
		t.Fatal("no before-hook outputs found; this check verified nothing")
	}

	ignoreRaw, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	ignored := map[string]bool{}
	for _, line := range strings.Split(string(ignoreRaw), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "/"))
		ignored[strings.TrimPrefix(line, "/")] = true
	}

	for name := range outputs {
		// A path into a subdirectory is not left at the root and is somebody
		// else's ignore rule; this is about what lands beside the go.mod.
		if strings.ContainsRune(name, '/') {
			continue
		}
		if !ignored[name] {
			t.Errorf(".goreleaser.yml's before hooks build %q into the working directory "+
				"and .gitignore has no anchored entry for it, so `task ci` now leaves it "+
				"untracked and the next `git add -A` commits a build artifact. Add this "+
				"line:\n\n    /%s\n", name, name)
		}
	}
}

// No shipped document calls the admin password a prerequisite.
//
// It is not one. `dibs web` raises the daemon-owned Touch ID sheet first and
// asks for a password only where there is no sensor to ask, which is the whole
// point of the presence work in this release. Four documents still said the
// password had to be set first, including the primary README and the Homebrew
// caveat that every macOS installer reads, so the release announced that it had
// removed a weaker credential while its own onboarding sent operators to create
// one.
//
// The check is CONDITIONALITY, not wording. Anywhere the command appears, the
// text around it has to say when it applies; a document is free to phrase that
// however it likes. Pinning one sentence would find the claim only where
// somebody already wrote it that way, which is how the same defect survived in
// four spellings and in two more places than the reviewer reached: the embedded
// plugin copy, which is the one that actually ships, and the tutorial.
//
// The changelog is exempt. It is a historical record, and an entry describing
// what an older version required is correct precisely because it is dated.
func TestNoDocumentMakesTheAdminPasswordAPrerequisite(t *testing.T) {
	const window = 10
	root := repoRoot(t)
	// Any of these near the command means the condition is stated.
	conditions := []string{"touch id", "sensor", "presence", "biometric", "fingerprint"}

	seen := 0
	walk(t, root, func(rel, abs string) {
		switch strings.ToLower(filepath.Ext(rel)) {
		case ".md", ".yml", ".yaml":
		default:
			return
		}
		if strings.EqualFold(filepath.Base(rel), "CHANGELOG.md") {
			return
		}
		body, err := os.ReadFile(abs) // #nosec G304 -- walking this repository
		if err != nil {
			return
		}
		lines := strings.Split(string(body), "\n")
		for i, line := range lines {
			if !strings.Contains(line, "set-password") {
				continue
			}
			seen++
			lo, hi := max(0, i-window), min(len(lines), i+window+1)
			near := strings.ToLower(strings.Join(lines[lo:hi], "\n"))
			stated := false
			for _, c := range conditions {
				if strings.Contains(near, c) {
					stated = true
					break
				}
			}
			if !stated {
				t.Errorf("%s:%d introduces `admin set-password` without saying when it "+
					"applies. `dibs web` uses the Touch ID sheet first and falls back to a "+
					"password only where there is no sensor, so a document that presents "+
					"the password as the way in sends macOS operators to create the weaker "+
					"credential this release exists to stop needing.\n  %s",
					rel, i+1, strings.TrimSpace(line))
			}
		}
	})
	if seen == 0 {
		t.Fatal("no document mentions `admin set-password`, so this check verified nothing")
	}
}
