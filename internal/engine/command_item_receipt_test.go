// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestCommandExitReconsidersOnlyUndeliveredItems(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		name := "same question"
		if fresh {
			name = "new question"
		}
		t.Run(name, func(t *testing.T) {
			(&fakeApp{holds: true}).install(t)
			e := New(core.NewState("command-items", core.DefaultLimits()), &memLedger{}, deadProber{})
			out, release := filepath.Join(t.TempDir(), "deliveries"), filepath.Join(t.TempDir(), "release")
			e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{os.Args[0], "-test.run=^TestCommandItemReceiptHelper$", "--", out, release}}})
			ctx, cancel := context.WithCancel(context.Background())
			joined := make(chan struct{})
			go func() { e.Run(ctx); close(joined) }()
			t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600); waitWakeDone(t, e, "worker"); cancel(); <-joined })
			do := func(op *core.Op) core.Result {
				t.Helper()
				r, err := e.Do(ctx, op)
				if err != nil {
					t.Fatal("setup ingress:", err)
				}
				return r
			}
			do(&core.Op{Kind: core.OpRegister, Name: "worker", SessionID: "01a0696b-8446-7821-a992-9dc7f6a43a27", Agent: &core.AgentInfo{Harness: "Codex"}})
			sender := do(&core.Op{Kind: core.OpRegister, Name: "sender"})["token"].(string)
			send := func() core.Result {
				return do(&core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgQuestion, Body: "real question"})
			}
			first := send()
			serial, ok := first["msg_serial"].(uint64)
			if !ok {
				t.Fatalf("setup mail was not accepted: %v", first)
			}
			await := func(label string, ready func(core.Result, []byte) bool) {
				t.Helper()
				until := time.Now().Add(5 * time.Second)
				var last core.Result
				var b []byte
				for time.Now().Before(until) {
					r, err := e.query(ctx, func() core.Result {
						e.wakers.mu.Lock()
						defer e.wakers.mu.Unlock()
						return core.Result{"running": e.wakers.running["worker"], "arrived": e.wakers.arrived["worker"], "batched": e.wakeBursts["worker"] != nil, "mail": len(e.state.Inbox("worker"))}
					})
					if err != nil {
						t.Fatal(err)
					}
					b, err = os.ReadFile(out)
					if err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					last = r
					if ready(r, b) {
						return
					}
					<-time.After(5 * time.Millisecond)
				}
				t.Fatalf("%s: receipts=%q state=%v", label, b, last)
			}
			await("first actual command is held", func(r core.Result, b []byte) bool { return string(b) == "x" && r["running"] == true })
			if fresh {
				send()
			} else {
				if _, err := e.query(ctx, func() core.Result {
					e.publish([]core.Event{{Serial: serial, Type: "message.sent", Agent: "sender", To: "worker", Data: map[string]any{"msg_type": core.MsgQuestion, "from": "sender", "attachments": 0}}})
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			await("arrival marked while original command runs", func(r core.Result, _ []byte) bool { return r["arrived"] == true && r["batched"] == false })
			if err := os.WriteFile(release, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			await("all deliveries settled", func(r core.Result, _ []byte) bool { return r["running"] == false && r["batched"] == false })
			b, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			want := "x"
			if fresh {
				want = "xx"
			}
			if string(b) != want {
				t.Fatalf("command delivered original item twice or suppressed fresh mail: receipts=%q want=%q", b, want)
			}
			r, err := e.query(ctx, func() core.Result { return core.Result{"unread": len(e.state.Inbox("worker"))} })
			wantUnread := 1
			if fresh {
				wantUnread = 2
			}
			if err != nil || r["unread"] != wantUnread {
				t.Fatalf("transport consumed unread coordination: %v %v", r, err)
			}
		})
	}
}

func TestCommandItemReceiptHelper(t *testing.T) {
	for i, arg := range os.Args {
		if arg != "--" || i+2 >= len(os.Args) {
			continue
		}
		f, err := os.OpenFile(os.Args[i+1], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString("x")
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("receipt: %v %v", err, closeErr)
		}
		until := time.Now().Add(10 * time.Second)
		for time.Now().Before(until) {
			if _, err := os.Stat(os.Args[i+2]); err == nil {
				return
			}
			<-time.After(5 * time.Millisecond)
		}
		t.Fatal("fixture release was not written")
	}
}
