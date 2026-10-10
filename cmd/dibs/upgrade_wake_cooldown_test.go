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

const upgradeCooldownBefore = "# retained operator notes\r\n[wake.exec.codex]\r\nargv = [\"codex\", \"queue\"]\r\n" +
	"cooldown = \"90s\" # obsolete\r\n# retain this too\r\n"

const upgradeCooldownAfter = "# retained operator notes\r\n[wake.exec.codex]\r\nargv = [\"codex\", \"queue\"]\r\n" +
	"# retain this too\r\n"

func cooldownCutoverPlan(t *testing.T, dir string, stop func(string) error) *plan {
	t.Helper()
	return &plan{
		dir: dir, serving: true, installed: filepath.Join(dir, "dibd"), stop: stop,
		start: func(_, _, _ string, _ daemonState) error { return nil }, confirm: func(string) error { return nil },
	}
}

// Enter the same cutover that stops the daemon: no migration setter or helper
// called by the test can make a dead production wire pass this assertion.
func TestUpgradeBacksUpAndRemovesCooldownBeforeStopping(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dibs.toml")
	if err := os.WriteFile(path, []byte(upgradeCooldownBefore), 0o600); err != nil {
		t.Fatal(err)
	}
	stops := 0
	p := cooldownCutoverPlan(t, dir, func(string) error {
		stops++
		got, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read migrated config: %w", err)
		}
		if string(got) != upgradeCooldownAfter {
			return fmt.Errorf("config not migrated before stop: %q", got)
		}
		backups, err := filepath.Glob(filepath.Join(dir, "dibs.toml.before-wake-settings-*"))
		if err != nil {
			return fmt.Errorf("find backup: %w", err)
		}
		if len(backups) != 1 {
			return fmt.Errorf("backup not written before stop: %v", backups)
		}
		backup, err := os.ReadFile(backups[0])
		if err != nil {
			return fmt.Errorf("read backup: %w", err)
		}
		if string(backup) != upgradeCooldownBefore {
			return fmt.Errorf("backup differs from exact old file: %q", backup)
		}
		return nil
	})
	output, err := captureStdout(t, p.cutover)
	if err != nil {
		t.Fatal(err)
	}
	if stops != 1 {
		t.Fatalf("cutover stopped %d times", stops)
	}
	backups, err := filepath.Glob(filepath.Join(dir, "dibs.toml.before-wake-settings-*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup: %v %v", backups, err)
	}
	for _, want := range []string{
		backups[0], "removed [wake.exec.codex] cooldown from " + path + ":4:",
		`cooldown = "90s" # obsolete`,
	} {
		if !strings.Contains(output, want) {
			t.Errorf("migration receipt lacks %q: %s", want, output)
		}
	}
	if err := p.cutover(); err != nil {
		t.Fatal("idempotent second cutover:", err)
	}
	again, err := filepath.Glob(filepath.Join(dir, "dibs.toml.before-wake-settings-*"))
	if err != nil || len(again) != 1 {
		t.Fatalf("repeat migration created another backup: %v %v", again, err)
	}
}

func TestUnwritableCooldownFailsUpgradeWithoutStopping(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dibs.toml")
	if err := os.WriteFile(path, []byte(upgradeCooldownBefore), 0o400); err != nil {
		t.Fatal(err)
	}
	stops, starts := 0, 0
	p := cooldownCutoverPlan(t, dir, func(string) error { stops++; return nil })
	p.start = func(_, _, _ string, _ daemonState) error { starts++; return nil }
	err := p.cutover()
	if err == nil || !strings.Contains(err.Error(), "nothing has been stopped") {
		t.Errorf("missing pre-stop rewrite refusal: %v", err)
	}
	if stops != 0 || starts != 0 {
		t.Errorf("unwritable configuration stopped %d / started %d daemons", stops, starts)
	}
	got, rerr := os.ReadFile(path)
	if rerr != nil || string(got) != upgradeCooldownBefore {
		t.Fatalf("failed migration changed the old config: %v %q", rerr, got)
	}
}

func TestUpgradeRemovesOnlyMultipleQuotedCooldownLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dibs.toml")
	before := "# cooldown = \"comment only\"\n[wake.exec.\"claude code\"]\nargv = [\"delivery-fixture\"]\n" +
		"'cooldown' = '''90s'''\n\n[wake.exec.codex]\nargv = [\"codex\", \"queue\"]\n" +
		"cooldown = \"\"\"\n90s\n\"\"\"\n# keep the ending comment\n"
	after := "# cooldown = \"comment only\"\n[wake.exec.\"claude code\"]\nargv = [\"delivery-fixture\"]\n" +
		"\n[wake.exec.codex]\nargv = [\"codex\", \"queue\"]\n# keep the ending comment\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	p := cooldownCutoverPlan(t, dir, func(string) error {
		got, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read multiple-key config: %w", err)
		}
		if string(got) != after {
			return fmt.Errorf("multiple-key rewrite was not exact before stop: %q", got)
		}
		return nil
	})
	if err := p.cutover(); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeRefusesUnsafeCooldownRewriteBeforeStop(t *testing.T) {
	for _, kind := range []string{"inline table", "symlink", "unwritable directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "dibs.toml")
			body := upgradeCooldownBefore
			if kind == "inline table" {
				body = "wake.exec.codex = { argv = [\"codex\", \"queue\"], cooldown = \"90s\" }\n"
			}
			if kind == "symlink" {
				target := filepath.Join(t.TempDir(), "operator.toml")
				if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if kind == "unwritable directory" {
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			}
			stops := 0
			p := cooldownCutoverPlan(t, dir, func(string) error { stops++; return nil })
			err := p.cutover()
			if err == nil || stops != 0 || !strings.Contains(err.Error(), "nothing has been stopped") {
				t.Fatalf("unsafe rewrite stopped %d daemons: %v", stops, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != body {
				t.Fatalf("unsafe rewrite changed old configuration: %v %q", err, got)
			}
		})
	}
}
