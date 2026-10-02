package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestHumanReceiptRequiresRelayAuthenticationAndHumanMail(t *testing.T) {
	h := newRelayHarness(t)
	h.enroll()
	session := h.session()
	serial := h.agentSends("requester", core.Op{MsgType: core.MsgRequest, Body: "approve fixture"})
	body := map[string]any{"serial": serial, "state": "posted"}
	if code, _ := h.post("/api/human/delivery", nil, body); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated receipt: %d", code)
	}
	if code, _ := h.post("/api/human/delivery", map[string]string{"X-Dibs-Secret": "board-secret"}, body); code != http.StatusUnauthorized {
		t.Fatalf("agent secret accepted: %d", code)
	}
	headers := map[string]string{"Authorization": "Bearer " + session}
	for _, state := range []string{"posted", "dismissed", "failed"} {
		body["state"] = state
		if code, out := h.post("/api/human/delivery", headers, body); code != http.StatusOK {
			t.Fatalf("receipt: %d %v", code, out)
		}
	}
	for _, b := range []map[string]any{{"serial": serial, "state": "answered"}, {"serial": uint64(999999), "state": "posted"}} {
		if code, _ := h.post("/api/human/delivery", headers, b); code != http.StatusBadRequest {
			t.Fatalf("invalid receipt: %d", code)
		}
	}
	n, open, err := h.eng.HumanNoticeFor(context.Background(), serial)
	if err != nil || !open || n.Serial != serial {
		t.Fatalf("receipt answered/granted something: %v %v %v", n, open, err)
	}
}
