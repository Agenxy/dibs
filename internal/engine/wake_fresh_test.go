// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestSocketWakeDropsMailAcknowledgedAfterPlanning(t *testing.T) {
	sock, sid := listeningSession(t)
	st := core.NewState("fresh", core.DefaultLimits())
	now := time.Now()
	for _, id := range []string{"sender", "sleeper"} {
		if _, _, err := st.Apply(&core.Op{
			Kind: core.OpRegister, Name: id, NewToken: "tok-" + id,
			SessionID: sid, Agent: &core.AgentInfo{Harness: "Claude Code"},
		}, now); err != nil {
			t.Fatal("register setup:", err)
		}
	}
	res, _, err := st.Apply(&core.Op{
		Kind: core.OpSendMessage, Token: "tok-sender", To: "sleeper",
		MsgType: core.MsgHandoff, Body: "stale-digest-marker",
	}, now)
	if err != nil {
		t.Fatal("send setup:", err)
	}
	serial := res["msg_serial"].(uint64)
	e := New(st, &memLedger{}, deadProber{})
	e.primePeerSessions()
	plan, ok := e.wakeFor(st.Agents["sleeper"], core.MsgHandoff, core.Event{
		Type: "message.sent", To: "sleeper", Agent: "sender",
		Data: map[string]any{"msg_type": core.MsgHandoff, "from": "sender"},
	})
	if !ok || !strings.Contains(plan.notice, "stale-digest-marker") {
		t.Fatalf("setup did not compose the mail into a socket plan: %+v", plan)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.Run(ctx)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckMessage, Token: "tok-sleeper", MsgSerial: serial}); err != nil {
		t.Fatal("ack setup:", err)
	}
	wire := make(chan string, 1)
	go func() {
		c, err := sock.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(time.Second))
		b, _ := io.ReadAll(c)
		wire <- string(b)
	}()
	e.runWakeAndReport(plan, "sleeper")
	select {
	case got := <-wire:
		t.Fatalf("acknowledged mail was sent from the old plan: %s", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestCommandWakeDropsMailAcknowledgedAfterPlanning(t *testing.T) {
	if _, err := os.Stat("/usr/bin/touch"); err != nil {
		t.Skip("no touch fixture on this platform")
	}
	st := core.NewState("fresh-command", core.DefaultLimits())
	const session = "7c3f0a11-2b44-4d90-9e57-1f2a3b4c5d6e"
	dir := t.TempDir()
	for _, id := range []string{"sender", "worker"} {
		if _, _, err := st.Apply(&core.Op{
			Kind: core.OpRegister, Name: id, NewToken: "tok-" + id,
			SessionID: session, Agent: &core.AgentInfo{Harness: "Codex", CWD: dir},
		}, time.Now()); err != nil {
			t.Fatal("setup:", err)
		}
	}
	res, _, err := st.Apply(&core.Op{
		Kind: core.OpSendMessage, Token: "tok-sender", To: "worker",
		MsgType: core.MsgHandoff, Body: "stale-command-marker",
	}, time.Now())
	if err != nil {
		t.Fatal("send setup:", err)
	}
	e := New(st, &memLedger{}, deadProber{})
	marker := filepath.Join(dir, "command-ran")
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"/usr/bin/touch", marker, "{thread}"}}})
	plan, ok := e.wakeFor(st.Agents["worker"], core.MsgHandoff, core.Event{Type: "message.sent", To: "worker", Agent: "sender"})
	if !ok || len(plan.argv) == 0 {
		t.Fatal("setup: no command plan")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stopWakeTimersOnCleanup(t, e)
	go e.Run(ctx)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckMessage, Token: "tok-worker", MsgSerial: res["msg_serial"].(uint64)}); err != nil {
		t.Fatal("ack setup:", err)
	}
	if !e.runWakeAndReport(plan, "worker") {
		t.Fatal("empty plan was reported as a failed wake")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("an acknowledged message still ran its wake command: %v", err)
	}
	if !e.wakeStamp("worker").IsZero() {
		t.Fatal("empty command plan spent its cooldown")
	}
	e.wakers.mu.Lock()
	_, started := e.wakers.dibsTurn["worker"]
	e.wakers.mu.Unlock()
	if started {
		t.Fatal("empty command plan recorded a turn it never started")
	}
}
