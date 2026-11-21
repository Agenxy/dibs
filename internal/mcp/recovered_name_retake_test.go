// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"fmt"
	"strings"
	"testing"
)

func TestOnlyTheSurvivorCanRetakeItsRecoveredName(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			f := recoveryFixture(t, version)
			old, survivor := f.source["agent_id"], f.into["agent_id"]
			token := f.into["token"]
			const work = "keep the survivor's own declaration"
			f.call("declare", map[string]any{"token": token, "text": work})
			serial := coordinatorRecoveryRequest(f)
			approve := f.call("respond", map[string]any{"token": token, "msg_serial": serial, "disposition": "approve"})
			if approve["recovered"] == nil {
				t.Fatal("setup: no recovery transition")
			}
			sendToOldName := func(text string) {
				t.Helper()
				sent := f.call("send", map[string]any{
					"token": f.lead["token"], "to": old, "type": "notify", "body": text,
				})
				read := f.call("read_mail", map[string]any{"token": token, "msg_serial": sent["msg_serial"]})
				if message := read["message"].(map[string]any); message["to"] != survivor || message["body"] != text {
					t.Fatalf("mail to the recovered address did not reach the survivor: %v", message)
				}
			}
			sendToOldName("before the rename")
			refused := f.raw("update", map[string]any{"token": f.lead["token"], "name": old})
			if refused["code"] != "E_NAME_TAKEN" {
				t.Fatalf("setup: another live identity could take the reserved address: %v", refused)
			}
			got := f.raw("update", map[string]any{"token": token, "name": old})
			if got["ok"] != true {
				t.Fatalf("survivor could not take its own recovered address: %v", got)
			}
			if got["id"] != survivor || got["name"] != old || got["renamed_from"] != "worker-recovery" {
				t.Fatalf("rename did not stay on the survivor: %v", got)
			}
			hint, _ := refused["hint"].(string)
			if !strings.Contains(hint, "update(name") || !strings.Contains(hint, "own recovered") {
				t.Fatalf("other identity was given no corrective rename guidance: %v", refused)
			}
			f.stop()
			f.srv, f.e, f.stop = supportedMergeServer(t, f.dir)
			f.e.SetHumanNotifier(desktopFixture{available: false})
			board, err := f.e.Board(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			assertRetakenNameRows(t, board["agents"].([]map[string]any), old, survivor, work)
			sendToOldName("after replay")
			assertMergedHistory(f)
			if r := f.raw("resume", map[string]any{"nonce": "source-secret", "resume_id": "retired-source"}); r["code"] != "E_SEAT_MERGED" {
				t.Fatalf("retaking a label revived the retired credential: %v", r)
			}
		})
	}
}

func assertRetakenNameRows(t *testing.T, agents []map[string]any, old, survivor any, work string) {
	t.Helper()
	found := false
	for _, row := range agents {
		if row["id"] == old && fmt.Sprint(row["status"]) == "active" {
			t.Fatal("replay revived the retired seat")
		}
		if row["id"] == survivor {
			found = true
			sessions, _ := row["sessions"].(map[string]any)
			if row["name"] != old || sessions["current"] != "worker-session" || !mentions(row, work) {
				t.Fatalf("replay lost the renamed identity, session or declaration: %v", row)
			}
		}
	}
	if !found {
		t.Fatal("replay lost the survivor")
	}
}
