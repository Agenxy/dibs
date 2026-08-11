package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// The advertised tool surface has a size budget.
//
// A reviewer reported its capability discovery being TRUNCATED by Lanes' tool
// list, measuring ~68k characters of which it said 58% was repeated orientation.
// Measured at the server, that does not reproduce: the descriptions total ~12k
// with no repeated sentence at all, and the whole tools/list payload is ~31k. The
// inflation was in that client's own rendering, which Lanes does not control.
//
// The concern survives the failed reproduction, though. Every tool this project
// adds is paid for by every agent on every connection, forever, and the failure
// mode is not an error: it is a client quietly truncating and an agent never
// learning a capability exists. Nothing measured that, so nothing would have
// noticed it drifting.
//
// So the budget is pinned here rather than argued about. The numbers are roughly
// double today's, which is room to grow without room to sprawl; a change that
// exceeds them is asked to justify itself, not forbidden.
func TestTheAdvertisedToolSurfaceStaysWithinBudget(t *testing.T) {
	const (
		maxPayload      = 64 << 10 // whole tools/list, JSON
		maxDescriptions = 24 << 10 // prose an agent must read
		maxOneTool      = 4 << 10  // any single tool's description + schema
	)
	payload, err := json.Marshal(map[string]any{"tools": agentTools})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(payload) > maxPayload {
		t.Errorf("tools/list is %d bytes, over the %d budget: clients truncate "+
			"capability lists silently, so an agent stops learning tools exist rather "+
			"than seeing an error", len(payload), maxPayload)
	}

	descs := 0
	for _, tool := range agentTools {
		d, _ := tool["description"].(string)
		descs += len(d)
		schema, _ := json.Marshal(tool["inputSchema"])
		if n := len(d) + len(schema); n > maxOneTool {
			name, _ := tool["name"].(string)
			t.Errorf("tool %q is %d bytes of description plus schema, over the %d "+
				"budget for one tool", name, n, maxOneTool)
		}
	}
	if descs > maxDescriptions {
		t.Errorf("tool descriptions total %d characters, over the %d budget",
			descs, maxDescriptions)
	}
}

// No sentence may be repeated across tool descriptions.
//
// This is the specific thing the reviewer believed it had found, and the reason
// it is worth a guard even though it was not true: shared preamble is the natural
// way a tool surface rots. Each new tool copies the orientation from the last,
// nobody reads the total, and the cost lands on every agent on every connection.
// Orientation belongs in the server instructions and lanes://skills, which are
// sent once.
func TestNoSentenceIsRepeatedAcrossToolDescriptions(t *testing.T) {
	seen := map[string]string{}
	for _, tool := range agentTools {
		name, _ := tool["name"].(string)
		d, _ := tool["description"].(string)
		for _, sentence := range strings.Split(d, ". ") {
			s := strings.TrimSpace(sentence)
			// Short fragments legitimately recur ("Returns the board."); only
			// substantial prose indicates copied orientation.
			if len(s) < 60 {
				continue
			}
			if first, dup := seen[s]; dup {
				t.Errorf("%q and %q share a sentence, which is preamble that belongs in "+
					"the server instructions instead:\n  %q", first, name, s)
			}
			seen[s] = name
		}
	}
}
