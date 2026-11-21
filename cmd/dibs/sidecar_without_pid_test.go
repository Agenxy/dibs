// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

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

// A STALE CLAUDE_PID must not hide the parent's sidecar.
//
// The Claude desktop app can hand a session an inherited CLAUDE_PID naming a
// process that exited long ago (measured 2026-11-20: Supply's session 26579
// carried CLAUDE_PID=4485, which no longer existed, and so did its bridge).
// readClaudeSidecar keyed on CLAUDE_PID whenever it was set, so it looked
// for 4485.json, and without a handshake it refused outright because 4485 was
// not the parent. The bridge sent `host-26579` on every call for two days,
// the row stayed bound to the previous session's dead process, and the board
// told senders the live session's harness was closed. A check-in could not
// repair it, because the check-in carried the placeholder too.
//
// The parent's sidecar is the proof; CLAUDE_PID is only a fallback.
func TestParentSidecarWinsOverAStaleClaudePid(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := strconv.Itoa(os.Getppid())
	const harnessSession = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	if err := os.WriteFile(filepath.Join(dir, parent+".json"), []byte(`{"pid":`+parent+
		`,"sessionId":"`+harnessSession+`","cwd":"/tmp","entrypoint":"claude-desktop"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// A foreign sidecar under the stale pid as well: the parent's still wins.
	const stale = "999999"
	if err := os.WriteFile(filepath.Join(dir, stale+".json"),
		[]byte(`{"pid":999999,"sessionId":"not-ours","cwd":"/tmp"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	for _, staleFile := range []bool{false, true} {
		pid := "4485" // no sidecar at all
		if staleFile {
			pid = stale
		}
		t.Setenv("CLAUDE_PID", pid)
		for _, isClaude := range []bool{true, false} {
			if got := sessionContext(isClaude)["session_id"]; got != harnessSession {
				t.Errorf("CLAUDE_PID=%s handshake=%v: session_id=%q, wanted the parent's %q",
					pid, isClaude, got, harnessSession)
			}
		}
	}
}
