// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

func TestOldHostBridgeNeverReceivesANavigatingAppWake(t *testing.T) {
	for _, nativeOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "cannot-negotiate-native-only", true: "native-only-capable"}[nativeOnly], func(t *testing.T) {
			e := New(core.NewState("host-background", core.DefaultLimits()), &memLedger{}, deadProber{})
			requests, release, err := e.AttachHostBridgeCapabilities("remote", []string{"codex"}, nil, 3, true, nativeOnly)
			if err != nil {
				t.Fatal("setup:", err)
			}
			defer release()
			plan := wakePlan{host: "remote", request: WakeRequest{Host: "remote", Agent: "worker", Harness: "codex", Surface: harnessenv.ChatGPTApp}}
			_, id := e.enqueueRemoteWake(&plan, "worker")
			if !nativeOnly {
				if id != 0 || !plan.routeRefused {
					t.Fatalf("old app bridge received a navigating plan: id=%d refused=%v", id, plan.routeRefused)
				}
				return
			}
			if id == 0 {
				t.Fatal("safe native-only delivery was lost")
			}
			select {
			case request := <-requests:
				if !request.NoNavigation {
					t.Fatal("old bridge received permission to navigate")
				}
			default:
				t.Fatal("setup: accepted handoff was not queued")
			}
		})
	}
}
