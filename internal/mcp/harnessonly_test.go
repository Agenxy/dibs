package mcp

import (
	"encoding/json"
	"testing"
)

// Tools an agent must never call are not advertised to agents, and are still
// callable, because the harness integrations depend on them.
//
// Both halves matter. A tool a model cannot correctly call is not a capability,
// it is a trap: hook_poll in a model's context is an invitation to a bug. And
// removing them from dispatch instead of from the listing would break every
// lifecycle hook silently.
func TestHarnessOnlyToolsAreHiddenButNotRemoved(t *testing.T) {
	listed := map[string]bool{}
	for _, td := range agentTools {
		name, _ := td["name"].(string)
		listed[name] = true
	}
	for name := range harnessOnly {
		if listed[name] {
			t.Errorf("%s is advertised to agents; it is harness plumbing they cannot call correctly", name)
		}
	}
	defined := map[string]bool{}
	for _, td := range toolDefs {
		name, _ := td["name"].(string)
		defined[name] = true
	}
	for name := range harnessOnly {
		if !defined[name] {
			t.Errorf("%s vanished from toolDefs entirely: the integrations that call it are now broken", name)
		}
	}
}

// The listing is what every agent pays for on a cold connection, so its size is
// a product decision rather than an accident. A reviewer agent reported skimming
// it, "which is dangerous when the unique sentence below it matters".
//
// The bound is deliberately loose: it is here to make growth visible, not to
// forbid it. If a change needs more room, raise it in the same commit and say
// what the agent gets for the tokens.
func TestToolListingStaysAffordable(t *testing.T) {
	b, err := json.Marshal(map[string]any{"tools": agentTools})
	if err != nil {
		t.Fatal(err)
	}
	// Raised from 34000, deliberately, per this test's own rule: say what the
	// agent gets for the tokens.
	//
	// The surface went from 42 tools to 44, adding `retitle_space` (redact a
	// topic your declaration published, which had no remedy but destroying the
	// space) and `adopt_agent` (recover a mailbox nobody can log back into,
	// found holding six unread messages on this project's own board). The
	// descriptions also gained what each message TYPE does to its recipient,
	// which is what an agent needs in order to choose one deliberately rather
	// than by tone.
	//
	// It is still SMALLER than it was: 36208 chars before any of this. Density
	// is the number that matters and it improved by a tenth, which an absolute
	// ceiling cannot see, so both are checked.
	const (
		budget  = 34500 // ~8.6k tokens
		perTool = 800   // the average that keeps a description worth reading
	)
	if len(b) > budget {
		t.Errorf("tools/list is %d chars (~%d tokens), over the %d budget. Every agent pays "+
			"this on every cold connection", len(b), len(b)/4, budget)
	}
	// A ceiling alone punishes adding a well-written tool and permits bloating
	// the ones already there. This catches the second.
	if avg := len(b) / len(agentTools); avg > perTool {
		t.Errorf("tools/list averages %d chars per tool, over %d: the surface is getting "+
			"wordier rather than wider, and prose belongs in dibs://skills, which is "+
			"fetched once", avg, perTool)
	}
	t.Logf("tools/list: %d tools, %d chars (~%d tokens), %d chars/tool",
		len(agentTools), len(b), len(b)/4, len(b)/len(agentTools))
}
