// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Enter through the shipped hook_poll tool, not addDelivery: a digest must
// reach the model once, in the event's carrier, after real registration/send.
func TestHookPollEmitsOneMailCarrier(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, strict := range []bool{false, true} {
			for _, event := range []string{"Stop", "SubagentStop", "SessionStart", "PostToolUse"} {
				t.Run(fmt.Sprintf("%s/strict=%t/%s", version, strict, event), func(t *testing.T) {
					srv, _ := newServer(t)
					call := func(name string, args map[string]any) map[string]any {
						t.Helper()
						out := waitingCall(t, srv, version, name, args)
						if out["__is_error"] == true {
							t.Fatalf("setup %s: %v", name, out)
						}
						return out
					}
					call("register", map[string]any{"name": "receiver", "session_id": "single-carrier"})
					sender := call("register", map[string]any{"name": "sender"})
					const marker = "one-unique-mail-digest"
					call("send", map[string]any{"token": sender["token"], "to": "receiver", "type": "question", "body": marker})
					out := call("hook_poll", map[string]any{"session_id": "single-carrier", "event": event, "strict_output": strict})
					encoded, err := json.Marshal(out)
					if err != nil {
						t.Fatal(err)
					}
					if n := strings.Count(string(encoded), marker); n != 1 {
						t.Errorf("digest appears %d times, want exactly once: %s", n, encoded)
					}
					if event == "Stop" || event == "SubagentStop" {
						if len(out) != 2 || out["decision"] != "block" || !strings.Contains(fmt.Sprint(out["reason"]), marker) {
							t.Fatalf("Stop must carry only decision:block and the reason digest: %s", encoded)
						}
					} else {
						h, _ := out["hookSpecificOutput"].(map[string]any)
						if len(out) != 1 || len(h) != 2 || h["hookEventName"] != event || !strings.Contains(fmt.Sprint(h["additionalContext"]), marker) {
							t.Fatalf("non-Stop context carrier changed: %s", encoded)
						}
					}
				})
			}
		}
	}
}
