package main

import (
	"os"
	"path/filepath"
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
//
// With a REAL sidecar. The first version called sessionContext(true) in a bare
// environment, where the function returns early and yields only the universal
// host and cwd: it named surface, session_id and title and then checked neither,
// so a sidecar/schema mismatch would have stayed green. Raised by the
// pre-release review.
func TestSidecarFieldsAreOnesRegisterDeclares(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	const pid = "424242"
	sidecar := `{"pid":424242,"sessionId":"11111111-2222-3333-4444-555555555555",` +
		`"cwd":"/tmp","entrypoint":"claude-desktop","kind":"interactive"}`
	if err := os.WriteFile(filepath.Join(home, ".claude", "sessions", pid+".json"),
		[]byte(sidecar), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_PID", pid)

	got := sessionContext(true)
	// The sidecar-only fields must actually be present, or this is measuring the
	// universal ones again.
	for _, want := range []string{"session_id", "surface", "cwd"} {
		if got[want] == "" {
			t.Fatalf("the sidecar yielded no %q, so this probe is reading the bare "+
				"environment rather than a session: %v", want, got)
		}
	}

	declared := mcp.ToolProperties("register")
	for field := range got {
		if !declared[field] {
			t.Errorf("the bridge injects %q into register from the session sidecar, and "+
				"register does not declare it: registration fails outright", field)
		}
	}
}
