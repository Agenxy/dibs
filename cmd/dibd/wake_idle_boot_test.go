// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/testport"
)

func TestDaemonBootsWithRetiredIdleDelayAndWarnsOnEveryStart(t *testing.T) {
	if os.Getenv("DIBS_IDLE_BOOT_CHILD") == "1" {
		os.Args = strings.Split(os.Getenv("DIBS_IDLE_BOOT_ARGS"), "\n")
		flag.CommandLine = flag.NewFlagSet("dibd", flag.ContinueOnError)
		if err := run(); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "dibs.toml")
	if err := os.WriteFile(path, []byte("[wake]\nopen_app_after_idle = \"0s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		port := testport.Reserve(t, "tcp", "127.0.0.1:0")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDaemonBootsWithRetiredIdleDelayAndWarnsOnEveryStart$")
		args := []string{"dibd", "--dir", dir, "--addr", port.Addr, "--allow-parallel"}
		cmd.Env = append(nativeProbeEnv(t.TempDir(), "", ""), "DIBS_NOTIFY=off", "GORACE=atexit_sleep_ms=0",
			"DIBS_IDLE_BOOT_CHILD=1", "DIBS_IDLE_BOOT_ARGS="+strings.Join(args, "\n"))
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		cmd.Stderr = cmd.Stdout
		port.Release(t)
		if err := cmd.Start(); err != nil {
			cancel()
			t.Fatal(err)
		}
		var lines []string
		scanner := bufio.NewScanner(pipe)
		ready, warned := false, false
		want := fmt.Sprintf("[wake] open_app_after_idle in %s:2 was removed and has no effect: Claude closed-session recovery opens immediately; delete the line.", path)
		for scanner.Scan() {
			line := scanner.Text()
			lines = append(lines, line)
			warned = warned || (strings.Contains(line, "level=WARN") && strings.Contains(line, want))
			if strings.Contains(line, "dibd up") {
				ready = true
				break
			}
		}
		cancel()
		_ = cmd.Wait()
		if !ready || !warned {
			t.Fatalf("boot %d ready=%v warned=%v: %s", attempt+1, ready, warned, strings.Join(lines, "\n"))
		}
	}
}
