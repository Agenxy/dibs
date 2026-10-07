// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/testport"
)

// A daemon that goes away does not take the bridge with it.
//
// The bridge returned on an undeliverable request, which ends the process. No
// harness restarts a stdio MCP server, so an operator restarting the daemon
// they were working on silently disconnected every agent on the machine for the
// rest of its session: the model saw one "server disconnected" line and went on
// working with no board, no mail, and no claims. Measured on this machine after
// one restart: six live sessions, zero bridge processes.
//
// Ordinary calls keep their ten-second grace. Startup discovery has a separate
// bounded allowance: a real upgrade took longer than ten seconds and a failed
// initialize leaves Claude Code's tools disconnected for the whole session.
//
// This drives the REAL loop, over real pipes, because the claim is about what
// the process does and not about what a helper returns.
func TestTheBridgeSurvivesADaemonThatGoesAwayAndComesBack(t *testing.T) {
	restore := upgradeGrace
	upgradeGrace = 150 * time.Millisecond
	t.Cleanup(func() { upgradeGrace = restore })

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"),
		[]byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_DIR", dir)

	port := testport.Reserve(t, "tcp", "127.0.0.1:0")
	// This outage fixture requires ECONNREFUSED. A held TCP socket instead
	// queues or times out on macOS; release is unavoidable for this premise.
	where := port.ReleaseForOutage(t)
	t.Setenv("DIBS_ADDR", "http://"+where)

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, stdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	t.Cleanup(func() { os.Stdin, os.Stdout = stdin, stdout })

	exited := make(chan error, 1)
	go func() { exited <- runBridge(nil) }()
	t.Cleanup(func() {
		_ = inW.Close()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("the bridge did not exit when stdin closed")
		}
	})

	replies := bufio.NewReader(outR)
	read := func() map[string]any {
		t.Helper()
		done := make(chan []byte, 1)
		go func() {
			line, rerr := replies.ReadBytes('\n')
			if rerr != nil {
				close(done)
				return
			}
			done <- line
		}()
		select {
		case e := <-exited:
			t.Fatalf("the bridge EXITED instead of answering (%v). Nothing restarts a "+
				"stdio MCP server, so the agent has no board for the rest of its session", e)
			return nil
		case line, ok := <-done:
			if !ok {
				t.Fatal("stdout closed: the bridge is gone")
			}
			var m map[string]any
			if uerr := json.Unmarshal(line, &m); uerr != nil {
				t.Fatalf("not JSON-RPC on stdout: %v (%q)", uerr, line)
			}
			return m
		case <-time.After(10 * time.Second):
			t.Fatal("no reply: the bridge answered nothing and did not exit either, " +
				"which is the one outcome an agent cannot act on")
			return nil
		}
	}

	// ── the daemon is down ────────────────────────────────────────────────
	if _, err = fmt.Fprintln(inW,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"board"}}`); err != nil {
		t.Fatal(err)
	}
	first := read()
	if first["id"] != float64(1) {
		t.Errorf("reply id %v: it must pair with the call it answers", first["id"])
	}
	rpcErr, ok := first["error"].(map[string]any)
	if !ok {
		t.Fatalf("a call to a daemon that is not there answered %v; it must be an error", first)
	}
	data, _ := rpcErr["data"].(map[string]any)
	hint, _ := data["hint"].(string)
	if hint == "" {
		t.Error("rule 6: every error carries a hint naming the corrective call")
	}
	// The connection was refused, so the request provably never arrived. That is
	// the one case where the agent can be told a retry is safe, and the one it
	// most needs to hear.
	if !strings.Contains(hint, "nothing was applied") {
		t.Errorf("hint %q does not say the call did not happen, which is what "+
			"decides whether the agent may repeat it", hint)
	}
	for _, recovery := range []string{"just upgraded or restarted", "wait a few seconds", "`dibs doctor`", "tell your human"} {
		if !strings.Contains(hint, recovery) {
			t.Errorf("connection-refused hint omitted truthful recovery %q: %s", recovery, hint)
		}
	}
	for _, forbidden := range []string{"dibs service", "`dibd`", "configure --service"} {
		if strings.Contains(hint, forbidden) {
			t.Errorf("agent-facing recovery must not name nonexistent commands or start/install the shared daemon: %s", hint)
		}
	}

	select {
	case e := <-exited:
		t.Fatalf("the bridge EXITED on an unreachable daemon (%v). "+
			"Nothing restarts a stdio server, so the agent has no board for the "+
			"rest of its session", e)
	default:
	}

	// ── the daemon comes back on the same address ─────────────────────────
	ln, err := net.Listen("tcp", where)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":2,"result":{"agents":[]}}`)
		}),
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	if _, err = fmt.Fprintln(inW,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"board"}}`); err != nil {
		t.Fatal(err)
	}
	back := read()
	if _, bad := back["error"]; bad {
		t.Fatalf("the daemon is up again and the call still failed: %v. "+
			"Reattaching on the next call is the whole point of not exiting", back)
	}
	if back["result"] == nil {
		t.Errorf("expected the daemon's own result, got %v", back)
	}
}
