package liveness

import "testing"

// A command that MENTIONS an agent is not an agent.
//
// This sweep runs over every process on the machine, including greps, editors
// and itself. Matching the tool name anywhere in the command line would fill
// the board with phantoms that can never be stuck and never be fixed.
func TestOnlyRealAgentProcessesCount(t *testing.T) {
	for _, c := range []struct{ cmd, want string }{
		{"codex exec --skip-git-repo-check -m gpt-5.6-sol", "codex"},
		{"/usr/local/bin/codex exec --sandbox read-only", "codex"},
		{"/Applications/ChatGPT.app/Contents/Resources/codex app-server", "codex"},
		{"claude --print hello", "claude"},
		{"/opt/homebrew/bin/opencode run", "opencode"},
		{"pi --model x", "pi"},

		// The desktop app's own binary, not a headless subagent run.
		{"/Applications/ChatGPT.app/Contents/Resources/codex", ""},
		// Things that merely mention it.
		{"grep -r codex exec /src", ""},
		{"vim codex.go", ""},
		{"tail -f /var/log/claude.log", ""},
		{"", ""},
		{"   ", ""},
	} {
		if got := HarnessOf(c.cmd); got != c.want {
			t.Errorf("HarnessOf(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}
}

// The environment is the attribution space, and it is read out of raw `ps`
// output. Each pattern is checked against the shape actually observed on a live
// process rather than an invented one.
func TestAttributionPatternsMatchWhatIsReallyThere(t *testing.T) {
	// Measured: the session directory Claude Desktop puts on a child's PATH.
	// The PATH value CONTAINS SPACES ("/Library/Application Support/..."), which
	// is why the environment is matched as a blob and not split into pairs,
	// splitting truncated this value and lost the id sitting further along.
	blob := `codex exec -m gpt-5.6-sol __CFBundleIdentifier=com.anthropic.claudefordesktop ` +
		`PATH=/usr/bin:/home/ada/Library/Application Support/Claude/local-agent-mode-sessions/` +
		`1cb9155b-d727-4cb2-8dc8-ed52667f8682/9049ccc5-6612-4c64-8527-9af1a207c41a/rpm/plugin_x/bin ` +
		`CLAUDE_CODE_ENTRYPOINT=claude-desktop`
	m := claudeSession.FindStringSubmatch(blob)
	if len(m) != 2 || m[1] != "9049ccc5-6612-4c64-8527-9af1a207c41a" {
		t.Errorf("did not recover the SESSION id (the second uuid) from a real PATH: %v", m)
	}

	// The explicit marker wins, and its value is space-free by construction.
	if m := agentsParent.FindStringSubmatch(`FOO=1 DIBS_PARENT=reviewer BAR=2`); len(m) != 2 || m[1] != "reviewer" {
		t.Errorf("DIBS_PARENT not recovered: %v", m)
	}
	// It must not match a variable that merely ends with the name.
	if agentsParent.MatchString(`MY_DIBS_PARENT=wrong`) {
		t.Error("matched MY_DIBS_PARENT; the marker must be its own variable")
	}
	if m := explicitSession.FindStringSubmatch(`CLAUDE_SESSION_ID=abc-123 X=1`); len(m) != 2 || m[1] != "abc-123" {
		t.Errorf("CLAUDE_SESSION_ID not recovered: %v", m)
	}
	// An environment with nothing to say must yield nothing, not a guess.
	if claudeSession.MatchString(`PATH=/usr/bin HOME=/Users/x`) {
		t.Error("matched a session in an environment that has none")
	}
}
