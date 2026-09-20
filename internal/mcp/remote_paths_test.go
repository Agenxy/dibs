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
