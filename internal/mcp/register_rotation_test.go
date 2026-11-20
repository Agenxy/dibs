// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import "testing"

func TestRegisterAdvanceSignalsCredentialRotationThroughMCP(t *testing.T) {
	for _, era := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(era, func(t *testing.T) {
			srv, _, _ := newServerWithEngine(t)
			register := func(session string) map[string]any {
				return recoveryOK(t, recoveryCall(t, srv, era, "register", map[string]any{
					"name": "moves", "nonce": "same-worker", "session_id": session,
				}, nil))
			}
			first := register("b79e65ec-454b-4195-a10f-c474bb5035ff")
			const nextSession = "439f3469-4e55-47ab-b6b9-03095111e471"
			moved := register(nextSession)
			if moved["agent_id"] != first["agent_id"] || moved["resumed"] != true || moved["token"] == first["token"] {
				t.Fatal("setup: the same row did not rotate on the real session move")
			}
			retry := register(nextSession)
			if retry["token"] != moved["token"] || retry["token_rotated"] == true {
				t.Fatal("an identical retry rotated the credential or claimed that it did")
			}
			if moved["token_rotated"] != true {
				t.Fatal("register moved and revoked the old token without disclosing the rotation")
			}
		})
	}
}
