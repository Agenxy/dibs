package mcp

import (
	"strings"
	"testing"
)

func TestDeclareDistinguishesReviewFromDuplicateImplementation(t *testing.T) {
	for _, roles := range []struct {
		incumbent, incoming string
		duplicate           bool
	}{
		{"implement", "review", false},
		{"review", "implement", false},
		{"implement", "implement", true},
		{"review", "review", true},
		{"", "review", true},
	} {
		t.Run(roles.incumbent+"/"+roles.incoming, func(t *testing.T) {
			srv, _ := newServer(t)
			tokens := map[string]string{}
			for _, id := range []string{"incumbent", "incoming"} {
				r := toolCall(t, srv, "register", map[string]any{"name": id, "nonce": "review-fixture-" + id})
				var ok bool
				tokens[id], ok = r["token"].(string)
				if !ok {
					t.Fatalf("register setup: %v", r)
				}
				toolCall(t, srv, "check_in", map[string]any{"token": tokens[id]})
			}
			first := toolCall(t, srv, "declare", map[string]any{"token": tokens["incumbent"], "text": "shared work", "refs": []string{"request:9813"}, "activity": roles.incumbent})
			if first["__is_error"] == true {
				t.Fatalf("incumbent setup: %v", first)
			}
			got := toolCall(t, srv, "declare", map[string]any{"token": tokens["incoming"], "text": "shared work", "refs": []string{"request:9813"}, "activity": roles.incoming})
			if got["__is_error"] == true {
				t.Fatalf("declare failed: %v", got)
			}
			warning, _ := got["warning"].(string)
			if strings.Contains(warning, "duplicate") != roles.duplicate {
				t.Fatalf("roles %q/%q: wrong duplicate verdict: %v", roles.incumbent, roles.incoming, got)
			}
			if !roles.duplicate {
				if note, _ := got["note"].(string); !strings.Contains(note, "complementary") {
					t.Fatalf("review relationship was not explained: %v", got)
				}
				rows, _ := got["overlaps"].([]any)
				if len(rows) != 1 || rows[0].(map[string]any)["signal"] != "same-objective" {
					t.Fatalf("different roles hid same-objective evidence: %v", got)
				}
			}
		})
	}
}

func TestReviewGuidanceUsesATerminalQuestion(t *testing.T) {
	srv, _ := newServer(t)
	r := rpc(t, srv, "2026-07-28", "resources/read", map[string]any{"uri": "dibs://skills"})
	contents := r["result"].(map[string]any)["contents"].([]any)
	text := contents[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "verdict-only review") || !strings.Contains(text, `type: "question"`) {
		t.Fatal("agent-facing skills do not teach that a verdict-only review is a question")
	}
	tokens := map[string]string{}
	for _, id := range []string{"author", "reviewer"} {
		r := toolCall(t, srv, "register", map[string]any{"name": id, "nonce": "review-question-fixture-" + id})
		var ok bool
		tokens[id], ok = r["token"].(string)
		if !ok {
			t.Fatalf("register setup: %v", r)
		}
		toolCall(t, srv, "check_in", map[string]any{"token": tokens[id]})
	}
	sent := toolCall(t, srv, "send", map[string]any{"token": tokens["author"], "to": "reviewer", "type": "question", "body": "review this artifact and give your verdict"})
	serial, ok := sent["msg_serial"].(float64)
	if !ok {
		t.Fatalf("send setup: %v", sent)
	}
	answered := toolCall(t, srv, "respond", map[string]any{"token": tokens["reviewer"], "msg_serial": serial, "disposition": "answer", "body": "accepted; validation checked"})
	if answered["__is_error"] == true {
		t.Fatalf("review answer: %v", answered)
	}
	mail := toolCall(t, srv, "read_mail", map[string]any{"token": tokens["author"], "msg_serial": serial})
	if msg, _ := mail["message"].(map[string]any); msg["state"] != "answered" {
		t.Fatalf("verdict left review as owed work: %v", mail)
	}
}

func TestDeclareKeepsDuplicateEvidenceWhenPeerAlsoReviews(t *testing.T) {
	srv, _ := newServer(t)
	tokens := map[string]string{}
	for _, id := range []string{"peer", "incoming"} {
		r := toolCall(t, srv, "register", map[string]any{"name": id, "nonce": "mixed-review-" + id})
		var ok bool
		tokens[id], ok = r["token"].(string)
		if !ok {
			t.Fatalf("register setup: %v", r)
		}
		toolCall(t, srv, "check_in", map[string]any{"token": tokens[id]})
	}
	for _, activity := range []string{"review", "implement"} {
		r := toolCall(t, srv, "declare", map[string]any{"token": tokens["peer"], "text": "shared work", "refs": []string{"request:9813"}, "activity": activity})
		if r["__is_error"] == true {
			t.Fatalf("peer slot setup: %v", r)
		}
	}
	got := toolCall(t, srv, "declare", map[string]any{"token": tokens["incoming"], "text": "shared work", "refs": []string{"request:9813"}, "activity": "implement"})
	warning, _ := got["warning"].(string)
	rows, _ := got["overlaps"].([]any)
	if !strings.Contains(warning, "duplicate") || len(rows) != 2 {
		t.Fatalf("complementary review hid the peer's duplicate implementation: %v", got)
	}
}
