package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A unit file records an absolute path and then outlives the shell that wrote
// it. Writing the wrong one produces a service that runs a different build
// indefinitely, or silently overwrites a unit somebody tuned by hand.
func TestServiceUnitsAreWrittenSafely(t *testing.T) {
	t.Run("refuses to overwrite an existing unit", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "nested", "lanes.service")
		if err := writeUnit(target, "original"); err != nil {
			t.Fatal(err)
		}
		if err := writeUnit(target, "replacement"); err == nil {
			t.Fatal("overwrote an existing unit — an operator's tuning is gone and the " +
				"command reads as idempotent")
		}
		body, err := os.ReadFile(target) // #nosec G304 -- test temp dir
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "original" {
			t.Fatalf("the existing unit was modified: %q", body)
		}
	})

	t.Run("creates the parent directory", func(t *testing.T) {
		// ~/.config/systemd/user and ~/Library/LaunchAgents do not always exist,
		// and a unit written nowhere is a service that never starts.
		target := filepath.Join(t.TempDir(), "a", "b", "c", "lanes.service")
		if err := writeUnit(target, "unit"); err != nil {
			t.Fatalf("could not create the parent path: %v", err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("configHome honours XDG_CONFIG_HOME", func(t *testing.T) {
		// A systemd user unit written to the wrong root is never loaded, and
		// nothing reports that — the service simply does not exist.
		t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-probe")
		if got := configHome(); got != "/tmp/xdg-probe" {
			t.Errorf("configHome() = %q, want the XDG value", got)
		}
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "/tmp/home-probe")
		if got := configHome(); !strings.HasSuffix(got, "/.config") {
			t.Errorf("configHome() = %q, want ~/.config when XDG is unset", got)
		}
	})
}
