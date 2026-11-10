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

// Physical footprint is the measure, not RSS. This budget is for a normal,
// non-race production image on the gate's macOS/arm64 platform. A test helper
// calling runBridge would miss command/package init.
//
// The guard used to read RSS, which this platform cannot hold still: it moves
// with the runtime scavenger's timing and with compression. On one quiet
// machine at one commit, five-second startup RSS ranged 12.5-21.1 MiB across
// ten bridges, so a 20 MiB budget failed about one run in four and failed a
// loaded gate at 20.3 MiB with no code change. #567 already found footprint
// the stable figure (its trim: idle 10.9 -> 8.9 MiB, ranges under 0.7 MiB).
// The same ten bridges, at main + #600 + #601 on 2026-11-09, used 9.08-10.49
// MiB at startup and 10.84-12.83 MiB after 100 calls; the 11.5/14 MiB budgets
// keep about 1 MiB above the largest of those.
func TestBridgeMemoryBudget(t *testing.T) {
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
		t.Run(name, func(t *testing.T) { testBridgeMemoryBudget(t, bin, upgraded) })
	}
}

func testBridgeMemoryBudget(t *testing.T, bin string, upgraded bool) {
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
	bridgeFootprintAtIdle(t, cmd.Process.Pid, "startup", 11.5)
	for i := range 100 {
		method := "server/discover"
		if i%2 == 0 {
			method = "tools/list"
		}
		rpc(i+2, method)
	}
	bridgeFootprintAtIdle(t, cmd.Process.Pid, "after 100 calls", 14)
}

func bridgeFootprintAtIdle(t *testing.T, pid int, phase string, budgetMiB float64) {
	t.Helper()
	// Same five-second quiescent interval as #567's before/after measurements.
	// No GC or memory-pressure operation is injected into the bridge.
	time.Sleep(5 * time.Second)
	var samples []string
	for range 3 {
		out, err := procexec.Command(t.Context(), "/usr/bin/footprint", "-f", "bytes", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			t.Fatal("setup: read actual bridge footprint:", err)
		}
		_, rest, ok := strings.Cut(string(out), "Footprint: ")
		field, _, _ := strings.Cut(rest, " B")
		n, err := strconv.Atoi(field)
		if !ok || err != nil || n <= 0 {
			t.Fatalf("setup: unreadable footprint output %q", out)
		}
		mib := float64(n) / (1 << 20)
		samples = append(samples, strconv.FormatFloat(mib, 'f', 2, 64))
		if mib > budgetMiB {
			t.Errorf("%s bridge footprint %.2f MiB exceeds %.1f MiB budget", phase, mib, budgetMiB)
		}
	}
	t.Logf("%s bridge footprint samples %v MiB; budget %.1f MiB", phase, samples, budgetMiB)
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
