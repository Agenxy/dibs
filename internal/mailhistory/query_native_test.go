package mailhistory_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/mailhistory"
)

type historyAPI interface {
	ReadMailHistory(context.Context, string, uint64, int, bool, string) (core.Result, error)
}

type nativeHistoryFixture struct {
	eng  *engine.Engine
	led  *ledger.Ledger
	ctx  context.Context
	stop func()
	ids  map[string]string
}

func nativeHistory(t *testing.T, dir string, wrap ...func(*ledger.Ledger) engine.Ledger) nativeHistoryFixture {
	t.Helper()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal("setup:", err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "history-native", box)
	if err != nil {
		t.Fatal("setup:", err)
	}
	limits := core.DefaultLimits()
	limits.ConsumedRetention = 0
	st := core.NewState("history-native", limits)
	if _, err := led.Replay(st); err != nil {
		t.Fatal("setup:", err)
	}
	var source engine.Ledger = led
	if len(wrap) > 0 {
		source = wrap[0](led)
	}
	eng := engine.New(st, source, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	joined := make(chan struct{})
	go func() { eng.Run(ctx); close(joined) }()
	if _, _, err := eng.SubscribeInfo(ctx, ""); err != nil {
		t.Fatal("setup:", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	srv.Listener = led.MailHistory().ServingListener(ctx, srv.Listener)
	srv.Start()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			srv.Close()
			cancel()
			<-joined
			if err := led.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(stop)
	return nativeHistoryFixture{eng, led, ctx, stop, map[string]string{}}
}

func historyOp(t *testing.T, f nativeHistoryFixture, op *core.Op) core.Result {
	t.Helper()
	if op.Token != "" {
		// Refill through the existing writer query: rate is not the property tested.
		if err := f.eng.SetRateTokens(f.ctx, f.ids[op.Token], 30); err != nil {
			t.Fatal("setup:", err)
		}
	}
	res, err := f.eng.Do(f.ctx, op)
	if err != nil || res["error"] != nil {
		t.Fatal("setup: real op failed:", op.Kind, err, res)
	}
	return res
}

func historyIdentity(t *testing.T, f nativeHistoryFixture, name string) string {
	t.Helper()
	res := historyOp(t, f, &core.Op{Kind: core.OpRegister, Name: name, Nonce: "native-history-nonce-" + name, PID: 1, AgentKind: core.KindPersistent})
	token, ok := res["token"].(string)
	if !ok || token == "" {
		t.Fatal("setup: no token", res)
	}
	f.ids[token], ok = res["agent_id"].(string)
	if !ok || f.ids[token] == "" {
		t.Fatal("setup: no immutable id", res)
	}
	historyOp(t, f, &core.Op{Kind: core.OpAckBoard, Token: token})
	return token
}

func historyOnce(t *testing.T, f nativeHistoryFixture, token string, bodies bool) core.Result {
	t.Helper()
	api, ok := any(f.eng).(historyAPI)
	if !ok {
		t.Fatal("history public query is missing after real native setup")
	}
	res, err := api.ReadMailHistory(f.ctx, token, 0, 100, bodies, "")
	if err != nil {
		var ce *core.Error
		if !errors.As(err, &ce) || !strings.HasPrefix(ce.Code, "E_HISTORY_") || res["error"] == nil {
			t.Fatal("history query:", err)
		}
	}
	return res
}

func settledHistory(t *testing.T, f nativeHistoryFixture, token string, bodies bool) core.Result {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := f.eng.SetRateTokens(f.ctx, f.ids[token], 30); err != nil {
			t.Fatal("setup:", err)
		}
		res := historyOnce(t, f, token, bodies)
		if res["error"] == nil {
			return res
		}
		ce, ok := res["error"].(*core.Error)
		if !ok || (ce.Code != "E_HISTORY_WARMING" && ce.Code != "E_HISTORY_SETTLING") {
			t.Fatal("history refusal:", res)
		}
		select {
		case <-deadline.C:
			t.Fatal("history did not settle:", res)
		case <-tick.C:
		}
	}
}

func historyJSON(t *testing.T, res core.Result) string {
	t.Helper()
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestMailHistoryNativeGCRetainsQuotedEvidenceAndPartyPrivacy(t *testing.T) {
	dir := t.TempDir()
	f := nativeHistory(t, dir)
	sender := historyIdentity(t, f, "sender")
	recipient := historyIdentity(t, f, "recipient")
	admin := historyIdentity(t, f, "admin")
	historyOp(t, f, &core.Op{Kind: core.OpGrantRole, To: "admin", Mode: core.RoleAdmin})
	parent := historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "recipient", MsgType: core.MsgNotify, Body: "PRIVATE-AFTER-GC"})["msg_serial"].(uint64)
	historyOp(t, f, &core.Op{Kind: core.OpAckMessage, Token: recipient, MsgSerial: parent})
	historyOp(t, f, &core.Op{Kind: core.OpSweep, PurgeMail: true})
	live, err := f.eng.GetMessage(f.ctx, sender, parent)
	var absent *core.Error
	if !errors.As(err, &absent) || absent.Code != "E_NO_MESSAGE" {
		t.Fatal("setup: GC did not remove live mail:", err, live)
	}
	before := historyJSON(t, settledHistory(t, f, sender, true))
	if !strings.Contains(before, "PRIVATE-AFTER-GC") || !strings.Contains(before, `"evicted":true`) {
		t.Fatal("GC discarded history:", before)
	}
	for _, token := range []string{recipient, admin} {
		raw := historyJSON(t, settledHistory(t, f, token, true))
		if token == recipient && !strings.Contains(raw, "PRIVATE-AFTER-GC") {
			t.Fatal("recipient lost historical text:", raw)
		}
		if token == admin && strings.Contains(raw, "PRIVATE-AFTER-GC") {
			t.Fatal("admin role bypassed party history:", raw)
		}
	}
	f.stop()
	restarted := nativeHistory(t, dir)
	restarted.ids[sender] = f.ids[sender]
	after := historyJSON(t, settledHistory(t, restarted, sender, true))
	if before != after {
		t.Fatal("derived history changed across real replay:", before, after)
	}
}

func TestMailHistoryNativeOwnershipFenceBeforeRemovedLiveHeader(t *testing.T) {
	f := nativeHistory(t, t.TempDir())
	sender := historyIdentity(t, f, "sender")
	old := historyIdentity(t, f, "old")
	heir := historyIdentity(t, f, "heir")
	admin := historyIdentity(t, f, "admin")
	historyOp(t, f, &core.Op{Kind: core.OpGrantRole, To: "admin", Mode: core.RoleAdmin})
	parent := historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "old", MsgType: core.MsgNotify, Body: "PRIVATE-OWNERSHIP-MOVE"})["msg_serial"].(uint64)
	baseline := settledHistory(t, f, old, false)
	drained, ok := baseline["as_of_serial"].(uint64)
	if !ok || drained < parent {
		t.Fatal("setup: index did not drain initial message:", baseline)
	}
	release := mailhistory.HoldHistoryPublicationForTest(f.led.MailHistory())
	var once sync.Once
	unlock := func() { once.Do(release) }
	defer unlock()
	historyOp(t, f, &core.Op{Kind: core.OpSweep, DeadAgents: []string{"old"}})
	moved := historyOp(t, f, &core.Op{Kind: core.OpAdoptAgent, Token: admin, To: "old", Space: "heir"})
	if moved["messages"] != 1 {
		t.Fatal("setup: actual adoption moved no mail:", moved)
	}
	historyOp(t, f, &core.Op{Kind: core.OpAckMessage, Token: heir, MsgSerial: parent})
	historyOp(t, f, &core.Op{Kind: core.OpSweep, PurgeMail: true})
	live, err := f.eng.GetMessage(f.ctx, sender, parent)
	var absent *core.Error
	if !errors.As(err, &absent) || absent.Code != "E_NO_MESSAGE" {
		t.Fatal("setup: actual GC left live header:", err, live)
	}
	for _, bodies := range []bool{false, true} {
		at := time.Now()
		res := historyOnce(t, f, old, bodies)
		elapsed := time.Since(at)
		ce, ok := res["error"].(*core.Error)
		if !ok || ce.Code != "E_HISTORY_SETTLING" {
			t.Fatal("stale ownership must specifically settle:", res)
		}
		if elapsed > 500*time.Millisecond {
			t.Fatal("held consumer blocked query past budget:", elapsed)
		}
		if res["as_of_serial"] != drained || res["behind_by"] == uint64(0) || res["units"] != nil {
			t.Fatal("settling leaked data or lacked lag:", res)
		}
	}
	unlock()
	after := historyJSON(t, settledHistory(t, f, old, true))
	if strings.Contains(after, "PRIVATE-OWNERSHIP-MOVE") || !strings.Contains(after, `"units":[]`) {
		t.Fatal("old recipient retained moved history:", after)
	}
	current := historyJSON(t, settledHistory(t, f, heir, true))
	if !strings.Contains(current, "PRIVATE-OWNERSHIP-MOVE") {
		t.Fatal("heir lost inherited history after GC:", current)
	}
}
