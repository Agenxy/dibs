// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/logs"
)

// Enters through the actual stdio register and subscription path, with a real
// daemon and a listening FOREIGN socket. No self-wake flag or setter is fed in.
func TestForeignInheritedSocketCannotOwnTheRegisteredAgentsWake(t *testing.T) {
	const sid = "71f97290-0001-4000-8000-111111111111"
	for _, mode := range []string{"no-parent-sidecar", "other-session", "other-socket"} {
		t.Run(mode, func(t *testing.T) {
			sock := sockPath(t)
			owner := economyIdentity{session: sid, socket: sock}
			switch mode {
			case "no-parent-sidecar":
				owner.missing = true
			case "other-session":
				owner.session = "71f97290-0001-4000-8000-222222222222"
			case "other-socket":
				owner.socket = sockPath(t)
				_ = listenLines(t, owner.socket)
			}
			f := newEconomyFixtureAt(t, sid, sock, nil, owner)
			f.tool(t, "send", map[string]any{"token": f.sender, "to": "worker", "type": "question", "body": "foreign-private-mail"})
			// The send drives the actual decision after the register callback. Keep the
			// listener up through the arrival batch, so a leaked digest is observable.
			wire := collect(f.lines, 2, 700*time.Millisecond)
			if live, session := f.eng.SelfWaking("worker"); live {
				t.Errorf("foreign credentials claimed self-wake for worker (session %q)", session)
			}
			if len(wire) != 0 {
				t.Fatalf("foreign socket received agent mail: %s", strings.Join(wire, "\n"))
			}
		})
	}
}

// The 2026 path has no initialize. A real direct-parent sidecar proves the
// route independently of clientInfo, and the production bridge must use it.
func TestDirectParentIdentityKeepsHandshakeFreeSelfWake(t *testing.T) {
	ring := logs.NewRing(64)
	previous := slog.Default()
	slog.SetDefault(slog.New(logs.NewHandler(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}), ring)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	f := newEconomyFixture(t)
	f.tool(t, "send", map[string]any{"token": f.sender, "to": "worker", "type": "question", "body": "own-private-mail"})
	wire := collect(f.lines, 2, 2*time.Second)
	if len(wire) != 2 || !strings.Contains(wire[1], "own-private-mail") {
		t.Fatalf("own socket lost its mail: %v", wire)
	}
	f.waitWritten(t)
	for _, record := range ring.Tail(0) {
		if record.Level == "DEBUG" && record.Msg == "delivery left to the agent's own bridge: self-wake declared" && record.Attrs["agent"] == "worker" && record.Attrs["session_id"] == f.sid && record.Attrs["bridge_pid"] == "unavailable" {
			return
		}
	}
	t.Fatalf("confirmed self-wake route was silently bypassed: %v", ring.Tail(0))
}

// Existing mechanics fixtures still enter through the normal route constructor;
// they now also provide the harness sidecar that production must observe.
func fixtureSelfWakeIdentity(t *testing.T, socket string) {
	t.Helper()
	session := streamSession()
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"sessionId": session, "messagingSocketPath": socket})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(os.Getppid())+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// An old image's owed digest is restored through the production handoff path.
// Its thread must not turn inherited credentials into a write to another session.
func TestUpgradeCannotWriteAPendingDigestToAForeignSession(t *testing.T) {
	previous := threadServed()
	noteThread("71f97290-0001-4000-8000-111111111111")
	t.Cleanup(func() { noteThread(previous); resetWakeStreams(); recordWakePending(false, "") })
	sock := sockPath(t)
	lines := listenLines(t, sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "fixture-child-token")
	fixtureSelfWakeIdentity(t, sock)
	raw, err := json.Marshal(bridgeState{
		Thread:      "71f97290-0001-4000-8000-222222222222",
		WakeStreams: []wakeHandoff{{Key: "worker", Token: "fixture-agent-token"}},
		WakePending: true, WakeNotice: "foreign-private-upgrade-digest",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(bridgeStateEnv, string(raw))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var streams sync.WaitGroup
	var watcher inboxWatcher
	out := &syncWriter{w: bufio.NewWriter(io.Discard)}
	restoreCarried(ctx, &http.Client{}, "http://127.0.0.1:1/mcp", "fixture-secret", out, &streams, &watcher, true, shipTiming{})
	if wire := collect(lines, 2, 300*time.Millisecond); len(wire) != 0 {
		t.Fatalf("upgrade wrote a foreign digest: %v", wire)
	}
	if len(watcher.tokens()) != 0 {
		t.Fatal("upgrade restored a foreign self-wake stream")
	}
}
