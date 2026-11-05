// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/boardconfig"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
	"github.com/agenxy/dibs/internal/testexec"
	"github.com/agenxy/dibs/internal/wakeexec"
)

func TestHostBridgeReportsCommandAmbiguityWithoutRetry(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "codex")
	if out, err := exec.Command("go", "build", "-o", binary, "../../internal/wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("setup fixture build: %v: %s", err, out)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	for _, mode := range []string{"queue", "nonqueue", "definite-failure"} {
		t.Run(mode, func(t *testing.T) { hostBridgeCommandAmbiguity(t, binary, mode) })
	}
}

func hostBridgeCommandAmbiguity(t *testing.T, binary, mode string) {
	t.Helper()
	dir := cliIPCProfile(t)
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("CODEX_HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	reports := make(chan engine.WakeResult, 1)
	hub := newLocalTestServer(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/wake-result" || r.Header.Get("X-Dibs-Local") != "fixture" {
			t.Error("setup report missed the authenticated result route")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var result engine.WakeResult
		if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
			t.Error("setup result decode:", err)
		}
		reports <- result
		_ = json.NewEncoder(w).Encode(map[string]bool{"accepted": true})
	}))
	t.Cleanup(hub.Close)
	argv := []string{binary, "queue", "--thread", "{thread}", "--message", "{message}"}
	switch mode {
	case "nonqueue":
		argv = []string{binary, "nonqueue-pipe"}
	case "definite-failure":
		argv = []string{binary, "primary"}
	}
	b := newWakeBridge(hub.URL, "fixture", "local", map[string]boardconfig.WakeExec{"codex": {Argv: argv}})
	b.show = harnessenv.Shower{Holds: func(string) bool { return true }, Open: func([]string) error {
		t.Error("unknown or failed bridge command opened the real app")
		return nil
	}}
	wr := engine.WakeRequest{
		ID: 1, Host: "local", Agent: "worker", Harness: "codex", Surface: harnessenv.CodexOutsideApp,
		Thread: testcodexipc.Thread, MsgType: "request",
	}
	controls := map[byte]net.Conn{}
	released := map[byte]bool{}
	if mode != "definite-failure" {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		if err := ln.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		t.Setenv("DIBS_TEST_QUEUE_PIPE_CONTROL", ln.Addr().String())
		t.Setenv("DIBS_TEST_QUEUE_PIPE_CONTROL_TIMEOUT", "30s")
		t.Setenv("DIBS_TEST_QUEUE_PIPE_CASE", "dispatched")
		t.Cleanup(func() {
			for role, control := range controls {
				_ = control.SetDeadline(time.Now().Add(5 * time.Second))
				// Preserve the parent's exit receipt: it already received its
				// one byte before the drain wait, so cleanup must not write again.
				if !released[role] {
					released[role] = true
					release := byte('x')
					if role == 'p' {
						release = 'e'
					}
					if _, err := control.Write([]byte{release}); err != nil {
						t.Errorf("fixture %c release: %v", role, err)
					}
				}
				if extra, err := io.ReadAll(control); err != nil || len(extra) != 0 {
					t.Errorf("fixture %c exit receipt: %q %v", role, extra, err)
				}
				_ = control.Close()
			}
		})
		go b.serve(context.Background(), wr)
		for range 2 {
			control, err := ln.Accept()
			if err != nil {
				t.Fatal("setup command did not reach its actual output pipe:", err)
			}
			_ = control.SetReadDeadline(time.Now().Add(5 * time.Second))
			var role [1]byte
			if _, err := io.ReadFull(control, role[:]); err != nil || (role[0] != 'p' && role[0] != 'g') || controls[role[0]] != nil {
				_ = control.Close()
				t.Fatalf("setup fixture role: %q %v", role, err)
			}
			controls[role[0]] = control
		}
		released['p'] = true
		if _, err := controls['p'].Write([]byte{'e'}); err != nil {
			t.Fatal("setup parent release:", err)
		}
	} else {
		go b.serve(context.Background(), wr)
	}
	select {
	case result := <-reports:
		if result.OK || result.NoRetry != (mode != "definite-failure") {
			t.Errorf("bridge lost command ambiguity or suppressed definite failure retry: %+v", result)
		}
		if mode != "definite-failure" && !strings.Contains(result.Detail, "unknown") {
			t.Errorf("bridge reported ambiguous acceptance as definite failure: %+v", result)
		}
	case <-time.After(wakeexec.Grace + 10*time.Second):
		t.Fatal("bridge did not report after the production drain grace")
	}
}
