// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Compiles the shipped broker with only native posting and UI contact doubled.
// Both concurrent asks enter through its real command pipe. A synthetic native
// response for A is delivered while B remains live; only A may be answered.
func TestNativeBrokerOwnsConcurrentAsks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	contents := filepath.Join(t.TempDir(), "BrokerFixture.app", "Contents")
	if err := os.MkdirAll(filepath.Join(contents, "MacOS"), 0o700); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>org.agenxy.dibs.broker-fixture</string><key>CFBundleExecutable</key><string>dibs-notify</string><key>CFBundlePackageType</key><string>APPL</string><key>LSUIElement</key><true/></dict></plist>`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(contents, "MacOS", "dibs-notify")
	if out, err := exec.CommandContext(ctx, "swiftc", "-D", "DIBS_BROKER_FIXTURE", "-D", "DIBS_READING_FIXTURE", "-o", binary, nativePromptSource(t)).CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	if out, err := exec.CommandContext(ctx, binary, "--broker-fixture-capability").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "broker-fixture-v1" {
		t.Fatalf("safe broker fixture unavailable: %v %s", err, out)
	}
	production := filepath.Join(contents, "MacOS", "production-notifier")
	if out, err := exec.CommandContext(ctx, "swiftc", "-o", production, nativePromptSource(t)).CombinedOutput(); err != nil {
		t.Fatalf("production build: %v %s", err, out)
	}
	capability := exec.CommandContext(ctx, production, "--broker-fixture-capability")
	if out, err := capability.Output(); err == nil || strings.Contains(string(out), "broker-fixture-v1") {
		t.Fatalf("production exposes fixture capability: %v %s", err, out)
	}
	cmd := exec.CommandContext(ctx, binary, "--broker")
	postedDir := t.TempDir()
	cmd.Env = append(os.Environ(), "DIBS_BROKER_FIXTURE_DIR="+t.TempDir(), "DIBS_BROKER_POSTED_DIR="+postedDir)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.CreateTemp(t.TempDir(), "broker.stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stderr.Close() }()
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); _ = cmd.Wait() }()
	frames := make(chan map[string]any, 16)
	go func() {
		defer close(frames)
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			var frame map[string]any
			if json.Unmarshal(scanner.Bytes(), &frame) == nil {
				frames <- frame
			}
		}
	}()
	read := func(event, id string) map[string]any {
		t.Helper()
		select {
		case f, ok := <-frames:
			if !ok || f["event"] != event || (id != "" && f["id"] != id) {
				t.Fatalf("got %v, want %s %s; stderr %s", f, event, id, func() string { data, _ := os.ReadFile(stderr.Name()); return string(data) }())
			}
			return f
		case <-time.After(3 * time.Second):
			t.Fatalf("missing %s %s", event, id)
		}
		return nil
	}
	write := func(frame map[string]any) {
		t.Helper()
		frame["v"] = 1
		if err := json.NewEncoder(input).Encode(frame); err != nil {
			t.Fatal(err)
		}
	}
	read("ready", "")
	// The production image ignores the fixture-only path override. It may see
	// the real notifier, but can never mistake our private fixture lock for it.
	productionStatus := exec.CommandContext(ctx, production, "--broker-status")
	productionStatus.Env = cmd.Env
	productionRaw, productionErr := productionStatus.Output()
	var realStatus BrokerStatus
	if productionErr != nil || json.Unmarshal(productionRaw, &realStatus) != nil {
		t.Fatalf("production lock observation: %s %v", productionRaw, productionErr)
	}
	for _, pid := range realStatus.PIDs {
		if pid == cmd.Process.Pid {
			t.Fatal("production honoured a test-only lock override")
		}
	}

	statusCmd := exec.CommandContext(ctx, binary, "--broker-status")
	statusCmd.Env = cmd.Env
	rawStatus, statusErr := statusCmd.Output()
	var status BrokerStatus
	if statusErr != nil || json.Unmarshal(rawStatus, &status) != nil || !status.Held || len(status.PIDs) != 1 || status.PIDs[0] != cmd.Process.Pid {
		t.Fatalf("lock holder diagnostic: %s %v", rawStatus, statusErr)
	}
	// A second parent cannot become another bundle instance and steal presses.
	second := exec.CommandContext(ctx, binary, "--broker")
	second.Env = cmd.Env
	rejected, err := second.Output()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(rejected), "Another Dibs notifier") {
		t.Fatalf("second broker did not refuse: %v %s", err, rejected)
	}

	for index, id := range []string{"dibs.msg.broker.7", "dibs.msg.broker.8"} {
		label := "A"
		buttons := []string{"Deny", "Approve"}
		if index == 1 {
			label = "B"
			buttons = []string{"No", "Yes"}
		}
		write(map[string]any{"type": "post", "id": id, "title": "Request effect " + label, "excerpt": "short", "detail": map[string]string{"sender": "daemon", "body": "private full text " + label, "kind": "request"}, "buttons": buttons, "until": float64(time.Now().Add(time.Minute).Unix())})
		read("posted", id)
	}
	// The actual broker posting path must persist its text, not just the old
	// argv path tested separately. A new no-args process reads that exact post.
	windowPath := filepath.Join(t.TempDir(), "cold.json")
	cold := exec.CommandContext(ctx, binary)
	cold.Env = append(os.Environ(), "DIBS_READING_FIXTURE=1", "DIBS_READING_POSTED="+filepath.Join(postedDir, "dibs.msg.broker.7.plist"), "DIBS_READING_WINDOW="+windowPath)
	if out, err := cold.Output(); err != nil || len(out) != 0 {
		t.Fatalf("cold broker post: %v %q", err, out)
	}
	coldBytes, err := os.ReadFile(windowPath)
	if err != nil {
		t.Fatal(err)
	}
	var coldView struct {
		Title, Sender, Body string
		Buttons             []string
		StoredBytes         int
	}
	if err := json.Unmarshal(coldBytes, &coldView); err != nil {
		t.Fatal(err)
	}
	if coldView.Title != "Request effect A" || coldView.Sender != "daemon" || !strings.Contains(coldView.Body, "private full text A") || !strings.Contains(coldView.Body, "cannot confirm") || strings.Join(coldView.Buttons, "|") != "Close" || coldView.StoredBytes > 8192 {
		t.Fatalf("broker did not retain safe cold reading: %+v", coldView)
	}

	// Ordinary protocol commands cannot inject answers, even on the parent's
	// private pipe. Only the compile-time native callback fixture can do so.
	write(map[string]any{"type": "action", "id": "dibs.msg.broker.7", "action": "Approve"})
	read("protocol_error", "dibs.msg.broker.7")
	write(map[string]any{"type": "fixture_response", "id": "dibs.msg.broker.7", "action": "Yes"})
	read("refused", "dibs.msg.broker.7")
	write(map[string]any{"type": "fixture_response", "id": "dibs.msg.broker.7", "action": "com.apple.UNNotificationDefaultActionIdentifier"})
	view := read("reading", "dibs.msg.broker.7")["view"].(map[string]any)
	if view["title"] != "Request effect A" || view["sender"] != "daemon" || view["body"] != "private full text A" {
		t.Fatalf("broker reading detail lost: %v", view)
	}

	write(map[string]any{"type": "fixture_window", "id": "dibs.msg.broker.7", "action": "Approve"})
	if frame := read("action", "dibs.msg.broker.7"); frame["action"] != "Approve" {
		t.Fatalf("A received another ask's choice: %v", frame)
	}
	write(map[string]any{"type": "fixture_response", "id": "dibs.msg.broker.8", "action": "Yes"})
	if frame := read("action", "dibs.msg.broker.8"); frame["action"] != "Yes" {
		t.Fatalf("B received another ask's choice: %v", frame)
	}
	id := "dibs.msg.broker.9"
	write(map[string]any{"type": "post", "id": id, "title": "Expiry", "excerpt": "short", "detail": map[string]string{"sender": "daemon", "body": "body", "kind": "request"}, "buttons": []string{"Approve"}, "until": float64(time.Now().Add(200*time.Millisecond).UnixNano()) / float64(time.Second)})
	read("posted", id)
	read("expired", id)
	id = "dibs.msg.broker.10"
	write(map[string]any{"type": "post", "id": id, "title": "Withdraw", "excerpt": "short", "detail": map[string]string{"sender": "daemon", "body": "body", "kind": "request"}, "buttons": []string{"Approve"}, "until": float64(time.Now().Add(time.Minute).Unix())})
	read("posted", id)
	write(map[string]any{"type": "cancel", "id": id})
	write(map[string]any{"type": "fixture_response", "id": id, "action": "Approve"})
	// No live entry remains to answer. A later invalid command yields a frame;
	// if cancellation leaked an answer it would arrive first and fail this read.
	write(map[string]any{"type": "action", "id": id, "action": "Approve"})
	read("protocol_error", id)
}
