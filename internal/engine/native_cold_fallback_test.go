// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

func TestNativePreInputFailureQueuesOriginalMailThroughSend(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("build fixture %v: %s", err, b)
	}
	for _, handled := range []bool{false, true} {
		t.Run(map[bool]string{false: "owed", true: "already-read"}[handled], func(t *testing.T) {
			t.Setenv("DIBS_DIR", t.TempDir())
			previous := shower
			var opens atomic.Int32
			shower = &harnessenv.Shower{
				Holds: func(string) bool { return false },
				Open:  func([]string) error { opens.Add(1); return nil },
			}
			t.Cleanup(func() { shower = previous })
			ready := make(chan struct{})
			var e *Engine
			var ctx context.Context
			var worker string
			var serial uint64
			s := testcodexipc.Start(t, "changed", func() {
				<-ready
				if handled {
					if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckMessage, Token: worker, MsgSerial: serial}); err != nil {
						t.Error("setup ack:", err)
					}
				}
			})
			var sender string
			e, ctx, worker, sender = nativeAppEngine(t)
			e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{
				binary, "queue", "--thread", "{thread}", "--message", "{message}",
			}}})
			serial = nativeAppSend(t, e, ctx, sender)
			close(ready)
			waitWakeDone(t, e, "worker")
			if len(s.Inputs()) != 0 {
				t.Fatal("failed owner discovery submitted native input")
			}
			b, err := os.ReadFile(filepath.Join(os.Getenv("CODEX_HOME"), "pending.json"))
			if handled {
				if !os.IsNotExist(err) || opens.Load() != 0 {
					t.Fatalf("already read mail entered cold queue: %s %v", b, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("pre-input failure left owed mail without a cold wake: %v", err)
			}
			if opens.Load() != 1 {
				t.Fatalf("cold fallback failed to reach app-open path: %d", opens.Load())
			}
			var rows []struct {
				Thread string `json:"threadId"`
				Input  []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			if json.Unmarshal(b, &rows) != nil || len(rows) != 1 || rows[0].Thread != testcodexipc.Thread ||
				len(rows[0].Input) != 1 {
				t.Fatalf("fallback duplicated, misrouted or leaked private facts: %s", b)
			}
			text := rows[0].Input[0].Text
			stamp := strings.TrimSuffix(strings.TrimPrefix(text, "Dibs: notify notice issued at "), ". It may already be handled.")
			if _, err = time.Parse(time.RFC3339, stamp); err != nil {
				t.Fatalf("fallback lost timestamped metadata-only notice: %s", text)
			}
		})
	}
}
