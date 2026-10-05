package mcp

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The ordinary authenticated MCP doors call readAppRestart even when no app
// restart was recorded. Inbox must remain a pure read. Check-in itself is a
// ledgered awareness checkpoint, so it must add exactly that one record, not
// a second record for the absent restart notice.
func TestAbsentRestartNoticeDoesNotAppendLedgerThroughMCP(t *testing.T) {
	dir := t.TempDir()
	srv, _, _ := restartableQueueServer(t, dir)
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		res := toolCall(t, srv, name, args)
		if res["__is_error"] == true {
			t.Fatalf("%s failed: %v", name, res)
		}
		return res
	}
	registered := call("register", map[string]any{"name": "worker", "nonce": "restart-read-worker", "kind": "persistent"})
	token, ok := registered["token"].(string)
	if !ok || token == "" {
		t.Fatalf("registration did not issue a token: %v", registered)
	}
	ledgerPath := filepath.Join(dir, "ledger.jsonl")
	readLedger := func() []byte {
		t.Helper()
		data, err := os.ReadFile(ledgerPath)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := readLedger()
	if len(before) == 0 {
		t.Fatal("setup wrote no registration record")
	}
	inbox := call("inbox", map[string]any{"token": token})
	if _, exists := inbox["agent_updates"]; exists {
		t.Fatalf("unexpected notice without an app restart: %v", inbox)
	}
	if after := readLedger(); !bytes.Equal(after, before) {
		t.Fatalf("empty inbox appended ledger bytes: before=%d after=%d", len(before), len(after))
	}
	check := call("check_in", map[string]any{"token": token})
	if updates, ok := check["agent_updates"].([]any); !ok || len(updates) != 0 {
		t.Fatalf("unexpected check-in restart notice: %v", check)
	}
	after := readLedger()
	if got := bytes.Count(after, []byte{'\n'}) - bytes.Count(before, []byte{'\n'}); got != 1 {
		t.Fatalf("check-in must append exactly one checkpoint op, not an extra restart read: got %d records", got)
	}
	if got, want := check["serial"].(float64), registered["serial"].(float64)+1; got != want {
		t.Fatalf("check-in advanced serial by more than its own op: got %.0f want %.0f", got, want)
	}
}
