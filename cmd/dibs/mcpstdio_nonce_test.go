package main

import (
	"path/filepath"
	"testing"
)

// The same role in the same checkout gets the same nonce across sessions.
//
// This is the product's central failure and the reason a real board carried
// nine rows for five roles: an agent is told to keep a nonce, its context ends,
// which is the exact event the nonce exists for, and the nonce ends with it.
// The next session registers under the same name with a fresh one and becomes a
// sibling that cannot read a word of its predecessor's mail.
//
// The bridge is the only participant with a memory that spans sessions, so it
// keeps it. Two "sessions" here are two calls with no shared state but the
// store on disk, which is exactly what a harness restart is.
func TestTheSameRoleInTheSameProjectReattaches(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())

	first := map[string]any{"name": "reviewer", "cwd": "/work/api"}
	enrichNonce(first)
	got1, _ := first["nonce"].(string)
	if got1 == "" {
		t.Fatal("no nonce was supplied, so this session cannot be reattached to later")
	}

	// A new session: nothing in common but the project and the name.
	second := map[string]any{"name": "reviewer", "cwd": "/work/api"}
	enrichNonce(second)
	if got2, _ := second["nonce"].(string); got2 != got1 {
		t.Errorf("a returning session got a different nonce (%s vs %s), so it "+
			"registers as a sibling and cannot read its own mail",
			short8(got2), short8(got1))
	}

	// A DIFFERENT role in the same project is a different agent.
	other := map[string]any{"name": "release", "cwd": "/work/api"}
	enrichNonce(other)
	if got, _ := other["nonce"].(string); got == got1 {
		t.Error("two roles in one project share a nonce, so either could reattach " +
			"as the other")
	}

	// The same role in a DIFFERENT project is also a different agent.
	elsewhere := map[string]any{"name": "reviewer", "cwd": "/work/other"}
	enrichNonce(elsewhere)
	if got, _ := elsewhere["nonce"].(string); got == got1 {
		t.Error("one role's nonce is shared across projects, so a session in an " +
			"unrelated checkout would reattach to somebody else's agent")
	}
}

// What the agent supplied wins, and is remembered.
//
// An agent that manages its own credential should not have it overwritten, and
// should not have to manage it twice.
func TestASuppliedNonceWinsAndIsRemembered(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())

	mine := map[string]any{"name": "reviewer", "cwd": "/work/api", "nonce": "the-agents-own"}
	enrichNonce(mine)
	if got, _ := mine["nonce"].(string); got != "the-agents-own" {
		t.Errorf("the bridge overwrote a nonce the agent supplied: %s", got)
	}
	next := map[string]any{"name": "reviewer", "cwd": "/work/api"}
	enrichNonce(next)
	if got, _ := next["nonce"].(string); got != "the-agents-own" {
		t.Errorf("the agent's own nonce was not remembered for its next session: %s", got)
	}
}

// Two worktrees of one repository are one project.
func TestARepoDirAndItsWorktreeAgree(t *testing.T) {
	a := projectKey(map[string]any{"repo_dir": "/work/api/.git", "cwd": "/work/api/sub"})
	if a != filepath.Clean("/work/api") {
		t.Errorf("repo_dir did not resolve to the project root: %s", a)
	}
	// With no repository, the working directory is what there is.
	if b := projectKey(map[string]any{"cwd": "/tmp/scratch/"}); b != "/tmp/scratch" {
		t.Errorf("cwd fallback = %q", b)
	}
	// With neither, nothing is remembered rather than something guessed.
	if c := projectKey(map[string]any{}); c != "" {
		t.Errorf("a project was invented from nothing: %q", c)
	}
}

func short8(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
