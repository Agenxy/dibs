// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestAppServiceIncompleteOutputIsUnknown(t *testing.T) {
	t.Run("service status", func(t *testing.T) {
		previous := executeAppServiceCommand
		t.Cleanup(func() { executeAppServiceCommand = previous })
		executeAppServiceCommand = func(cmd *exec.Cmd) ([]byte, error) {
			if cmd.Args[1] != "--service-status" || cmd.WaitDelay <= 0 {
				t.Fatalf("wrong or unbounded service query: %q", cmd.Args)
			}
			return []byte(`{"status":"enabled"}`), exec.ErrWaitDelay
		}
		_, err := appServiceCall(t.TempDir(), "status")
		if !errors.Is(err, exec.ErrWaitDelay) || !strings.Contains(err.Error(), "incomplete output from dibs-notify") {
			t.Fatalf("incomplete service reply became a tool verdict: %v", err)
		}
	})
	t.Run("loaded job", func(t *testing.T) {
		previous := appServiceDiagnostic
		t.Cleanup(func() { appServiceDiagnostic = previous })
		appServiceDiagnostic = func(cmd *exec.Cmd) ([]byte, error) {
			if cmd.Args[1] != "print" || cmd.WaitDelay <= 0 {
				t.Fatalf("wrong or unbounded launchctl query: %q", cmd.Args)
			}
			return []byte("Could not find service"), exec.ErrWaitDelay
		}
		_, err := readLoadedAppService()
		if errors.Is(err, errAppServiceNotLoaded) || !errors.Is(err, exec.ErrWaitDelay) || !strings.Contains(err.Error(), "incomplete output from launchctl") {
			t.Fatalf("incomplete inventory became evidence of absence: %v", err)
		}
	})
}
