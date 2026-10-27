// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/paths"
)

func TestConfigureEnabledAppServiceRequiresItsOwnRunningDaemonForNoOp(t *testing.T) {
	for _, scenario := range []string{"owned", "failed", "failed-approval", "different-pid", "unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAppServiceTestFixture(t)
			if err := serviceCommand(nil); err != nil {
				t.Fatal("first configuration:", err)
			}
			stopped := f.liveManual(t)
			live, err := paths.LiveDaemons()
			if err != nil || len(live) != 1 {
				t.Fatalf("private daemon setup: %v %v", live, err)
			}
			pid := live[0].PID
			reader := readAppServiceJob
			t.Cleanup(func() { readAppServiceJob = reader })
			manual := startManualAppRecovery
			t.Cleanup(func() { startManualAppRecovery = manual })
			manualStarts := 0
			startManualAppRecovery = func(installed, dir, unit string, _ daemonState) error {
				manualStarts++
				state, err := os.ReadFile(f.state)
				if installed != f.root || dir != f.dir || unit != "" || err != nil || string(state) != "not-registered" {
					t.Fatal("recovery changed the manual serving mode or preceded removal")
				}
				return nil
			}
			if scenario == "failed-approval" {
				t.Setenv("DIBS_TEST_APP_SERVICE_RESULT", "requires-approval")
			}
			readAppServiceJob = func() ([]byte, error) {
				switch scenario {
				case "unavailable":
					return nil, errors.New("fixture native observation unavailable")
				case "failed", "failed-approval":
					return launchOutcomeFixture("current", 1, "spawn failed", "OS_REASON_CODESIGNING", ""), nil
				case "different-pid":
					return launchOutcomeFixture("current", 1, "running", "", fmt.Sprintf("\tpid = %d\n", pid+1)), nil
				default:
					return launchOutcomeFixture("current", 1, "running", "", fmt.Sprintf("\tpid = %d\n", pid)), nil
				}
			}
			var result error
			out := capturedUpgradeOutput(t, func() { result = serviceCommand(nil) })
			if scenario == "failed-approval" {
				if result == nil || manualStarts != 1 || !strings.Contains(out, "previous service mode is serving") {
					t.Fatalf("registered manual serving did not recover its mode: %v manual=%d %s", result, manualStarts, out)
				}
				select {
				case <-stopped:
				default:
					t.Fatal("manual daemon was not stopped")
				}
				return
			}
			if result != nil || manualStarts != 0 {
				t.Fatal("repeat configure:", result, out)
			}
			trace, err := os.ReadFile(f.trace)
			if err != nil {
				t.Fatal(err)
			}
			registers := strings.Count(string(trace), `"mode":"register"`)
			if scenario == "owned" {
				if registers != 1 || !strings.Contains(out, "nothing to do") || f.requests.Load() != 1 {
					t.Fatalf("owned current service restarted: %s\n%s", trace, out)
				}
				select {
				case <-stopped:
					t.Fatal("owned service was stopped")
				default:
				}
			} else {
				if registers != 2 || strings.Contains(out, "nothing to do") || !strings.Contains(string(trace), `"mode":"unregister"`) || f.requests.Load() != 2 {
					t.Errorf("enabled registration hid unsupervised serving: %s\n%s", trace, out)
				}
				select {
				case <-stopped:
				default:
					t.Error("configure did not stop the old serving PID")
				}
			}
		})
	}
}
