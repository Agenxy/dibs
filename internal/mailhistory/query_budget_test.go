package mailhistory_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
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
	return core.Agent{ID: old.ID, CreatedSerial: old.CreatedSerial}
}

func TestMailHistoryNativeCandidateScanIsBoundedAfterAdoption(t *testing.T) {
	dir := t.TempDir()
	reader := historyBudgetLedger(t, dir, "history-native")
	f := nativeHistory(t, dir)
	f.ids["budget-token-old"] = reader.ID
	settledHistory(t, f, "budget-token-old", false)
	index := f.led.MailHistory()
	status := index.Status()
	req := mailhistory.PageRequest{
		Reader: reader, Upper: status.Drained.Serial,
		OwnershipChange: status.OwnershipChange, Limit: 100, Deadline: time.Now().Add(5 * time.Second),
	}
	page, err := index.ReadPage(f.ctx, req)
	if err != nil {
		t.Fatal("numeric consumer query failed:", err)
	}
	if page.Examined != 4096 || len(page.Units) != 0 || page.Next == "" {
		t.Fatal("candidate scan exceeded SPEC bound or lost continuation:", page.Examined, len(page.Units), page.Next)
	}
	req.Cursor, req.Deadline = page.Next, time.Now().Add(5*time.Second)
	tail, err := index.ReadPage(f.ctx, req)
	if err != nil || tail.Examined <= 0 || tail.Examined > 4096 || len(tail.Units) != 0 || tail.Next != "" {
		t.Fatal("candidate scan did not finish its fixed prefix:", err, tail)
	}
}
