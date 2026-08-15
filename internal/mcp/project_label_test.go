package mcp

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A board that cannot say which project an agent is in is not situational
// awareness, it is a list of names.
//
// The failure is specific and was easy to miss because every row looked
// populated: agents in three repositories all reported branch "main", so the
// board rendered three identical-looking identities and the human had no way,
// from the board, to tell which of their projects each agent was working on.
// Dibs coordinates a MACHINE, and a machine usually has more than one project
// open, so this is the common case rather than an edge.
//
// This asserts the WIRING, from register through to what a reader gets
// back. Testing paths.ProjectName alone would stay green with the resolve
// deleted from agentInfo, which is this codebase's most repeated defect: a
// correct helper that nothing calls.
func TestTheBoardSaysWhichProjectAnAgentIsIn(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "payments-api")
	nested := filepath.Join(repo, "internal", "store")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init"},
		{"config", "user.name", "Dibs Test"},
		{"config", "user.email", "agents@example.invalid"},
		{"commit", "--allow-empty", "-m", "fixture"},
	} {
		cmd := exec.Command(git, append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	srv, _ := newServer(t)
	// Registered from a SUBDIRECTORY, which is where agents usually are. The
	// label must name the project, not whatever folder the harness happened to
	// start in: "store" would be indistinguishable from any other repository's
	// internal/store.
	out := toolCall(t, srv, "register", map[string]any{
		"name": "payments-worker", "cwd": nested, "branch": "main",
	})
	token, _ := out["token"].(string)
	if token == "" {
		t.Fatalf("register returned no token: %v", out)
	}

	board := toolCall(t, srv, "check_in", map[string]any{"token": token})
	blob, err := json.Marshal(board)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), `"project":"payments-api"`) {
		t.Errorf("the board does not say which project the agent is in.\n"+
			"Expected a project of %q somewhere in the view; got:\n%s", "payments-api", blob)
	}
}

// An agent outside a checkout must not acquire a project it does not have.
// Guessing from the directory name is worse than a blank: a blank sends the
// reader to the cwd, whereas "tmp" reads as a fact.
func TestAnAgentOutsideARepositoryIsNotGivenAProject(t *testing.T) {
	info := agentInfo(json.RawMessage(`{}`), &toolArgs{CWD: t.TempDir()}, nil)
	if info != nil && info.Project != "" {
		t.Errorf("a non-repository directory produced project %q, want none", info.Project)
	}
}
