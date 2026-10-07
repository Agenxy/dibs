// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"io"
	"log/slog"
	"testing"

	"github.com/agenxy/dibs/internal/logs"
)

// The real strict hook must keep deferred progress, without calling that
// deliberate deferral lost information. Capture through the daemon's actual
// redacting handler, including both stateless 2026 and legacy callers.
func TestStrictHookDeferredProgressIsDebugThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, event := range []string{"Stop", "SubagentStop"} {
			t.Run(version+"/"+event, func(t *testing.T) {
				assertDeferredHookDiagnosis(t, version, event)
			})
		}
	}
}

func strictHookLogRing(t *testing.T, level slog.Level) *logs.Ring {
	t.Helper()
	ring := logs.NewRing(32)
	previous := slog.Default()
	slog.SetDefault(slog.New(logs.NewHandler(
		slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: level}), ring)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return ring
}

func assertDeferredHookDiagnosis(t *testing.T, version, event string) {
	t.Helper()
	dir := t.TempDir()
	srv, _, _ := restartableQueueServer(t, dir)
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		out := toolCallOn(t, srv, version, name, args)
		if out["error"] != nil || out["code"] != nil {
			t.Fatalf("setup %s: %v", name, out)
		}
		return out
	}
	lead := call("register", map[string]any{
		"name": "lead", "nonce": "strict-log-lead", "session_id": "strict-log-session",
	})
	worker := call("register", map[string]any{"name": "worker", "nonce": "strict-log-worker"})
	leadToken, _ := lead["token"].(string)
	workerToken, _ := worker["token"].(string)
	leadID, _ := lead["agent_id"].(string)
	if leadToken == "" || workerToken == "" || leadID == "" {
		t.Fatal("setup: registration returned no token or identity")
	}
	call("check_in", map[string]any{"token": leadToken})
	call("check_in", map[string]any{"token": workerToken})
	request := call("send", map[string]any{
		"token": leadToken, "to": "worker", "type": "request",
		"body": "work", "milestones": []string{"one"},
	})
	serial, _ := request["msg_serial"].(float64)
	if serial <= 0 {
		t.Fatal("setup: request returned no serial")
	}
	approved := call("respond", map[string]any{
		"token": workerToken, "msg_serial": serial, "disposition": "approve",
	})
	if approved["state"] != "approved" {
		t.Fatalf("setup: request was not approved: %v", approved)
	}
	call("check_in", map[string]any{"token": leadToken})
	const marker = "strict-hook-retained-progress"
	progress := call("respond", map[string]any{
		"token": workerToken, "msg_serial": serial, "disposition": "progress",
		"milestone": 1, "body": marker,
	})
	if progress["ok"] != true {
		t.Fatalf("setup: progress was not recorded: %v", progress)
	}
	args := map[string]any{"session_id": "strict-log-session", "event": event}
	loose := call("hook_poll", args)
	if loose["agent"] != leadID || loose["queued"] == nil || loose["reason"] != nil {
		t.Fatalf("setup: did not reach the actual deferred-progress branch: %v", loose)
	}
	before := readStopLedger(t, dir)
	args["strict_output"] = true
	debug := strictHookLogRing(t, slog.LevelDebug)
	strict := call("hook_poll", args)
	if strict["agent"] != nil || strict["queued"] != nil || strict["decision"] != nil ||
		strict["reason"] != nil || strict["hookSpecificOutput"] != nil {
		t.Fatalf("strict deferred hook carried diagnosis or model context: %v", strict)
	}
	info := strictHookLogRing(t, slog.LevelInfo)
	call("hook_poll", args)
	after := readStopLedger(t, dir)
	if after.Serial != before.Serial {
		t.Fatalf("deferred hook mutated the ledger: %d -> %d", before.Serial, after.Serial)
	}
	if !mentions(call("check_in", map[string]any{"token": leadToken}), marker) {
		t.Fatal("the held progress was lost instead of reaching the next check-in")
	}
	if mentions(call("check_in", map[string]any{"token": leadToken}), marker) {
		t.Fatal("the same progress was delivered twice")
	}
	for _, record := range info.Tail(0) {
		if record.Msg == "dropped from a strict hook response: the caller's schema cannot carry it, and it is not nothing" {
			t.Fatalf("deliberate progress deferral was reported as lost information at INFO: %+v", record)
		}
	}
	assertDebugHookFields(t, debug, event)
}

func assertDebugHookFields(t *testing.T, ring *logs.Ring, event string) {
	t.Helper()
	found := map[string]bool{}
	for _, record := range ring.Tail(0) {
		field, _ := record.Attrs["field"].(string)
		if record.Level == "DEBUG" && record.Attrs["event"] == event {
			found[field] = true
		}
	}
	if !found["agent"] || !found["queued"] {
		t.Fatalf("debug diagnosis must identify both omitted fields and hook event after redaction: %v", ring.Tail(0))
	}
}
