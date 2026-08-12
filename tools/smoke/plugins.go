package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The plugin payloads are configuration we ship for other people's harnesses,
// and nothing executed them, so the rename walked straight through:
// plugins/claude-code/.mcp.json declared `"command": "agents"`, a binary that
// has never existed. Claude Code would spawn it, fail, and show a server that
// never starts. Every test in the repo passed.
//
// A binary can be asked what it is called. A JSON file cannot, so this checks
// the two things that must be true of it: the command names a binary we ship,
// and the server is published under the product's name so the docs match what
// the agent sees.
var (
	shippedBinaries = map[string]bool{"dibs": true, "dibd": true}
	serverName      = "dibs"
	serverKeys      = map[string]bool{"mcpServers": true, "mcp_servers": true, "mcp": true}
)

// checkPlugins reports one problem per payload that names something we do not
// ship.
func checkPlugins() []string {
	files, err := trackedJSON()
	if err != nil {
		return []string{"could not list the plugin payloads: " + err.Error()}
	}
	var problems []string
	read := 0
	for _, path := range files {
		body, err := os.ReadFile(path) // #nosec G304 -- paths come from git ls-files
		if err != nil {
			problems = append(problems, path+": "+err.Error())
			continue
		}
		var doc any
		if json.Unmarshal(body, &doc) != nil {
			continue // other tests own JSON validity; this one owns the names
		}
		read++
		problems = append(problems, inspect(path, doc)...)
	}
	// A payload check that read nothing passes, and would have passed through
	// every bug it exists to catch.
	if read < 5 {
		problems = append(problems, fmt.Sprintf(
			"only %d plugin payloads parsed, too few to have covered the harnesses: "+
				"the file list is broken, not the payloads", read))
	}
	return problems
}

// trackedJSON lists the payload files, from git so an untracked scratch file
// cannot fail the build and a deleted one cannot be silently skipped.
func trackedJSON() ([]string, error) {
	out, err := exec.Command("git", "ls-files",
		"plugins/*.json", "plugins/**/*.json",
		"internal/plugins/data/*.json", "internal/plugins/data/**/*.json").Output()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if name != "" {
			files = append(files, name)
		}
	}
	return files, nil
}

// inspect walks a decoded payload looking for the two keys that matter.
func inspect(path string, node any) []string {
	switch v := node.(type) {
	case map[string]any:
		var problems []string
		for key, child := range v {
			problems = append(problems, judge(path, key, child)...)
			problems = append(problems, inspect(path, child)...)
		}
		return problems
	case []any:
		var problems []string
		for _, child := range v {
			problems = append(problems, inspect(path, child)...)
		}
		return problems
	}
	return nil
}

// judge checks one key/value pair against the two rules.
func judge(path, key string, child any) []string {
	if key == "command" {
		s, ok := child.(string)
		if ok && !namesAShippedBinary(s) {
			return []string{fmt.Sprintf(
				"%s: spawns %q, which does not name a binary this project ships.\n"+
					"      The harness starts it, it is not found, and the server never comes up.",
				path, s)}
		}
		return nil
	}
	if !serverKeys[key] {
		return nil
	}
	servers, ok := child.(map[string]any)
	if !ok {
		return nil
	}
	var problems []string
	for name := range servers {
		if name != serverName {
			problems = append(problems, fmt.Sprintf(
				"%s: publishes the server as %q, not %q, so every document naming the "+
					"tools is wrong for anyone using this payload", path, name, serverName))
		}
	}
	return problems
}

// namesAShippedBinary accepts either a bare command or a command line that
// invokes one.
//
// A hook's `command` is a whole shell line, not a program path, so requiring
// the value to BE `dibs` flagged a perfectly good hook. Requiring it to mention
// one keeps the check honest for both shapes: what must never happen is a
// payload naming a binary that does not exist.
func namesAShippedBinary(command string) bool {
	if shippedBinaries[filepath.Base(command)] {
		return true
	}
	for _, field := range strings.FieldsFunc(command, func(r rune) bool {
		return strings.ContainsRune(" \t\n\"'$()|&;=", r)
	}) {
		if shippedBinaries[filepath.Base(field)] {
			return true
		}
	}
	return false
}
