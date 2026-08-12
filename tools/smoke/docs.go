package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// The docs pass. Everything above checks what the binaries say; this checks
// what we say ABOUT them, which is where the worst of these hid: the README's
// primary install line read `brew install agenxy/tap/agents` for two releases,
// naming a cask that has never existed. A reader's very first command failed,
// and no test in this repository had an opinion about it.
//
// The verb table comes from the built binary, so it cannot drift from the CLI.
// The cask names are written out by hand because the tap is a different
// repository: there is nothing local to derive them from, and a list of two is
// cheap to keep honest.
var knownCasks = map[string]bool{"dibs": true, "remap": true}

// Command lines inside fenced blocks only. Prose says "dibs will notice" and
// means it; a fenced line is something a reader is invited to run.
var (
	fence     = regexp.MustCompile("^\\s*```")
	dibsCall  = regexp.MustCompile(`^\s*\$?\s*(dibs|dibd)\s+([a-z][a-z-]*)`)
	brewCall  = regexp.MustCompile(`brew\s+install\s+agenxy/tap/([a-z-]+)`)
	tapModern = regexp.MustCompile(`brew\s+(tap|trust)\s+agenxy/tap`)
)

// checkDocs reports one problem per bad command line found in the docs.
func checkDocs() []string {
	verbs, err := verbTable()
	if err != nil {
		return []string{"could not read the verb table from the built binary: " + err.Error()}
	}
	files, err := markdownFiles()
	if err != nil {
		return []string{"could not list the docs: " + err.Error()}
	}

	var problems []string
	inspected := 0
	for _, path := range files {
		found, n, err := scanDoc(path, verbs)
		if err != nil {
			problems = append(problems, path+": "+err.Error())
			continue
		}
		problems = append(problems, found...)
		inspected += n

		// Homebrew 6 will not load a cask from an untrusted tap, so an install
		// line without the trust step in front of it is an instruction that
		// fails on a clean machine. The README shipped exactly that.
		body, err := os.ReadFile(path) // #nosec G304 -- paths come from git ls-files
		if err == nil && brewCall.Match(body) && !tapModern.Match(body) {
			problems = append(problems, path+": documents `brew install agenxy/tap/...` "+
				"without `brew trust agenxy/tap`, which Homebrew 6 requires first")
		}
	}

	// A pass that inspected nothing is not a pass. This check reads files it
	// finds by pattern, and a moved doc tree or a changed fence style would
	// quietly reduce it to a green no-op.
	if inspected < 100 {
		problems = append(problems, fmt.Sprintf(
			"only %d fenced lines were inspected across %d files, which is too few to "+
				"have read the docs: the scan is broken, not the docs", inspected, len(files)))
	}
	return problems
}

// scanDoc reads one file's fenced blocks, returning any problems and how many
// fenced lines it actually looked at.
func scanDoc(path string, verbs map[string]bool) ([]string, int, error) {
	f, err := os.Open(path) // #nosec G304 -- paths come from git ls-files
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()

	var problems []string
	fenced, line, inspected := false, 0, 0
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scan.Scan() {
		line++
		text := scan.Text()
		if fence.MatchString(text) {
			fenced = !fenced
			continue
		}
		if !fenced {
			continue
		}
		inspected++
		problems = append(problems, checkLine(fmt.Sprintf("%s:%d", path, line), text, verbs)...)
	}
	return problems, inspected, scan.Err()
}

// checkLine judges one fenced line.
func checkLine(where, text string, verbs map[string]bool) []string {
	var problems []string
	if m := dibsCall.FindStringSubmatch(text); m != nil && m[1] == "dibs" {
		// Flags are the command, not a verb: `dibs --help` is fine.
		if verb := m[2]; !verbs[verb] && !strings.HasPrefix(verb, "-") {
			problems = append(problems, fmt.Sprintf(
				"%s: documents `dibs %s`, which the built binary does not accept.\n"+
					"      Known verbs: %s", where, verb, joined(verbs)))
		}
	}
	if m := brewCall.FindStringSubmatch(text); m != nil && !knownCasks[m[1]] {
		problems = append(problems, fmt.Sprintf(
			"%s: documents `brew install agenxy/tap/%s`, which is not a cask in the tap.\n"+
				"      Known casks: dibs, remap", where, m[1]))
	}
	return problems
}

// verbTable asks the BUILT binary which verbs it has, by parsing the completion
// script it generates from its own dispatch table.
func verbTable() (map[string]bool, error) {
	out, err := exec.Command("bin/dibs", "completion", "zsh").Output()
	if err != nil {
		return nil, err
	}
	verbs := map[string]bool{}
	entry := regexp.MustCompile(`^\s*'([a-z][a-z-]*):`)
	for _, line := range strings.Split(string(out), "\n") {
		if m := entry.FindStringSubmatch(line); m != nil {
			verbs[m[1]] = true
		}
	}
	if len(verbs) < 5 {
		return nil, fmt.Errorf("parsed only %d verbs from the completion script, "+
			"so the parser is wrong and every doc check below it is vacuous", len(verbs))
	}
	return verbs, nil
}

// markdownFiles lists tracked markdown, so an untracked scratch file cannot
// fail the build and a deleted one cannot be silently skipped.
func markdownFiles() ([]string, error) {
	out, err := exec.Command("git", "ls-files", "*.md").Output()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if name == "" || strings.HasPrefix(filepath.Base(name), "CHANGELOG") {
			continue // the changelog quotes history on purpose
		}
		files = append(files, name)
	}
	return files, nil
}

func joined(set map[string]bool) string {
	var names []string
	for k := range set {
		names = append(names, k)
	}
	return strings.Join(names, " ")
}
