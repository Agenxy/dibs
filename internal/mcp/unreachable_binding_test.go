// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
	"github.com/agenxy/dibs/internal/testexec"
)

func TestUnavailableCodexBindingInSendAndCheckIn(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "codex")
	if out, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("setup: queue executable: %v: %s", err, out)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	for _, mode := range []string{"missing", "gone"} {
		t.Run(mode, func(t *testing.T) {
			home, dir := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CODEX_HOME", home)
			t.Setenv("DIBS_DIR", dir)
			testcodexipc.Start(t, "no-owner", nil)
			previous := harnessenv.RealShower
			harnessenv.RealShower = harnessenv.Shower{
				Holds: func(string) bool { return true },
				Open:  func([]string) error { t.Error("unavailable binding attempted app navigation"); return nil },
			}
			t.Cleanup(func() { harnessenv.RealShower = previous })
			srv, eng, _ := restartableQueueServer(t, dir)
			eng.SetWakeCommands(map[string]engine.WakeCommand{"codex": {Argv: []string{
				binary, "queue", "--thread", "{thread}", "--message", "{message}",
			}}})
			thread, want := "host-9655", "No local ChatGPT app thread is known"
			if mode == "gone" {
				thread, want = testcodexipc.Thread, "no rollout found"
				t.Setenv("DIBS_TEST_QUEUE_ERROR_THREAD", thread)
				t.Setenv("DIBS_TEST_QUEUE_ERROR", "thread/queue/add failed: no rollout found for thread id "+thread+" (code -32603)")
			}
			worker := registerObservationAppWorker(t, srv, eng, thread, home)
			sender := sendNoteCall(t, srv, "register", map[string]any{"name": "sender"})["token"]
			sendNoteCall(t, srv, "check_in", map[string]any{"token": sender})
			aliasSweep(t, eng, "queue-worker")
			send := func() map[string]any {
				t.Helper()
				return sendNoteCall(t, srv, "send", map[string]any{
					"token": sender, "to": "queue-worker", "type": "question", "body": "unreachable fixture",
				})
			}
			if mode == "gone" {
				send()
				awaitUnavailableBindingObservation(t, eng)
			}
			sent := send()
			note, _ := sent["note"].(string)
			if sent["msg_serial"] == nil || !strings.Contains(note, want) || !strings.Contains(note, "Start a local app thread") ||
				strings.Contains(note, "in progress") || strings.Contains(note, "when it next wakes") {
				t.Errorf("send did not disclose the unavailable binding and recovery: %v", sent)
			}
			check := func(projection string, row map[string]any) {
				t.Helper()
				wake, _ := row["wake"].(string)
				if fmt.Sprint(row["status"]) != "unreachable" || !strings.Contains(wake, want) ||
					!strings.Contains(wake, "Start a local app thread") || !strings.HasSuffix(note, wake) {
					t.Errorf("%s disagreed with the send's binding remedy: status=%v wake=%q", projection, row["status"], wake)
				}
			}
			check("board", aliasRow(t, eng, "queue-worker"))
			for _, args := range []map[string]any{
				{"token": sender}, {"token": sender, "board_view": "coordination"},
			} {
				checkpoint := sendNoteCall(t, srv, "check_in", args)
				check("check_in", bindingCheckpointRow(t, checkpoint))
			}
			response := rpc(t, srv, "2026-07-28", "tools/call", map[string]any{
				"name": "board", "arguments": map[string]any{"token": sender},
			})
			panel := asMap(asMap(asMap(response["result"])["_meta"])["com.dibs/panel"])
			check("board panel", bindingCheckpointRow(t, panel))
			if state := readStopLedger(t, dir).Agents["queue-worker"].Status; state != core.StatusDormant {
				t.Errorf("binding view changed the replayable lifecycle state to %q", state)
			}
			const nextThread = "0199a0b1-c2d3-4e5f-8a9b-0c1d2e3f4a5c"
			rebound := toolCallWithMeta(t, srv, "update", map[string]any{
				"token": worker["token"], "description": "real new binding",
			}, map[string]any{"threadId": nextThread})
			if rebound["code"] != nil {
				t.Fatalf("setup: current binding did not change: %v", rebound)
			}
			if row := aliasRow(t, eng, "queue-worker"); fmt.Sprint(row["status"]) == "unreachable" ||
				strings.Contains(fmt.Sprint(row["wake"]), "no rollout found") {
				t.Errorf("a new binding inherited an earlier refusal: %v", row)
			}
		})
	}
}

func bindingCheckpointRow(t *testing.T, checkpoint map[string]any) map[string]any {
	t.Helper()
	for _, row := range asMaps(asMap(checkpoint["board"])["agents"]) {
		if row["id"] == "queue-worker" {
			return row
		}
	}
	t.Fatal("setup: compact checkpoint omitted queue-worker")
	return nil
}

func awaitUnavailableBindingObservation(t *testing.T, eng *engine.Engine) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		row := aliasRow(t, eng, "queue-worker")
		if queue, _ := row["queue_wake"].(core.Result); queue["thread_state"] == "gone" {
			return
		}
		<-time.After(10 * time.Millisecond)
	}
	t.Fatal("setup: actual app queue command never recorded its explicit refusal")
}
