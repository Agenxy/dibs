// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func passivePresentationFixture(t *testing.T, version string) (*httptest.Server, string, string) {
	t.Helper()
	srv, _ := newServer(t)
	reader := waitingCall(t, srv, version, "register", map[string]any{"name": "reader", "session_id": "reader-session"})
	sender := waitingCall(t, srv, version, "register", map[string]any{"name": "sender"})
	for _, r := range []map[string]any{reader, sender} {
		waitingCall(t, srv, version, "check_in", map[string]any{"token": r["token"]})
	}
	return srv, reader["token"].(string), sender["token"].(string)
}

func passivePresentationText(t *testing.T, srv *httptest.Server, version, session string) string {
	t.Helper()
	r := waitingCall(t, srv, version, "hook_poll", map[string]any{"session_id": session, "event": "UserPromptSubmit"})
	h, _ := r["hookSpecificOutput"].(map[string]any)
	s, _ := h["additionalContext"].(string)
	return s
}

// Real HTTP MCP entry, including session resolution and presentation side
// effects. The incident was a queued-input drain: repeated prompts, not Stop.
func TestPassivePresentationEmitsOnlyWhenOutstandingStateChanges(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, reader, sender := passivePresentationFixture(t, version)
			send := func(body string) float64 {
				return waitingCall(t, srv, version, "send", map[string]any{"token": sender, "to": "reader", "type": "question", "body": body})["msg_serial"].(float64)
			}
			firstID := send("first question")
			first := passivePresentationText(t, srv, version, "reader-session")
			if !strings.Contains(first, fmt.Sprintf("#%.0f", firstID)) {
				t.Fatalf("setup: first prompt did not present question: %q", first)
			}
			for i := range 50 {
				if got := passivePresentationText(t, srv, version, "reader-session"); got != "" {
					t.Fatalf("unchanged prompt %d repeated summary: %q", i, got)
				}
			}
			// A global ledger serial change is not a mailbox change.
			waitingCall(t, srv, version, "declare", map[string]any{"token": sender, "text": "unrelated work"})
			if got := passivePresentationText(t, srv, version, "reader-session"); got != "" {
				t.Fatalf("unrelated work re-emitted summary: %q", got)
			}
			newID := send("new question between prompts")
			if got := passivePresentationText(t, srv, version, "reader-session"); !strings.Contains(got, fmt.Sprintf("#%.0f", newID)) {
				t.Fatalf("new item was suppressed: %q", got)
			}
			waitingCall(t, srv, version, "read_mail", map[string]any{"token": reader, "msg_serial": firstID})
			if got := passivePresentationText(t, srv, version, "reader-session"); !strings.Contains(got, "delivered") {
				t.Fatalf("existing item's pending-to-delivered state change suppressed: %q", got)
			}
			if got := passivePresentationText(t, srv, version, "reader-session"); got != "" {
				t.Fatalf("changed state repeated: %q", got)
			}
			waitingCall(t, srv, version, "bind_session", map[string]any{"token": reader, "session_id": "reader-new-session"})
			if got := passivePresentationText(t, srv, version, "reader-new-session"); got == "" {
				t.Fatal("new session lost its first presentation")
			}
		})
	}
}

func TestPassivePresentationFingerprintIncludesHiddenItems(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, reader, sender := passivePresentationFixture(t, version)
			var last float64
			for i := range 10 {
				last = waitingCall(t, srv, version, "send", map[string]any{"token": sender, "to": "reader", "type": "question", "body": fmt.Sprintf("question %d", i)})["msg_serial"].(float64)
			}
			first := passivePresentationText(t, srv, version, "reader-session")
			if !strings.Contains(first, "more mailbox items") || strings.Contains(first, fmt.Sprintf("#%.0f", last)) {
				t.Fatalf("setup: last item is not beyond shown page: %q", first)
			}
			// Replace a hidden item, retaining the count and identical shown
			// text. Text-only or count-only suppression loses this change.
			waitingCall(t, srv, version, "respond", map[string]any{"token": reader, "msg_serial": last, "disposition": "answer", "body": "answer"})
			waitingCall(t, srv, version, "send", map[string]any{"token": sender, "to": "reader", "type": "question", "body": "replacement hidden question"})
			if got := passivePresentationText(t, srv, version, "reader-session"); got != first {
				t.Fatalf("hidden identity change did not re-emit identical visible summary: first=%q got=%q", first, got)
			}
			if got := passivePresentationText(t, srv, version, "reader-session"); got != "" {
				t.Fatalf("unchanged hidden identity repeated summary: %q", got)
			}
		})
	}
}

func TestPassivePresentationKeepsEveryPendingFYIPage(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _, sender := passivePresentationFixture(t, version)
			for i := range 12 {
				waitingCall(t, srv, version, "send", map[string]any{"token": sender, "to": "reader", "type": "notify", "body": fmt.Sprintf("unique-fyi-marker-%02d", i)})
			}
			var all strings.Builder
			for range 3 {
				all.WriteString(passivePresentationText(t, srv, version, "reader-session"))
			}
			for i := range 12 {
				if n := strings.Count(all.String(), fmt.Sprintf("unique-fyi-marker-%02d", i)); n != 1 {
					t.Fatalf("FYI %d presented %d times, want once", i, n)
				}
			}
			if got := passivePresentationText(t, srv, version, "reader-session"); got != "" {
				t.Fatalf("fully presented FYIs repeated: %q", got)
			}
		})
	}
}

func TestPassivePresentationIncludesEveryOutstandingOutcome(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, reader, sender := passivePresentationFixture(t, version)
			id := waitingCall(t, srv, version, "send", map[string]any{"token": reader, "to": "sender", "type": "request", "body": "make progress"})["msg_serial"]
			waitingCall(t, srv, version, "respond", map[string]any{"token": sender, "msg_serial": id, "disposition": "approve"})
			for i := range 18 {
				waitingCall(t, srv, version, "respond", map[string]any{"token": sender, "msg_serial": id, "disposition": "progress", "body": fmt.Sprintf("progress-%d", i)})
			}
			if got := passivePresentationText(t, srv, version, "reader-session"); !strings.Contains(got, "19 outstanding update units") {
				t.Fatalf("setup: expected more outcomes than inline limit: %q", got)
			}
			if got := passivePresentationText(t, srv, version, "reader-session"); got != "" {
				t.Fatalf("unchanged outcome backlog repeated: %q", got)
			}
			waitingCall(t, srv, version, "respond", map[string]any{"token": sender, "msg_serial": id, "disposition": "progress", "body": "new hidden progress"})
			if got := passivePresentationText(t, srv, version, "reader-session"); !strings.Contains(got, "20 outstanding update units") {
				t.Fatalf("new hidden outcome suppressed: %q", got)
			}
			if got := passivePresentationText(t, srv, version, "reader-session"); got != "" {
				t.Fatalf("changed outcome backlog repeated: %q", got)
			}
		})
	}
}

// Existing batching, not a repair for already accepted harness frames. Plain
// resource reads represent dormant old bridges; offer reads use the existing
// additive capability. Neither is changed by passive-presentation suppression.
func TestPassivePresentationPreservesSocketBatchAndNewWake(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, reader, sender := passivePresentationFixture(t, version)
			send := func(body string) {
				waitingCall(t, srv, version, "send", map[string]any{"token": sender, "to": "reader", "type": "question", "body": body})
			}
			for i := range 3 {
				send(fmt.Sprintf("batch-question-%d", i))
			}
			// Natural prompts do not spend socket offers or durable mail.
			if passivePresentationText(t, srv, version, "reader-session") == "" {
				t.Fatal("setup: no passive presentation")
			}
			passivePresentationText(t, srv, version, "reader-session")
			read := func(extra map[string]any) map[string]any {
				meta := map[string]any{metaTokenKey: reader, SessionMetaKey: "reader-session"}
				for k, v := range extra {
					meta[k] = v
				}
				return result(t, rpc(t, srv, version, "resources/read", map[string]any{"uri": WakeDigestURI, "_meta": meta}), "wake digest")
			}
			text := func(r map[string]any) string { return r["contents"].([]any)[0].(map[string]any)["text"].(string) }
			for range 2 {
				legacy := text(read(nil))
				for i := range 3 {
					if !strings.Contains(legacy, fmt.Sprintf("batch-question-%d", i)) {
						t.Fatalf("old bridge read lost pending batch: %q", legacy)
					}
				}
			}
			offer := read(map[string]any{SocketOfferMetaKey: true})
			body := text(offer)
			for i := range 3 {
				if strings.Count(body, fmt.Sprintf("batch-question-%d", i)) != 1 {
					t.Fatalf("one atomic offer did not coalesce all pending items: %q", body)
				}
			}
			id, _ := offer["_meta"].(map[string]any)[SocketOfferIDMetaKey].(string)
			if id == "" {
				t.Fatal("setup: no offer reservation")
			}
			read(map[string]any{SocketOfferMetaKey: true, SocketOfferIDMetaKey: id, SocketWrittenMetaKey: true})
			// The session is mid-turn, so the harness queued that notice; its
			// dequeue runs UserPromptSubmit, and only then is a later item
			// written rather than carried by that hook.
			passivePresentationText(t, srv, version, "reader-session")
			send("fresh-after-written-batch")
			if got := text(read(map[string]any{SocketOfferMetaKey: true})); !strings.Contains(got, "fresh-after-written-batch") {
				t.Fatalf("later new item lost its own wake: %q", got)
			}
		})
	}
}
