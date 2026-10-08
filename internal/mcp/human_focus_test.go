// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/humanask"
)

func TestHumanFocusEvidenceThroughActualSendAndReadMail(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native macOS Focus observations")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	db := filepath.Join(dir, "Library", "DoNotDisturb", "DB")
	if err := os.MkdirAll(db, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFocus := func(name string) {
		t.Helper()
		assertions := `{"data":[{"storeAssertionRecords":[{"assertionDetails":{"assertionDetailsModeIdentifier":"com.apple.focus.fixture"}}]}]}`
		mode := map[string]any{"header": map[string]any{"version": 3}, "data": []any{map[string]any{"modeConfigurations": map[string]any{"com.apple.focus.fixture": map[string]any{"mode": map[string]any{"name": name}}}}}}
		b, err := json.Marshal(mode)
		if err != nil {
			t.Fatal(err)
		}
		for p, v := range map[string][]byte{"Assertions.json": []byte(assertions), "ModeConfigurations.json": b} {
			if err := os.WriteFile(filepath.Join(db, p), v, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeFocus("At send")
	srv, eng, _ := newServerWithEngine(t)
	asked, release := make(chan humanask.Message, 1), make(chan struct{})
	t.Cleanup(func() { close(release) })
	eng.SetHumanNotifier(desktopFixture{available: true, ask: func(m humanask.Message) (humanask.Answer, error) {
		asked <- m
		<-release
		return humanask.Answer{}, nil
	}})
	r := toolCall(t, srv, "register", map[string]any{"name": "focus-sender", "nonce": "focus-fixture"})
	token, ok := r["token"].(string)
	if !ok {
		t.Fatalf("setup register: %v", r)
	}
	sent := toolCall(t, srv, "send", map[string]any{"token": token, "to": "human", "type": "question", "body": "Focus receipt fixture"})
	d, ok := sent["human_delivery"].(map[string]any)
	if !ok || d["state"] != "pending" || d["posted"] != false || d["shown"] != "unknown" || d["focus"] != "At send" || !strings.Contains(sent["notify_hint"].(string), "pending") {
		t.Fatalf("send evidence: %v", sent)
	}
	serial, ok := sent["msg_serial"].(float64)
	if !ok {
		t.Fatalf("setup send: %v", sent)
	}
	var message humanask.Message
	select {
	case message = <-asked:
	case <-time.After(3 * time.Second):
		t.Fatal("send did not dispatch notifier")
	}
	writeFocus("At posting")
	message.Receipt("posted")
	read := toolCall(t, srv, "read_mail", map[string]any{"token": token, "msg_serial": serial})
	d, ok = read["human_delivery"].(map[string]any)
	if !ok || d["posted"] != true || d["state"] != "posted" || d["shown"] != "unknown" || d["focus"] != "At posting" {
		t.Fatalf("posting-time evidence: %v", read)
	}
	// A later Focus change cannot rewrite the posting-time receipt.
	if err := os.WriteFile(filepath.Join(db, "Assertions.json"), []byte(`{"data":[{"storeAssertionRecords":[]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	read = toolCall(t, srv, "read_mail", map[string]any{"token": token, "msg_serial": serial})
	d = read["human_delivery"].(map[string]any)
	if d["focus"] != "At posting" || d["shown"] != "unknown" {
		t.Fatalf("receipt changed after posting: %v", d)
	}
}
