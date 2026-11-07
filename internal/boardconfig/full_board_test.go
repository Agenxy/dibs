// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package boardconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFullBoardPolicyLoadsAndRefusesInvalidValues(t *testing.T) {
	for _, value := range []string{"displace_idle", "refuse", "discard"} {
		t.Run(value, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte("[limits]\nfull_board = \""+value+"\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(dir)
			if value == "discard" {
				if err == nil {
					t.Fatal("invalid full_board accepted")
				}
				return
			}
			if err != nil || cfg.Limits.FullBoard != value {
				t.Fatalf("load: %q %v", cfg.Limits.FullBoard, err)
			}
		})
	}
}
