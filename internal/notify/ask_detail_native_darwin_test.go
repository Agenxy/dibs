// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Compile the shipped program with ONLY the native posting contact doubled.
// The real stdin decoder, run loop and deadline callback execute. The handshake
// keeps an old helper from reaching native permission or notification calls.
func TestNativeAskReadsBoundedDetailAndExpiresAtRequestDeadline(t *testing.T) {
	dir := t.TempDir()
	contents := filepath.Join(dir, "AskFixture.app", "Contents")
	if err := os.MkdirAll(filepath.Join(contents, "MacOS"), 0o700); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>org.agenxy.dibs.ask-fixture</string><key>CFBundleExecutable</key><string>dibs-notify</string><key>CFBundlePackageType</key><string>APPL</string><key>LSUIElement</key><true/></dict></plist>`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(contents, "MacOS", "dibs-notify")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "swiftc", "-D", "DIBS_ASK_FIXTURE", "-o", binary, nativePromptSource(t)).CombinedOutput(); err != nil {
		t.Fatalf("native ask build: %v\n%s", err, out)
	}
	if out, err := exec.CommandContext(ctx, binary, "--ask-fixture-capability").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "ask-fixture-v1" {
		t.Fatalf("safe fixture contact unavailable: %q %v", out, err)
	}
	for _, mode := range []string{"deadline", "correct-action", "foreign-action", "oversize", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			deadline := time.Now().Add(2 * time.Second)
			body := `{"sender":"daemon identity","body":"full private reading text"}`
			want := 1
			if mode == "correct-action" {
				want = 0
			}
			if mode == "oversize" {
				body = strings.Repeat("x", 8193)
				want = 2
			}
			if mode == "invalid" {
				body = `{"sender":false}`
				want = 2
			}
			receipt := filepath.Join(dir, mode+".json")
			callCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			cmd := exec.CommandContext(callCtx, binary, "Request effect", "", "excerpt", "Deny", "Later", "Approve")
			cmd.Env = append(os.Environ(), "DIBS_ASK_FIXTURE=timeout", "DIBS_NOTIFY_DETAIL=stdin",
				"DIBS_NOTIFY_UNTIL="+strconv.FormatFloat(float64(deadline.UnixNano())/float64(time.Second), 'f', -1, 64),
				"DIBS_NOTIFY_ID=dibs.msg.ask-fixture.7",
				"DIBS_NOTIFY_RECEIPT="+receipt)
			if mode == "correct-action" || mode == "foreign-action" {
				id := "dibs.msg.ask-fixture.7"
				if mode == "foreign-action" {
					id = "dibs.msg.ask-fixture.8"
				}
				cmd.Env = append(cmd.Env, "DIBS_ASK_FIXTURE_ID="+id, "DIBS_ASK_FIXTURE_ACTION=Approve")
			}
			cmd.Stdin = strings.NewReader(body)
			started := time.Now()
			// Answers use stdout in production. AppKit may emit diagnostics on
			// stderr for an uninstalled fixture bundle; those are not an answer.
			out, err := cmd.Output()
			var ee *exec.ExitError
			code := 0
			if err != nil {
				if !errors.As(err, &ee) {
					t.Fatalf("helper invocation: %v", err)
				}
				code = ee.ExitCode()
			}
			if code != want || callCtx.Err() != nil {
				t.Fatalf("%s: exit=%v output=%q", mode, err, out)
			}
			if mode == "deadline" || mode == "correct-action" || mode == "foreign-action" {
				if mode != "correct-action" && time.Since(started) < 500*time.Millisecond {
					t.Fatal("helper exited before its future deadline")
				}
				b, err := os.ReadFile(receipt)
				if err != nil || !strings.Contains(string(b), `"posted"`) {
					t.Fatalf("helper never entered actual posting callback: %q %v", b, err)
				}
				if mode == "correct-action" && strings.TrimSpace(string(out)) != "Approve" {
					t.Fatalf("native button did not return the offered action: %q", out)
				}
			} else if _, err := os.Stat(receipt); !os.IsNotExist(err) {
				t.Fatal("invalid detail reached the posting path")
			}
		})
	}
	t.Run("caller-exit", func(t *testing.T) {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		pidPath := filepath.Join(dir, "native-child.pid")
		callCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		parent := exec.CommandContext(callCtx, self, "-test.run=^TestNativeAskParentDriver$")
		parent.Env = append(os.Environ(), "DIBS_ASK_PARENT_BINARY="+binary, "DIBS_ASK_PARENT_PID="+pidPath, "DIBS_ASK_PARENT_RECEIPT="+filepath.Join(dir, "parent-receipt.json"))
		parent.WaitDelay = time.Second
		out, err := parent.CombinedOutput()
		if err != nil {
			// Only the process this private fixture just started can hold the
			// inherited stdout pipe open. Clean it up on a failed guard.
			if raw, readErr := os.ReadFile(pidPath); readErr == nil {
				if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw))); parseErr == nil {
					if process, findErr := os.FindProcess(pid); findErr == nil {
						_ = process.Kill()
					}
				}
			}
			t.Fatalf("helper outlived its only answer carrier: %v %s", err, out)
		}
	})
}

func TestNativeAskParentDriver(t *testing.T) {
	binary := os.Getenv("DIBS_ASK_PARENT_BINARY")
	if binary == "" {
		return
	}
	receipt := os.Getenv("DIBS_ASK_PARENT_RECEIPT")
	cmd := exec.Command(binary, "Request effect", "", "excerpt", "Deny", "Later", "Approve")
	cmd.Env = append(os.Environ(), "DIBS_ASK_FIXTURE=timeout", "DIBS_NOTIFY_DETAIL=stdin",
		"DIBS_NOTIFY_UNTIL="+strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10), "DIBS_NOTIFY_RECEIPT="+receipt)
	cmd.Stdin = strings.NewReader(`{"sender":"daemon identity","body":"request"}`)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("DIBS_ASK_PARENT_PID"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if b, err := os.ReadFile(receipt); err == nil && strings.Contains(string(b), `"posted"`) {
				return
			}
		case <-deadline.C:
			t.Fatal("native helper did not post before parent exit")
		}
	}
}
