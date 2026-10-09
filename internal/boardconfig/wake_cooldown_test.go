// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package boardconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemovedWakeCooldownIsRefusedByPresence(t *testing.T) {
	for _, value := range []string{`"90s"`, `"0s"`, `""`, `0`, `"-1s"`} {
		t.Run(value, func(t *testing.T) {
			dir := t.TempDir()
			body := "[wake.exec.codex]\nargv = [\"codex\", \"queue\"]\ncooldown = " + value + "\n"
			if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(dir)
			if err == nil || !strings.Contains(err.Error(), "[wake.exec.codex] cooldown") ||
				!strings.Contains(err.Error(), "removed") || !strings.Contains(err.Error(), "Remove") {
				t.Fatalf("retired key needs its corrective refusal regardless of value: %v", err)
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
