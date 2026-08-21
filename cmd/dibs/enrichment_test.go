package main

import (
	"testing"

	"github.com/agenxy/dibs/internal/mcp"
)

// Every field the bridge injects must be one `register` actually declares.
//
// identityEnv is applied to register and to nothing else, and it carried an
// entry for `effort`, which is an update field. Unknown arguments are refused
// rather than ignored, so injecting it did not add a field: it failed the call.
// Every Claude Code session with CLAUDE_EFFORT set got `-32602 register does
// not take "effort"` and registered no agent at all, which means no lifecycle
// hook could resolve that session, no mail was delivered to it, and its claim
// guard returned allow. Reproduced against the shipped v0.0.6 binary.
//
// The import is test-only, so none of internal/mcp lands in the CLI: the same
// arrangement the tool-name copy uses, for the same reason.
func TestEveryEnrichedFieldIsOneRegisterDeclares(t *testing.T) {
	declared := mcp.ToolProperties("register")
	if len(declared) == 0 {
		t.Fatal("register declares no properties: this probe is reading nothing")
	}
	for _, e := range identityEnv {
		if !declared[e.field] {
			t.Errorf("the bridge injects %q into register (from %s), which register does "+
				"not declare. Unknown arguments are REFUSED, so this does not add a field, "+
				"it fails registration outright for every %s session that sets %s",
				e.field, e.env, e.harness, e.env)
		}
	}
}

// And the fields read from Claude Code's sidecar go into register too.
func TestSidecarFieldsAreOnesRegisterDeclares(t *testing.T) {
	declared := mcp.ToolProperties("register")
	for field := range sessionContext(true) {
		if !declared[field] {
			t.Errorf("the bridge injects %q into register from the session sidecar, and "+
				"register does not declare it: registration fails outright", field)
		}
	}
}
