package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestLegacyUnreadPartialVerdictSurvivesCheckInAndReplay(t *testing.T) {
	st := core.NewState("legacy-partial", core.DefaultLimits())
	journal := &queueReplayLedger{}
	apply := func(op *core.Op) core.Result {
		t.Helper()
		if actor := st.AgentByToken(op.Token); actor != nil {
			op.AgentID = actor.ID
		}
		now := time.Now()
		before := st.Serial
		r, _, err := st.Apply(op, now)
		if err != nil {
			t.Fatalf("pre-upgrade setup %s: %v", op.Kind, err)
		}
		if st.Serial != before {
			if err := journal.Append(st.Serial, now, op); err != nil {
				t.Fatal(err)
			}
		}
		return r
	}
	for _, id := range []string{"lead", "worker"} {
		apply(&core.Op{Kind: core.OpRegister, Name: id, NewToken: id + "-token", Nonce: "partial-" + id})
		apply(&core.Op{Kind: core.OpAckBoard, Token: id + "-token"})
	}
	parent := apply(&core.Op{
		Kind: core.OpSendMessage, Token: "lead-token", To: "worker",
		MsgType: core.MsgQuestion, Body: "question",
	})["msg_serial"].(uint64)
	body := "unread-legacy-partial-" + strings.Repeat("λ", mailQuoteEach+1)
	apply(&core.Op{Kind: core.OpRespond, Token: "worker-token", MsgSerial: parent, Disposition: "answer", Body: body})
	for restart := range 2 {
		e := New(st, journal, deadProber{})
		e.SetSocketWakes(false)
		e.SetWakePolicy(WakeNone)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { e.Run(ctx); close(done) }()
		for read := range 2 {
			r, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: "lead-token"})
			if err != nil {
				cancel()
				<-done
				t.Fatal(err)
			}
			lines := fmt.Sprint(r["agent_updates"])
			if !strings.Contains(lines, "unread-legacy-partial-") || !strings.Contains(lines, "trimmed; read_mail") {
				t.Errorf("restart%d/read%d dropped a partial legacy verdict", restart, read)
			}
		}
		if restart == 1 {
			if _, err := e.GetMessage(ctx, "lead-token", parent); err != nil {
				t.Error("explicit full read:", err)
			}
			r, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: "lead-token"})
			if err != nil || strings.Contains(fmt.Sprint(r["agent_updates"]), "unread-legacy-partial-") {
				t.Error("fully read legacy verdict repeated")
			}
		}
		cancel()
		<-done
		// Serialize every recorded op, including the cutoff and both first
		// check-ins. Replaying the wire, not cloning memory, is the restart.
		st = core.NewState("legacy-partial", core.DefaultLimits())
		for _, record := range journal.records {
			var op core.Op
			if err := json.Unmarshal(record.raw, &op); err != nil {
				t.Fatal(err)
			}
			if _, _, err := st.Apply(&op, record.at); err != nil {
				t.Fatal("recorded replay:", err)
			}
		}
	}
	if st.Messages[parent].OutcomeReadAt == 0 {
		t.Fatal("explicit full read was not persisted")
	}
}
