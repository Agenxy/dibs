package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestRegisterDefaultsToRosterAndOffersDetailThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, detail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/detail=%v", version, detail), func(t *testing.T) {
				srv, _, _ := aliasReplayServer(t, t.TempDir())
				long := strings.Repeat("a long public declaration with identity details. ", 100)
				for n := range 3 {
					_, token := aliasRegister(t, srv, fmt.Sprintf("worker-%d", n), fmt.Sprintf("register-peer-%d", n))
					aliasCall(t, srv, "declare", map[string]any{"token": token, "text": long})
				}
				args := map[string]any{"name": "reader", "nonce": "register-reader"}
				if detail {
					args["detail"] = true
				}
				r := toolCallOn(t, srv, version, "register", args)
				if r["token"] == "" || r["token"] == nil || r["agent_id"] != "reader" {
					t.Fatalf("registration/detail contract lost identity: %v", r)
				}
				board, ok := r["board"].(map[string]any)
				if !ok {
					t.Fatalf("registration lost its board: %v", r)
				}
				rows, ok := board["agents"].([]any)
				if !ok || len(rows) != 4 {
					t.Fatalf("registration summary omitted peers: %v", board)
				}
				for _, row := range rows {
					agent := row.(map[string]any)
					_, full := agent["agent"]
					if full != detail || agent["id"] == "" || agent["status"] == "" {
						t.Fatalf("wrong detail projection: %v", agent)
					}
				}
				if !detail {
					full := toolCallOn(t, srv, version, "check_in", map[string]any{"token": r["token"], "detail": true})
					compactBytes, err := json.Marshal(board)
					if err != nil {
						t.Fatal(err)
					}
					fullBytes, err := json.Marshal(full["board"])
					if err != nil {
						t.Fatal(err)
					}
					if len(compactBytes)*2 >= len(fullBytes) {
						t.Fatalf("register still charges for other agents' full notes: %d vs %d", len(compactBytes), len(fullBytes))
					}
				}
			})
		}
	}
}
