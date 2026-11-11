// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package harnessenv

import (
	"context"
	"os"
	"testing"

	"github.com/agenxy/dibs/internal/procinfo"
)

func TestAppEpochSelectsDesktopProcessesBeforeReadingPaths(t *testing.T) {
	rows, err := procinfo.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var own procinfo.Process
	for _, row := range rows {
		if row.PID == os.Getpid() {
			own = row
		}
	}
	if own.PID == 0 {
		t.Fatal("setup: own kernel row missing")
	}
	for _, tc := range []struct {
		name    string
		desktop bool
	}{
		{"ChatGPT", true}, {"ChatGPT Helper", true}, {"ChatGPT Helper (", true},
		{"Codex (GPU)", true}, {"Codex (Renderer", true},
		{"codex", true}, {"chrome_crashpad", true},
		{"ChatGPT for Chro", false}, {"ChatGPT for Chr", false}, {"ChatGPT for Chrome", false},
		{"ChatGPT plugin", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldSnapshot, oldPath, oldFallback := nativeProcessSnapshot, nativeProcessPath, processPathFallback
			t.Cleanup(func() {
				nativeProcessSnapshot, nativeProcessPath, processPathFallback = oldSnapshot, oldPath, oldFallback
			})
			row := own
			row.Comm = tc.name
			nativeProcessSnapshot = func(context.Context) ([]procinfo.Process, error) {
				return []procinfo.Process{row}, nil
			}
			reads, fallbacks := 0, 0
			nativeProcessPath = func(int) (string, error) { reads++; return "", os.ErrPermission }
			processPathFallback = func(context.Context, int) (string, error) { fallbacks++; return "", os.ErrPermission }
			epoch, known := ChatGPTAppEpoch()
			if tc.desktop {
				if known || epoch != "" || reads != 1 || fallbacks != 1 {
					t.Fatalf("denied desktop path lost unknown evidence: epoch=%q known=%v reads=%d fallbacks=%d",
						epoch, known, reads, fallbacks)
				}
			} else if !known || epoch != "absent" || reads != 0 || fallbacks != 0 {
				t.Fatalf("unrelated process triggered desktop path observation: epoch=%q known=%v reads=%d fallbacks=%d",
					epoch, known, reads, fallbacks)
			}
		})
	}
}
