// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/paths"
)

// Drive configure's real planning, signed-payload preflight, stop, native
// registration and board verification. Only the external native boundary is
// substituted; no test sets the diagnostic field it is meant to exercise.
func TestConfigureAppServiceLaunchOutcome(t *testing.T) {
	for _, scenario := range []string{
		"new-job-refused", "new-uuid-refused", "new-run-refused",
		"stale-refusal", "running-with-old-refusal", "unknown-probe",
		"foreign-job", "nested-reason", "missing-reason", "successful",
	} {
		t.Run(scenario, func(t *testing.T) {
			f := newAppServiceTestFixture(t)
			if err := os.WriteFile(f.state, []byte("enabled"), 0o600); err != nil {
				t.Fatal(err)
			}
			stopped := f.liveManual(t)
			reader, executor := readAppServiceJob, executeAppServiceCommand
			t.Cleanup(func() { readAppServiceJob, executeAppServiceCommand = reader, executor })
			registrations, observations := 0, 0
			executeAppServiceCommand = func(cmd *exec.Cmd) ([]byte, error) {
				out, err := cmd.CombinedOutput()
				if err == nil && cmd.Args[1] == "--service-register" {
					registrations++
					if registrations == 2 && strings.HasSuffix(scenario, "refused") && f.unavailable.Load() != 3 {
						t.Error("fresh refusal waited for board polls before recovery")
					}
					// A brief unavailable board bounds the old-code control too:
					// it eventually recovers externally, but that must not hide
					// the freshly observed terminal refusal of this attempt.
					if registrations == 1 && scenario != "successful" {
						f.unavailable.Store(3)
					} else {
						f.unavailable.Store(0)
					}
				}
				return out, err
			}
			readAppServiceJob = func() ([]byte, error) {
				observations++
				if scenario == "unknown-probe" {
					return nil, errors.New("fixture probe unavailable")
				}
				if registrations == 0 {
					if scenario == "new-job-refused" || scenario == "successful" || scenario == "foreign-job" || scenario == "nested-reason" || scenario == "missing-reason" {
						return nil, errAppServiceNotLoaded
					}
					return launchOutcomeFixture("old", 1, "spawn failed", "OS_REASON_CODESIGNING", ""), nil
				}
				if registrations > 1 || scenario == "running-with-old-refusal" || scenario == "successful" {
					return launchOutcomeFixture("old", 2, "running", "OS_REASON_CODESIGNING", "\tpid = 123\n"), nil
				}
				uuid, runs := "old", 1
				if scenario == "new-uuid-refused" {
					uuid = "new"
				}
				if scenario == "new-run-refused" {
					runs = 2
				}
				body := launchOutcomeFixture(uuid, runs, "spawn failed", "OS_REASON_CODESIGNING", "")
				switch scenario {
				case "foreign-job":
					body = []byte(strings.Replace(string(body), appServiceLabel, "other.service", 1))
				case "nested-reason":
					body = []byte(strings.Replace(string(body), "\tlast exit reason", "\t\tlast exit reason", 1))
				case "missing-reason":
					body = []byte(strings.Replace(string(body), "\tlast exit reason = OS_REASON_CODESIGNING\n", "", 1))
				}
				return body, nil
			}
			var result error
			out := capturedUpgradeOutput(t, func() { result = serviceCommand(nil) })
			<-stopped
			trace, err := os.ReadFile(f.trace)
			if err != nil || !strings.Contains(string(trace), `"mode":"unregister"`) || registrations == 0 {
				t.Fatalf("setup did not enter upgrade registration: %v %s", err, trace)
			}
			refused := strings.HasSuffix(scenario, "refused")
			if refused {
				if result == nil || !strings.Contains(result.Error(), "OS_REASON_CODESIGNING") || !strings.Contains(result.Error(), "dibs configure --service") {
					t.Errorf("fresh native refusal was hidden: %v\n%s", result, out)
				}
				if observations < 2 || registrations != 2 || f.requests.Load() != 0 {
					t.Errorf("refusal/recovery path: observations=%d registrations=%d permission=%d", observations, registrations, f.requests.Load())
				}
				if strings.Contains(out, "upgraded:") || !strings.Contains(out, "recovered:") || !strings.Contains(out, "NEW build") || !strings.Contains(out, "still failed") {
					t.Errorf("failed upgrade falsely reported success or hid recovery: %s", out)
				}
			} else if result != nil || registrations != 1 || strings.Count(out, "upgraded:") != 1 || f.requests.Load() != 1 {
				t.Errorf("non-current/unknown outcome must keep board proof: %v registrations=%d permission=%d\n%s", result, registrations, f.requests.Load(), out)
			}
		})
	}
}

func launchOutcomeFixture(uuid string, runs int, state, reason, extra string) []byte {
	return fmt.Appendf(nil, "gui/%d/%s = {\n\tstate = spawn scheduled\n\tBTM uuid = %s\n\truns = %d\n\tjob state = %s\n\tlast exit reason = %s\n%s}\n", os.Getuid(), appServiceLabel, uuid, runs, state, reason, extra)
}

func TestConfigureFailedAppMigrationDoesNotReportUpgradedAfterRecovery(t *testing.T) {
	f := newAppServiceTestFixture(t)
	stopped := f.liveManual(t)
	t.Setenv("DIBS_TEST_APP_SERVICE_RESULT", "requires-approval")
	previous := startManualAppRecovery
	t.Cleanup(func() { startManualAppRecovery = previous })
	recovered := false
	startManualAppRecovery = func(_, dir, unit string, _ daemonState) error {
		recovered = true
		if dir != f.dir || unit != "" {
			t.Fatal("changed recovery target")
		}
		return nil
	}
	var result error
	out := capturedUpgradeOutput(t, func() { result = serviceCommand(nil) })
	<-stopped
	if result == nil || !recovered {
		t.Fatalf("did not enter failed cutover recovery: %v %t", result, recovered)
	}
	if strings.Contains(out, "upgraded:") || !strings.Contains(out, "recovered:") || !strings.Contains(out, "still failed") {
		t.Fatalf("recovery contradicted failed transaction: %s", out)
	}
	if _, found, err := paths.PinnedService(); found || err != nil {
		t.Fatalf("failed migration pin: %t %v", found, err)
	}
}

func TestConfigureAppServiceReportsSuccessOnlyAfterReceiptCommit(t *testing.T) {
	f := newAppServiceTestFixture(t)
	stopped := f.liveManual(t)
	legacy := f.legacy(t, "")
	if err := os.WriteFile(legacy+".migrated", []byte("different prior receipt"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := runAppLaunchctl
	t.Cleanup(func() { runAppLaunchctl = previous })
	var transitions []string
	runAppLaunchctl = func(args ...string) ([]byte, error) {
		transitions = append(transitions, args[0])
		if args[0] == "print" {
			return []byte("Could not find service"), errors.New("fixture job absent")
		}
		return nil, nil
	}
	var result error
	out := capturedUpgradeOutput(t, func() { result = serviceCommand(nil) })
	<-stopped
	if result == nil || !strings.Contains(result.Error(), "legacy migration receipt changed") {
		t.Fatalf("did not reach post-verification receipt failure: %v\n%s", result, out)
	}
	if strings.Join(transitions, ",") != "bootout,print,bootstrap,kickstart" {
		t.Fatal("legacy recovery did not run:", transitions)
	}
	if strings.Contains(out, "upgraded:") || !strings.Contains(out, "recovered:") || !strings.Contains(out, "still failed") || f.requests.Load() != 0 {
		t.Fatalf("uncommitted upgrade reported success: requests=%d\n%s", f.requests.Load(), out)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatal("failed migration lost original supervisor:", err)
	}
}

func capturedUpgradeOutput(t *testing.T, run func()) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	stdout, stderr := os.Stdout, os.Stderr
	defer func() { os.Stdout, os.Stderr = stdout, stderr }()
	os.Stdout, os.Stderr = f, f
	run()
	body, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
