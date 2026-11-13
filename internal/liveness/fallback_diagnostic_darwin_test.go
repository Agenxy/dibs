// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package liveness

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/procexec"
	"github.com/agenxy/dibs/internal/procinfo"
)

func TestNativeLivenessFallbackDiagnostics(t *testing.T) {
	if os.Getenv("DIBS_FALLBACK_LIVENESS_PROBE") != "1" {
		cmd := procexec.Command(t.Context(), os.Args[0], "-test.run=^TestNativeLivenessFallbackDiagnostics$")
		cmd.Env = append(os.Environ(), "DIBS_FALLBACK_LIVENESS_PROBE=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("liveness diagnostic probe: %v\n%s", err, out)
		}
		return
	}
	rows, err := procinfo.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	comm := ""
	for _, row := range rows {
		if row.PID == os.Getpid() {
			comm = row.Comm
		}
	}
	if comm == "" {
		t.Fatal("setup: own kernel name missing")
	}
	oldCPU, oldFiles := nativeCPU, nativeOpenFiles
	t.Cleanup(func() { nativeCPU, nativeOpenFiles = oldCPU, oldFiles })
	nativeCPU = func(int) (time.Duration, error) { return 0, errors.New("diagnostic native CPU refusal") }
	nativeOpenFiles = func(int, int) ([]string, error) { return nil, errors.New("diagnostic native files refusal") }
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	commands := map[string]int{}
	restore := procexec.ObserveCommands(func(name string) { commands[name]++ })
	defer restore()
	for range 2 {
		if sample := Observe(os.Getpid(), ""); !sample.Alive {
			t.Fatal("diagnostics changed the existing CPU fallback's liveness verdict")
		}
		_ = FindTranscript(os.Getpid())
	}
	if len(commands) != 2 || commands["ps"] != 2 || commands["lsof"] != 2 {
		t.Fatalf("diagnostics changed command fallbacks: %v", commands)
	}
	warnings, counts := map[string]int{}, map[string]int{}
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
		switch record.Level {
		case "WARN":
			warnings[record.Operation]++
			if record.PID != os.Getpid() || record.Comm != comm || record.Count != 1 {
				t.Errorf("liveness fallback WARN lacks target/count evidence: %s", line)
			}
			entry := "Observe"
			if record.Operation == "process open files" {
				entry = "FindTranscript"
			}
			if len(record.Callers) == 0 || len(record.Callers) > 8 || !strings.Contains(strings.Join(record.Callers, " "), "liveness."+entry) {
				t.Errorf("liveness fallback WARN lacks real entry chain: %s", line)
			}
		case "DEBUG":
			counts[record.Operation]++
			if record.Count != 2 {
				t.Errorf("liveness fallback count missing: %s", line)
			}
		default:
			t.Errorf("unexpected diagnostic: %s", line)
		}
	}
	for _, operation := range []string{"process CPU", "process open files"} {
		if warnings[operation] != 1 || counts[operation] != 1 {
			t.Errorf("%s cadence WARN=%d DEBUG=%d, want 1/1", operation, warnings[operation], counts[operation])
		}
	}
}
