// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradeRetiresIdleDelayAndCooldownWithOriginalSourceLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dibs.toml")
	before := "# retained comment\nwake.'open_app_after_idle' = '''\n10m\n'''\n" +
		"[wake.exec.codex]\nargv = [\"codex\", \"queue\"]\ncooldown = \"90s\"\n# retained ending\n"
	after := "# retained comment\n[wake.exec.codex]\nargv = [\"codex\", \"queue\"]\n# retained ending\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	stops := 0
	p := idleCutoverPlan(t, dir, func(string) error {
		stops++
		got, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read mixed migration before stop: %w", err)
		}
		if string(got) != after {
			return fmt.Errorf("mixed migration not complete before stop: %q", got)
		}
		backups, err := filepath.Glob(filepath.Join(dir, "dibs.toml.before-wake-settings-*"))
		if err != nil {
			return fmt.Errorf("find mixed backup before stop: %w", err)
		}
		if len(backups) != 1 {
			return fmt.Errorf("backup missing before stop: %v", backups)
		}
		got, err = os.ReadFile(backups[0])
		if err != nil {
			return fmt.Errorf("read mixed backup: %w", err)
		}
		if string(got) != before {
			return fmt.Errorf("mixed backup not exact: %q", got)
		}
		return nil
	})
	out, err := captureStdout(t, p.cutover)
	if err != nil || stops != 1 {
		t.Fatalf("mixed cutover: stops=%d %v", stops, err)
	}
	for _, want := range []string{
		"removed [wake] open_app_after_idle from " + path + ":2: wake.'open_app_after_idle' = '''\n10m\n'''",
		"removed [wake.exec.codex] cooldown from " + path + ":7: cooldown = \"90s\"",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("original-source receipt lacks %q: %s", want, out)
		}
	}
}
