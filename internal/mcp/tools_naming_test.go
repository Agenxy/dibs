package mcp

import (
	"strings"
	"testing"
)

// close_lane and lane_close must each point at the other.
//
// The two names are near-anagrams whose subjects are opposite: close_lane ends
// the CALLER's own agent lane, lane_close retires a CHANNEL of work. "Lane"
// legitimately means both things in this project, which is what makes the pair
// dangerous rather than merely untidy.
//
// The asymmetry is the reason this is a guard and not a style note. Calling
// lane_close by mistake fails safely: it is coordinator-only and needs an id it
// will not have. Calling close_lane by mistake SUCCEEDS: it takes nothing but a
// token, so no argument check can catch the error, and a coordinator who meant
// to retire the channel they opened silently removes themselves from the board
// instead. An agent reads the description before it calls, so the description is
// where the collision has to be defused.
func TestTheTwoCloseToolsDisambiguateEachOther(t *testing.T) {
	desc := map[string]string{}
	for _, tool := range toolDefs {
		name, _ := tool["name"].(string)
		if name == "close_lane" || name == "lane_close" {
			desc[name], _ = tool["description"].(string)
		}
	}
	for _, name := range []string{"close_lane", "lane_close"} {
		if desc[name] == "" {
			t.Fatalf("%s is not in the tool list; this guard is pinning the wrong names", name)
		}
	}
	if !strings.Contains(desc["close_lane"], "lane_close") {
		t.Error("close_lane's description does not mention lane_close: the tool that " +
			"succeeds when called by mistake is the one that must warn")
	}
	if !strings.Contains(desc["lane_close"], "close_lane") {
		t.Error("lane_close's description does not mention close_lane")
	}
	// close_lane must say who it ends. "Close your lane" reads as "the lane you
	// opened" to a coordinator holding a channel, which is the exact misreading.
	low := strings.ToLower(desc["close_lane"])
	if !strings.Contains(low, "yourself") && !strings.Contains(low, "caller") {
		t.Errorf("close_lane does not say it closes the caller: %s", desc["close_lane"])
	}
}
