// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
	"github.com/agenxy/dibs/internal/testexec"
)

func TestUnreadNotifyWithdrawalThroughMCPAndRestart(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, presentation := range []string{"unread", "announced", "inbox", "hook-body", "ack"} {
			t.Run(version+"/"+presentation, func(t *testing.T) {
				dir := t.TempDir()
				srv, _, stop := questionWithdrawalServer(t, dir, version)
				lead := questionWithdrawalOK(t, srv, version, "register", map[string]any{"name": "lead"})["token"]
				worker := questionWithdrawalOK(t, srv, version, "register", map[string]any{"name": "worker", "session_id": "notify-withdraw-worker"})["token"]
				body := "withdrawable FYI"
				if presentation == "announced" {
					body = strings.Repeat("unpresented FYI ", 1000)
				}
				n := questionWithdrawalOK(t, srv, version, "send", map[string]any{
					"token": lead, "to": "worker", "type": "notify", "body": body,
				})["msg_serial"]
				switch presentation {
				case "inbox":
					questionWithdrawalOK(t, srv, version, "inbox", map[string]any{"token": worker})
				case "announced", "hook-body":
					event := "UserPromptSubmit"
					if presentation == "hook-body" {
						event = "Stop"
					}
					shown := questionWithdrawalOK(t, srv, version, "hook_poll", map[string]any{
						"session_id": "notify-withdraw-worker", "event": event,
					})
					if presentation == "announced" && (!strings.Contains(fmt.Sprint(shown), "read_mail") || strings.Contains(fmt.Sprint(shown), body)) {
						t.Fatalf("setup: expected a pointer without the complete FYI: %v", shown)
					}
				case "ack":
					questionWithdrawalOK(t, srv, version, "ack", map[string]any{"token": worker, "msg_serial": n})
				}
				result := questionWithdrawalCall(t, srv, version, "respond", map[string]any{
					"token": lead, "msg_serial": n, "disposition": "withdraw", "body": "no longer relevant",
				})
				if presentation != "unread" && presentation != "announced" {
					if result["__is_error"] != true || result["code"] != "E_MSG_FINAL" ||
						!strings.Contains(fmt.Sprint(result["hint"]), "presented") {
						t.Fatalf("presented notify withdrawal must explain its refusal: %v", result)
					}
					stop()
					return
				}
				if result["__is_error"] == true || result["state"] != "withdrawn" {
					t.Fatalf("unread notify withdrawal: %v", result)
				}
				for _, phase := range []string{"live", "restart"} {
					if phase == "restart" {
						stop()
						srv, _, stop = questionWithdrawalServer(t, dir, version)
					}
					for _, actor := range []map[string]any{
						{"session_id": "notify-withdraw-worker", "event": "Stop"},
						{"token": worker},
					} {
						name := "hook_poll"
						if actor["token"] != nil {
							name = "check_in"
						}
						got := questionWithdrawalOK(t, srv, version, name, actor)
						if got["decision"] == "block" || strings.Contains(fmt.Sprint(got["agent_updates"]), "withdrew") ||
							strings.Contains(fmt.Sprint(got), "outstanding update units") {
							t.Errorf("%s retained withdrawn FYI delivery: %v", phase, got)
						}
					}
				}
				mail := questionWithdrawalOK(t, srv, version, "read_mail", map[string]any{"token": lead, "msg_serial": n})
				if mail["message"].(map[string]any)["state"] != "withdrawn" {
					t.Fatalf("withdrawal was lost: %v", mail)
				}
				stop()
			})
		}
	}
}

func TestUnreadInformationalWithdrawalClearsContactAndSenderStop(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "codex")
	if out, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("build queue fixture: %v %s", err, out)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, kind := range []string{"notify", "handoff"} {
			t.Run(version+"/"+kind, func(t *testing.T) {
				testcodexipc.Start(t, "no-owner", nil)
				previous := harnessenv.RealShower
				harnessenv.RealShower = harnessenv.Shower{
					Holds: func(string) bool { return false },
					Open:  func([]string) error { return errors.New("fixture open failure") },
				}
				t.Cleanup(func() { harnessenv.RealShower = previous })
				dir := t.TempDir()
				t.Setenv("DIBS_DIR", dir)
				srv, eng, stop := questionWithdrawalServer(t, dir, version)
				eng.SetWakeCommands(map[string]engine.WakeCommand{"codex": {Argv: []string{
					binary, "queue", "--thread", "{thread}", "--message", "{message}",
				}}})
				worker := toolCallWithMeta(t, srv, "register", map[string]any{
					"name": "worker", "session_id": testcodexipc.Thread, "harness": "Codex",
				}, map[string]any{SurfaceMetaKey: harnessenv.ChatGPTApp})
				if worker["token"] == nil {
					t.Fatalf("setup registration: %v", worker)
				}
				lead := questionWithdrawalOK(t, srv, version, "register", map[string]any{
					"name": "lead", "session_id": "notify-withdraw-lead",
				})["token"]
				args := map[string]any{"token": lead, "to": "worker", "type": kind, "body": "pending evidence"}
				if kind == "notify" {
					args["priority"] = "high"
				}
				n := questionWithdrawalOK(t, srv, version, "send", args)["msg_serial"]
				until := time.Now().Add(5 * time.Second)
				for {
					board, err := eng.Board(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					if alerts := board["contact_alerts"]; strings.Contains(fmt.Sprint(alerts), "worker") {
						break
					}
					if time.Now().After(until) {
						t.Fatalf("setup: app failure did not produce a contact: %v", board)
					}
					<-time.After(5 * time.Millisecond)
				}
				before := questionWithdrawalOK(t, srv, version, "hook_poll", map[string]any{
					"session_id": "notify-withdraw-lead", "event": "UserPromptSubmit",
				})
				if !strings.Contains(fmt.Sprint(before), "outstanding update units") {
					t.Fatalf("setup: sender was not warned: %v", before)
				}
				r := questionWithdrawalCall(t, srv, version, "respond", map[string]any{
					"token": lead, "msg_serial": n, "disposition": "withdraw",
				})
				if r["__is_error"] == true || r["state"] != "withdrawn" {
					t.Fatalf("withdraw notify after failed delivery: %v", r)
				}
				for _, phase := range []string{"live", "restart"} {
					if phase == "restart" {
						stop()
						srv, eng, stop = questionWithdrawalServer(t, dir, version)
					}
					board, err := eng.Board(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					if board["contact_alerts"] != nil {
						t.Errorf("%s retained contact after its only source was withdrawn: %v", phase, board["contact_alerts"])
					}
					for _, event := range []string{"Stop", "UserPromptSubmit", "Stop"} {
						got := questionWithdrawalOK(t, srv, version, "hook_poll", map[string]any{
							"session_id": "notify-withdraw-lead", "event": event,
						})
						if strings.Contains(fmt.Sprint(got), "outstanding update units") || got["decision"] == "block" {
							t.Errorf("%s %s re-raised withdrawn source: %v", phase, event, got)
						}
					}
				}
				stop()
			})
		}
	}
}
