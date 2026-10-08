// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/humanask"
	"github.com/agenxy/dibs/internal/ledger"
)

func restartableHumanServer(t *testing.T, dir string) (*httptest.Server, *engine.Engine, func()) {
	t.Helper()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "fixture", box)
	if err != nil {
		t.Fatal(err)
	}
	st := core.NewState("fixture", core.DefaultLimits())
	if _, err = journal.Replay(st); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, journal, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	server := New(eng)
	srv := httptest.NewServer(server)
	var once sync.Once
	closeFixture := func() {
		once.Do(func() {
			srv.Close()
			cancel()
			<-done
			if err := journal.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(closeFixture)
	return srv, eng, closeFixture
}

func TestHumanConcreteMailboxAfterRestartHasNoWakeFailure(t *testing.T) {
	dir := t.TempDir()
	srv, eng, stop := restartableHumanServer(t, dir)
	token := toolCall(t, srv, "register", map[string]any{"name": "sender", "nonce": "sender-stable"})["token"].(string)
	human, _, err := eng.HumanAgent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stop()
	srv, eng, _ = restartableHumanServer(t, dir)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	eng.SetHumanNotifier(desktopFixture{available: true, ask: func(humanask.Message) (humanask.Answer, error) { <-release; return humanask.Answer{}, nil }})
	r := toolCall(t, srv, "send", map[string]any{"token": token, "to": human, "type": "notify", "body": "desktop after restart"})
	if r["human_route"] != "desktop" {
		t.Fatalf("setup: %v", r)
	}
	if note, _ := r["note"].(string); strings.Contains(note, "nothing") || strings.Contains(note, "starts that agent") {
		t.Fatalf("desktop handoff called unwakeable: %v", r)
	}
}

// The pre-restart human token remains valid. Replaying its reserved identity
// must preserve its authority without requiring another unlock/cache fill.
func TestHumanAdoptionAuthorityAfterRealRestart(t *testing.T) {
	for _, route := range []string{"direct", "request"} {
		t.Run(route, func(t *testing.T) {
			dir := t.TempDir()
			srv, eng, stop := restartableHumanServer(t, dir)
			sender := toolCall(t, srv, "register", map[string]any{"name": "sender", "nonce": "sender-stable"})["token"].(string)
			toolCall(t, srv, "check_in", map[string]any{"token": sender})
			lost := toolCall(t, srv, "register", map[string]any{"name": "stranded", "session_id": "stranded-session"})
			lostID, ok := lost["agent_id"].(string)
			if !ok {
				t.Fatalf("setup lost: %v", lost)
			}
			mail := toolCall(t, srv, "send", map[string]any{"token": sender, "to": lostID, "type": "notify", "body": "private abandoned mail"})
			serial, ok := mail["msg_serial"].(float64)
			if !ok {
				t.Fatalf("setup mail: %v", mail)
			}
			if _, err := eng.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{lostID}}); err != nil {
				t.Fatal("setup sweep:", err)
			}
			human, humanToken, err := eng.HumanAgent(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var request float64
			if route == "request" {
				r := toolCall(t, srv, "send", map[string]any{"token": humanToken, "to": human, "type": "request", "adopt": lostID, "body": "recover my abandoned mailbox"})
				request, ok = r["msg_serial"].(float64)
				if !ok {
					t.Fatalf("setup request: %v", r)
				}
			}
			stop()
			srv, _, _ = restartableHumanServer(t, dir)
			// Denials remain denials for an ordinary token, after the same replay.
			r := toolCall(t, srv, "adopt_agent", map[string]any{"token": sender, "agent": lostID})
			if r["__is_error"] != true {
				t.Fatalf("ordinary member got adoption authority: %v", r)
			}
			if route == "direct" {
				r = toolCall(t, srv, "adopt_agent", map[string]any{"token": humanToken, "agent": lostID})
			} else {
				r = toolCall(t, srv, "respond", map[string]any{"token": humanToken, "msg_serial": request, "disposition": "approve"})
			}
			if r["__is_error"] == true {
				t.Fatalf("human authority lost after replay: %v", r)
			}
			read := toolCall(t, srv, "read_mail", map[string]any{"token": humanToken, "msg_serial": serial})
			m, ok := read["message"].(map[string]any)
			if !ok || m["to"] != human {
				t.Fatalf("adoption reported success without moving mail: %v", read)
			}
		})
	}
}

// Grant approval already used replay identity; retain that authority boundary
// while repairing its sibling adoption paths.
func TestHumanGrantApprovalRemainsScopedAfterRestart(t *testing.T) {
	dir := t.TempDir()
	srv, eng, stop := restartableHumanServer(t, dir)
	member := toolCall(t, srv, "register", map[string]any{"name": "grant-requester"})["token"].(string)
	stranger := toolCall(t, srv, "register", map[string]any{"name": "other-member"})["token"].(string)
	human, humanToken, err := eng.HumanAgent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sent := toolCall(t, srv, "send", map[string]any{"token": member, "to": human, "type": "request", "grant": "coordinator", "body": "coordinate this board"})
	serial, ok := sent["msg_serial"].(float64)
	if !ok {
		t.Fatalf("setup grant: %v", sent)
	}
	stop()
	srv, _, _ = restartableHumanServer(t, dir)
	r := toolCall(t, srv, "respond", map[string]any{"token": stranger, "msg_serial": serial, "disposition": "approve"})
	if r["__is_error"] != true {
		t.Fatalf("ordinary member approved another's human grant: %v", r)
	}
	r = toolCall(t, srv, "respond", map[string]any{"token": humanToken, "msg_serial": serial, "disposition": "approve"})
	if r["__is_error"] == true {
		t.Fatalf("human grant authority lost: %v", r)
	}
	r = toolCall(t, srv, "all_mail", map[string]any{"token": member, "census": true})
	if r["__is_error"] == true {
		t.Fatalf("approval reported success without granting coordinator: %v", r)
	}
	r = toolCall(t, srv, "all_mail", map[string]any{"token": stranger, "census": true})
	if r["__is_error"] != true {
		t.Fatalf("unrelated member gained coordinator authority: %v", r)
	}
}
