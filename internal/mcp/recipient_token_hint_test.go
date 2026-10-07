package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// Exercise the reported shortened address through the same lookup/refusal as
// production. The real recipient is beyond the bounded alphabetic roster.
func TestRecipientTokenSubsetHintThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, want := range []string{"gpt-labs", "GPT-LABS"} {
			t.Run(version+"/"+want, func(t *testing.T) {
				srv, _, _ := aliasReplayServer(t, t.TempDir())
				_, sender := aliasRegister(t, srv, "sender", "token-hint-sender")
				for n := range 10 {
					aliasRegister(t, srv, fmt.Sprintf("alpha-%02d", n), fmt.Sprintf("token-hint-%d", n))
				}
				aliasRegister(t, srv, "gpt-agenxy-labs", "token-hint-labs")
				r := toolCallOn(t, srv, version, "send", map[string]any{
					"token": sender, "to": want, "type": "notify", "body": "must not deliver a suggested address",
				})
				if r["code"] != "E_NO_AGENT" || r["msg_serial"] != nil {
					t.Fatalf("short name was resolved rather than refused: %v", r)
				}
				if !strings.Contains(fmt.Sprint(r["hint"]), "did you mean gpt-agenxy-labs?") {
					t.Fatalf("token-subset recipient buried behind roster: %v", r)
				}
			})
		}
	}
}
