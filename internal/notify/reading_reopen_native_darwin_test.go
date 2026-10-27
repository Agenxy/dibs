// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The transport double writes the actual posted content, exits, then a second
// invocation enters with NO arguments and receives that content through the
// same response handler as UNUserNotificationCenter. No real notification or
// UI opens. This proves persistence/dispatch/layout, not LaunchServices itself.
func TestNativeReadingWindowReopensPersistedNotification(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	contents := filepath.Join(t.TempDir(), "ReadingFixture.app", "Contents")
	if err := os.MkdirAll(filepath.Join(contents, "MacOS"), 0o700); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>org.agenxy.dibs.reading-fixture</string><key>CFBundleExecutable</key><string>dibs-notify</string><key>CFBundlePackageType</key><string>APPL</string><key>LSUIElement</key><true/></dict></plist>`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(contents, "MacOS", "dibs-notify")
	if out, err := exec.CommandContext(ctx, "swiftc", "-D", "DIBS_READING_FIXTURE", "-o", binary, "app/notify_darwin.swift").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	if out, err := exec.CommandContext(ctx, binary, "--reading-fixture-capability").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "reading-fixture-v1" {
		t.Fatalf("safe native reading fixture unavailable: %v %s", err, out)
	}
	for _, kind := range []string{"notify", "handoff", "contact", "request", "question"} {
		t.Run(kind, func(t *testing.T) {
			for _, mode := range []string{"live", "live-action", "cold", "cold-action", "foreign", "oversize", "invalid", "missing", "bounded"} {
				t.Run(mode, func(t *testing.T) {
					dir := t.TempDir()
					posted, window := filepath.Join(dir, "posted.plist"), filepath.Join(dir, "window.json")
					heading, text := "Daemon effect", "Full text\nPRIVATE END"
					if mode == "bounded" {
						heading = strings.Repeat("\x01", 1600)
						text = strings.Repeat("本文", 900)
					}
					detail, _ := json.Marshal(map[string]string{"sender": "Daemon sender", "body": text, "kind": kind})
					args := []string{heading, "subtitle", "excerpt"}
					buttons := []string{"Close"}
					if kind == "contact" {
						args = append(args, "Later", "Open")
						buttons = []string{"Later", "Open"}
					}
					if kind == "request" || kind == "question" {
						args = append(args, "Deny", "Later", "Approve")
						buttons = []string{"Deny", "Later", "Approve"}
					}
					env := append(os.Environ(), "DIBS_READING_FIXTURE=1", "DIBS_READING_POSTED="+posted, "DIBS_READING_WINDOW="+window, "DIBS_NOTIFY_DETAIL=stdin", "DIBS_NOTIFY_UNTIL=4102444800", "DIBS_NOTIFY_ID=dibs.msg.reading.7")
					action := "com.apple.UNNotificationDefaultActionIdentifier"
					if mode == "cold-action" {
						action = "Approve"
					}
					callArgs := args
					if mode == "live-action" && (kind == "request" || kind == "question" || kind == "contact") {
						env = append(env, "DIBS_READING_CHOICE="+buttons[len(buttons)-1])
					}
					if mode == "live" || mode == "live-action" || mode == "foreign" {
						env = append(env, "DIBS_READING_ACTION="+action)
					}
					if mode == "foreign" {
						env = append(env, "DIBS_READING_RESPONSE_ID=dibs.msg.reading.8")
					}
					cmd := exec.CommandContext(ctx, binary, callArgs...)
					cmd.Env = env
					cmd.Stdin = strings.NewReader(string(detail))
					out, err := cmd.Output()
					if err != nil {
						t.Fatalf("posting/live response: %v %s", err, out)
					}
					if mode == "cold" || mode == "cold-action" || mode == "oversize" || mode == "invalid" || mode == "missing" || mode == "bounded" {
						cmd = exec.CommandContext(ctx, binary)
						// A fresh app launch has none of the original invocation's detail,
						// buttons, deadline, ID or answer pipe. Only persisted content survives.
						cmd.Env = append(os.Environ(), "DIBS_READING_FIXTURE=1", "DIBS_READING_POSTED="+posted, "DIBS_READING_WINDOW="+window, "DIBS_READING_ACTION="+action, "DIBS_READING_CORRUPT="+mode)
						out, err = cmd.Output()
						if err != nil {
							t.Fatalf("cold response: %v %s", err, out)
						}
					}
					wantAnswer := ""
					if mode == "live-action" && (kind == "request" || kind == "question" || kind == "contact") {
						wantAnswer = buttons[len(buttons)-1]
					}
					if strings.TrimSpace(string(out)) != wantAnswer {
						t.Fatalf("reading fabricated an answer: %q", out)
					}
					raw, err := os.ReadFile(window)
					if err != nil {
						t.Fatalf("response did not open reading window: %v", err)
					}
					var view struct {
						Title, Sender, Body string
						Buttons             []string
						StoredBytes         int
					}
					if err := json.Unmarshal(raw, &view); err != nil {
						t.Fatal(err)
					}
					corrupt := mode == "oversize" || mode == "invalid" || mode == "missing"
					if mode != "bounded" && !corrupt && (view.Title != heading || view.Sender != "Daemon sender" || !strings.Contains(view.Body, "PRIVATE END")) {
						t.Fatalf("lost reading detail: %+v", view)
					}
					if mode != "live" && mode != "live-action" {
						buttons = []string{"Close"}
					}
					if strings.Join(view.Buttons, "|") != strings.Join(buttons, "|") {
						t.Fatalf("buttons %v want %v", view.Buttons, buttons)
					}
					if mode != "live" && mode != "live-action" && (kind == "request" || kind == "question" || kind == "contact") && !strings.Contains(view.Body, "Dibs board") {
						t.Fatalf("dead answer route not explained: %+v", view)
					}
					if corrupt {
						if view.Title != heading || !strings.Contains(view.Body, "excerpt") || strings.Contains(view.Body, "PRIVATE END") {
							t.Fatalf("invalid saved content was trusted: %+v", view)
						}
					} else if view.StoredBytes <= 0 || view.StoredBytes > 8192 {
						t.Fatalf("persisted payload size %d", view.StoredBytes)
					}
					if mode == "bounded" && !strings.Contains(view.Body, "Rest on the Dibs board") {
						t.Fatalf("stored truncation not explained: %+v", view)
					}
				})
			}
		})
	}
}
