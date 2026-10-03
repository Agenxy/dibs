package engine

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

type queueReplayRecord struct {
	raw []byte
	at  time.Time
}
type queueReplayLedger struct{ records []queueReplayRecord }

func (l *queueReplayLedger) Append(_ uint64, at time.Time, op *core.Op) error {
	b, err := json.Marshal(op)
	if err == nil {
		l.records = append(l.records, queueReplayRecord{raw: b, at: at})
	}
	return err
}

func TestNativeQueueObservedThroughEngineAfterReplayRestart(t *testing.T) {
	for _, contact := range []bool{false, true} {
		name := "restart"
		if contact {
			name = "busy-contact-and-restart"
		}
		t.Run(name, func(t *testing.T) { exerciseNativeQueueRestart(t, contact) })
	}
}

func exerciseNativeQueueRestart(t *testing.T, contact bool) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("DIBS_DIR", t.TempDir())
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("build fixture %v: %s", err, b)
	}
	ledger := &queueReplayLedger{}
	newEngine := func(state *core.State) (*Engine, context.Context, func()) {
		e := New(state, ledger, deadProber{})
		e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{binary, "queue", "--thread", "{thread}", "--message", "{message}"}, Cooldown: time.Millisecond}})
		ctx, cancel := context.WithCancel(context.Background())
		joined := make(chan struct{})
		go func() { e.Run(ctx); close(joined) }()
		stop := func() {
			cancel()
			<-joined
			e.wakers.mu.Lock()
			for _, timer := range e.wakers.deferred {
				timer.Stop()
			}
			e.wakers.mu.Unlock()
		}
		return e, ctx, stop
	}
	e, ctx, stop := newEngine(core.NewState("queue-test", core.DefaultLimits()))
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			stop()
		}
	})
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, err := e.Do(ctx, op)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	worker := do(&core.Op{Kind: core.OpRegister, Name: "worker", Nonce: "worker-queue-restart-nonce", AgentKind: core.KindPersistent, SessionID: contThread, Agent: &core.AgentInfo{Harness: "Codex", CWD: t.TempDir()}})
	asker := do(&core.Op{Kind: core.OpRegister, Name: "asker", Nonce: "asker-queue-restart-nonce"})
	do(&core.Op{Kind: core.OpAckBoard, Token: asker["token"].(string)})
	do(&core.Op{Kind: core.OpSweep, DeadAgents: []string{"worker"}})
	count := func() int {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(home, "pending.json"))
		if err != nil {
			t.Fatal(err)
		}
		var items []any
		if err = json.Unmarshal(b, &items); err != nil {
			t.Fatal(err)
		}
		return len(items)
	}
	send := func(id string) {
		t.Helper()
		<-time.After(20 * time.Millisecond)
		do(&core.Op{Kind: core.OpSendMessage, Token: asker["token"].(string), To: "worker", MsgType: core.MsgQuestion, Body: "queue test", OpID: id})
		deadline := time.After(3 * time.Second)
		for {
			e.wakers.mu.Lock()
			busy := e.wakers.running["worker"]
			e.wakers.mu.Unlock()
			_, err := os.Stat(filepath.Join(home, "pending.json"))
			if !busy && err == nil {
				return
			}
			select {
			case <-deadline:
				t.Fatal("setup: command never settled")
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	send("first")
	if count() != 1 {
		t.Fatal("first wake did not queue exactly one")
	}
	if contact {
		do(&core.Op{Kind: core.OpAckBoard, Token: worker["token"].(string)})
		do(&core.Op{Kind: core.OpSweep, DeadAgents: []string{"worker"}})
		send("busy-contact")
		if count() != 1 {
			t.Fatal("MCP traffic queued a second pending wake")
		}
	}
	stop()
	stopped = true
	state := core.NewState("queue-test", core.DefaultLimits())
	for _, r := range ledger.records {
		var op core.Op
		if err := json.Unmarshal(r.raw, &op); err != nil {
			t.Fatal(err)
		}
		if _, _, err := state.Apply(&op, r.at); err != nil {
			t.Fatal(err)
		}
	}
	e, ctx, stop = newEngine(state)
	stopped = false
	send("after-restart")
	if count() != 1 {
		t.Fatal("replayed daemon duplicated a pending native wake")
	}
	if err := os.WriteFile(filepath.Join(home, "pending.json"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	// No inferred receipt: the authoritative empty native queue must re-arm.
	send("after-consumption")
	if count() != 1 {
		t.Fatal("observed empty queue did not re-arm")
	}
	b, err := os.ReadFile(filepath.Join(home, "methods"))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("real command route never observed the app queue")
	}
}
