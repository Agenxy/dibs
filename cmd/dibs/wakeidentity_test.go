package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// Register must bind the SAME session id the lifecycle hooks quote, or the wake
// path cannot work at all.
//
// This is the defect that made Dibs' own messaging worse than the harness's
// native channel on this project's own board. The bridge enriches a register
// call with `session_id`, and it used bridgeSessionID(), which is
// `host-<ppid>`. The hooks quote the harness's session id, the one the sidecar
// at ~/.claude/sessions/<pid>.json names. Those two never match, so hook_poll
// resolved the agent to nobody and no message ever woke it.
//
// It could not heal either, which is what made it permanent rather than
// transient. The ambient repair binds the correct id from `_meta
// com.dibs/session`, which the bridge already sends on every call, but it binds
// only when the agent has NO session id. The primary was already occupied by
// the wrong one, so the repair was a no-op for the life of the board.
//
// Measured before it was fixed: an agent on this board registered as
// `host-5360` while both the sidecar and its hooks said
// 19d67315-7718-449e-be3f-3864f577eeed, and its mail was delivered into a
// different agent's session for hours.
func TestRegisterBindsTheHarnessSessionNotTheBridgePid(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Keyed on our OWN parent, because the discriminator is "CLAUDE_PID names
	// the process that spawned this bridge". A nested process inherits
	// CLAUDE_PID without being that session's child, and must NOT read it.
	pid := strconv.Itoa(os.Getppid())
	const harnessSession = "11111111-2222-3333-4444-555555555555"
	sidecar := `{"pid":` + pid + `,"sessionId":"` + harnessSession + `",` +
		`"cwd":"/tmp","entrypoint":"claude-desktop","kind":"interactive"}`
	if err := os.WriteFile(filepath.Join(home, ".claude", "sessions", pid+".json"),
		[]byte(sidecar), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_PID", pid)
	// sessionID() memoises for the life of the bridge process, which is right in
	// production and wrong inside a test binary that has already resolved it.
	sessionOnce = struct {
		sync.Once
		id string
	}{}
	// The handshake, because the sidecar is read only for a client that says it
	// is Claude Code. That gate is deliberate: CLAUDE_PID is inherited by every
	// process a Claude Code session spawns, so an ungated read lets an unrelated
	// nested bridge adopt its parent's session. Removing it broke nine checks in
	// the guard e2e. So this test performs the real sequence, initialize then
	// register, rather than skipping to the call it wants to assert on.
	// NO HANDSHAKE, deliberately. That is the case that was broken: clientIs
	// reads what an `initialize` left behind, so a 2026 caller that sends none,
	// or any register that arrives before the handshake is seen, read no sidecar
	// and bound the bridge pid instead. Simulating a handshake here would assert
	// the path that already worked.
	prevClient := lastClientInfo
	lastClientInfo = nil
	t.Cleanup(func() { lastClientInfo = prevClient })
	if clientIs("claude") {
		t.Fatal("setup: a handshake is visible, so this would not exercise the " +
			"no-handshake path it exists for")
	}

	// Setup must hold: the sidecar has to be readable, or the assertion below
	// would pass for the wrong reason (both sides falling back to the pid).
	if got := sessionContext(true)["session_id"]; got != harnessSession {
		t.Fatalf("setup: the sidecar yielded %q, wanted the harness session %q",
			got, harnessSession)
	}
	if bridgeSessionID() == harnessSession {
		t.Fatal("setup: the bridge fallback happens to equal the harness session, " +
			"so this test cannot tell the two apart")
	}

	line := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call",` +
		`"params":{"name":"register","arguments":{"name":"probe"}}}`)
	out := enrichRegister(line)

	var msg struct {
		Params struct {
			Arguments map[string]any `json:"arguments"`
			Meta      map[string]any `json:"_meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal(out, &msg); err != nil {
		t.Fatalf("enriched request is not JSON: %v", err)
	}

	got, _ := msg.Params.Arguments["session_id"].(string)
	if got != harnessSession {
		t.Errorf("register was enriched with session_id=%q, wanted the harness "+
			"session %q that the hooks quote. Binding anything else makes the agent "+
			"unreachable by every lifecycle hook, permanently, because the ambient "+
			"repair only fills an EMPTY session id", got, harnessSession)
	}

	// And the two channels must agree, which is the property that actually
	// matters: _meta carries the id the ambient repair would use, arguments
	// carry the id register binds. They disagreed, and that disagreement was
	// invisible from both ends.
	if meta, _ := msg.Params.Meta["com.dibs/session"].(string); meta != got {
		t.Errorf("the bridge sends session_id=%q in the register arguments and %q in "+
			"_meta. One becomes the agent's identity and the other is what the "+
			"repair would bind; when they differ the agent is unreachable and "+
			"nothing reports it", got, meta)
	}
}
