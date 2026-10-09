// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package boardconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExistingWakeCooldownLoadsWithoutTakingEffect(t *testing.T) {
	for _, value := range []string{`"90s"`, `"0s"`, `""`, `0`, `"-1s"`} {
		t.Run(value, func(t *testing.T) {
			dir := t.TempDir()
			body := "[wake.exec.codex]\nargv = [\"codex\", \"queue\"]\ncooldown = " + value + "\n"
			if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(dir)
			if err != nil {
				t.Fatal("old operator configuration must still boot:", err)
			}
			if cfg.Wake.Exec["codex"].Cooldown != 0 {
				t.Fatal("retired key still takes effect")
			}
		})
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte("[wake.exec.codex]\nargv = [\"codex\", \"queue\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err != nil {
		t.Fatal("omitted cooldown must remain valid:", err)
	}
}

func TestNewCooldownOverrideIsRefusedWithoutWriting(t *testing.T) {
	for _, value := range []string{"90s", "0s", "", "0", "-1s"} {
		t.Run(value, func(t *testing.T) {
			dir := t.TempDir()
			err := SaveOverride(dir, "wake.exec.codex.cooldown", value, "fixture-writer")
			if err == nil || !strings.Contains(err.Error(), "removed") ||
				!strings.Contains(err.Error(), "Remove the cooldown line") {
				t.Errorf("controlled config writer did not refuse retired key: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, OverridesName)); !os.IsNotExist(err) {
				t.Fatalf("retired config key was written: %v", err)
			}
		})
	}
}
