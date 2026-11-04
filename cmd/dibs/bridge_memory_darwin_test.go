// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin && arm64

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/mcp"
	"github.com/agenxy/dibs/internal/procexec"
	"github.com/agenxy/dibs/internal/testexec"
)

// RSS includes resident code and runtime pages, not just live Go objects. This
// budget is for a normal, non-race production image on the gate's macOS/arm64
// platform. A test helper calling runBridge would miss command/package init.
// Three measured fresh bridges used 20.3–20.6 MiB idle and 24.3–24.5 MiB after
// a burst before the trim, versus 18.0–18.2 and 22.0–22.5 MiB afterwards. The
// 20/24 MiB budgets retain about 1.8/1.5 MiB margin above the new observations.
func TestBridgeRSSBudget(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dibs")
	build := procexec.Command(t.Context(), "go", "build", "-trimpath", "-ldflags=-s -w", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("setup: build production CLI: %v %s", err, out)
	}
	testexec.PrepareExecutable(t, exec.Command(bin, "--help"))
	for _, upgraded := range []bool{false, true} {
		name := "fresh"
		if upgraded {
			name = "upgraded"
		}
		t.Run(name, func(t *testing.T) { testBridgeRSSBudget(t, bin, upgraded) })
	}
}

func testBridgeRSSBudget(t *testing.T, bin string, upgraded bool) {
	t.Helper()
	dir := cliIPCProfile(t)
	for name, data := range map[string]string{
		"local.secret": strings.Repeat("a", 64), "dibs.toml": "[wake]\nsockets = false\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "memory-fixture", box)
	if err != nil {
		t.Fatal(err)
	}
	// Testing cancels t.Context before Cleanup; EOF must get the first chance
	// to stop this real bridge, with cancellation reserved for cleanup failure.
	ctx, cancel := context.WithCancel(context.Background())
	eng := engine.New(core.NewState("memory-fixture", core.DefaultLimits()), led, nil)
	joined := make(chan struct{})
	go func() { eng.Run(ctx); close(joined) }()
	srv := newLocalTestServer(t, dir, mcp.New(eng))
	t.Cleanup(func() { srv.Close(); cancel(); <-joined; _ = led.Close() })
	cmd := procexec.Command(ctx, bin, "mcp-stdio")
	cmd.Dir = dir
	cmd.Env = bridgeMemoryEnv(dir, srv.URL)
	if upgraded {
		cmd.Env = append(cmd.Env, `DIBS_BRIDGE_STATE={"tools_hash":"before-upgrade"}`)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, writer, err := os.Pipe()
	if err != nil {
		_ = in.Close()
		t.Fatal(err)
	}
	cmd.Stdout = writer
	if err := cmd.Start(); err != nil {
		_ = in.Close()
		_ = out.Close()
		_ = writer.Close()
		t.Fatal(err)
	}
	_ = writer.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	replies := make(chan json.RawMessage, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		decoder := json.NewDecoder(out)
		for {
			var reply json.RawMessage
			if decoder.Decode(&reply) != nil {
				return
			}
			select {
			case replies <- reply:
			case <-ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = in.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("bridge EOF: %v %s", err, stderr.String())
			}
		case <-time.After(5 * time.Second):
			cancel()
			if err := <-done; err == nil {
				t.Error("bridge failed to exit on EOF")
			} else {
				t.Errorf("bridge needed cancellation: %v", err)
			}
		}
		cancel()
		_ = out.Close()
		<-readerDone
	})
	changed := 0
	rpc := func(id int, method string) {
		t.Helper()
		request := map[string]any{
			"jsonrpc": "2.0", "id": id, "method": method,
			"params": map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"}},
		}
		if err := json.NewEncoder(in).Encode(request); err != nil {
			t.Fatal("setup: request:", err)
		}
		deadline := time.After(10 * time.Second)
		for {
			select {
			case raw := <-replies:
				var reply struct {
					ID     int
					Method string
					Result json.RawMessage
					Error  json.RawMessage
				}
				if err := json.Unmarshal(raw, &reply); err != nil {
					t.Fatal("setup: malformed bridge frame:", err)
				}
				if reply.Method == "notifications/tools/list_changed" {
					changed++
					continue
				}
				if reply.ID != id || len(reply.Result) == 0 || len(reply.Error) != 0 {
					t.Fatalf("setup: unmatched or failed %s: %s", method, raw)
				}
				return
			case <-deadline:
				t.Fatalf("setup: %s reply deadline", method)
			}
		}
	}
	rpc(1, "server/discover")
	wantChanged := 0
	if upgraded {
		wantChanged = 1
	}
	if changed != wantChanged {
		t.Fatalf("setup: upgrade restoration notifications=%d, want %d", changed, wantChanged)
	}
	bridgeRSSAtIdle(t, cmd.Process.Pid, "startup", 20*1024)
	for i := range 100 {
		method := "server/discover"
		if i%2 == 0 {
			method = "tools/list"
		}
		rpc(i+2, method)
	}
	bridgeRSSAtIdle(t, cmd.Process.Pid, "after 100 calls", 24*1024)
}

func bridgeRSSAtIdle(t *testing.T, pid int, phase string, budgetKiB int) {
	t.Helper()
	// Same five-second quiescent interval as the before/after measurements.
	// No GC or memory-pressure operation is injected into the bridge.
	time.Sleep(5 * time.Second)
	var samples []int
	for range 3 {
		out, err := procexec.Command(t.Context(), "/bin/ps", "-p", strconv.Itoa(pid), "-o", "rss=").Output()
		if err != nil {
			t.Fatal("setup: read actual bridge RSS:", err)
		}
		rss, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil || rss <= 0 {
			t.Fatalf("setup: invalid RSS %q: %v", out, err)
		}
		samples = append(samples, rss)
		if rss > budgetKiB {
			t.Errorf("%s bridge RSS %d KiB exceeds %d KiB budget", phase, rss, budgetKiB)
		}
	}
	t.Logf("%s bridge RSS samples %v KiB; budget %d KiB", phase, samples, budgetKiB)
}

func bridgeMemoryEnv(dir, address string) []string {
	var env []string
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "DIBS_") || strings.HasPrefix(key, "CODEX_") || strings.HasPrefix(key, "CLAUDE_") ||
			key == "HOME" || key == "USERPROFILE" || strings.HasPrefix(key, "XDG_") ||
			key == "GOGC" || key == "GOMEMLIMIT" || key == "GODEBUG" {
			continue
		}
		env = append(env, value)
	}
	return append(env, "HOME="+dir, "USERPROFILE="+dir, "DIBS_DIR="+dir,
		"DIBS_ADDR="+address, "DIBS_TRANSPORT=local", "CODEX_HOME="+filepath.Join(dir, "codex"))
}
