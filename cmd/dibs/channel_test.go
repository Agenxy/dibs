// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Channel mode turns on only where Claude Code registers the channel: an
// event written into an unregistered channel is dropped silently, after the
// daemon has stood down for this session. Measured against Claude Code 2.1.233
// and the desktop app's bundled 2.1.293 on 2026-10-22.
func TestChannelModeOnlyWhereClaudeCodeRegistersIt(t *testing.T) {
	const dev = "--dangerously-load-development-channels"
	for _, c := range []struct {
		name     string
		argv     []string
		override string
		want     bool
	}{
		{"interactive development opt-in", []string{"claude", dev, "plugin:dibs@dibs"}, "", true},
		{"several entries", []string{"claude", dev, "plugin:other@x", "plugin:dibs@dibs", "--model", "m"}, "", true},
		{"equals form", []string{"claude", dev + "=server:dibs"}, "", true},
		{"desktop Code tab: SDK mode ignores the development flag", []string{"claude", "--output-format", "stream-json", "--input-format", "stream-json", dev, "plugin:dibs@dibs"}, "", false},
		{"print mode ignores it too", []string{"claude", "-p", dev, "plugin:dibs@dibs"}, "", false},
		{"--channels with an unlisted marketplace does not register", []string{"claude", "--channels", "plugin:dibs@dibs"}, "", false},
		{"--channels from the approved marketplace registers anywhere", []string{"claude", "--input-format", "stream-json", "--channels", "plugin:dibs@claude-plugins-official"}, "", true},
		{"another plugin", []string{"claude", dev, "plugin:dibsx@dibs"}, "", false},
		{"no opt-in", []string{"claude", "--resume", "x"}, "", false},
		{"operator rollback", []string{"claude", dev, "plugin:dibs@dibs"}, "off", false},
		{"no parent evidence", nil, "", false},
	} {
		if got := claudeChannelOptedIn(c.argv, c.override); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// In channel mode every delivery is a channel event on the bridge's stdout,
// entering through newSelfWaker exactly as production does, and nothing is
// written to the session socket: one writer per session.
func TestChannelModeDeliversAsAChannelEventAndNotThroughTheSocket(t *testing.T) {
	previous := claudeChannel
	claudeChannel = true
	var buf bytes.Buffer
	channelOut = &syncWriter{w: bufio.NewWriter(&buf)}
	t.Cleanup(func() { claudeChannel, channelOut = previous, nil; recordWakePending(false, "") })
	sock := sockPath(t)
	lines := listenLines(t, sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "fixture-child-token")
	fixtureSelfWakeIdentity(t, sock)
	w := newSelfWaker()
	if w == nil {
		t.Fatal("setup: no proven own-session route")
	}
	var written []bool
	w.offerFn = func(string) (string, func(bool), error) {
		return "Dibs: new request from planner (#42).", func(ok bool) { written = append(written, ok) }, nil
	}
	if err := w.wake("stale-notice"); err != nil {
		t.Fatal(err)
	}
	var event struct {
		Method string `json:"method"`
		Params struct {
			Content string            `json:"content"`
			Meta    map[string]string `json:"meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &event); err != nil {
		t.Fatalf("no channel event on stdout: %v %q", err, buf.String())
	}
	if event.Method != "notifications/claude/channel" || event.Params.Content != "Dibs: new request from planner (#42)." {
		t.Fatalf("wrong channel event: %+v", event)
	}
	if len(written) != 1 || !written[0] {
		t.Fatalf("the daemon's receipt was not told the write happened: %v", written)
	}
	if wire := collect(lines, 2, 300*time.Millisecond); len(wire) != 0 {
		t.Fatalf("channel mode also wrote to the session socket: %v", wire)
	}
}

func TestChannelBridgeHelper(t *testing.T) {
	if os.Getenv("DIBS_TEST_CHANNEL_BRIDGE") != "1" {
		return
	}
	claudeChannel = true // what the parent's launch arguments decide in production
	if err := runBridge(nil); err != nil {
		t.Fatal(err)
	}
}

// Through the real stdio loop against the real daemon: the 2026 probe is
// declined as a pre-2026 server declines it, so Claude Code's v2 runtime can
// register the channel, and initialize carries the channel capability while
// keeping the daemon's own instructions and tools.
func TestChannelBridgeStaysOnTheHandshakeThatRegistersAChannel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, addr := startupDaemon(t, dir)
	t.Cleanup(func() { _ = srv.Close() })
	cmd := exec.Command(os.Args[0], "-test.run=^TestChannelBridgeHelper$") // own test binary
	cmd.Env = append(os.Environ(), "DIBS_TEST_CHANNEL_BRIDGE=1", "DIBS_DIR="+dir, "DIBS_ADDR=http://"+addr)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	reader := bufio.NewReader(out)
	call := func(id int, method string, params map[string]any) map[string]any {
		t.Helper()
		if err := json.NewEncoder(in).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		got := make(chan []byte, 1)
		go func() { line, _ := reader.ReadBytes('\n'); got <- line }()
		select {
		case line := <-got:
			var reply map[string]any
			if err := json.Unmarshal(line, &reply); err != nil {
				t.Fatalf("%s: invalid reply %v %s", method, err, line)
			}
			return reply
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: no reply", method)
			return nil
		}
	}

	probe := call(1, "server/discover", map[string]any{"_meta": map[string]any{"protocolVersion": "2026-07-28"}})
	if e, _ := probe["error"].(map[string]any); e == nil || e["code"] != float64(-32601) || probe["id"] != float64(1) {
		t.Fatalf("the 2026 probe was not declined as a pre-2026 server declines it: %v", probe)
	}
	init := call(2, "initialize", map[string]any{
		"protocolVersion": "2025-11-25", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "claude-code", "version": "2.1.233"},
	})
	result, _ := init["result"].(map[string]any)
	caps, _ := result["capabilities"].(map[string]any)
	experimental, _ := caps["experimental"].(map[string]any)
	if _, ok := experimental["claude/channel"]; !ok {
		t.Fatalf("initialize does not declare the channel capability: %v", init)
	}
	if caps["tools"] == nil {
		t.Fatalf("the daemon's own capabilities were lost: %v", caps)
	}
	instructions, _ := result["instructions"].(string)
	if !strings.Contains(instructions, "<channel") || len(instructions) <= len(channelInstructions) {
		t.Fatalf("instructions should keep the daemon's and add the channel's: %q", instructions)
	}
	tools := call(3, "tools/list", map[string]any{})
	if list, _ := tools["result"].(map[string]any)["tools"].([]any); len(list) == 0 {
		t.Fatalf("tools were not discovered after the channel handshake: %v", tools)
	}
}
