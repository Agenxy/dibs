// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"strings"
	"testing"
)

// All setup and observations enter through the real MCP door. The stable
// fields must remain ids; additive names are the human-facing address.
func TestRenamedAddressIsPresentedAcrossBothMCPVersions(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _, _ := aliasReplayServer(t, t.TempDir())
			if version == "2025-11-25" {
				out := rpc(t, srv, "", "initialize", map[string]any{"protocolVersion": version})
				if result(t, out, "legacy rename setup")["protocolVersion"] != version {
					t.Fatal("legacy handshake did not select the requested version")
				}
			}
			call := func(name string, args map[string]any) map[string]any {
				t.Helper()
				r := toolCallOn(t, srv, version, name, args)
				if r["code"] != nil || r["__is_error"] == true {
					t.Fatalf("%s failed: %v", name, r)
				}
				return r
			}
			worker := call("register", map[string]any{"name": "worker-id", "nonce": "rename-worker"})
			id, tok := worker["agent_id"].(string), worker["token"].(string)
			sender := call("register", map[string]any{"name": "sender-id", "nonce": "rename-sender"})
			senderID, senderTok := sender["agent_id"].(string), sender["token"].(string)
			call("check_in", map[string]any{"token": tok})
			call("check_in", map[string]any{"token": senderTok})
			call("update", map[string]any{"token": tok, "name": "former-name"})
			call("update", map[string]any{"token": tok, "name": "current-name"})
			collision := toolCallOn(t, srv, version, "update", map[string]any{
				"token": senderTok, "name": id,
			})
			if collision["code"] != "E_NAME_TAKEN" ||
				!strings.Contains(collision["message"].(string), "current-name") {
				t.Fatalf("id collision did not name its current holder: %v", collision)
			}
			board := call("board", map[string]any{"token": senderTok, "detail": true})["board"].(map[string]any)
			found := false
			for _, raw := range board["agents"].([]any) {
				row := raw.(map[string]any)
				if row["id"] == id {
					found = row["name"] == "current-name"
				}
			}
			if !found {
				t.Fatalf("current name missing from board: %v", board)
			}
			for _, target := range []string{"current-name", id, "former-name"} {
				sent := call("send", map[string]any{
					// A handoff stays actionable across both reads. Complete FYIs
					// are consumed by the first bounded recipient presentation.
					"token": senderTok, "to": target, "type": "handoff", "body": "one mailbox",
				})
				note, _ := sent["addressed"].(string)
				if sent["to_name"] != "current-name" ||
					(target != id && !strings.Contains(note, "current-name")) {
					t.Fatalf("%q did not present the current address: %v", target, sent)
				}
				for _, readTool := range []string{"inbox", "check_in"} {
					box := call(readTool, map[string]any{"token": tok})
					items := box["inbox"].([]any)
					found := false
					for _, item := range items {
						m := item.(map[string]any)
						if m["serial"] == sent["msg_serial"] {
							found = m["to"] == id && m["to_name"] == "current-name" &&
								m["from"] == senderID && m["from_name"] == "sender-id"
						}
					}
					if !found {
						t.Fatalf("%s did not preserve ids and present current names: %v", readTool, box)
					}
				}
				read := call("read_mail", map[string]any{"token": tok, "msg_serial": sent["msg_serial"]})
				mail := read["message"].(map[string]any)
				if mail["to"] != id || mail["from"] != senderID ||
					mail["to_name"] != "current-name" || mail["from_name"] != "sender-id" {
					t.Fatalf("%q changed stable identity or omitted names: %v", target, mail)
				}
			}
		})
	}
}

func TestReleasedFormerNameReassignmentIsExplicitThroughBothMCPVersions(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _, _ := aliasReplayServer(t, t.TempDir())
			if version == "2025-11-25" {
				rpc(t, srv, "", "initialize", map[string]any{"protocolVersion": version})
			}
			call := func(name string, args map[string]any) map[string]any {
				t.Helper()
				r := toolCallOn(t, srv, version, name, args)
				if r["code"] != nil || r["__is_error"] == true {
					t.Fatalf("%s failed: %v", name, r)
				}
				return r
			}
			former := call("register", map[string]any{"name": "first-id", "nonce": "first-owner"})
			firstTok := former["token"].(string)
			call("check_in", map[string]any{"token": firstTok})
			call("update", map[string]any{"token": firstTok, "name": "reused-name"})
			call("update", map[string]any{"token": firstTok, "name": "now-first"})
			call("update", map[string]any{"token": firstTok, "release_names": []string{"reused-name"}})
			second := call("register", map[string]any{"name": "second-id", "nonce": "second-owner"})
			secondTok := second["token"].(string)
			call("check_in", map[string]any{"token": secondTok})
			call("update", map[string]any{"token": secondTok, "name": "reused-name"})
			sender := call("register", map[string]any{"name": "sender-id", "nonce": "reused-sender"})
			senderTok := sender["token"].(string)
			call("check_in", map[string]any{"token": senderTok})
			sent := call("send", map[string]any{
				"token": senderTok, "to": "reused-name", "type": "notify", "body": "new owner",
			})
			note, _ := sent["addressed"].(string)
			if sent["to_name"] != "reused-name" ||
				!strings.Contains(note, "now-first") || !strings.Contains(note, "second-id") {
				t.Fatalf("reused name did not identify new and former holders: %v", sent)
			}
			read := call("read_mail", map[string]any{"token": secondTok, "msg_serial": sent["msg_serial"]})
			mail := read["message"].(map[string]any)
			if mail["to"] != second["agent_id"] || mail["to_name"] != "reused-name" {
				t.Fatalf("reused name reached the wrong mailbox: %v", mail)
			}
			miss := toolCallOn(t, srv, version, "read_mail", map[string]any{
				"token": firstTok, "msg_serial": sent["msg_serial"],
			})
			if miss["code"] != "E_NOT_YOUR_MESSAGE" {
				t.Fatalf("former holder could read reassigned mail: %v", miss)
			}
		})
	}
}
