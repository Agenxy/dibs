package mcp

import "testing"

// This regression uses only entry points that existed before receipt support,
// so it can be run unchanged against the parent commit.
func TestHumanRouteResultMatchesActualRelayHandoff(t *testing.T) {
	srv, eng, _ := newServerWithEngine(t)
	feed, detach := eng.AttachHumanRelay()
	defer detach()
	reg := toolCall(t, srv, "register", map[string]any{"name": "sender", "nonce": "route-contract"})
	token, ok := reg["token"].(string)
	if !ok {
		t.Fatalf("register setup: %v", reg)
	}
	args := map[string]any{"token": token, "to": "human", "type": "question", "body": "route fixture", "op_id": "route-retry"}
	sent := toolCall(t, srv, "send", args)
	if sent["human_route"] != "relay" || sent["human_relay_count"] != float64(1) {
		t.Fatalf("route unobservable: %v", sent)
	}
	serial, ok := sent["msg_serial"].(float64)
	if !ok {
		t.Fatalf("send setup: %v", sent)
	}
	read := toolCall(t, srv, "read_mail", map[string]any{"token": token, "msg_serial": serial})
	d, ok := read["human_delivery"].(map[string]any)
	if !ok || d["state"] != "queued" {
		t.Fatalf("handoff misrepresented: %v", read)
	}
	select {
	case <-feed:
	default:
		t.Fatal("first send was not queued")
	}
	retry := toolCall(t, srv, "send", args)
	if retry["human_route"] != "relay" || retry["deduplicated"] != true {
		t.Fatalf("retry: %v", retry)
	}
	select {
	case <-feed:
		t.Fatal("retry posted another notification")
	default:
	}
}
