package ledger

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func historyBootFixture(t *testing.T) (*Ledger, *core.State) {
	t.Helper()
	return historyBootFixtureWith(t, "original ledger body")
}

func historyBootFixtureWith(t *testing.T, body string) (*Ledger, *core.State) {
	t.Helper()
	l, _ := newLedger(t)
	limits := core.DefaultLimits()
	limits.MaxMailboxDepth = 7 // the shadow must receive these actual limits
	limits.ConsumedRetention = 2 * time.Hour
	limits.MaxBodyBytes = max(limits.MaxBodyBytes, len(body))
	st := core.NewState("test", limits)
	apply(t, st, l, &core.Op{Kind: core.OpRegister, Name: "sender", NewToken: "sender-token", Nonce: "sender-nonce", AgentKind: core.KindPersistent}, t0)
	apply(t, st, l, &core.Op{Kind: core.OpRegister, Name: "worker", NewToken: "worker-token", Nonce: "worker-nonce", AgentKind: core.KindPersistent}, t0)
	apply(t, st, l, &core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "worker", MsgType: core.MsgNotify, Body: body}, t0)
	live := core.NewState("test", limits)
	if n, err := l.Replay(live); err != nil || n != 3 || live.Serial != 3 {
		t.Fatal("setup: actual boot replay", n, err)
	}
	return l, live
}

// This enters through Open + Replay, not a builder flag or a mock observer.
// The former production path captured every boot transition and fails here.
func TestHistoryBootReplayDoesNoPerRecordProjection(t *testing.T) {
	l, _ := historyBootFixture(t)
	m := l.MailHistory().Measurement()
	if m.Started || m.Ready || m.Failed || m.Records != 3 || m.CapturedUnits != 0 || m.QueuedBytes != 0 || m.Units != 0 || m.Blocks != 0 {
		t.Fatalf("boot captured or encoded history before serving: %+v", m)
	}
}
