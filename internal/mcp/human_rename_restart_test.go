package mcp

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The fixture closes the daemon and encrypted ledger, then opens and replays
// that journal into a fresh engine. Rename and send both use actual MCP doors.
func TestHumanRoleDeliveryAfterRenameAndEncryptedLedgerRestart(t *testing.T) {
	dir := t.TempDir()
	srv, eng, stop := restartableHumanServer(t, dir)
	sender := toolCall(t, srv, "register", map[string]any{"name": "sender", "nonce": "rename-sender"})["token"].(string)
	human, humanToken, err := eng.HumanAgent(context.Background())
	if err != nil || human == "" || humanToken == "" {
		t.Fatal("setup: human:", err)
	}
	renamed := toolCall(t, srv, "update", map[string]any{"token": humanToken, "name": "operator-label"})
	if renamed["__is_error"] == true || renamed["name"] != "operator-label" {
		t.Fatalf("setup: rename did not happen: %v", renamed)
	}
	// A fresh active registration has an idempotent retry path that masks the
	// failure. Record the normal idle transition before restart so recovery
	// has to use the renamed row rather than that short-lived retry.
	if _, err := eng.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{human}}); err != nil {
		t.Fatal("setup: dormant human transition:", err)
	}
	stop()
	srv, eng, _ = restartableHumanServer(t, dir)
	if eng.HumanIdentity() != human {
		t.Fatal("reserved human identity lost at boot after label rename")
	}
	eng.SetHumanNotifier(desktopFixture{available: false})
	sent := toolCall(t, srv, "send", map[string]any{"token": sender, "to": "human", "type": "notify", "body": "private note after restart"})
	serial, ok := sent["msg_serial"].(float64)
	if sent["__is_error"] == true || !ok {
		t.Fatalf("send to human after actual restart: %v", sent)
	}
	recovered, token, err := eng.HumanAgent(context.Background())
	if err != nil || recovered != human || token == "" {
		t.Fatal("human recovery changed identity:", recovered, err)
	}
	read := toolCall(t, srv, "read_mail", map[string]any{"token": token, "msg_serial": serial})
	mail, ok := read["message"].(map[string]any)
	if !ok || mail["to"] != human || mail["body"] != "private note after restart" {
		t.Fatalf("send reported success without delivery to the reserved human mailbox: %v", read)
	}
	checkpoint := toolCall(t, srv, "check_in", map[string]any{"token": token})
	board, ok := checkpoint["board"].(map[string]any)
	if !ok {
		t.Fatalf("setup: board missing: %v", checkpoint)
	}
	rows, ok := board["agents"].([]any)
	if !ok {
		t.Fatalf("setup: board agents missing: %v", board)
	}
	for _, row := range rows {
		agent, ok := row.(map[string]any)
		if ok && agent["id"] == human {
			if agent["name"] != "operator-label" {
				t.Fatalf("recovery silently reverted human display label: %v", agent)
			}
			return
		}
	}
	t.Fatal("human mailbox owner absent from board after successful delivery")
}
