package mcp

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A remote caller's paths are not resolved on the hub's filesystem.
//
// canonPath resolves symlinks so that two spellings of one local directory
// meet (/tmp/x and /private/tmp/x on macOS). Applied to a path on ANOTHER
// machine it answers about the wrong computer: a Linux member's claim on
// /tmp/repo/file, taken through a macOS hub, was recorded as
// /private/tmp/repo/file, its recorded checkout root /tmp/repo no longer
// prefixed it, and the portable repository rule, which exists for exactly
// that agent, had no relative path to work with. guard_path did the same.
// Registration had already learned this (resolveRemoteLocation); the claim
// and guard paths had not. Found by the pre-release review, round five.
func TestARemoteCallersClaimPathIsNotResolvedOnTheHub(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("needs a filesystem where /tmp is a symlink, which macOS provides")
	}
	if resolved, err := filepath.EvalSymlinks("/tmp"); err != nil || resolved == "/tmp" {
		t.Skip("/tmp is not a symlink here, so canonicalisation would change nothing")
	}
	srv, eng, _ := newServerWithEngine(t)
	meta := map[string]any{HostMetaKey: "member-host"}

	reg := toolCallWithMeta(t, srv, "register", map[string]any{"name": "member", "cwd": "/tmp/repo"}, meta)
	tok, _ := reg["token"].(string)
	if tok == "" {
		t.Fatalf("setup: register returned no token: %v", reg)
	}
	toolCall(t, srv, "check_in", map[string]any{"token": tok})
	out := rpc(t, srv, "2026-07-28", "tools/call", map[string]any{
		"name": "claim", "_meta": meta,
		"arguments": map[string]any{"token": tok, "path": "/tmp/repo/file.go", "mode": "exclusive"},
	})
	if _, bad := out["error"]; bad {
		t.Fatalf("claim refused: %v", out)
	}

	b, err := eng.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	claims, _ := b["claims"].([]*core.Claim)
	if len(claims) != 1 {
		t.Fatalf("board holds %d claims, want 1: %v", len(claims), b["claims"])
	}
	if got := claims[0].Path; got != "/tmp/repo/file.go" {
		t.Errorf("a remote agent's claim was recorded as %v: the hub resolved a path that "+
			"names nothing on its own disk, and the member's checkout root no longer prefixes it", got)
	}
}

// A resume records the machine it happens on. The same nonce presented from
// another computer is a new activation there: the row used to keep the host
// it registered on, so its wakes went to the machine it had left and its new
// claims were keyed there. Round ten of the pre-release review.
func TestAResumeMovesTheAgentToTheMachineItResumedOn(t *testing.T) {
	srv, eng, _ := newServerWithEngine(t)
	reg := toolCallWithMeta(t, srv, "register", map[string]any{
		"name": "rover", "cwd": "/w", "nonce": "n-rover-0123456789abcdef0123456789abcdef",
	}, map[string]any{HostMetaKey: "desktop"})
	if reg["token"] == nil {
		t.Fatalf("setup: %v", reg)
	}
	res := toolCallWithMeta(t, srv, "resume", map[string]any{
		"nonce": "n-rover-0123456789abcdef0123456789abcdef", "resume_id": "r1",
	}, map[string]any{HostMetaKey: "laptop"})
	if res["token"] == nil {
		t.Fatalf("resume: %v", res)
	}
	b, err := eng.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := hostIDOnBoard(t, b, "rover"); got != "laptop" {
		t.Errorf("after resuming from laptop the row's host is %q: its wakes go to the desktop it left", got)
	}
}

// A bridge's session id repeats across machines, and the guard must not
// resolve one machine's hook to another machine's agent.
//
// The stdio bridge derives `host-<ppid>` from its harness process, and pids
// repeat across computers, so two agents on two machines can both state
// host-12345. The guard and hook lookups matched on the session id alone:
// beta's hook resolved to alpha, and beta's guard answered "alpha, allow,
// no-claim" for a file alpha held exclusively, while the equivalent
// token-authenticated claim from beta was correctly refused. Round twelve
// of the pre-release review reproduced it. The machine a hook comes from is
// what the transport established for the call, and it decides.
func TestAHookFromAnotherMachineDoesNotResolveToThisOnesAgent(t *testing.T) {
	srv, _, _ := newServerWithEngine(t)
	const shared = "host-12345"
	alpha := toolCallWithMeta(t, srv, "register", map[string]any{
		"name": "alpha", "cwd": "/w/repo", "session_id": shared,
	}, map[string]any{HostMetaKey: "machine-a"})
	beta := toolCallWithMeta(t, srv, "register", map[string]any{
		"name": "beta", "cwd": "/w/repo", "session_id": shared,
	}, map[string]any{HostMetaKey: "machine-b"})
	for _, tok := range []any{alpha["token"], beta["token"]} {
		if tok == nil {
			t.Fatalf("setup: %v %v", alpha, beta)
		}
		toolCall(t, srv, "check_in", map[string]any{"token": tok})
	}

	// beta's hook, from machine-b, is beta's.
	res := toolCallWithMeta(t, srv, "hook_poll", map[string]any{
		"session_id": shared, "event": "SessionStart", "cwd": "/w/repo",
	}, map[string]any{HostMetaKey: "machine-b"})
	if got := res["agent"]; got != "beta" {
		t.Errorf("a hook from machine-b resolved to %v, want beta", got)
	}
	// And so is beta's guard, which alpha's claims do not cover... except by
	// the repository rule, which is not in play here: two paths, two disks.
	res = toolCallWithMeta(t, srv, "guard_path", map[string]any{
		"session_id": shared, "path": "/w/repo/file.go", "cwd": "/w/repo",
	}, map[string]any{HostMetaKey: "machine-b"})
	if got := res["agent"]; got != "beta" {
		t.Errorf("a guard query from machine-b resolved to %v, want beta: the guard is deciding "+
			"for the wrong machine's agent", got)
	}
}

// And a session announced on one machine is not inherited on another.
//
// The decision is tested in the engine (announcedSession); this is the
// wiring: the host the transport established for the hook_poll has to
// reach the announcement, and the host stamped on the register has to
// reach the inference, or the two compare "" and "" and agree. Round
// fifteen of the pre-release review reproduced alpha on machine-a
// acquiring a thread that exists only on machine-b.
//
// EVERY announcing call, not only hook_poll: hook_session and hook_blocked
// built their announcement without the host after hook_poll had been fixed,
// and an announcement with no host is matched on the directory alone. Round
// sixteen.
func TestAnAnnouncementOnAnotherMachineIsNotInheritedThroughTheWire(t *testing.T) {
	const thread = "b7804476-3292-4ad9-bddb-16f823328751"
	announcements := map[string]map[string]any{
		"hook_poll":    {"session_id": thread, "event": "SessionStart", "cwd": "/w/repo"},
		"hook_session": {"session_id": thread, "event": "SessionStart", "cwd": "/w/repo"},
		"hook_blocked": {"session_id": thread, "event": "PermissionRequest", "cwd": "/w/repo", "tool_name": "Edit"},
	}
	for call, args := range announcements {
		t.Run(call, func(t *testing.T) {
			srv, _, _ := newServerWithEngine(t)
			toolCallWithMeta(t, srv, call, args, map[string]any{HostMetaKey: "machine-b"})
			alpha := toolCallWithMeta(t, srv, "register", map[string]any{
				"name": "alpha", "cwd": "/w/repo", "session_id": "host-12345",
			}, map[string]any{HostMetaKey: "machine-a"})
			if alpha["token"] == nil {
				t.Fatalf("setup: %v", alpha)
			}
			res := toolCallWithMeta(t, srv, "guard_path", map[string]any{
				"session_id": thread, "cwd": "/w/repo", "path": "/w/repo/file.go",
			}, map[string]any{HostMetaKey: "machine-a"})
			if got := res["agent"]; got == "alpha" {
				t.Fatalf("the thread announced by %s only on machine-b resolves to alpha on "+
					"machine-a: alpha inherited a session that is not on its machine: %v", call, res)
			}
		})
	}
}
