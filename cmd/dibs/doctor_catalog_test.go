// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
)

func TestDoctorNamesConnectedMismatchedAndUnknownCatalogs(t *testing.T) {
	start := harnessenv.ProcessStart(os.Getpid())
	if start == "" {
		t.Skip("process incarnation inventory unavailable")
	}
	for _, hash := range []string{"", "older", "current"} {
		t.Run("hash="+hash, func(t *testing.T) {
			b := &boardView{HostID: hostID(), BridgeToolCatalogs: []engine.BridgeToolCatalog{{PID: os.Getpid(), StartedAt: start, Hash: hash, CurrentHash: "current", Agents: []string{"worker"}}}}
			var warnings []string
			check := func() {
				checkBridgeToolCatalogs(b, func(what, fix string) { warnings = append(warnings, what+" "+fix) })
			}
			check()
			if hash == "current" {
				if len(warnings) != 0 {
					t.Fatal(warnings)
				}
			} else if len(warnings) != 1 || !strings.Contains(warnings[0], "worker") || !strings.Contains(warnings[0], "start a new chat") || !strings.Contains(warnings[0], "coordinator") {
				t.Fatalf("dishonest/missing corrective: %v", warnings)
			}
			warnings = nil
			b.BridgeToolCatalogs[0].StartedAt = "another-incarnation"
			check()
			if len(warnings) != 0 {
				t.Fatal("disconnected incarnation named as connected")
			}
			b.HostID = "another-machine"
			check()
			if len(warnings) != 0 {
				t.Fatal("foreign PID treated as local")
			}
		})
	}
}
