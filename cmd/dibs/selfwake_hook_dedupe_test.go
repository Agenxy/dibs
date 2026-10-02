package main

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/mcp"
)

// Real listen -> additive capability -> refresh -> session socket -> receipt.
// A test that calls SocketOfferFor by hand cannot prove the bridge uses it.
func TestSelfWakeAndStopSharePresentationAfterActualTurnActivity(t *testing.T) {
	sock := sockPath(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	resetWakeStreams()
	t.Cleanup(resetWakeStreams)
	t.Cleanup(func() { recordWakePending(false, "") })
	lines := listenLines(t, sock)
	st := core.NewState("receipt", core.DefaultLimits())
	for _, id := range []string{"sender", "worker"} {
		session := ""
		if id == "worker" {
			session = streamSession()
		}
		if _, _, err := st.Apply(&core.Op{
			Kind: core.OpRegister, Name: id,
			NewToken: "tok-" + id, SessionID: session,
		}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	key, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "receipt", key)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	engineCtx, stopEngine := context.WithCancel(context.Background())
	eng := engine.New(st, led, nil)
	go eng.Run(engineCtx)
	if _, err := eng.HookPoll(ctx, streamSession(), "Stop", "", false, false); err != nil {
		t.Fatal("setup: idle session", err)
	}
	srv := httptest.NewServer(mcp.New(eng))
	t.Cleanup(func() { cancel(); srv.Close(); stopEngine(); _ = led.Close() })
	iw := &inboxWatcher{cooldown: time.Hour}
	iw.startFor(ctx, srv.Client(), srv.URL, "test-secret", "worker", "tok-worker", 0)
	for deadline := time.Now().Add(time.Second); ; {
		live, _ := eng.SelfWaking("worker")
		if live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("setup: no self-wake subscription")
		}
		time.Sleep(time.Millisecond)
	}
	mail, err := eng.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: "tok-sender", To: "worker",
		MsgType: core.MsgQuestion, Body: "selfwake-dedupe-marker",
	})
	if err != nil {
		t.Fatal(err)
	}
	wire := collect(lines, 2, time.Second)
	if len(wire) != 2 || !strings.Contains(strings.Join(wire, "\n"), "selfwake-dedupe-marker") {
		t.Fatalf("setup: no actual socket delivery: %v", wire)
	}
	// A real authenticated read is turn evidence, but does not read mail or
	// deliver a hook digest. Even if it races the receipt, both facts must win.
	if _, err := eng.SpaceRead(ctx, "tok-worker", "missing-space", 1); err == nil {
		t.Fatal("setup: space unexpectedly exists")
	}
	// finish runs synchronously inside send, before wake returns and the
	// subscription cursor advances. That cursor is the production barrier.
	for deadline := time.Now().Add(time.Second); iw.sinceOf("tok-worker") < mail["msg_serial"].(uint64); {
		if time.Now().After(deadline) {
			t.Fatal("setup: bridge never settled its write")
		}
		time.Sleep(time.Millisecond)
	}
	got, err := eng.HookPoll(ctx, streamSession(), "Stop", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got["reason"] != nil {
		t.Fatalf("the self-waker delivered this question and the model took a turn; Stop repeated it: %v", got)
	}
}
