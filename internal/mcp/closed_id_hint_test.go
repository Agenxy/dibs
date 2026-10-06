package mcp

import (
	"strings"
	"testing"
)

// Enter through update, not Admit by hand: the deployed refusal has to tell
// the caller why pruning a closed row cannot free its immutable address.
func TestUpdateExplainsClosedImmutableIDAndAllowsReleasedLabel(t *testing.T) {
	srv, _ := newServer(t)
	setup := func(name string, args map[string]any) map[string]any {
		t.Helper()
		res := toolCall(t, srv, name, args)
		if res["__is_error"] == true {
			t.Fatalf("setup %s failed: %v", name, res)
		}
		return res
	}
	register := func(name string) string {
		t.Helper()
		res := setup("register", map[string]any{
			"name": name, "nonce": "closed-id-hint-" + name, "cwd": t.TempDir(),
		})
		token, _ := res["token"].(string)
		if token == "" {
			t.Fatalf("setup register returned no token: %v", res)
		}
		return token
	}
	closed := register("reserved-worker")
	setup("update", map[string]any{"token": closed, "name": "released-label"})
	setup("sign_off", map[string]any{"token": closed})
	live := register("live-worker")
	refused := toolCall(t, srv, "update", map[string]any{"token": live, "name": "reserved-worker"})
	if refused["__is_error"] != true || refused["code"] != "E_NAME_TAKEN" {
		t.Fatalf("closed immutable ID was not refused: %v", refused)
	}
	hint, _ := refused["hint"].(string)
	for _, required := range []string{"reserved-worker", "closed", "permanently reserved", "prune cannot free", "merge_agents", "admin", "choose another name"} {
		if !strings.Contains(hint, required) {
			t.Errorf("update hint %q omits corrective information %q", hint, required)
		}
	}
	// The retired LABEL is a different thing. Reuse it through the same door
	// and prove addressing follows the live holder instead of the closed row.
	setup("update", map[string]any{"token": live, "name": "released-label"})
	sender := register("hint-sender")
	sent := setup("send", map[string]any{
		"token": sender, "to": "released-label", "type": "notify", "body": "live label receipt",
	})
	serial, _ := sent["msg_serial"].(float64)
	if serial == 0 {
		t.Fatalf("setup send produced no message serial: %v", sent)
	}
	read := setup("read_mail", map[string]any{"token": live, "msg_serial": serial})
	message, _ := read["message"].(map[string]any)
	if message["to"] != "live-worker" || message["body"] != "live label receipt" {
		t.Fatalf("reused label did not reach the live identity: %v", read)
	}
}
