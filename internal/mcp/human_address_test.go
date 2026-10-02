package mcp

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
)

// Exercise real send ingress, not a pre-created HumanAgent or resolved To.
func TestSendToHumanCreatesAnOSOwnedMailboxAndNotifiesTheRelay(t *testing.T) {
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "human-address", box)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(core.NewState("human-address", core.DefaultLimits()), led, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	srv := httptest.NewServer(New(eng))
	t.Cleanup(func() { srv.Close(); cancel(); <-done; _ = led.Close() })
	feed, detach := eng.AttachHumanRelay()
	t.Cleanup(detach)
	registered := toolCall(t, srv, "register", map[string]any{"name": "requester", "nonce": "human-address-requester-fixture"})
	token, ok := registered["token"].(string)
	if !ok {
		t.Fatalf("register setup: %v", registered)
	}
	toolCall(t, srv, "check_in", map[string]any{"token": token})
	if _, err := eng.Board(ctx); err != nil {
		t.Fatal(err)
	}
	if got := eng.HumanIdentity(); got != "" {
		t.Fatalf("observing registered a human: %q", got)
	}
	for _, args := range []map[string]any{
		{"token": "bad-token", "to": "human", "type": "request", "body": "approve?"},
		{"token": token, "to": "human", "type": "request", "body": "approve?", "choices": []string{"invalid on a request"}},
	} {
		r := toolCall(t, srv, "send", args)
		if r["__is_error"] != true {
			t.Fatalf("invalid send succeeded: %v", r)
		}
		if got := eng.HumanIdentity(); got != "" {
			t.Fatalf("refused send minted human %q", got)
		}
	}
	sent := toolCall(t, srv, "send", map[string]any{
		"token": token, "to": "human", "type": "request", "body": "approve concrete artifact", "op_id": "human-first-send",
	})
	if sent["__is_error"] == true {
		t.Fatalf("first authenticated send cannot address the person: %v", sent)
	}
	serial, ok := sent["msg_serial"].(float64)
	if !ok {
		t.Fatalf("send returned no message: %v", sent)
	}
	human := eng.HumanIdentity()
	if human == "" {
		t.Fatalf("role was not resolved to OS-owned identity: %q", human)
	}
	select {
	case n := <-feed:
		if n.Serial != uint64(serial) || n.Type != core.MsgRequest || n.Body != "approve concrete artifact" {
			t.Fatalf("wrong human notification: %+v", n)
		}
	case <-time.After(time.Second):
		t.Fatal("send stored mail but did not reach the human notification relay")
	}
	retry := toolCall(t, srv, "send", map[string]any{
		"token": token, "to": "human", "type": "request", "body": "approve concrete artifact", "op_id": "human-first-send",
	})
	if retry["msg_serial"] != serial {
		t.Fatalf("retry duplicated the approval request: %v", retry)
	}
	// Stop the writer before inspecting the independent replay.
	srv.Close()
	cancel()
	<-done
	replayed := core.NewState("human-address", core.DefaultLimits())
	if _, err := led.Replay(replayed); err != nil {
		t.Fatal(err)
	}
	l := replayed.Agents[human]
	if l == nil || l.PID != 0 || l.Kind != core.KindPersistent {
		t.Fatalf("human identity did not replay without a process: %+v", l)
	}
	if !strings.HasPrefix(l.Nonce, "human:") || l.Agent == nil || l.Agent.Harness != "dibs web" {
		t.Fatalf("sender-chosen identity was used as the human: %+v", l)
	}
	m := replayed.Messages[uint64(serial)]
	if m == nil || m.To != human {
		t.Fatalf("ledger retained the role instead of concrete recipient: %+v", m)
	}
	if len(replayed.Agents) != 2 || len(replayed.Messages) != 1 {
		t.Fatalf("replay duplicated identity/mail: agents=%d mail=%d", len(replayed.Agents), len(replayed.Messages))
	}
}
