// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package harnessenv

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/procexec"
	"github.com/agenxy/dibs/internal/procinfo"
)

// Start at the public ancestry observation, not the diagnostic setter. A fresh
// test process owns the once-per-operation counters, independent of test order.
func TestNativePathFallbackDiagnostic(t *testing.T) {
	if os.Getenv("DIBS_FALLBACK_PATH_PROBE") != "1" {
		cmd := procexec.Command(t.Context(), os.Args[0], "-test.run=^TestNativePathFallbackDiagnostic$")
		cmd.Env = append(os.Environ(), "DIBS_FALLBACK_PATH_PROBE=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("path diagnostic probe: %v\n%s", err, out)
		}
		return
	}
	bridge, parent := nativeAppProcesses(t)
	if parent.Comm == "" {
		t.Fatal("setup: kernel process name missing")
	}
	oldSnapshot, oldPath, oldFallback := nativeProcessSnapshot, nativeProcessPath, processPathFallback
	t.Cleanup(func() {
		nativeProcessSnapshot, nativeProcessPath, processPathFallback = oldSnapshot, oldPath, oldFallback
	})
	nativeProcessSnapshot = func(context.Context) ([]procinfo.Process, error) { return []procinfo.Process{bridge, parent}, nil }
	nativeProcessPath = func(pid int) (string, error) {
		if pid != parent.PID {
			t.Fatalf("path read escaped the denied ancestor: %d", pid)
		}
		return "", os.ErrPermission
	}
	fallbacks := 0
	processPathFallback = func(_ context.Context, pid int) (string, error) {
		if pid != parent.PID {
			t.Fatalf("fallback escaped the denied ancestor: %d", pid)
		}
		fallbacks++
		return "", os.ErrPermission
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	commands := 0
	restore := procexec.ObserveCommands(func(string) { commands++ })
	defer restore()
	for range 33 {
		if _, err := AppForBridges(t.Context(), []BridgeIncarnation{{PID: bridge.PID, Start: procinfo.Stamp(bridge.Start)}}); err == nil {
			t.Fatal("denied ancestry became a known absent app")
		}
	}
	if fallbacks != 33 || commands != 0 {
		t.Fatalf("diagnostics changed fallback decisions or prepared commands: fallbacks=%d commands=%d", fallbacks, commands)
	}
	warnings, debug := 0, 0
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte{'\n'}) {
		var record struct {
			Level, Operation, Comm string
			PID                    int
			Callers                []string
			Count                  uint64 `json:"fallback_count"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record.Operation != "process executable path" {
			t.Fatalf("unexpected diagnostic: %s", line)
		}
		switch record.Level {
		case "WARN":
			warnings++
			if record.PID != parent.PID || record.Comm != parent.Comm || record.Count != 1 {
				t.Errorf("fallback WARN lacks target/count evidence: %s", line)
			}
			joined := strings.Join(record.Callers, " ")
			if len(record.Callers) == 0 || len(record.Callers) > 8 || !strings.Contains(joined, "(*processPass).readPath") || !strings.Contains(joined, "AppForBridges") {
				t.Errorf("fallback WARN lacks bounded real caller chain: %s", line)
			}
			for _, name := range record.Callers {
				if len(name) > 192 || strings.Contains(name, "github.com/agenxy/dibs/") {
					t.Errorf("unbounded or unshortened caller: %q", name)
				}
			}
		case "DEBUG":
			debug++
			if record.Count != uint64(1)<<debug {
				t.Errorf("fallback count does not match cumulative power-of-two schedule: %s", line)
			}
		default:
			t.Errorf("unexpected diagnostic level: %s", line)
		}
	}
	if warnings != 1 || debug != 5 {
		t.Fatalf("fallback log cadence: WARN=%d DEBUG=%d, want 1/5", warnings, debug)
	}
}
