// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
)

// Enter through the writer and the daemon's actual reconciliation function.
// Merely checking alias resolution or comparing two fingerprints would not
// prove the grant wiring or its refusal after a current name shadows an alias.
func TestDeclaredRoleFollowsFormerNameButNotItsShadowingOwner(t *testing.T) {
	eng, ctx := testEngine(t)
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, err := eng.Do(ctx, op)
		if err != nil {
			t.Fatal("setup:", err)
		}
		return r
	}
	owner := do(&core.Op{Kind: core.OpRegister, Name: "worker-id", Nonce: "alias-role-owner"})
	peer := do(&core.Op{Kind: core.OpRegister, Name: "peer-id", Nonce: "alias-role-peer"})
	ownerID, peerID := owner["agent_id"].(string), peer["agent_id"].(string)
	ownerToken, peerToken := owner["token"].(string), peer["token"].(string)
	do(&core.Op{Kind: core.OpUpdate, Token: ownerToken, Name: "configured-label"})
	do(&core.Op{Kind: core.OpUpdate, Token: ownerToken, Name: "current-worker"})
	cfg := RolesConfig{
		Admin:    []string{"configured-label"},
		Identity: map[string]string{"configured-label": engine.RolePinFingerprint("alias-role-owner")},
	}
	pinDir := t.TempDir()
	applyDeclaredRoles(ctx, eng, cfg, loadRolePins(pinDir))
	if !holdsRole(t, eng, ownerID, core.RoleAdmin) {
		t.Fatal("configured former name did not grant its pinned owner's role")
	}
	do(&core.Op{Kind: core.OpUpdate, Token: peerToken, Name: "configured-label"})
	// Reload the on-disk pin as a restart would; the new current-name owner
	// must not inherit the former owner's standing privilege.
	applyDeclaredRoles(ctx, eng, cfg, loadRolePins(pinDir))
	if holdsRole(t, eng, peerID, core.RoleAdmin) || holdsRole(t, eng, peerID, core.RoleCoordinator) {
		t.Fatal("a shadowing current-name owner inherited the alias owner's standing role")
	}
	if !holdsRole(t, eng, ownerID, core.RoleAdmin) {
		t.Fatal("a name shadow revoked the still-authorized identity's role")
	}
}
