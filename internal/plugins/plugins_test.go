package plugins

import (
	"strings"
	"testing"
)

// The setup procedure Dibs hands an agent must be one that works.
//
// These strings are instructions an agent will FOLLOW, and they were stale in
// the way that fails silently for the person reading them: the procedure told
// agents to run `claude plugin marketplace add agenxy/agents && claude plugin
// install agents`, naming a marketplace and a plugin that have not existed
// since the rename, and to look for files under ~/.claude/plugins/agents. An
// agent that did exactly as it was told got an error it had no way to
// attribute, from the very payload meant to get it set up.
//
// No other check covers them: the docs smoke pass reads markdown, and these are
// Go string literals.
func TestTheSetupProcedureNamesThingsThatExist(t *testing.T) {
	stale := []string{"agenxy/agents", "install agents", "plugins/agents", "agents@agents", "agenxy/lanes"}
	for _, h := range catalog {
		blob := h.buys + " " + h.install + " " + h.root
		for _, s := range h.setup {
			blob += " " + s.Do + " " + s.Check + " " + s.IfNot
		}
		for _, bad := range stale {
			if strings.Contains(blob, bad) {
				t.Errorf("%s setup tells an agent to use %q, which does not exist: "+
					"an agent that follows this gets an error it cannot attribute",
					h.harness, bad)
			}
		}
	}
}

// An agent that cannot install software must be told to ask the person who can.
//
// Most harnesses will not let an agent rewrite their own configuration, so
// "install the plugin" is advice a large share of readers cannot act on. Saying
// so is the difference between a procedure and a dead end.
func TestSetupTellsAnAgentToAskItsOperator(t *testing.T) {
	for _, h := range catalog {
		// Only harnesses that ask for an install. Codex is asked for nothing on
		// purpose (a mail-fetching hook would make Dibs drive the harness, which
		// this project refuses), so demanding an operator there is a false alarm:
		// this test produced exactly that on its first run.
		if h.install == "" {
			continue
		}
		var blob string
		for _, s := range h.setup {
			blob += " " + s.Do
		}
		if !strings.Contains(strings.ToLower(blob), "operator") &&
			!strings.Contains(strings.ToLower(blob), "your human") {
			t.Errorf("%s setup never suggests asking the operator: an agent that cannot "+
				"install software is left with no next step", h.harness)
		}
	}
}
