// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// Create the new app lineage only after the engine cached the old directory.
// A real send must rediscover the running session without any app contact.
func TestMailRelinksARunningClaudeLineageAndDeliversOnceWithoutNavigation(t *testing.T) {
	f := newReopenContactFixture(t)
	socket, live := listeningSession(t)
	t.Cleanup(func() { f.cancel(); <-f.joined })
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	worker, err := f.e.query(f.ctx, func() core.Result {
		return core.Result{"cwd": f.e.state.Agents["lead"].Agent.CWD}
	})
	if err != nil {
		t.Fatal(err)
	}
	// Keep the real socket's directory evidence aligned with the registered
	// agent. A different directory would correctly refuse delivery, hiding
	// the relinking behavior this guard must discriminate.
	peerPath := filepath.Join(home, ".claude", "sessions", "999001.json")
	peer, err := json.Marshal(map[string]any{"pid": 999001, "sessionId": live, "cwd": worker["cwd"]})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(peerPath, peer, 0o600); err != nil {
		t.Fatal(err)
	}
	const bound = "6336088d-1111-4222-8333-444455556666"
	const local = "local_08697f65-6786-4c61-96f8-9af1aa07b24e"
	dir := filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions", "a", "o")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(map[string]any{
		"sessionId": local, "cliSessionId": live,
		"priorCliSessionIds": []string{bound}, "isArchived": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, local+".json"), record, 0o600); err != nil {
		t.Fatal(err)
	}
	// ClaudeSessionRunning reads the same sidecar shape in production. This
	// test process is the live fixture; no app or Claude process is started.
	process, err := json.Marshal(map[string]any{"pid": os.Getpid(), "sessionId": live})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "sessions", "running.json"), process, 0o600); err != nil {
		t.Fatal(err)
	}
	if !harnessenv.ClaudeSessionRunning(live) {
		t.Fatal("setup: current lineage has no live process evidence")
	}
	if localFound, current := harnessenv.ClaudeAppSessionNow("", bound); localFound != local || current != live {
		t.Fatalf("setup: app lineage not readable: %q %q", localFound, current)
	}
	if _, found := f.e.peerSessionFor([]string{bound}); found {
		t.Fatal("setup: old cache already links the resumed id; this would bypass recovery")
	}
	listener := socket.(*net.UnixListener)
	if err := listener.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	f.do(t, &core.Op{
		Kind: core.OpSendMessage, Token: f.sender, To: "lead", MsgType: core.MsgRequest,
		Body: "running-lineage-background-delivery",
	})
	wire := readAll(t, socket)
	if !strings.Contains(wire, "running-lineage-background-delivery") {
		t.Fatalf("real send failed to reach the newer running session: %q", wire)
	}
	waitWakeDone(t, f.e, "lead")
	if err := listener.SetDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if connection, err := listener.Accept(); err == nil {
		_ = connection.Close()
		t.Fatal("one mail arrival produced a second socket delivery")
	} else {
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("duplicate-delivery observation failed: %v", err)
		}
	}
	select {
	case argv := <-f.opened:
		t.Fatalf("running session received app contact: %q", argv)
	default:
	}
	board, err := f.e.Board(f.ctx)
	if err != nil || board["app_binding_alerts"] != nil {
		t.Fatalf("running session mislabeled as waiting for an open: %v %v", board["app_binding_alerts"], err)
	}
}
