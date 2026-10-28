// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

// Only the bridge ingress can set the transient trust verdict. Validation
// stays here; replay folds the recorded decision without re-admitting it.
func admitBridgeSurface(op *Op) error {
	if !op.BridgeSurfaceEvidence {
		return nil
	}
	if !op.BridgeSurfaceTrusted || op.Kind != OpUpdate || op.Agent == nil || op.Agent.Surface == "" ||
		*op.Agent != (AgentInfo{Surface: op.Agent.Surface}) || op.Name != "" || op.Description != "" ||
		bridgeSurfaceChangesBindings(op) {
		return errf("E_BAD_BRIDGE_SURFACE", "send surface evidence only through the local bridge metadata path",
			"bridge surface evidence must be a trusted surface-only update")
	}
	return nil
}

func bridgeSurfaceChangesBindings(op *Op) bool {
	return op.NoProcess || op.ReleaseSession || len(op.ReleaseNames) > 0 || op.PID != 0 ||
		op.SessionAlias != "" || op.SessionTakenFrom != "" || op.SessionAliasTakenFrom != ""
}
