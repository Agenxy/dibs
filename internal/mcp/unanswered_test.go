// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// A deadline is `deadline_s`, in seconds, and the names a caller reaches for
// are refused. k7-dev tried `deadline` and `deadline_minutes`; both were
// rejected without naming the real one, so every request took ten minutes.
func TestAMisnamedDeadlineIsPointedAtTheRealOne(t *testing.T) {
	srv, _, _ := newServerWithEngine(t)
	reg := toolCall(t, srv, "register", map[string]any{"name": "lead", "cwd": "/w"})
	toolCall(t, srv, "register", map[string]any{"name": "worker", "cwd": "/w"})
	tok := reg["token"].(string)
	toolCall(t, srv, "check_in", map[string]any{"token": tok})
	for _, wrong := range []string{"deadline", "deadline_minutes"} {
		out := rpc(t, srv, "2026-07-28", "tools/call", map[string]any{"name": "send", "arguments": map[string]any{
			"token": tok, "to": "worker", "type": "request", "body": "build C", wrong: 120,
		}})
		if s := fmt.Sprint(out); !strings.Contains(s, "deadline_s") || !strings.Contains(s, "seconds") {
			t.Errorf("send(%s:…) was refused without naming deadline_s and its unit: %v", wrong, out)
		}
	}
}

// Replying to a pending request with a message does not answer it, and the
// sender of that message is told so at once. Measured: a worker accepted
// request #2500 with a notify, the request expired unanswered, and the task
// looked dropped.
func TestAMessageInReplyToAnOpenAskSaysItDidNotAnswerIt(t *testing.T) {
	srv, _, _ := newServerWithEngine(t)
	lead := toolCall(t, srv, "register", map[string]any{"name": "lead", "cwd": "/w"})["token"].(string)
	worker := toolCall(t, srv, "register", map[string]any{"name": "worker", "cwd": "/w"})["token"].(string)
	toolCall(t, srv, "check_in", map[string]any{"token": lead})
	toolCall(t, srv, "check_in", map[string]any{"token": worker})
	asked := toolCall(t, srv, "send", map[string]any{"token": lead, "to": "worker", "type": "request", "body": "build C", "deadline_s": 600})
	serial := fmt.Sprint(asked["msg_serial"])
	if serial == "" || serial == "<nil>" {
		t.Fatalf("setup: no serial in %v", asked)
	}
	third := toolCall(t, srv, "register", map[string]any{"name": "third", "cwd": "/w"})["token"].(string)
	toolCall(t, srv, "check_in", map[string]any{"token": third})
	elsewhere := fmt.Sprint(toolCall(t, srv, "send", map[string]any{"token": third, "to": "worker", "type": "question", "body": "unrelated?"})["msg_serial"])
	reply := toolCall(t, srv, "send", map[string]any{"token": worker, "to": "lead", "type": "notify", "body": "request" + serial + " accepted, queued"})
	if n, _ := reply["unanswered"].(string); strings.Contains(n, "#"+elsewhere) {
		t.Errorf("the note to lead named a question from somebody else: %q", n)
	}
	note, _ := reply["unanswered"].(string)
	if !strings.Contains(note, "#"+serial) || !strings.Contains(note, "respond("+serial+", approve") {
		t.Errorf("a notify to the asker said nothing about the request it left open: %v", reply)
	}
	other := toolCall(t, srv, "send", map[string]any{"token": lead, "to": "worker", "type": "notify", "body": "fyi"})
	if _, said := other["unanswered"]; said {
		t.Error("the asker, who owes nothing, was told it left something unanswered")
	}
}
