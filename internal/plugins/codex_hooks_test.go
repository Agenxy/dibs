package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The Codex hook file must only use what Codex's deserialiser accepts.
//
// Codex validates hook configuration and hook OUTPUT against schemas generated
// from Rust structs carrying #[serde(deny_unknown_fields)] at every level. One
// key it does not know fails the whole parse, and the failure is reported as a
// hook that failed rather than as a configuration error, so a typo here looks
// like Dibs being broken.
//
// The permitted sets below are transcribed from codex-rs at 8e649e3a:
// config/src/hook_config.rs HooksFile, HookEventsToml, MatcherGroup and
// HookHandlerConfig::McpTool. They are frozen deliberately. If Codex widens
// them this test is what says the file may now say more; if Codex narrows them
// a released Dibs breaks quietly, which is the failure this guards.
func TestTheCodexHookFileOnlyUsesFieldsCodexAccepts(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "plugins", "codex", "hooks", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("the Codex hook file is not valid JSON: %v", err)
	}

	for k := range file {
		if k != "description" && k != "hooks" {
			t.Errorf("top-level key %q: HooksFile denies unknown fields and takes only "+
				"description and hooks", k)
		}
	}

	events := map[string]bool{
		"PreToolUse": true, "PermissionRequest": true, "PostToolUse": true,
		"PreCompact": true, "PostCompact": true, "SessionStart": true,
		"SessionEnd": true, "UserPromptSubmit": true, "SubagentStart": true,
		"SubagentStop": true, "Stop": true,
	}
	handlerFields := map[string]bool{
		"type": true, "server": true, "tool": true, "input": true,
		"timeout": true, "statusMessage": true,
	}

	hooks, _ := file["hooks"].(map[string]any)
	if len(hooks) == 0 {
		t.Fatal("the file binds no events, so it wakes nothing")
	}
	for event, groups := range hooks {
		if !events[event] {
			t.Errorf("event %q is not in Codex's HookEventName", event)
		}
		for _, g := range groups.([]any) {
			group := g.(map[string]any)
			for k := range group {
				if k != "matcher" && k != "hooks" {
					t.Errorf("%s: MatcherGroup has no field %q", event, k)
				}
			}
			for _, h := range group["hooks"].([]any) {
				handler := h.(map[string]any)
				for k := range handler {
					if !handlerFields[k] {
						t.Errorf("%s: McpTool has no field %q", event, k)
					}
				}
				if handler["type"] != "mcp_tool" {
					t.Errorf("%s: handler type is %v; a command handler would make Dibs "+
						"spawn a process to drive a harness, which PHILOSOPHY rule 5 "+
						"forbids and WAKE-MECHANISMS.md records as already removed once",
						event, handler["type"])
				}
				// The whole point: a response Codex refuses to parse delivers
				// nothing, however correct the daemon's side effect was.
				// A STRING, because that is what the tool advertises.
				//
				// This required the boolean `true`, which is the natural JSON and
				// not what hook_poll's inputSchema declares: every flag on that
				// tool is a string, because a harness template expands to one. A
				// host that validates arguments against the advertised schema
				// would reject the bundled call before the deliberately-loose
				// handler ever saw it, and the whole file would go quiet. The
				// handler accepting either is not a licence for the shipped
				// config to disagree with the schema.
				in, _ := handler["input"].(map[string]any)
				if in["strict_output"] != "true" {
					t.Errorf("%s: strict_output is %#v; hook_poll declares it a STRING, and "+
						"a host that validates against the advertised schema refuses the "+
						"call. Without it hook_poll may answer with its own diagnostic "+
						"keys, Codex rejects the whole object, and no mail is injected",
						event, in["strict_output"])
				}
			}
		}
	}
}
