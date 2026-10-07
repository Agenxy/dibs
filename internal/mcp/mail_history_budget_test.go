package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/mailhistory"
)

// Identical native fixture recipe in query_budget_test.go and
// mail_history_budget_test.go. Real admitted folds and encrypted Append create
// just over SPEC's 4096 old-party units; no index or authorization setter.
type historyBudgetWriter struct {
	t      *testing.T
	state  *core.State
	ledger *ledger.Ledger
	now    time.Time
}

func (w *historyBudgetWriter) apply(op *core.Op) core.Result {
	w.t.Helper()
	if err := w.state.Admit(op); err != nil {
		w.t.Fatal("setup: admit:", err)
	}
	before := w.state.Serial
	res, _, err := w.state.Apply(op, w.now)
	if err != nil || w.state.Serial != before+1 {
		w.t.Fatal("setup: fold did not advance:", op.Kind, err)
	}
	if err := w.ledger.Append(w.state.Serial, w.now, op); err != nil {
		w.t.Fatal("setup: append:", err)
	}
	w.now = w.now.Add(time.Millisecond)
	return res
}

func historyBudgetLedger(t *testing.T, dir, node string) core.Agent {
	t.Helper()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal("setup:", err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), node, box)
	if err != nil {
		t.Fatal("setup:", err)
	}
	defer func() {
		if err := led.Close(); err != nil {
			t.Error(err)
		}
	}()
	st := core.NewState(node, core.DefaultLimits())
	w := historyBudgetWriter{t: t, state: st, ledger: led, now: time.Now().UTC().Add(-10 * time.Second)}
	apply := w.apply
	for _, id := range []string{"sender", "old", "heir", "admin"} {
		apply(&core.Op{
			Kind: core.OpRegister, Name: id, NewToken: "budget-token-" + id,
			Nonce: "budget-nonce-" + id, PID: 1, AgentKind: core.KindPersistent,
		})
		apply(&core.Op{Kind: core.OpAckBoard, Token: "budget-token-" + id})
	}
	apply(&core.Op{Kind: core.OpGrantRole, To: "admin", Mode: core.RoleAdmin})
	for n := 0; n < 70; n++ {
		res := apply(&core.Op{
			Kind: core.OpSendMessage, Token: "budget-token-sender", To: "old",
			MsgType: core.MsgRequest, Body: "bounded native history fixture",
		})
		parent, ok := res["msg_serial"].(uint64)
		if !ok || parent == 0 {
			t.Fatal("setup: missing parent", res)
		}
		apply(&core.Op{Kind: core.OpRespond, Token: "budget-token-old", MsgSerial: parent, Disposition: "approve"})
		for report := 0; report < 60; report++ {
			apply(&core.Op{
				Kind: core.OpRespond, Token: "budget-token-old", MsgSerial: parent,
				Disposition: "progress", Body: "recorded progress",
			})
		}
	}
	apply(&core.Op{Kind: core.OpSweep, DeadAgents: []string{"old"}})
	moved := apply(&core.Op{Kind: core.OpAdoptAgent, Token: "budget-token-admin", To: "old", Space: "heir", AdoptAuthorised: true})
	if moved["messages"] != 70 {
		t.Fatal("setup: native adoption did not move all mail", moved)
	}
	old := st.AgentByToken("budget-token-old")
	if old == nil || old.Status != core.StatusDormant {
		t.Fatal("setup: missing old incarnation", old)
	}
	for _, m := range st.Messages {
		if allowed, _ := core.MessageAccess(m.Serial, m, old); allowed || m.To != "heir" {
			t.Fatal("setup: old party still authorized after adoption", old.ID, m.From, m.To)
		}
	}
	return core.Agent{ID: old.ID, CreatedSerial: old.CreatedSerial}
}

func TestMailHistoryRealMCPCandidateBudgetAfterAdoption(t *testing.T) {
	dir := t.TempDir()
	historyBudgetLedger(t, dir, "history-mcp")
	var index *mailhistory.Index
	var eng *engine.Engine
	srv, _ := historyServer(t, dir, func(e *engine.Engine, l *ledger.Ledger) { eng = e; index = l.MailHistory() })
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for !index.Status().InitialReady {
		select {
		case <-deadline.C:
			t.Fatal("setup: native boot did not finish")
		case <-tick.C:
		}
	}
	if err := eng.SetRateTokens(context.Background(), "old", 30); err != nil {
		t.Fatal("setup:", err)
	}
	denied := toolCall(t, srv, "read_mail", map[string]any{"token": "budget-token-old", "msg_serial": 10})
	if denied["__is_error"] != true || denied["code"] != "E_NOT_YOUR_MESSAGE" {
		t.Fatal("setup: real read_mail allowed old party", denied)
	}
	payload := historyCall(t, srv, "2026-07-28", map[string]any{"token": "budget-token-old", "limit": 100})
	rows, ok := payload["units"].([]any)
	cursor, hasCursor := payload["cursor"].(string)
	if !ok || len(rows) != 0 || !hasCursor || cursor == "" {
		t.Fatal("public candidate scan exhausted or leaked the moved prefix:", payload)
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	var boundary struct {
		Reference uint64 `json:"r"`
	}
	if err != nil || len(raw) < 16 || json.Unmarshal(raw[:len(raw)-16], &boundary) != nil || boundary.Reference >= 4096 {
		t.Fatal("public candidate scan exceeded SPEC bound:", cursor)
	}
}
