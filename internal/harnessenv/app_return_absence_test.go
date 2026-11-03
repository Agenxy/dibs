// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnownedRestartWithoutReadableStoreRemainsOpenable(t *testing.T) {
	for _, mode := range []string{"missing-store", "unreadable-store"} {
		t.Run(mode, func(t *testing.T) {
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
			s := Shower{Open: func(argv []string) error {
				if argv[len(argv)-1] != "codex://threads/new" {
					opened++
				}
				return nil
			}}
			for range 2 {
				_, _, _ = s.LoadChatGPTThreads([]string{thread}, func(string) (bool, error) { return false, nil }, 0)
			}
			if opened != 2 {
				t.Fatalf("unknown rollout absence permanently blocked thread: %d opens", opened)
			}
		})
	}
}
