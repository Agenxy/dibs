package ledger

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/mailhistory"
)

func historyBootFixture(t *testing.T) (*Ledger, *core.State) {
	t.Helper()
	l, _ := newLedger(t)
	limits := core.DefaultLimits()
	limits.MaxMailboxDepth = 7 // the shadow must receive these actual limits
	limits.ConsumedRetention = 2 * time.Hour
	st := core.NewState("test", limits)
	apply(t, st, l, &core.Op{Kind: core.OpRegister, Name: "sender", NewToken: "sender-token", Nonce: "sender-nonce", AgentKind: core.KindPersistent}, t0)
	apply(t, st, l, &core.Op{Kind: core.OpRegister, Name: "worker", NewToken: "worker-token", Nonce: "worker-nonce", AgentKind: core.KindPersistent}, t0)
	apply(t, st, l, &core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "worker", MsgType: core.MsgNotify, Body: "original ledger body"}, t0)
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

func TestHistoryBootstrapOwnsPrivateStateAndKeepsLiveCommitAfterS0(t *testing.T) {
	l, live := historyBootFixture(t)
	expected, err := historyStateHash(live)
	if err != nil {
		t.Fatal("setup: canonical board hash", err)
	}
	// Change only the live objects before the engine starts. A retained State
	// pointer OR a shallow State copy would leak both changes into the shadow.
	live.Messages[3].Body = "private live mutation must not enter the reader"
	live.Agents["worker"].Description = "private live object"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := engine.New(live, l, nil)
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	if _, _, err := eng.SubscribeInfo(ctx, ""); err != nil {
		t.Fatal("setup: real writer", err)
	}
	res, err := eng.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "worker", MsgType: core.MsgNotify, Body: "live after S0"})
	if err != nil || res["error"] != nil {
		t.Fatal("setup: real live commit", err, res)
	}
	if m := l.MailHistory().Measurement(); m.Started || m.Ready || m.CapturedUnits == 0 || m.QueuedBytes == 0 {
		t.Fatal("live op before serving was not captured behind boot prefix", m)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	srv.Listener = l.MailHistory().ServingListener(ctx, srv.Listener)
	srv.Start()
	defer srv.Close()
	response, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal("setup: actual HTTP Accept", err)
	}
	_ = response.Body.Close()
	m := awaitBootHistory(t, l.MailHistory())
	if m.BootSerial != 3 || m.BootHash != expected || m.Conversations != 2 || m.QueuedBytes != 0 || m.BuiltSerial <= 3 {
		t.Fatalf("private S0 fold or ordered live suffix disagrees: %+v expected hash=%x", m, expected)
	}
}

func awaitBootHistory(t *testing.T, index *mailhistory.Index) mailhistory.Measurement {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		m := index.Measurement()
		if m.Failed {
			t.Fatal("background history failed")
		}
		if m.Ready {
			return m
		}
		select {
		case <-timeout.C:
			t.Fatal("background history did not catch up")
		case <-tick.C:
		}
	}
}
