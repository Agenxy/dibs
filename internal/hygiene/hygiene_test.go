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
	for _, place := range []struct{ what, needle string }{
		{"built by a before hook", "-o " + want},
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
