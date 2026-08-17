package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A hook whose TYPE the target harness cannot run is not a wake path waiting to
// be wired. It is inert, and it can take working entries down with it.
//
// Dibs shipped `plugins/codex/hooks.json` using `type: "mcp_tool"` for three
// releases. Codex never ran a single entry in it:
//
//   - Codex Desktop 0.148.0-alpha.9 parses the file and then prints "skipping
//     MCP tool hook in ~/.codex/hooks.json: MCP tool hooks are not supported
//     yet", once per entry.
//   - A build from codex main has no `mcp_tool` variant at all and rejects the
//     WHOLE file with `unknown variant`, which is the part that makes this
//     worse than a no-op: one unsupported entry disables the supported ones
//     beside it.
//
// The claim came from reading a Rust enum and writing the file against it. A
// type in a source tree is not a feature, and nothing here checked. Worse, the
// existing hook test globs `plugins/*/hooks/hooks.json`, and this file lived at
// `plugins/codex/hooks.json`, so no test ever opened it: it was not that the
// guard disagreed, it is that the guard could not see it.
//
// So this walks BOTH layouts, and it is a list of what each harness RUNS, not
// of what its types are named.
func TestShippedHooksUseOnlySupportedTypes(t *testing.T) {
	// Measured against running binaries on 2026-08-17, not read from source.
	// Claude Code documents five handler types and runs them; Codex's
	// HookHandlerConfig has three, and its Desktop build skips `mcp_tool`
	// explicitly as unimplemented.
	supported := map[string]map[string]bool{
		"claude-code": {"command": true, "http": true, "mcp_tool": true, "prompt": true, "agent": true},
		"codex":       {"command": true, "prompt": true, "agent": true},
	}

	var files []string
	for _, pat := range []string{"../../plugins/*/hooks.json", "../../plugins/*/hooks/hooks.json"} {
		found, _ := filepath.Glob(pat)
		files = append(files, found...)
	}
	if len(files) == 0 {
		t.Skip("no shipped hook definitions to check")
	}

	for _, f := range files {
		plugin := pluginOf(f)
		allowed, known := supported[plugin]
		if !known {
			// A new plugin shipping hooks must say which types its harness
			// runs. Defaulting to "anything" is how the last one shipped.
			t.Errorf("%s ships hooks but %q is not in this test's table of what "+
				"each harness actually runs: add it, from a measurement rather "+
				"than from the harness's type definitions", f, plugin)
			continue
		}
		b, err := os.ReadFile(f) // #nosec G304 -- a repo path from a fixed glob
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		var doc any
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Errorf("%s is not valid JSON: %v", f, err)
			continue
		}
		for _, typ := range hookTypesIn(doc) {
			if !allowed[typ] {
				t.Errorf("%s declares a hook of type %q, which %s does not run. "+
					"That entry will never fire, and on builds that reject the "+
					"variant outright it disables every other hook in the file. "+
					"Ship no hook rather than one the harness skips", f, typ, plugin)
			}
		}
	}
}

// pluginOf returns the plugin directory name for either shipped layout:
// plugins/<name>/hooks.json or plugins/<name>/hooks/hooks.json.
func pluginOf(path string) string {
	dir := filepath.Dir(path)
	if filepath.Base(dir) == "hooks" {
		dir = filepath.Dir(dir)
	}
	return filepath.Base(dir)
}

// hookTypesIn collects every "type" a hook definition declares, at any depth,
// because the shape nests differently per harness and a walker that assumed one
// shape would silently check nothing.
func hookTypesIn(n any) []string {
	var out []string
	switch v := n.(type) {
	case map[string]any:
		if t, ok := v["type"].(string); ok && strings.TrimSpace(t) != "" {
			out = append(out, t)
		}
		for _, child := range v {
			out = append(out, hookTypesIn(child)...)
		}
	case []any:
		for _, child := range v {
			out = append(out, hookTypesIn(child)...)
		}
	}
	return out
}
