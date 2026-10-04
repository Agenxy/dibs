package engine

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/humanask"
	"github.com/agenxy/dibs/internal/notify"
)

type cleanupNotifier struct {
	started chan humanask.Message
	post    chan struct{}
	removed chan NotificationCleanup
}

func (n *cleanupNotifier) Available() bool { return true }
func (n *cleanupNotifier) Ask(m humanask.Message) (humanask.Answer, error) {
	n.started <- m
	<-n.post
	m.Receipt("posted")
	return humanask.Answer{}, nil
}

func (n *cleanupNotifier) RemoveMessages(node string, serials []uint64) notify.Cleanup {
	n.removed <- NotificationCleanup{Node: node, Serials: append([]uint64(nil), serials...)}
	return notify.Cleanup{State: "requested", BestEffort: true}
}

func TestHumanWithdrawalCleansAfterAppendAndAgainAfterLatePost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n := &cleanupNotifier{started: make(chan humanask.Message, 1), post: make(chan struct{}), removed: make(chan NotificationCleanup, 4)}
	e := New(core.NewState("cleanup-node", core.DefaultLimits()), &memLedger{}, deadProber{})
	e.SetHumanNotifier(n)
	go e.Run(ctx)
	human, _, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal("setup human:", err)
	}
	register, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "sender", NewToken: "sender-token", Nonce: "cleanup-sender"})
	if err != nil || register["agent_id"] != "sender" {
		t.Fatalf("setup register: %v %v", register, err)
	}
	token := register["token"].(string)
	sent, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: token, To: human, MsgType: core.MsgQuestion, Body: "Question"})
	if err != nil || sent["ok"] != true {
		t.Fatalf("setup send: %v %v", sent, err)
	}
	serial := sent["msg_serial"].(uint64)
	select {
	case m := <-n.started:
		if m.Node != "cleanup-node" || m.Serial != serial {
			t.Fatalf("posting identity: %+v", m)
		}
	case <-ctx.Done():
		t.Fatal("setup posting did not start")
	}
	withdrawn, err := e.Do(ctx, &core.Op{Kind: core.OpRespond, Token: token, MsgSerial: serial, Disposition: "withdraw", Body: "answered elsewhere"})
	if err != nil || withdrawn["state"] != core.MsgStateWithdrawn {
		t.Fatalf("withdrawal: %v %v", withdrawn, err)
	}
	expect := func() {
		t.Helper()
		select {
		case batch := <-n.removed:
			if batch.Node != "cleanup-node" || !reflect.DeepEqual(batch.Serials, []uint64{serial}) {
				t.Fatalf("wrong cleanup: %+v", batch)
			}
		case <-ctx.Done():
			t.Fatal("committed withdrawal did not clean its notification")
		}
	}
	expect()
	close(n.post)
	expect()
	if err := e.AnswerAsHuman(ctx, serial, "answer", "stale button"); err == nil {
		t.Fatal("stale button answered a withdrawn question")
	}
	_, err = e.query(ctx, func() core.Result {
		m := e.state.Messages[serial]
		d := e.deliveryForHuman(m)
		if m.State != core.MsgStateWithdrawn || d.State != core.MsgStateWithdrawn || !d.Posted {
			t.Errorf("withdrawal/OS history misrepresented: message=%+v delivery=%+v", m, d)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestHumanDecisionsCleanEveryRelayAndRebuildAfterRestart(t *testing.T) {
	for _, disposition := range []string{"answer", "approve", "deny", "decline"} {
		t.Run(disposition, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			n := &cleanupNotifier{removed: make(chan NotificationCleanup, 8)}
			journal := &memLedger{}
			e := New(core.NewState("cleanup-node", core.DefaultLimits()), journal, deadProber{})
			e.SetHumanNotifier(n)
			go e.Run(ctx)
			human, humanToken, err := e.HumanAgent(ctx)
			if err != nil {
				t.Fatal("setup human:", err)
			}
			reg, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "sender", Nonce: "cleanup-sender"})
			if err != nil || reg["token"] == nil {
				t.Fatalf("setup sender: %v %v", reg, err)
			}
			a, detachA := e.AttachHumanRelay()
			defer detachA()
			b, detachB := e.AttachHumanRelay()
			defer detachB()
			kind := core.MsgRequest
			if disposition == "answer" {
				kind = core.MsgQuestion
			}
			sent, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: reg["token"].(string), To: human, MsgType: kind, Body: "question"})
			if err != nil || sent["ok"] != true {
				t.Fatalf("setup send: %v %v", sent, err)
			}
			serial := sent["msg_serial"].(uint64)
			read := func(feed <-chan HumanNotice) HumanNotice {
				t.Helper()
				select {
				case notice := <-feed:
					return notice
				case <-ctx.Done():
					t.Fatal("relay did not receive notice")
					return HumanNotice{}
				}
			}
			for _, feed := range []<-chan HumanNotice{a, b} {
				if notice := read(feed); notice.Serial != serial || notice.Cleanup != nil {
					t.Fatalf("setup notice: %+v", notice)
				}
			}
			// These are the two real ingress routes used by the web/MCP human
			// identity and the relay/desktop human answer respectively.
			if disposition == "answer" {
				err = e.AnswerAsHuman(ctx, serial, disposition, "yes")
			} else {
				var result core.Result
				result, err = e.Do(ctx, &core.Op{Kind: core.OpRespond, Token: humanToken, MsgSerial: serial, Disposition: disposition})
				if result["ok"] != true {
					t.Fatalf("decision: %v", result)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, feed := range []<-chan HumanNotice{a, b} {
				notice := read(feed)
				if notice.Serial != 0 || notice.Cleanup == nil || !reflect.DeepEqual(notice.Cleanup.Serials, []uint64{serial}) {
					t.Fatalf("old relay would present cleanup, or cleanup missing: %+v", notice)
				}
			}
			select {
			case batch := <-n.removed:
				if !reflect.DeepEqual(batch.Serials, []uint64{serial}) {
					t.Fatalf("local cleanup: %+v", batch)
				}
			case <-ctx.Done():
				t.Fatal("no local cleanup")
			}
			st := replayed(t, ctx, e, journal)
			// replayed's shared fixture uses node "t". A real reopened ledger
			// retains its own board node; preserve that metadata here too.
			st.NodeID = "cleanup-node"
			cancel()
			ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel2()
			restored := New(st, &memLedger{}, deadProber{})
			restored.SetHumanNotifier(n)
			go restored.Run(ctx2)
			select {
			case batch := <-n.removed:
				if batch.Node != "cleanup-node" || !reflect.DeepEqual(batch.Serials, []uint64{serial}) {
					t.Fatalf("restart cleanup: %+v", batch)
				}
			case <-ctx2.Done():
				t.Fatal("restart lost derived cleanup")
			}
			batch, err := restored.HumanCleanupForRelay(ctx2)
			if err != nil || batch == nil || !reflect.DeepEqual(batch.Serials, []uint64{serial}) {
				t.Fatalf("reconnect cleanup: %+v %v", batch, err)
			}
		})
	}
}

func TestHumanCleanupRebuildIsOneCappedRetainedBatch(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()
	human, _, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal("setup human:", err)
	}
	now := time.Now()
	var st *core.State
	onLoop(t, ctx, e, func(state *core.State) {
		for serial := uint64(1); serial <= 70; serial++ {
			state.Messages[serial] = &core.Message{
				Serial: serial, To: human, Type: core.MsgQuestion,
				State: core.MsgStateAnswered, RetainUntil: now.Add(time.Hour),
			}
		}
		state.Messages[71] = &core.Message{
			Serial: 71, To: human, Type: core.MsgQuestion,
			State: core.MsgStateAnswered, RetainUntil: now.Add(-time.Second),
		}
		state.Messages[72] = &core.Message{
			Serial: 72, To: "other", Type: core.MsgQuestion,
			State: core.MsgStateAnswered, RetainUntil: now.Add(time.Hour),
		}
		state.Messages[73] = &core.Message{
			Serial: 73, To: human, Type: core.MsgRequest,
			State: core.MsgStateQueued, RetainUntil: now.Add(time.Hour),
		}
		// Clone on the writer. The restarted loop never shares the original
		// state's maps; this fixture isolates derived selection, not replay.
		bytes, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			t.Error(marshalErr)
			return
		}
		st = core.NewState(state.NodeID, state.Limits)
		if unmarshalErr := json.Unmarshal(bytes, st); unmarshalErr != nil {
			t.Error(unmarshalErr)
		}
	})
	cancel()
	if st == nil {
		t.Fatal("setup state clone failed")
	}
	n := &cleanupNotifier{removed: make(chan NotificationCleanup, 2)}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	restored := New(st, &memLedger{}, deadProber{})
	restored.SetHumanNotifier(n)
	go restored.Run(ctx2)
	select {
	case batch := <-n.removed:
		if len(batch.Serials) != notify.CleanupBatch || batch.Serials[0] != 70 || batch.Serials[len(batch.Serials)-1] != 7 {
			t.Fatalf("boot did not use one capped retained batch: %+v", batch)
		}
	case <-ctx2.Done():
		t.Fatal("no boot cleanup batch")
	}
}
