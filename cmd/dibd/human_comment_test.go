// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// Enter at the relay's authenticated HTTP door. A comment on a privileged
// request is a human message to its sender, never a verdict or a grant. The
// later approval still needs its original fresh signature.
func TestHumanReplyWithoutDecidingKeepsPrivilegedRequestPending(t *testing.T) {
	h := newRelayHarness(t)
	h.enroll()
	serial := h.agentSends("comment-asker", core.Op{MsgType: core.MsgRequest, Body: "please grant", Grant: core.RoleCoordinator})
	auth := map[string]string{"Authorization": "Bearer " + h.session()}
	code, out := h.post("/api/human/answer", auth, map[string]any{"serial": serial, "disposition": "comment", "body": "I need more details"})
	if code != http.StatusOK {
		t.Fatalf("comment: %d %v", code, out)
	}
	n, open, err := h.eng.HumanNoticeFor(context.Background(), serial)
	if err != nil || !open || n.Grant != core.RoleCoordinator {
		t.Fatalf("comment changed request/fence: %+v %v %v", n, open, err)
	}
	reg, err := h.eng.Do(context.Background(), &core.Op{Kind: core.OpRegister, Name: "comment-asker", Nonce: "n-comment-asker"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.eng.Inbox(context.Background(), reg["token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	// The mailbox projection must contain the actual human-authored linked
	// text, not a canned desktop receipt in place of what they wrote.
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	if !strings.Contains(encoded, "I need more details") || !strings.Contains(encoded, "no decision") {
		t.Fatalf("linked human reply lost: %s", encoded)
	}
	if code, out := h.post("/api/human/delivery", auth, map[string]any{"serial": serial, "state": "posted", "reply_field_available": false}); code != http.StatusOK {
		t.Fatalf("reply capability receipt: %d %v", code, out)
	}
	mail, err := h.eng.ReadMail(context.Background(), reg["token"].(string), serial, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(mail)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"reply_field_available":false`) || !strings.Contains(string(raw), "typed reply unavailable") {
		t.Fatalf("relay reply-unavailable evidence lost in real read_mail projection: %s", raw)
	}
	if code, _ := h.post("/api/human/answer", auth, map[string]any{"serial": serial, "disposition": "approve"}); code != http.StatusUnauthorized {
		t.Fatalf("comment bypassed approval presence: %d", code)
	}
	if code, _ := h.post("/api/human/answer", auth, map[string]any{"serial": serial, "disposition": "comment", "body": "  "}); code != http.StatusBadRequest {
		t.Fatalf("empty comment accepted: %d", code)
	}
	q := h.agentSends("comment-question", core.Op{MsgType: core.MsgQuestion, Body: "question"})
	if code, _ := h.post("/api/human/answer", auth, map[string]any{"serial": q, "disposition": "comment", "body": "wrong kind"}); code != http.StatusBadRequest {
		t.Fatalf("question comment accepted: %d", code)
	}
}

func TestRelayPreservesDecodedReplyLimitThroughJSONEscaping(t *testing.T) {
	h := newRelayHarness(t)
	h.enroll()
	serial := h.agentSends("escaped-reply", core.Op{MsgType: core.MsgQuestion, Body: "Please type a reply"})
	body := strings.Repeat("<>&", (core.DefaultLimits().MaxBodyBytes-20)/3)
	code, out := h.post("/api/human/answer", map[string]string{"Authorization": "Bearer " + h.session()}, map[string]any{"serial": serial, "disposition": "answer", "body": body})
	if code != http.StatusOK {
		t.Fatalf("valid decoded reply refused after JSON escaping: %d %v", code, out)
	}
	reg, err := h.eng.Do(context.Background(), &core.Op{Kind: core.OpRegister, Name: "escaped-reply", Nonce: "n-escaped-reply"})
	if err != nil {
		t.Fatal(err)
	}
	mail, err := h.eng.ReadMail(context.Background(), reg["token"].(string), serial, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(mail)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct{ Message struct{ Response string } }
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Message.Response != body {
		t.Fatal("the exact typed reply was not recorded")
	}
}
