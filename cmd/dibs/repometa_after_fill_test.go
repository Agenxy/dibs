package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/agenxy/dibs/internal/mcp"
	"github.com/agenxy/dibs/internal/paths"
)

// The repository identity on a register describes the checkout the
// register names, after the sidecar has filled it in.
//
// The bridge stamps `_meta com.dibs/repo` for a hub on another machine that
// cannot ask Git about paths here. It computed that from the register's
// cwd, which at that point was blank, so it fell back to the bridge's own
// process directory; the register's cwd was then filled from Claude Code's
// sidecar, which names the session's real checkout, and the meta was never
// recomputed. A hub recorded the sidecar's directory beside another
// checkout's identity (or none), and the claims made in the real checkout
// carried the wrong repository-relative key, which is the thing the
// cross-machine collision rule compares. Round sixteen of the pre-release
// review.
func TestRegisterRepoMetaDescribesTheCheckoutTheSidecarNames(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(home, "checkout")
	if err := os.MkdirAll(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = checkout
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup: git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	git("remote", "add", "origin", "https://example.invalid/team/checkout.git")

	// The bridge's own directory is NOT the checkout: that is the case.
	elsewhere := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(elsewhere); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	pid := strconv.Itoa(os.Getppid())
	const harnessSession = "11111111-2222-3333-4444-555555555555"
	sidecar := `{"pid":` + pid + `,"sessionId":"` + harnessSession + `",` +
		`"cwd":` + strconv.Quote(checkout) + `,"entrypoint":"claude-desktop","kind":"interactive"}`
	if err := os.WriteFile(filepath.Join(home, ".claude", "sessions", pid+".json"),
		[]byte(sidecar), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_PID", pid)
	sessionOnce = struct {
		sync.Once
		id string
	}{}
	prevClient := lastClientInfo
	lastClientInfo = map[string]any{"name": "claude-code", "version": "0"}
	t.Cleanup(func() { lastClientInfo = prevClient })
	if got := sessionContext(true)["cwd"]; got != checkout {
		t.Fatalf("setup: the sidecar yielded cwd %q, wanted %q", got, checkout)
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
	if got, _ := msg.Params.Arguments["cwd"].(string); got != paths.Canonical(checkout) {
		t.Fatalf("setup: register's cwd is %q, wanted the sidecar's checkout %q", got, checkout)
	}
	repo, _ := msg.Params.Meta[mcp.RepoMetaKey].(map[string]any)
	_, wantRemote, _, ok := paths.Identify(paths.Canonical(checkout)).Identity()
	if !ok || wantRemote == "" {
		t.Fatalf("setup: the checkout has no identity to compare: %v", wantRemote)
	}
	if got, _ := repo["remote"].(string); got != wantRemote {
		t.Fatalf("the register names cwd %q and carries repo meta %v: a hub records that "+
			"directory beside the wrong repository identity", msg.Params.Arguments["cwd"], repo)
	}
}
