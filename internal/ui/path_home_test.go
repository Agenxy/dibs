// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathUsesHomeAtHomeAndRelativeInsideAProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(home)
	if got := Path(filepath.Join(home, ".dibs")); got != "~/.dibs" {
		t.Fatalf("HOME=cwd: %q, want ~/.dibs", got)
	}
	project := filepath.Join(home, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	if got := Path(filepath.Join(project, "internal", "auth")); got != "./"+filepath.Join("internal", "auth") {
		t.Fatalf("inside project: %q, want ./internal/auth", got)
	}
	if got := Path(filepath.Join(home, "other", ".dibs")); got != "~/"+filepath.Join("other", ".dibs") {
		t.Fatalf("beside project: %q, want ~/other/.dibs", got)
	}
}

func TestPathRecognizesASymlinkedHome(t *testing.T) {
	home := t.TempDir()
	alias := filepath.Join(t.TempDir(), "linked-home")
	if err := os.Symlink(home, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", alias)
	t.Setenv("USERPROFILE", alias)
	t.Chdir(home)
	for _, path := range []string{filepath.Join(home, ".dibs"), filepath.Join(alias, ".dibs")} {
		if got := Path(path); got != "~/.dibs" {
			t.Errorf("same home under %q: got %q", path, got)
		}
	}
}
