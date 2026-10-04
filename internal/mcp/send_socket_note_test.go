package mcp

import (
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
	for _, mode := range []string{"idle-notify", "idle-question", "busy-question"} {
		t.Run(mode, func(t *testing.T) {
			session, wire := sendNoteSocket(t)
			dir := t.TempDir()
			srv, _, _ := restartableQueueServer(t, dir)
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
			if mode == "idle-notify" {
				kind = "notify"
			}
			if mode == "busy-question" {
				call("hook_poll", map[string]any{"session_id": session, "event": "PreToolUse"})
			}
			marker := "send-note-" + mode
			sent := call("send", map[string]any{"token": sender["token"], "to": "worker", "type": kind, "body": marker})
			note, _ := sent["note"].(string)
			switch mode {
			case "idle-notify":
				if !strings.Contains(note, "worker's mailbox") || !strings.Contains(note, "informational, so no wake was sent") ||
					!strings.Contains(note, "next activation") || strings.Contains(note, "being handed") {
					t.Errorf("FYI send claimed a wake instead of deferral: %v", sent)
				}
			case "idle-question":
				if !strings.Contains(note, "best-effort notice") {
					t.Errorf("actionable socket note lost the attempt wording: %v", sent)
				}
			case "busy-question":
				if !strings.Contains(note, "mid-turn") || !strings.Contains(note, "deferred until the turn ends") ||
					strings.Contains(note, "being handed") {
					t.Errorf("busy send claimed an immediate wake: %v", sent)
				}
			}
			if mode != "idle-question" {
				noSendNoteFrame(t, wire)
			}
			if mode == "idle-notify" {
				state := readStopLedger(t, dir)
				m := state.Messages[uint64(sent["msg_serial"].(float64))]
				if m == nil || m.DeliveredAt != 0 {
					t.Fatalf("the advisory note delivered or lost held FYI mail: %+v", m)
				}
				pulled := call("check_in", map[string]any{"token": worker["token"]})
				if !strings.Contains(fmt.Sprint(pulled), marker) {
					t.Fatalf("held FYI was missing at the next authenticated activation: %v", pulled)
				}
				return
			}
			if mode == "busy-question" {
				call("hook_poll", map[string]any{"session_id": session, "event": "Stop", "stop_hook_active": true})
			}
			select {
			case frame := <-wire:
				if !strings.Contains(frame, marker) || !strings.Contains(frame, `"type":"auth"`) {
					t.Fatalf("wrong actual socket frame: %q", frame)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("actionable mail did not produce an actual socket frame")
			}
			noSendNoteFrame(t, wire)
		})
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
