package mcp

import (
	"fmt"
	"testing"
)

// Coordination must not excuse a separate shared objective or a reference
// whose real participants are somebody else. These remain genuine warnings.
func TestCoordinationDoesNotHideUncoveredWorkThroughDeclareMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, scenario := range []string{"mixed-objective", "one-sided-wait", "third-party", "wrong-kind"} {
			t.Run(version+"/"+scenario, func(t *testing.T) {
				srv, _, _ := aliasReplayServer(t, t.TempDir())
				_, lead := aliasRegister(t, srv, "lead", "boundary-lead")
				_, worker := aliasRegister(t, srv, "worker", "boundary-worker")
				_, owner := aliasRegister(t, srv, "owner", "boundary-owner")
				sender := lead
				if scenario == "one-sided-wait" || scenario == "third-party" {
					sender = owner
				}
				sent := toolCallOn(t, srv, version, "send", map[string]any{
					"token": sender, "to": "worker", "type": "request", "body": "bounded coordination",
				})
				serial, _ := sent["msg_serial"].(float64)
				if serial == 0 {
					t.Fatalf("setup request: %v", sent)
				}
				kind := "request"
				if scenario == "wrong-kind" {
					kind = "question"
				}
				refs := []string{fmt.Sprintf("%s:%.0f", kind, serial)}
				if scenario == "mixed-objective" {
					refs = append(refs, "issue:42")
				}
				for i, token := range []string{lead, worker} {
					args := map[string]any{"token": token, "text": "implement work", "activity": "implement", "refs": refs}
					if scenario == "mixed-objective" || (scenario == "one-sided-wait" && i == 0) {
						args["waiting"] = "owner"
					}
					r := toolCallOn(t, srv, version, "declare", args)
					if r["ok"] != true || (i == 1 && r["warning"] == nil) {
						t.Fatalf("uncovered objective lost its warning: %v", r)
					}
				}
			})
		}
	}
}
