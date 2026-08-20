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
// releases. Codex never ran a single entry in it, and the reason is worth
// stating precisely, because "the type exists" was the whole of the evidence
// that put it there.
//
// On 2026-08-17, codex main parses `mcp_tool` hooks (since 2026-08-07) and its
// hooks engine has a handler for them (since 2026-08-15), and they still do not
// run: `core/src/session/mod.rs` passes `mcp_executor: None` when building the
// HooksConfig for a real session, and the engine then drops every such handler
// at startup. The shipped Desktop build reports the same outcome, once per
// entry. On an older build the variant is absent entirely and the WHOLE file is
// rejected, which is the part that makes this worse than a no-op: one
// unsupported entry disables the supported ones beside it.
//
// A type in a source tree is not a feature, and nothing here checked. Worse,
// the existing hook test globs `plugins/*/hooks/hooks.json`, and this file
// lived at `plugins/codex/hooks.json`, so no test ever opened it: it was not
// that the guard disagreed, it is that the guard could not see it.
//
// So this walks BOTH layouts, and it is a list of what each harness RUNS, not
// of what its types are named. When Codex supplies that executor, move
// `mcp_tool` into the codex row deliberately, having watched a hook fire.
func TestShippedHooksUseOnlySupportedTypes(t *testing.T) {
	// Measured against running binaries on 2026-08-17, not read from source.
	// Claude Code documents five handler types and runs them. Codex declares
	// `mcp_tool` and has an engine handler for it, but no session supplies the
	// MCP executor it needs, so it is dropped at startup: declared is not run,
	// and this table is about what runs.
	supported := map[string]map[string]bool{
		"claude-code": {"command": true, "http": true, "mcp_tool": true, "prompt": true, "agent": true},
		// Codex declares four and runs ONE. `prompt` and `agent` are empty
		// structs that discovery skips by name ("prompt hooks are not supported
		// yet"), and `mcp_tool` is dropped for want of an executor. Listing a
		// declared-but-skipped type here would be the original mistake with a
		// different spelling.
		"codex": {"command": true},
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
