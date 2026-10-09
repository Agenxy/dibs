// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// A local receiving incarnation can remain attached to an identity after its
// stated host changes. The remote command factory must carry the same fence.
// A prompt hook releases the independent queue hold without delivering mail,
// so the repeated event measures the per-item receipt rather than that hold.
func TestRemoteCommandDeduplicatesAfterObservedLocalAppIncarnation(t *testing.T) {
	(&fakeApp{holds: true}).install(t)
	t.Setenv("DIBS_NOTIFY", "off")
	e := New(core.NewState("command-host-move", core.DefaultLimits()), &memLedger{}, deadProber{})
	e.SetHostID("local-host")
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() { e.Run(ctx); close(joined) }()
	out, release := filepath.Join(t.TempDir(), "deliveries"), filepath.Join(t.TempDir(), "release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600); waitWakeDone(t, e, "worker"); cancel(); <-joined })
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, err := e.Do(ctx, op)
		if err != nil {
			t.Fatal("setup ingress:", err)
		}
		return r
	}
	const thread = "01a0696b-8446-7821-a992-9dc7f6a43a29"
	worker := do(&core.Op{Kind: core.OpRegister, Name: "worker", SessionID: thread, Agent: &core.AgentInfo{Harness: "Codex", Surface: harnessenv.ChatGPTApp, HostID: "local-host"}})["token"].(string)
	sender := do(&core.Op{Kind: core.OpRegister, Name: "sender"})["token"].(string)
	mail := do(&core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgQuestion, Body: "one original remote question"})
	serial, ok := mail["msg_serial"].(uint64)
	if !ok {
		t.Fatalf("setup mail not accepted: %v", mail)
	}
	if err := e.AppReconnected(ctx, "local-host", harnessenv.AppIncarnation{PID: 4242, Start: "measured-before-host-move"}); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(thread))
	if _, err := os.Stat(filepath.Join(os.Getenv("DIBS_DIR"), "queued-wakes", hex.EncodeToString(digest[:])+".json.reconnect")); err != nil {
		t.Fatal("setup receiving incarnation was not observed:", err)
	}
	do(&core.Op{Kind: core.OpUpdate, Token: worker, Agent: &core.AgentInfo{HostID: "remote-host"}})
	r, err := e.query(ctx, func() core.Result { return core.Result{"host": e.remoteHostOf(e.state.Agents["worker"])} })
	if err != nil || r["host"] != "remote-host" {
		t.Fatalf("setup host move did not apply: %v %v", r, err)
	}
	requests, detach, err := e.AttachHostBridge("remote-host", []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	bridgeDone := make(chan struct{})
	go func() {
		defer close(bridgeDone)
		for {
			select {
			case <-ctx.Done():
				return
			case request, open := <-requests:
				if !open {
					return
				}
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommandItemReceiptHelper$", "--", out, release)
				runErr := cmd.Run()
				e.ReportWakeResult(WakeResult{ID: request.ID, Host: "remote-host", OK: runErr == nil})
			}
		}
	}()
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0o600)
		waitWakeDone(t, e, "worker")
		cancel()
		detach()
		<-bridgeDone
	})
	publish := func() {
		t.Helper()
		if _, err := e.query(ctx, func() core.Result {
			e.publish([]core.Event{{Serial: serial, Type: "message.sent", Agent: "sender", To: "worker", Data: map[string]any{"msg_type": core.MsgQuestion, "from": "sender", "attachments": 0}}})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	await := func(label string, ready func(core.Result, []byte) bool) {
		t.Helper()
		until := time.Now().Add(5 * time.Second)
		var b []byte
		var last core.Result
		for time.Now().Before(until) {
			r, err := e.query(ctx, func() core.Result {
				e.wakers.mu.Lock()
				defer e.wakers.mu.Unlock()
				return core.Result{"running": e.wakers.running["worker"], "arrived": e.wakers.arrived["worker"], "batched": e.wakeBursts["worker"] != nil}
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
	publish()
	await("first remote command held", func(r core.Result, b []byte) bool { return string(b) == "x" && r["running"] == true })
	publish()
	await("same item reached remote in-flight reconsideration", func(r core.Result, _ []byte) bool { return r["arrived"] == true && r["batched"] == false })
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	await("remote deliveries settled", func(r core.Result, _ []byte) bool { return r["running"] == false && r["batched"] == false })
	// Codex admission has its own queue hold. The real receiving prompt hook
	// releases it; only the original-item receipt may suppress this later offer.
	result, err := e.HookPollFrom(ctx, thread, "UserPromptSubmit", "", "remote-host", false, false)
	if err != nil || result["agent"] != "worker" || deliveredSomething(result) {
		t.Fatalf("setup remote receiving hook did not resolve: %v %v", result, err)
	}
	publish()
	await("post-start reconsideration settled", func(r core.Result, _ []byte) bool { return r["running"] == false && r["batched"] == false })

	r, err = e.query(ctx, func() core.Result { return core.Result{"unread": len(e.state.Inbox("worker"))} })
	if err != nil || r["unread"] != 1 {
		t.Fatalf("remote transport consumed unread coordination: %v %v", r, err)
	}
	b, err := os.ReadFile(out)
	if err != nil || string(b) != "x" {
		t.Fatalf("same item duplicated on remote bridge: %q %v", b, err)
	}
}
