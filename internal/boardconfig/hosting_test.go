package boardconfig

import (
	"strings"
	"testing"
)

// Dibs never hosts an agent. It is a channel into the harness the agent
// already lives in.
//
// IT DID, AND IT TOLD OPERATORS TO MAKE IT. The documented Codex wake was
// `codex exec resume {thread}`, which does not deliver a message to anybody: it
// starts a headless Codex of its own and runs the thread itself, outside the
// ChatGPT app the operator was using. the maintainer found Dibs had opened his ChatGPT
// threads in its own process and called it unacceptable, rightly: a board that
// runs agents has become a harness, with the operator's threads doing work
// nobody watched, on a model allowance nobody approved. doctor printed that
// recipe, the Codex plugin notes recommended it, and the Claude Code one
// (`claude --resume {thread} -p`) did the same to Claude Code sessions.
//
// So the rule is enforced where every wake command is read, which is here: both
// the daemon and `dibs host-bridge` take their routes from this, so neither can
// run a command the other refuses.
func TestACommandThatRunsAnAgentIsNeverAWakeRoute(t *testing.T) {
	hosting := map[string][]string{
		"codex exec resume":      {"codex", "exec", "resume", "{thread}", "{message}"},
		"codex exec, full path":  {"/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex", "exec", "resume", "--skip-git-repo-check", "{thread}", "{message}"},
		"codex alone (the TUI)":  {"codex"},
		"codex resume (the TUI)": {"codex", "resume", "{thread}"},
		"claude -p":              {"claude", "--resume", "{thread}", "-p", "{message}"},
		"claude, full path":      {"/Users/x/.local/bin/claude", "--resume", "{thread}"},
	}
	for name, argv := range hosting {
		if why := HostsAnAgent(argv); why == "" {
			t.Errorf("%s: %v was accepted as a wake route. It runs an agent rather than "+
				"delivering to one, which turns Dibs into a harness", name, argv)
		}
	}
	delivers := map[string][]string{
		"codex queue":            {"codex", "queue", "--thread", "{thread}", "--message", "{message}"},
		"codex queue, full path": {"/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex", "queue", "--thread", "{thread}", "--message", "{message}"},
		"an operator's own tool": {"/usr/local/bin/notify-my-agent", "{agent}"},
	}
	for name, argv := range delivers {
		if why := HostsAnAgent(argv); why != "" {
			t.Errorf("%s: %v was refused (%s), but it delivers into a harness that is "+
				"already running", name, argv, why)
		}
	}
}

// The recipe Dibs used to recommend is repaired, not rejected: its fallback was
// always the right command.
//
// Every operator who followed the old advice has `exec resume` as argv and
// `codex queue` as the fallback. Refusing the whole entry would leave them with
// no wake at all; dropping only the hosting half leaves exactly the channel,
// with no edit to anybody's dibs.toml.
func TestTheOldCodexRecipeKeepsOnlyItsDeliveringHalf(t *testing.T) {
	routes, refused := DeliveringWakeRoutes(map[string]WakeExec{
		"codex": {
			Argv:     []string{"codex", "exec", "resume", "{thread}", "{message}"},
			Fallback: []string{"codex", "queue", "--thread", "{thread}", "--message", "{message}"},
		},
		"claude code": {Argv: []string{"claude", "--resume", "{thread}", "-p", "{message}"}},
	})
	c, ok := routes["codex"]
	if !ok {
		t.Fatal("the old Codex recipe lost its wake entirely; its queue fallback delivers " +
			"and should have been kept")
	}
	if strings.Join(c.Argv, " ") != "codex queue --thread {thread} --message {message}" || len(c.Fallback) != 0 {
		t.Errorf("the Codex route is %v / %v; want the queue command alone", c.Argv, c.Fallback)
	}
	if _, kept := routes["claude code"]; kept {
		t.Error("the Claude Code route was kept: `claude --resume -p` runs a session of its own, " +
			"and Claude Code is reached through its socket and hooks")
	}
	for _, h := range []string{"codex", "claude code"} {
		if refused[h] == "" {
			t.Errorf("nothing says why the %s route changed: an operator's config was "+
				"altered and the daemon must say so", h)
		}
	}
}
