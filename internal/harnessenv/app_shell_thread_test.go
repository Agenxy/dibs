// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package harnessenv

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/procexec"
)

func TestAppShellThreadOwnerRejectsNestedHarnesses(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "fixture")
	if b, err := exec.Command("go", "build", "-o", binary, "./testdata/appshell").CombinedOutput(); err != nil {
		t.Fatalf("setup: build process fixture: %v: %s", err, b)
	}
	image, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal("setup:", err)
	}
	appDir := filepath.Join(root, "ChatGPT.app", "Contents", "MacOS")
	for _, dir := range []string{appDir, filepath.Join(root, "shell")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal("setup:", err)
		}
	}
	app, codex := filepath.Join(appDir, "ChatGPT"), filepath.Join(appDir, "codex")
	python := filepath.Join(root, "shell", "python3.13")
	claude := filepath.Join(root, "shell", "claude")
	for _, path := range []string{app, codex, python, claude} {
		if err := os.WriteFile(path, image, 0o700); err != nil {
			t.Fatal("setup:", err)
		}
	}
	for _, tc := range []struct {
		name   string
		nested []string
		want   bool
	}{
		{name: "direct Python shell", want: true},
		{name: "Claude below the shell", nested: []string{claude, "--child"}},
		{name: "bundled Codex exec below the shell", nested: []string{codex, "exec", "app-server", "--child"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"--child", codex, "-c", "fixture=true", "app-server", "--child", python, "--child"}
			args = append(args, tc.nested...)
			args = append(args, binary)
			checkAppShellOwner(t, app, args, tc.want)
		})
	}
}

func checkAppShellOwner(t *testing.T, app string, args []string, want bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	control, release, err := os.Pipe()
	if err != nil {
		t.Fatal("setup: pipe:", err)
	}
	defer func() { _ = control.Close(); _ = release.Close() }()
	cmd := procexec.Command(ctx, app, args...)
	cmd.Env = append(os.Environ(), "CODEX_THREAD_ID=test-shell-thread")
	cmd.Stdin = control
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal("setup: read fixture readiness:", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal("setup: start fixture:", err)
	}
	defer func() { _ = release.Close(); _ = cmd.Wait() }()
	var pid int
	var inherited bool
	if _, err := fmt.Fscan(bufio.NewReader(out), &pid, &inherited); err != nil || !inherited || pid <= 1 {
		t.Fatalf("setup: process or inherited environment missing: pid=%d inherited=%t err=%v", pid, inherited, err)
	}
	if got := AppShellThreadOwner(pid); got != want {
		t.Fatalf("owner evidence %t, want %t", got, want)
	}
}
