// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestRealBridgeRetriesUnavailableClaudeSidecar(t *testing.T) {
	const session = "1194ac0e-d041-4fde-9454-b93d7aa7288d"
	for _, mode := range []string{"absent", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryStdio(t)
			f.thread = "" // Claude supplies the sidecar and no Codex threadId.
			parent := strconv.Itoa(os.Getpid())
			path := filepath.Join(f.dir, ".claude", "sessions", parent+".json")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if mode == "truncated" {
				if err := os.WriteFile(path, []byte(`{"sessionId":`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			call := func() map[string]any {
				return recoveryStdioOK(t, f.call("register", map[string]any{
					"name": "late-sidecar", "nonce": "late-sidecar-nonce", "cwd": f.dir,
				}))
			}
			first := call()
			current := func(id any) string {
				t.Helper()
				board, err := f.eng.Board(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range board["agents"].([]map[string]any) {
					if row["id"] == id {
						value, _ := row["sessions"].(map[string]any)["current"].(string)
						return value
					}
				}
				t.Fatal("setup: registered agent missing from board")
				return ""
			}
			if got := current(first["agent_id"]); got != "host-"+parent {
				t.Fatalf("setup: initial call did not use its parent placeholder: %q", got)
			}
			if err := os.WriteFile(path, []byte(`{"sessionId":"`+session+`","entrypoint":"claude-desktop"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			recoveryStdioOK(t, f.call("check_in", map[string]any{"token": first["token"]}))
			if got := current(first["agent_id"]); got != session {
				t.Fatalf("bridge retained %q after its parent published %q", got, session)
			}
		})
	}
}
