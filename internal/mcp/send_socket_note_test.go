// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/peerwake"
)

// The same files a live harness publishes, with this test process's real PID.
// No installed service, mocked route decision, or hand-set lifecycle flag.
func sendNoteSocket(t *testing.T) (string, <-chan string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the harness session discovery path uses Unix process evidence")
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal("setup: sessions:", err)
	}
	pid := strconv.Itoa(os.Getpid())
	session := "e2c03160-6000-4000-8000-000000000001"
	key := []byte(`{"peerToken":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	if err := os.WriteFile(filepath.Join(dir, pid+"."+strings.Repeat("b", 64)+".key"), key, 0o600); err != nil {
		t.Fatal("setup: peer key:", err)
	}
	side, err := json.Marshal(map[string]any{"pid": os.Getpid(), "sessionId": session})
	if err != nil {
		t.Fatal("setup: sidecar:", err)
	}
	if err = os.WriteFile(filepath.Join(dir, pid+".json"), side, 0o600); err != nil {
		t.Fatal("setup: sidecar:", err)
	}
	run, err := os.MkdirTemp("/tmp", "send-note-")
	if err != nil {
		t.Fatal("setup: short socket directory:", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(run) })
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", run)
	if err = os.MkdirAll(filepath.Join(run, "cc-socks"), 0o700); err != nil {
		t.Fatal("setup: socket directory:", err)
	}
	ln, err := net.Listen("unix", filepath.Join(run, "cc-socks", pid+".sock"))
	if err != nil {
		t.Fatal("setup: real listener:", err)
	}
	found, err := peerwake.Discover(home, peerwake.Alive)
	if err != nil || len(found) != 1 || found[0].SessionID != session {
		_ = ln.Close()
		t.Fatalf("setup: real process/socket was not discovered: %v %v", found, err)
	}
	wire := make(chan string, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			body, _ := io.ReadAll(conn)
			_ = conn.Close()
			wire <- string(body)
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); <-done })
	return session, wire
}

func TestSendSocketNoteReportsDecisionThroughMCP(t *testing.T) {
	for _, mode := range []string{"idle-notify", "idle-human-notify", "idle-question", "busy-notify", "busy-question"} {
		t.Run(mode, func(t *testing.T) {
			session, wire := sendNoteSocket(t)
			dir := t.TempDir()
			srv, eng, _ := restartableQueueServer(t, dir)
			call := func(name string, args map[string]any) map[string]any {
				t.Helper()
				return sendNoteCall(t, srv, name, args)
			}
			worker := call("register", map[string]any{"name": "worker", "session_id": session, "harness": "Claude Code"})
			sender := call("register", map[string]any{"name": "sender", "session_id": "send-note-sender"})
			call("check_in", map[string]any{"token": worker["token"]})
			call("check_in", map[string]any{"token": sender["token"]})
			call("hook_poll", map[string]any{"session_id": session, "event": "Stop", "stop_hook_active": true})
			kind := "question"
			if strings.Contains(mode, "notify") {
				kind = "notify"
			}
			busy := strings.HasPrefix(mode, "busy-")
			if busy {
				call("hook_poll", map[string]any{"session_id": session, "event": "PreToolUse"})
			}
			sendToken := sender["token"]
			if mode == "idle-human-notify" {
				_, humanToken, err := eng.HumanAgent(context.Background())
				if err != nil {
					t.Fatal("setup: human identity:", err)
				}
				sendToken = humanToken
			}
			marker := "send-note-" + mode
			sent := call("send", map[string]any{"token": sendToken, "to": "worker", "type": kind, "body": marker})
			note, _ := sent["note"].(string)
			if !strings.Contains(note, "best-effort notice") ||
				(!strings.Contains(note, "being handed") && !strings.Contains(note, "already written")) {
				t.Errorf("idle authored mail lost the wake-attempt wording: %v", sent)
			}
			select {
			case frame := <-wire:
				if !strings.Contains(frame, marker) || !strings.Contains(frame, `"type":"auth"`) {
					t.Fatalf("wrong actual socket frame: %q", frame)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("actionable mail did not produce an actual socket frame")
			}
			m := readStopLedger(t, dir).Messages[uint64(sent["msg_serial"].(float64))]
			if m == nil || m.DeliveredAt != 0 || m.Consumed {
				t.Fatalf("a socket write was falsely treated as a read: %+v", m)
			}
			// EOF precedes the daemon's settlement query. Observe the actual
			// writer receipt before asserting the note for later coalesced mail;
			// this read neither sets a flag nor confirms mail presentation.
			settled := time.After(3 * time.Second)
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for !strings.Contains(eng.SendDeliveryNoteFor(context.Background(), "worker",
				uint64(sent["msg_serial"].(float64))), "already written") {
				select {
				case <-tick.C:
				case <-settled:
					t.Fatal("the actual socket writer did not report its completed write")
				}
			}
			// A later original item gets its own frame, even without turn evidence.
			second := marker + "-second"
			coalesced := call("send", map[string]any{"token": sendToken, "to": "worker", "type": kind, "body": second})
			if !strings.Contains(fmt.Sprint(coalesced["note"]), "being handed") {
				t.Fatalf("new original item was refused: %v", coalesced)
			}
			select {
			case frame := <-wire:
				content := sendNoteFrameContent(t, frame)
				if !strings.Contains(content, second) || strings.Contains(content, `"`+marker+`"`) {
					t.Fatalf("fresh frame lost the new item or repeated the old one: %q", frame)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("fresh original item produced no second socket frame")
			}
			got := call("hook_poll", map[string]any{"session_id": session, "event": "Stop", "strict_output": true})
			if got["decision"] != "block" || !mentions(got, marker) || !mentions(got, second) {
				t.Fatalf("coalesced authored mail lost its Stop fallback: %v", got)
			}
			noSendNoteFrame(t, wire)
			again := call("hook_poll", map[string]any{"session_id": session, "event": "Stop", "strict_output": true})
			if again["decision"] == "block" || again["reason"] != nil {
				t.Fatalf("authored mail blocked Stop twice: %v", again)
			}
		})
	}
}

func sendNoteFrameContent(t *testing.T, frame string) string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(frame), "\n") {
		var decoded struct{ Message struct{ Content string } }
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatal("setup: invalid actual socket JSON:", err)
		}
		if decoded.Message.Content != "" {
			out = append(out, decoded.Message.Content)
		}
	}
	return strings.Join(out, "\n")
}

func TestGeneratedProgressKeepsSocketAndStopQuietThroughMCP(t *testing.T) {
	session, wire := sendNoteSocket(t)
	dir := t.TempDir()
	srv, _, _ := restartableQueueServer(t, dir)
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		return sendNoteCall(t, srv, name, args)
	}
	worker := call("register", map[string]any{"name": "worker", "session_id": session, "harness": "Claude Code"})
	sender := call("register", map[string]any{"name": "sender", "session_id": "progress-sender"})
	call("check_in", map[string]any{"token": worker["token"]})
	call("check_in", map[string]any{"token": sender["token"]})
	n := call("send", map[string]any{
		"token": worker["token"], "to": "sender", "type": "request", "body": "work", "milestones": []string{"one"},
	})["msg_serial"]
	call("respond", map[string]any{"token": sender["token"], "msg_serial": n, "disposition": "approve"})
	call("check_in", map[string]any{"token": worker["token"]})
	call("hook_poll", map[string]any{"session_id": session, "event": "Stop", "stop_hook_active": true})
	call("respond", map[string]any{
		"token": sender["token"], "msg_serial": n, "disposition": "progress", "milestone": 1, "body": "quiet-progress",
	})
	noSendNoteFrame(t, wire)
	before := readStopLedger(t, dir)
	got := call("hook_poll", map[string]any{"session_id": session, "event": "Stop", "strict_output": true})
	if got["decision"] == "block" || got["reason"] != nil || got["hookSpecificOutput"] != nil {
		t.Fatalf("generated progress forced a turn: %v", got)
	}
	after := readStopLedger(t, dir)
	if after.Serial != before.Serial {
		t.Fatalf("quiet Stop changed persisted progress awareness: %d -> %d", before.Serial, after.Serial)
	}
	noSendNoteFrame(t, wire)
	pulled := call("check_in", map[string]any{"token": worker["token"]})
	if !mentions(pulled, "quiet-progress") {
		t.Fatalf("quiet progress was lost at the next activation: %v", pulled)
	}
	again := call("check_in", map[string]any{"token": worker["token"]})
	if strings.Contains(fmt.Sprint(again["agent_updates"]), "quiet-progress") {
		t.Fatalf("read progress was delivered again: %v", again)
	}
}

func TestPresentedNotifyNeverRearmsAcrossIdleEpochsThroughMCP(t *testing.T) {
	for _, through := range []string{"Stop", "check_in", "socket"} {
		t.Run(through, func(t *testing.T) {
			session, wire := sendNoteSocket(t)
			dir := t.TempDir()
			srv, _, _ := restartableQueueServer(t, dir)
			call := func(name string, args map[string]any) map[string]any {
				t.Helper()
				return sendNoteCall(t, srv, name, args)
			}
			worker := call("register", map[string]any{"name": "worker", "session_id": session, "harness": "Claude Code"})
			sender := call("register", map[string]any{"name": "sender"})
			call("check_in", map[string]any{"token": worker["token"]})
			call("check_in", map[string]any{"token": sender["token"]})
			if through == "socket" {
				call("hook_poll", map[string]any{"session_id": session, "event": "Stop", "stop_hook_active": true})
			} else {
				call("hook_poll", map[string]any{"session_id": session, "event": "PreToolUse"})
			}
			marker := "notify-once-" + through
			args := map[string]any{
				"token": sender["token"], "to": "worker", "type": "notify", "body": marker, "op_id": "notify-once",
			}
			n := call("send", args)["msg_serial"]
			switch through {
			case "Stop":
				got := call("hook_poll", map[string]any{"session_id": session, "event": "Stop", "strict_output": true})
				if got["decision"] != "block" || !mentions(got, marker) {
					t.Fatalf("setup: Stop did not present the authored FYI: %v", got)
				}
			case "check_in":
				if got := call("check_in", map[string]any{"token": worker["token"]}); !mentions(got, marker) {
					t.Fatalf("setup: authenticated call did not present the FYI: %v", got)
				}
			case "socket":
				select {
				case frame := <-wire:
					if !strings.Contains(frame, marker) {
						t.Fatalf("setup: wrong first FYI frame: %q", frame)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("setup: the authored FYI never reached the socket")
				}
				// Actual starting-hook evidence confirms the socket presentation;
				// a kernel write alone must retain the held-peer fallback.
				call("hook_poll", map[string]any{"session_id": session, "event": "SessionStart"})
			}
			for range 2 {
				call("hook_poll", map[string]any{"session_id": session, "event": "PreToolUse"})
				got := call("hook_poll", map[string]any{"session_id": session, "event": "Stop", "strict_output": true})
				if got["decision"] == "block" || got["reason"] != nil || got["hookSpecificOutput"] != nil {
					t.Fatalf("presented FYI blocked another idle epoch: %v", got)
				}
				noSendNoteFrame(t, wire)
			}
			retried := call("send", args)
			if retried["msg_serial"] != n || !strings.Contains(fmt.Sprint(retried["note"]), "already presented") ||
				!strings.Contains(fmt.Sprint(retried["note"]), "no new wake was sent") {
				t.Fatalf("idempotent retry claimed another FYI wake: %v", retried)
			}
			noSendNoteFrame(t, wire)
			m := readStopLedger(t, dir).Messages[uint64(n.(float64))]
			if m == nil || !m.Consumed || m.State != core.MsgStateAcked || m.Body != marker {
				t.Fatalf("full presentation did not consume and retain the FYI: %+v", m)
			}
		})
	}
}

// ApplySetting is the daemon's configuration ingress. The routes then enter
// through real HTTP send, real lifecycle hooks and the discovered session.
func TestWakePhaseSocketAndStopThroughMCP(t *testing.T) {
	for _, phase := range []string{"all", "urgent", "none"} {
		for _, kind := range []string{"notify", "question"} {
			t.Run(phase+"/"+kind, func(t *testing.T) {
				session, wire := sendNoteSocket(t)
				dir := t.TempDir()
				srv, eng, _ := restartableQueueServer(t, dir)
				eng.ApplySetting("wake.extend_turn_for", phase)
				call := func(name string, args map[string]any) map[string]any {
					t.Helper()
					return sendNoteCall(t, srv, name, args)
				}
				worker := call("register", map[string]any{"name": "worker", "session_id": session, "harness": "Claude Code"})
				sender := call("register", map[string]any{"name": "sender"})
				call("check_in", map[string]any{"token": worker["token"]})
				call("check_in", map[string]any{"token": sender["token"]})
				call("hook_poll", map[string]any{"session_id": session, "event": "Stop", "stop_hook_active": true})
				marker := "phase-" + phase + "-" + kind
				sent := call("send", map[string]any{"token": sender["token"], "to": "worker", "type": kind, "body": marker})
				wakes := phase != "none" && (phase == "all" || kind == "question")
				if wakes {
					select {
					case frame := <-wire:
						if !strings.Contains(frame, marker) {
							t.Fatalf("wrong phase socket frame: %q", frame)
						}
					case <-time.After(3 * time.Second):
						t.Fatal("enabled authored mail did not wake the actual socket")
					}
				} else {
					if !strings.Contains(fmt.Sprint(sent["note"]), "operator's wake policy suppresses") {
						t.Fatalf("suppressed phase claimed a socket wake: %v", sent)
					}
					noSendNoteFrame(t, wire)
				}
				before := readStopLedger(t, dir)
				got := call("hook_poll", map[string]any{"session_id": session, "event": "Stop", "strict_output": true})
				if wakes {
					if got["decision"] != "block" || !mentions(got, marker) {
						t.Fatalf("enabled Stop omitted authored mail: %v", got)
					}
				} else {
					if got["decision"] == "block" || got["reason"] != nil || got["hookSpecificOutput"] != nil {
						t.Fatalf("suppressed Stop forced a turn: %v", got)
					}
					if after := readStopLedger(t, dir); after.Serial != before.Serial {
						t.Fatal("suppressed Stop consumed mail presentation")
					}
					if pulled := call("check_in", map[string]any{"token": worker["token"]}); !mentions(pulled, marker) {
						t.Fatalf("suppressed mail disappeared: %v", pulled)
					}
				}
				noSendNoteFrame(t, wire)
			})
		}
	}
}

func sendNoteCall(t *testing.T, srv *httptest.Server, name string, args map[string]any) map[string]any {
	t.Helper()
	r := toolCall(t, srv, name, args)
	if r["__is_error"] == true {
		t.Fatalf("setup %s: %v", name, r)
	}
	return r
}

func noSendNoteFrame(t *testing.T, wire <-chan string) {
	t.Helper()
	select {
	case frame := <-wire:
		t.Fatalf("unexpected extra or informational socket frame: %q", frame)
	case <-time.After(1100 * time.Millisecond):
	}
}
