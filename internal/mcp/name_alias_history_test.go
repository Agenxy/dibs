package mcp

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/ledger"
)

// An older writer had no former-name cap. Its legal updates must reconstruct
// every former name even when new admission would refuse another addition.
func TestHistoricalNameAliasesAreNotTruncatedOnReplay(t *testing.T) {
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "alias-fixture", box)
	if err != nil {
		t.Fatal(err)
	}
	st := core.NewState("alias-fixture", core.DefaultLimits())
	writeHistorical := func(op *core.Op) {
		t.Helper()
		now := time.Unix(1234+int64(st.Serial), 0)
		if _, _, err := st.Apply(op, now); err != nil {
			t.Fatal("setup:", err)
		}
		if err := led.Append(st.Serial, now, op); err != nil {
			t.Fatal("setup:", err)
		}
	}
	writeHistorical(&core.Op{Kind: core.OpRegister, Name: "worker-id", Nonce: "historical-worker",
		AgentKind: core.KindPersistent, NewToken: "historical-fixture-token"})
	for n := 0; n <= 70; n++ {
		writeHistorical(&core.Op{Kind: core.OpUpdate, Token: "historical-fixture-token", Name: fmt.Sprintf("historic-%02d", n)})
	}
	if err := led.Close(); err != nil {
		t.Fatal(err)
	}
	srv, _, _ := aliasReplayServer(t, dir)
	_, sender := aliasRegister(t, srv, "sender", "historical-sender")
	n := aliasSend(t, srv, sender, "historic-00", "worker-id", "the oldest name survives")
	aliasRead(t, srv, "historical-fixture-token", n, "worker-id", "the oldest name survives")
	r := toolCall(t, srv, "update", map[string]any{"token": "historical-fixture-token", "name": "historic-71"})
	if r["code"] != "E_TOO_LARGE" {
		t.Fatalf("historical owners bypassed new admission: %v", r)
	}
}
