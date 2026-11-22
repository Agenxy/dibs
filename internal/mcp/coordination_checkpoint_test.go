// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCheckInOffersBoundedCoordinationView(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _ := newServer(t)
			call := func(name string, args map[string]any) map[string]any {
				t.Helper()
				return toolCallOn(t, srv, version, name, args)
			}
			reader := registerOn(t, srv, version, "reader")
			worker := registerOn(t, srv, version, "amy-worker")
			call("check_in", map[string]any{"token": worker})
			for i, work := range []string{"Coordinate the gate", "Review the decision", "Check a follow-up"} {
				decl := call("declare", map[string]any{
					"token": worker, "text": work, "activity": "review", "waiting": "ci",
					"refs": []string{fmt.Sprintf("pr:%d", i+60)}, "holds": []string{fmt.Sprintf("port:%d", i+4600)},
				})
				if decl["slot_id"] == nil {
					t.Fatalf("declaration setup: %v", decl)
				}
			}
			for i := range 2 {
				sent := call("send", map[string]any{
					"token": worker, "to": "reader", "type": "request", "body": fmt.Sprintf("mail %d", i),
				})
				if sent["msg_serial"] == nil {
					t.Fatalf("mail setup: %v", sent)
				}
			}
			claim := call("claim", map[string]any{"token": worker, "path": "/e2e/ruling", "mode": "exclusive"})
			if claim["granted"] != true {
				t.Fatalf("claim setup: %v", claim)
			}
			res := call("check_in", map[string]any{"token": reader, "board_view": "coordination", "limit": 1})
			board := asMap(res["board"])
			if board["detail"] != "coordination" || board["agents_total"] != float64(2) {
				t.Fatalf("check_in did not select the coordination projection: %v", board)
			}
			claims := asMaps(board["claims"])
			if len(claims) != 1 || claims[0]["agent"] != "amy-worker" || claims[0]["mode"] != "exclusive" {
				t.Fatalf("coordination view lost the claim scope: %v", board)
			}
			if len(asMaps(res["inbox"])) != 1 || res["next_cursor"] == nil || res["serial"] == nil {
				t.Fatalf("board projection changed the atomic mailbox checkpoint: %v", res)
			}
			rows := asMaps(board["agents"])
			if len(rows) != 2 || rows[0]["id"] != "amy-worker" {
				t.Fatalf("coordination roster did not preserve the agent rows: %v", board)
			}
			row := rows[0]
			slots := asMaps(row["declarations"])
			if row["slots"] != float64(3) || len(slots) != 2 ||
				slots[0]["waiting"] != "ci" || !strings.Contains(fmt.Sprint(slots[0]["refs"]), "pr:60") ||
				!strings.Contains(fmt.Sprint(slots[0]["holds"]), "port:4600") {
				t.Errorf("coordination view lost its bounded declaration details: %v", row)
			}
			full := call("check_in", map[string]any{"token": reader, "detail": true})
			fullRows := asMaps(asMap(full["board"])["agents"])
			if len(fullRows) != 2 || len(asMaps(fullRows[0]["slots"])) != 3 {
				t.Fatalf("detail:true lost the full declarations: %v", fullRows)
			}
			compact := call("check_in", map[string]any{"token": reader})
			compactRows := asMaps(asMap(compact["board"])["agents"])
			if len(compactRows) != 2 || compactRows[0]["doing"] != "Coordinate the gate" ||
				compactRows[0]["declarations"] != nil {
				t.Errorf("default checkpoint changed its existing compact roster: %v", compactRows)
			}

			for _, bad := range []map[string]any{
				{"board_view": "unknown"}, {"board_view": "coordination", "detail": true},
			} {
				bad["token"] = reader
				if result := call("check_in", bad); result["code"] != "E_BAD_ARG" {
					t.Errorf("invalid board selection was accepted: %v", result)
				}
			}
		})
	}
}

func TestCoordinationCheckpointCapsLargeBoardJSON(t *testing.T) {
	srv, _ := newServer(t)
	const version = "2026-07-28"
	reader := registerOn(t, srv, version, "reader")
	for i := range 36 {
		name := fmt.Sprintf("agent-%02d", i)
		actor := registerOn(t, srv, version, name)
		ackOn(t, srv, version, actor)
		claim := toolCallOn(t, srv, version, "claim", map[string]any{
			"token": actor, "path": "/e2e/" + name + "/" + strings.Repeat("segment/", 60), "mode": "shared",
		})
		if claim["granted"] != true {
			t.Fatalf("large claim roster setup: %v", claim)
		}
		for slot := range 2 {
			result := toolCallOn(t, srv, version, "declare", map[string]any{
				"token": actor, "text": strings.Repeat("<&>", 90),
				"refs": []string{fmt.Sprintf("pr:%d", 4000+i*2+slot)},
			})
			if result["slot_id"] == nil {
				t.Fatalf("large roster setup: %v", result)
			}
		}
	}
	full := toolCallOn(t, srv, version, "check_in", map[string]any{"token": reader, "detail": true})
	raw, err := json.Marshal(full["board"])
	if err != nil || len(raw) <= 24*1024 {
		t.Fatalf("setup: expected more than 24 KiB of full board JSON: %d bytes (%v)", len(raw), err)
	}
	fullBytes := len(raw)
	result := toolCallOn(t, srv, version, "check_in", map[string]any{"token": reader, "board_view": "coordination"})
	board := asMap(result["board"])
	raw, err = json.Marshal(board)
	shown := len(asMaps(board["agents"]))
	claims := len(asMaps(board["claims"]))
	if err != nil || len(raw) > 24*1024 || shown == 0 || shown >= 37 || board["agents_total"] != float64(37) {
		t.Fatalf("bounded board should show a counted roster prefix: %d bytes, %d of %v agents (%v)",
			len(raw), shown, board["agents_total"], err)
	}
	if claims == 0 || claims >= 36 || board["claims_total"] != float64(36) {
		t.Fatalf("expected a counted claim prefix, got %d of %v claims", claims, board["claims_total"])
	}
	t.Logf("board JSON reduced from %d to %d bytes with %d agents and %d claims shown", fullBytes, len(raw), shown, claims)
}
