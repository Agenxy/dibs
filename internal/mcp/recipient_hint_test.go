package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// Register and send through the real writer/MCP door: no fabricated roster or
// direct hint call can establish what a misaddressed sender actually receives.
func TestRecipientHintNamesOmissionsAndClosestTypoThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, fuzzy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fuzzy=%v", version, fuzzy), func(t *testing.T) {
				srv, _, _ := aliasReplayServer(t, t.TempDir())
				_, sender := aliasRegister(t, srv, "sender", "hint-sender")
				for n := range 10 {
					aliasRegister(t, srv, fmt.Sprintf("alpha-%02d", n), fmt.Sprintf("hint-%d", n))
				}
				aliasRegister(t, srv, "gpt-agenxy-labs", "hint-labs")
				want := "completely-unrelated-recipient"
				if fuzzy {
					want = "gpt-agenxy-lbas"
				}
				r := toolCallOn(t, srv, version, "send", map[string]any{
					"token": sender, "to": want, "type": "notify", "body": "hint proof",
				})
				if r["code"] != "E_NO_AGENT" {
					t.Fatalf("setup: expected missing recipient: %v", r)
				}
				hint, _ := r["hint"].(string)
				if fuzzy {
					if !strings.Contains(hint, "did you mean gpt-agenxy-labs?") {
						t.Fatalf("near typo buried behind unrelated roster: %s", hint)
					}
				} else if !strings.Contains(hint, "4 more") || !strings.Contains(hint, "call board") {
					t.Fatalf("partial roster masquerades as all live agents: %s", hint)
				}
			})
		}
	}
}
