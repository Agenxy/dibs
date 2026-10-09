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

// Actual writer ingress and actual child command exits. No Stop or elapsed
// cooldown is allowed to rescue the second fresh message.
func TestSuccessfulCommandCannotSuppressLaterMail(t *testing.T) {
	e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	out := filepath.Join(t.TempDir(), "deliveries")
	e.SetWakeCommands(map[string]WakeCommand{"codex": {
		Argv: []string{os.Args[0], "-test.run=^TestSuccessfulWakeCommandHelper$", "--", out}, Cooldown: time.Hour,
	}})
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() { e.Run(ctx); close(joined) }()
	t.Cleanup(func() { cancel(); <-joined })
	do := func(op *core.Op) core.Result {
		r, err := e.Do(ctx, op)
		if err != nil {
			t.Fatal("setup ingress:", err)
		}
		return r
	}
	do(&core.Op{Kind: core.OpRegister, Name: "worker", SessionID: "01a0696b-8446-7821-a992-9dc7f6a43a25", Agent: &core.AgentInfo{Harness: "Codex"}})
	sender := do(&core.Op{Kind: core.OpRegister, Name: "sender"})["token"].(string)
	for attempt := 1; attempt <= 2; attempt++ {
		do(&core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgQuestion, Body: "fresh mail"})
		deadline := time.After(4 * time.Second)
		ticker := time.NewTicker(10 * time.Millisecond)
		for {
			r, err := e.query(ctx, func() core.Result {
				e.wakers.mu.Lock()
				defer e.wakers.mu.Unlock()
				return core.Result{"running": e.wakers.running["worker"]}
			})
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(out)
			if err == nil && len(b) == attempt && r["running"] == false {
				break
			}
			select {
			case <-ticker.C:
			case <-deadline:
				ticker.Stop()
				t.Fatalf("successful command suppressed fresh mail %d: writes=%q running=%v", attempt, b, r)
			}
		}
		ticker.Stop()
	}
}

func TestSuccessfulWakeCommandHelper(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			f, err := os.OpenFile(os.Args[i+1], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.WriteString("x"); err != nil {
				t.Fatal(err)
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
}
