package main

import (
	"path/filepath"
	"runtime"
	"testing"
)

// The bridge resolves a call's path arguments on the machine they name,
// because the daemon can no longer do it for a caller on another one: it
// leaves a remote caller's paths alone (a Linux member's /tmp/repo must not
// become the macOS hub's /private/tmp/repo), so the spelling an agent types
// has to meet the spelling its bridge registered here, where the filesystem
// is. Found by the two-host suite after round five of the pre-release review
// moved the hub's canonicalisation out of the remote path.
func TestTheBridgeResolvesPathArgumentsOnItsOwnMachine(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("needs a filesystem where /tmp is a symlink, which macOS provides")
	}
	resolved, err := filepath.EvalSymlinks("/tmp")
	if err != nil || resolved == "/tmp" {
		t.Skip("/tmp is not a symlink here")
	}
	params := map[string]any{"name": "claim", "arguments": map[string]any{"path": "/tmp/repo/x.go"}}
	canonicalisePathArgs(params)
	if got := params["arguments"].(map[string]any)["path"]; got != resolved+"/repo/x.go" {
		t.Errorf("claim path = %v, want it resolved on this machine to %s/repo/x.go", got, resolved)
	}
	// A relative path is left for the daemon to refuse with its hint.
	params = map[string]any{"name": "claim", "arguments": map[string]any{"path": "internal/x.go"}}
	canonicalisePathArgs(params)
	if got := params["arguments"].(map[string]any)["path"]; got != "internal/x.go" {
		t.Errorf("a relative path was rewritten to %v", got)
	}
	// Tools with no path arguments are untouched.
	params = map[string]any{"name": "send", "arguments": map[string]any{"path": "/tmp/not-a-path-arg"}}
	canonicalisePathArgs(params)
	if got := params["arguments"].(map[string]any)["path"]; got != "/tmp/not-a-path-arg" {
		t.Errorf("an argument of a tool with no path arguments was rewritten to %v", got)
	}
}
