// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/boardconfig"
	"github.com/agenxy/dibs/internal/mcp"
)

func TestRemoteBridgeDoesNotAdvertiseRetiredCooldown(t *testing.T) {
	b := newWakeBridge("", "", "fixture", map[string]boardconfig.WakeExec{
		"codex": {Argv: []string{"codex", "queue"}, Cooldown: 30 * time.Minute},
	})
	var body struct {
		Params struct {
			Meta map[string]any `json:"_meta"`
		}
	}
	if err := json.Unmarshal(b.listenBody(), &body); err != nil {
		t.Fatal(err)
	}
	if value, present := body.Params.Meta[mcp.WakeCooldownsMetaKey]; present {
		t.Fatalf("bridge still asks the hub to pace wakes: %v", value)
	}
	if body.Params.Meta[mcp.WakeHarnessesMetaKey] == nil {
		t.Fatal("setup: lost the bridge's actual harnesses")
	}
}
