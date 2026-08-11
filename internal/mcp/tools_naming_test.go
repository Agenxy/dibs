package mcp

import (
	"strings"
	"testing"
)

// sign_off and close_space must each point at the other.
//
// The two names are near-anagrams whose subjects are opposite: sign_off ends
// the CALLER's own agent agent, close_space retires a CHANNEL of work. "Agent"
// legitimately means both things in this project, which is what makes the pair
// dangerous rather than merely untidy.
//
// The asymmetry is the reason this is a guard and not a style note. Calling
// close_space by mistake fails safely: it is coordinator-only and needs an id it
// will not have. Calling sign_off by mistake SUCCEEDS: it takes nothing but a
// token, so no argument check can catch the error, and a coordinator who meant
// to retire the space they opened silently removes themselves from the board
// instead. An agent reads the description before it calls, so the description is
// where the collision has to be defused.
func TestTheTwoCloseToolsDisambiguateEachOther(t *testing.T) {
	desc := map[string]string{}
	for _, tool := range toolDefs {
		name, _ := tool["name"].(string)
		if name == "sign_off" || name == "close_space" {
			desc[name], _ = tool["description"].(string)
		}
	}
	for _, name := range []string{"sign_off", "close_space"} {
		if desc[name] == "" {
			t.Fatalf("%s is not in the tool list; this guard is pinning the wrong names", name)
		}
	}
	if !strings.Contains(desc["sign_off"], "close_space") {
		t.Error("sign_off's description does not mention close_space: the tool that " +
			"succeeds when called by mistake is the one that must warn")
	}
	if !strings.Contains(desc["close_space"], "sign_off") {
		t.Error("close_space's description does not mention sign_off")
	}
	// sign_off must say who it ends. "Close your agent" reads as "the agent you
	// opened" to a coordinator holding a space, which is the exact misreading.
	low := strings.ToLower(desc["sign_off"])
	if !strings.Contains(low, "yourself") && !strings.Contains(low, "caller") {
		t.Errorf("sign_off does not say it closes the caller: %s", desc["sign_off"])
	}
}
