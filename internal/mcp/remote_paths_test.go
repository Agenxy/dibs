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
