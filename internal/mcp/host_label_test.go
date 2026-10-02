package mcp

import (
	"os"
	"testing"
)

// Enter through real registration and check_in, including the compact projection.
func TestOneMachineHasOneDisplayLabelAcrossRegistrations(t *testing.T) {
	srv, _ := newServer(t)
	for _, a := range []struct{ name, label string }{{"first", "old-name"}, {"second", "different-name"}} {
		r := toolCall(t, srv, "register", map[string]any{"name": a.name, "host": a.label})
		if r["token"] == nil {
			t.Fatalf("setup: register: %v", r)
		}
	}
	r := toolCall(t, srv, "register", map[string]any{"name": "reader"})
	token, ok := r["token"].(string)
	if !ok {
		t.Fatalf("setup: reader: %v", r)
	}
	want, err := os.Hostname()
	if err != nil || want == "" {
		t.Fatalf("setup: hostname: %v", err)
	}
	for _, detail := range []bool{false, true} {
		result := toolCall(t, srv, "check_in", map[string]any{"token": token, "detail": detail})
		board := result["board"].(map[string]any)
		found := 0
		for _, raw := range board["agents"].([]any) {
			row := raw.(map[string]any)
			id := row["id"].(string)
			if id != "first" && id != "second" {
				continue
			}
			found++
			if row["host"] != want {
				t.Errorf("detail=%v %s display=%v want %s", detail, id, row["host"], want)
			}
			if detail {
				info := row["agent"].(map[string]any)
				rawLabel := map[string]string{"first": "old-name", "second": "different-name"}[id]
				if info["host"] != rawLabel {
					t.Errorf("raw label lost: %v", info)
				}
			}
		}
		if found != 2 {
			t.Fatalf("setup: missing rows: %v", board)
		}
	}
}

func TestRemoteDisplayLabelsAreSharedButUnknownHostsStaySeparate(t *testing.T) {
	srv, _ := newServer(t)
	meta := map[string]any{HostMetaKey: "foreign-computer"}
	for _, a := range []struct{ name, label string }{{"older", "old-remote"}, {"newer", "new-remote"}} {
		r := toolCallWithMeta(t, srv, "register", map[string]any{"name": a.name, "host": a.label}, meta)
		if r["token"] == nil {
			t.Fatalf("setup: %v", r)
		}
	}
	for _, a := range []struct{ name, label string }{{"unknown-one", "unproved-one"}, {"unknown-two", "unproved-two"}} {
		r := toolCallWithMeta(t, srv, "register", map[string]any{"name": a.name, "host": a.label}, map[string]any{HostlessMetaKey: true})
		if r["token"] == nil {
			t.Fatalf("setup: %v", r)
		}
	}
	r := toolCall(t, srv, "register", map[string]any{"name": "reader"})
	token, ok := r["token"].(string)
	if !ok {
		t.Fatalf("setup: %v", r)
	}
	result := toolCall(t, srv, "check_in", map[string]any{"token": token, "detail": true})
	board := result["board"].(map[string]any)
	wants := map[string]string{"older": "new-remote", "newer": "new-remote", "unknown-one": "unproved-one", "unknown-two": "unproved-two"}
	for _, raw := range board["agents"].([]any) {
		row := raw.(map[string]any)
		id := row["id"].(string)
		if want, exists := wants[id]; exists {
			if row["host"] != want {
				t.Errorf("%s display=%v want %s", id, row["host"], want)
			}
			if id == "older" || id == "newer" {
				info := row["agent"].(map[string]any)
				if info["host_id"] != "foreign-computer" {
					t.Errorf("foreign identity overwritten: %v", info)
				}
			}
			delete(wants, id)
		}
	}
	if len(wants) != 0 {
		t.Fatalf("setup: missing rows: %v", wants)
	}
}
