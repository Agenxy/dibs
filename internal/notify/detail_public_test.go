// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/agenxy/dibs/internal/humanask"
	"github.com/agenxy/dibs/internal/notify"
	"github.com/agenxy/dibs/internal/testexec"
)

func TestHumanAskDetailUsesStdinAndWithdrawsLiveHelper(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS installed-helper route")
	}
	if os.Getenv("DIBS_TEST_DETAIL_DRIVER") != "1" {
		dir := t.TempDir()
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		image, err := os.ReadFile(self)
		if err != nil {
			t.Fatal(err)
		}
		driver, helper := filepath.Join(dir, "driver.test"), filepath.Join(dir, "Dibs.app/Contents/MacOS/dibs-notify")
		if err := os.MkdirAll(filepath.Dir(helper), 0o700); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{driver, helper} {
			if err := os.WriteFile(path, image, 0o700); err != nil {
				t.Fatal(err)
			}
			testexec.PrepareExecutable(t, exec.Command(path, "--dibs-test-prepare"))
		}
		captureDir := filepath.Join(dir, "captures")
		if err := os.Mkdir(captureDir, 0o700); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, driver, "-test.run=^TestHumanAskDetailUsesStdinAndWithdrawsLiveHelper$")
		cmd.Env = append(os.Environ(), "DIBS_TEST_DETAIL_DRIVER=1", "DIBS_TEST_RECEIPT_PUBLIC=1", "DIBS_DIR="+dir, "DIBS_TEST_DETAIL_CAPTURE_DIR="+captureDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("public detail/withdrawal path: %v\n%s", err, out)
		}
		return
	}
	t.Setenv("DIBS_NOTIFY", "")
	for i, mode := range []string{"detail", "question", "bounded", "withdraw", "handoff", "notify", "urgent", "contact", "contact-passive"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("DIBS_TEST_DETAIL_MODE", mode)
			body := strings.Repeat("excerpt ", 30) + "PRIVATE DETAIL AFTER EXCERPT\nSecond paragraph."
			if mode == "withdraw" {
				body += " WITHDRAW_FIXTURE"
			}
			if mode == "bounded" {
				body = strings.Repeat("長い本文", 4000)
			}
			deadline := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
			// One persistent helper serves every mode. Distinct message IDs keep
			// both captured bytes and late withdrawal frames scoped to this mode.
			m := humanask.Message{Type: "request", From: "fixture-sender", Who: "Daemon-composed sender line", Body: body, Node: "detail-fixture", Serial: uint64(7 + i)}
			wantID := "dibs.msg.detail-fixture." + strconv.FormatUint(m.Serial, 10)
			wantDisposition := "approve"
			if mode == "question" {
				m.Type, m.Choices, wantDisposition = "question", []string{"Approve", "Decline"}, "answer"
			}
			wantBody, wantSender := body, m.Who
			switch mode {
			case "handoff", "notify", "urgent":
				m.Type, wantDisposition = mode, ""
				if mode == "urgent" {
					m.Type, m.Priority = "notify", "urgent"
				}
			case "contact", "contact-passive":
				m.Type, wantDisposition = "contact", ""
				m.Contact = &humanask.Contact{Sender: "system sender", Recipient: "recipient", Kind: "request", Message: 8, OpenHint: body}
				if mode == "contact" {
					m.Contact.OpenURL = "codex://threads/019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
				}
				wantSender = m.Contact.Sender
				wantBody = "Unread request #8; " + body
			}
			// This input is also understood by the old source (which ignores the
			// added field), so the same test has a runtime old-code proof.
			encoded, _ := json.Marshal(map[string]any{"Deadline": deadline})
			if err := json.Unmarshal(encoded, &m); err != nil {
				t.Fatal(err)
			}
			posted := make(chan struct{}, 1)
			m.Receipt = func(state string) {
				if state == "posted" {
					select {
					case posted <- struct{}{}:
					default:
					}
				}
			}
			finished := make(chan error, 1)
			go func() {
				a, err := humanask.Ask(m)
				if err == nil && mode != "withdraw" && a.Disposition != wantDisposition {
					err = errors.New("unexpected human disposition: " + a.Disposition)
				}
				finished <- err
			}()
			if mode == "withdraw" {
				select {
				case <-posted:
				case err := <-finished:
					t.Fatalf("helper exited before cleanup: %v", err)
				case <-time.After(3 * time.Second):
					t.Fatal("helper never posted")
				}
				cleanup := notify.RemoveMessages(m.Node, []uint64{m.Serial})
				if cleanup.State != "requested" {
					t.Fatalf("cleanup: %+v", cleanup)
				}
			}
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("withdrawal left a helper and reading window alive")
			}
			raw, err := os.ReadFile(filepath.Join(os.Getenv("DIBS_TEST_DETAIL_CAPTURE_DIR"), wantID+".json"))
			if err != nil {
				t.Fatal("setup: helper not reached:", err)
			}
			var capture struct {
				Args                   []string
				Stdin, Mode, Until, ID string
			}
			if err := json.Unmarshal(raw, &capture); err != nil {
				t.Fatal(err)
			}
			if capture.Mode != "broker-jsonl" || capture.ID != wantID {
				t.Fatalf("detail did not cross the actual helper boundary: %+v", capture)
			}
			until, err := strconv.ParseInt(capture.Until, 10, 64)
			if err != nil || until != deadline.Unix() {
				t.Fatalf("helper lifetime lost request deadline: %q %v", capture.Until, err)
			}
			if len(capture.Stdin) > 8192 || !utf8.ValidString(capture.Stdin) {
				t.Fatalf("detail is not bounded UTF-8: %d bytes", len(capture.Stdin))
			}
			var detail struct{ Sender, Body, Kind string }
			if err := json.Unmarshal([]byte(capture.Stdin), &detail); err != nil {
				t.Fatalf("full text absent from stdin: %q %v", capture.Stdin, err)
			}
			if mode == "handoff" || mode == "notify" || mode == "urgent" || strings.HasPrefix(mode, "contact") {
				if detail.Kind != m.Type {
					t.Fatalf("notification kind missing: %q want %q", detail.Kind, m.Type)
				}
			}
			if detail.Sender != wantSender {
				t.Fatalf("sender line lost: %q", detail.Sender)
			}
			if mode != "bounded" && detail.Body != wantBody {
				t.Fatalf("detail truncated before its limit: %q", detail.Body)
			}
			if mode == "bounded" && !strings.Contains(detail.Body, "Rest on the Dibs board") {
				t.Fatal("truncated detail did not explain where to read the rest")
			}
			if strings.Contains(strings.Join(capture.Args, " "), "PRIVATE DETAIL AFTER EXCERPT") {
				t.Fatal("private detail leaked into world-readable argv")
			}
		})
	}
}
