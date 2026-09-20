package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The sidecar is found by OUR OWN PARENT, not by CLAUDE_PID.
//
// Claude Code 2.1.275 (desktop app, 2026-09-19) spawns its MCP servers with
// CLAUDE_CODE_SESSION_ID but without CLAUDE_PID; only shell children get that.
// sessionContext keyed on CLAUDE_PID alone, so every plugin bridge on this
// machine read no sidecar: `_meta com.dibs/session` carried `host-<ppid>` on
// every call, check_in bound that as an alias, and the sidecar's cwd, surface
// and title never reached the board. register still bound the right primary,
// through the environment table, which is why it took a check_in result
// reading "session_id": "host-62908" to notice.
//
// The parent pid is the better key anyway: a sidecar named for the process
// that spawned this bridge is proof that process is a Claude Code session and
// that this bridge is its direct child. CLAUDE_PID is inherited by everything
// beneath a session and proves neither.
func TestSidecarIsFoundByParentPidWhenClaudePidIsUnset(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	pid := strconv.Itoa(os.Getppid())
	const harnessSession = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	sidecar := `{"pid":` + pid + `,"sessionId":"` + harnessSession + `",` +
		`"cwd":"/tmp","entrypoint":"claude-desktop","kind":"interactive"}`
	if err := os.WriteFile(filepath.Join(home, ".claude", "sessions", pid+".json"),
		[]byte(sidecar), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_PID", "")

	for _, isClaude := range []bool{true, false} {
		got := sessionContext(isClaude)
		if got["session_id"] != harnessSession {
			t.Errorf("handshake=%v: session_id=%q, wanted the parent's sidecar %q: "+
				"without it every call carries host-<ppid> in _meta and the wake "+
				"path depends on the register argument alone", isClaude,
				got["session_id"], harnessSession)
		}
		if got["surface"] != "claude-desktop" {
			t.Errorf("handshake=%v: surface=%q, the sidecar's entrypoint was not read",
				isClaude, got["surface"])
		}
	}
}

// A sidecar that is NOT our parent's is still refused without CLAUDE_PID
// naming it and a handshake vouching: that is the nested-bridge case the
// guard e2e exercises, and the parent-pid key must not loosen it.
func TestAForeignSidecarIsStillNotAdoptedWithoutClaudePid(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	other := strconv.Itoa(os.Getpid()) // any pid that is not our parent
	sidecar := `{"pid":` + other + `,"sessionId":"not-ours","cwd":"/tmp"}`
	if err := os.WriteFile(filepath.Join(home, ".claude", "sessions", other+".json"),
		[]byte(sidecar), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_PID", "")
	if got := sessionContext(false)["session_id"]; got != "" {
		t.Errorf("adopted %q from a sidecar that is not our parent's", got)
	}
}
