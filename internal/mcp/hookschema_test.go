package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A shipped hook must call its tool the way that tool advertises itself.
//
// The Codex hook file passes `strict_output` and the daemon's handler reads it
// through `truthy`, which takes a bool or a string. That looseness hid a real
// disagreement: the file sent the boolean `true` while hook_poll's inputSchema
// declares every one of its flags a string, because a harness template expands
// to one. Nothing compared the two. A host that validates arguments against the
// advertised schema, which is a thing hosts are entitled to do, would refuse the
// bundled call before the tolerant handler ever ran, and every hook in the file
// would go silently quiet: the exact failure mode this plugin exists to fix.
//
// So the comparison is made here rather than left to a reader. Both directions
// matter: a key the schema does not declare is as broken as a type mismatch,
// because that is what a validating host rejects.
func TestShippedHooksMatchTheAdvertisedToolSchemas(t *testing.T) {
	schemas := map[string]map[string]any{}
	for _, td := range toolDefs {
		name, _ := td["name"].(string)
		in, _ := td["inputSchema"].(map[string]any)
		props, _ := in["properties"].(map[string]any)
		if name != "" && props != nil {
			schemas[name] = props
		}
	}
	if len(schemas) == 0 {
		t.Fatal("no tool schemas found; this check would pass against anything")
	}

	files, err := filepath.Glob(filepath.Join("..", "..", "plugins", "*", "hooks", "hooks.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no shipped hooks.json found (%v): this check verified nothing", err)
	}

	checked := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		events, _ := doc["hooks"].(map[string]any)
		for event, v := range events {
			for _, group := range asSlice(v) {
				g, _ := group.(map[string]any)
				for _, h := range asSlice(g["hooks"]) {
					handler, _ := h.(map[string]any)
					// Only mcp_tool handlers call a Dibs tool. A `command`
					// handler is a different contract entirely, and Dibs does
					// not ship one.
					if handler["type"] != "mcp_tool" {
						continue
					}
					tool, _ := handler["tool"].(string)
					props, known := schemas[tool]
					if !known {
						t.Errorf("%s %s: calls tool %q, which this server does not define",
							filepath.Base(filepath.Dir(filepath.Dir(f))), event, tool)
						continue
					}
					input, _ := handler["input"].(map[string]any)
					for k, val := range input {
						spec, declared := props[k].(map[string]any)
						if !declared {
							t.Errorf("%s: passes %q to %s, which declares no such "+
								"parameter. A host that validates arguments refuses the "+
								"whole call, and a parameter no handler reads is invisible "+
								"from outside anyway", event, k, tool)
							continue
						}
						checked++
						want, _ := spec["type"].(string)
						if got := jsonType(val); want != "" && got != want {
							t.Errorf("%s: passes %q as %s (%#v); %s declares it %s. The "+
								"handler may be loose enough to take either, but a host "+
								"that checks arguments against the advertised schema is "+
								"not, and it rejects the call before the handler runs",
								event, k, got, val, tool, want)
						}
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no hook input parameters were compared against a schema; " +
			"this check verified nothing")
	}
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// jsonType names a decoded JSON value the way a schema does.
func jsonType(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case nil:
		return "null"
	}
	return "unknown"
}
