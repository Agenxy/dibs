// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"testing"

	"github.com/agenxy/dibs/internal/selfupdate"
)

func TestGuestContractFloorDoesNotGatePrivateFleet(t *testing.T) {
	prior := selfupdate.GuestSupportingMinimum
	selfupdate.GuestSupportingMinimum = "v0.0.9"
	t.Cleanup(func() { selfupdate.GuestSupportingMinimum = prior })
	srv, cancel := newServer(t)
	defer cancel()
	for _, method := range []string{"server/discover", "tools/list"} {
		out := rpc(t, srv, "2026-07-28", method, map[string]any{})
		if out["error"] != nil || out["result"] == nil {
			t.Fatalf("guest floor gated private fleet %s: %v", method, out)
		}
	}
}
