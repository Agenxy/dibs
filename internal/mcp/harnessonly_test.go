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
	const budget = 34000 // ~8.5k tokens
	if len(b) > budget {
		t.Errorf("tools/list is %d chars (~%d tokens), over the %d budget. Every agent pays "+
			"this on every cold connection", len(b), len(b)/4, budget)
	}
	t.Logf("tools/list: %d tools, %d chars (~%d tokens)", len(agentTools), len(b), len(b)/4)
}
