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
	"time"

	"github.com/agenxy/dibs/internal/paths"
)

// Configure is the entry point. Only native calls, process startup and the
// installer's wait are substituted; planning, stopping, registry selection,
// fleet proof and transaction recovery run normally against private fixtures.
func TestConfigureAppServiceLoadedRefresh(t *testing.T) {
	for _, scenario := range []string{"retry-serves", "retry-refused", "retry-register-error", "native-takes-over", "removal-refused"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAppServiceTestFixture(t)
			if err := os.WriteFile(f.state, []byte("enabled"), 0o600); err != nil {
				t.Fatal(err)
			}
			oldStopped := f.liveManual(t)
			reader, executor, manual, wait := readAppServiceJob, executeAppServiceCommand, startManualAppRecovery, waitForAppServiceRefresh
			t.Cleanup(func() {
				readAppServiceJob, executeAppServiceCommand = reader, executor
				startManualAppRecovery, waitForAppServiceRefresh = manual, wait
			})
			registers, unregisters, starts, waits := 0, 0, 0, 0
			var fallbackStopped <-chan struct{}
			var nativeStopped <-chan struct{}
			nativeOwns := false
			startManualAppRecovery = func(installed, dir, unit string, was daemonState) error {
				starts++
				if installed != f.root || dir != f.dir || unit != "" || hostPort(was.addr) != os.Getenv("DIBS_TEST_APP_SERVICE_ADDR") {
					t.Fatal("fallback changed the installed build, board directory, address or mode")
				}
				state, err := os.ReadFile(f.state)
				want := "enabled"
				if starts > 1 && scenario == "retry-register-error" {
					want = "not-registered"
				}
				if err != nil || string(state) != want {
					t.Errorf("failed registration residence lost before fallback: got %s want %s (%v)", state, want, err)
				}
				fallbackStopped = f.liveManual(t)
				f.unavailable.Store(0)
				return nil
			}
			waitForAppServiceRefresh = func(delay time.Duration) {
				waits++
				if delay != 20*time.Second || starts != 1 || registers != 1 || unregisters != 1 {
					t.Errorf("loaded residence was not one 20 s hold: %s starts=%d registers=%d unregisters=%d", delay, starts, registers, unregisters)
				}
				if appServiceFixturePID(t, f.dir) == 0 {
					t.Fatal("no fallback owns the board during the hold")
				}
				if board, err := fleetSnapshotAt(f.addr); err != nil || board.Serial != 7 {
					t.Fatalf("board unavailable during loaded residence: %+v %v", board, err)
				}
				if scenario == "native-takes-over" {
					if err := stopDaemon(f.dir); err != nil {
						t.Fatal(err)
					}
					nativeStopped = f.liveManual(t)
					nativeOwns = true
				}
			}
			executeAppServiceCommand = func(cmd *exec.Cmd) ([]byte, error) {
				if cmd.Args[1] == "--service-unregister" {
					unregisters++
					if waits > 0 {
						select {
						case <-fallbackStopped:
						default:
							t.Error("fallback PID still alive when native retry removed the job")
						}
						if scenario == "removal-refused" {
							return nil, errors.New("fixture removal refused")
						}
					}
				}
				out, err := cmd.CombinedOutput()
				if err == nil && cmd.Args[1] == "--service-register" {
					registers++
					switch {
					case registers == 1 || scenario == "retry-refused":
						f.unavailable.Store(3)
					case scenario == "retry-register-error":
						return nil, errors.New("fixture second registration refused")
					default:
						nativeStopped = f.liveManual(t)
						nativeOwns = true
						f.unavailable.Store(0)
					}
				}
				return out, err
			}
			readAppServiceJob = func() ([]byte, error) {
				if registers == 0 {
					return nil, errAppServiceNotLoaded
				}
				if nativeOwns {
					return launchOutcomeFixture("native", 1, "running", "", fmt.Sprintf("\tpid = %d\n", appServiceFixturePID(t, f.dir))), nil
				}
				return launchOutcomeFixture(fmt.Sprint(registers), registers, "spawn failed", "OS_REASON_CODESIGNING", ""), nil
			}
			var result error
			out := capturedUpgradeOutput(t, func() { result = serviceCommand(nil) })
			<-oldStopped
			if waits != 1 || strings.Count(out, "waiting 20 s before registering the service again") != 1 {
				t.Errorf("missing bounded, serving hold: waits=%d\n%s", waits, out)
			}
			if scenario == "retry-serves" || scenario == "native-takes-over" {
				wantRegisters := 2
				if scenario == "native-takes-over" {
					wantRegisters = 1
				}
				if result != nil || registers != wantRegisters || starts != 1 || f.requests.Load() != 1 || strings.Count(out, "upgraded:") != 1 {
					t.Errorf("native recovery did not commit once: %v registers=%d manual=%d permission=%d\n%s", result, registers, starts, f.requests.Load(), out)
				}
				select {
				case <-nativeStopped:
					t.Error("verified native owner was stopped")
				default:
				}
			} else {
				wantRegisters := 2
				if scenario == "removal-refused" {
					wantRegisters = 1
				}
				if result == nil || registers != wantRegisters || starts != 2 || f.requests.Load() != 0 || strings.Contains(out, "upgraded:") {
					t.Errorf("failed retry exceeded bound or lost fallback: %v registers=%d manual=%d permission=%d\n%s", result, registers, starts, f.requests.Load(), out)
				}
				if result != nil && (!strings.Contains(result.Error(), "serving unsupervised") || strings.Contains(result.Error(), "in the foreground") || strings.Contains(result.Error(), "again in a minute")) {
					t.Errorf("final error contradicts recovery: %v", result)
				}
				if scenario == "retry-register-error" && (result == nil || !strings.Contains(result.Error(), "fixture second registration refused") || !strings.Contains(result.Error(), "OS_REASON_CODESIGNING")) {
					t.Errorf("recovery discarded a launch or registration failure: %v", result)
				}
				if board, err := fleetSnapshotAt(f.addr); err != nil || board.Serial != 7 || appServiceFixturePID(t, f.dir) == 0 {
					t.Errorf("failed retry did not leave the board serving: %+v %v", board, err)
				}
				if scenario == "retry-refused" {
					state, err := os.ReadFile(f.state)
					if err != nil || string(state) != "enabled" || unregisters != 2 {
						t.Errorf("second refused registration was removed or retried: %s %v removes=%d", state, err, unregisters)
					}
				}
			}
		})
	}
}

// Read the public process registry, so the same guard also compiles against
// the original code before daemonState gained a PID field.
func appServiceFixturePID(t *testing.T, dir string) int {
	t.Helper()
	live, err := paths.LiveDaemons()
	if err != nil {
		t.Fatal(err)
	}
	mine, _, err := selectDaemon(live, dir)
	if err != nil || mine == nil {
		return 0
	}
	return mine.PID
}
