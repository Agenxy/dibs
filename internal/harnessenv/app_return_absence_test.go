// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

// An unavailable local store cannot quarantine an ordinary admitted wake.
func TestUnownedReturnWithoutReadableStoreRemainsWakeable(t *testing.T) {
	for _, mode := range []string{"missing-store", "unreadable-store"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				t.Setenv("DIBS_DIR", t.TempDir())
				home := t.TempDir()
				t.Setenv("CODEX_HOME", home)
				if mode == "unreadable-store" {
					if err := os.WriteFile(filepath.Join(home, "sessions"), []byte("not a directory"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				const thread = "01a0f45e-cbf8-7ef0-acb8-79c25cb4343d"
				opened := 0
				s := Shower{Ownership: func(string) ThreadOwnership { return ThreadOwnership{Running: true, Known: true, Epoch: "app:A"} }, Open: func(argv []string) error {
					if len(argv) != 3 || argv[1] != "-g" || argv[2] != "codex://threads/wake/"+thread {
						t.Errorf("unexpected wake: %q", argv)
					}
					opened++
					return nil
				}}
				for i := 0; i < 2; i++ {
					if i > 0 {
						synctest.Sleep(AppOpenRetryAfter + time.Second)
					}
					if did, err := s.Show(OpenArgv(ChatGPTApp, thread), thread); err != nil || !did {
						t.Fatalf("eligible wake blocked: %v %v", did, err)
					}
				}
				stale, err := ChatGPTBindingStale(thread)
				if opened != 2 || stale || err != nil {
					t.Fatalf("unknown store quarantined a thread: opens=%d stale=%v err=%v", opened, stale, err)
				}
			})
		})
	}
}
