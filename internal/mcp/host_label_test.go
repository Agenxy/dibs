package mcp

import (
	"testing"

	"github.com/agenxy/dibs/internal/hostname"
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
	want := hostname.Name()
	if want == "" {
		t.Fatal("setup: display name unavailable")
	}
	// The actual board tool trims its private panel payload independently.
	// Assert the row field, not agent.host: the latter survives the broken filter.
	panel := rawToolResult(t, srv, "board", map[string]any{"token": token})
	meta := panel["_meta"].(map[string]any)
	payload := asMap(meta[panelDataMetaKey])
	panelBoard := asMap(payload["board"])
	panelFound := 0
	for _, row := range asMaps(panelBoard["agents"]) {
		if row["id"] == "first" || row["id"] == "second" {
			panelFound++
			if row["host"] != want {
				t.Errorf("actual panel row label=%v want %s", row["host"], want)
			}
		}
	}
	if panelFound != 2 {
		t.Fatalf("setup: private panel missing rows: %v", panelBoard)
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
	var olderToken string
	for _, a := range []struct{ name, label string }{{"older", "old-remote"}, {"newer", "new-remote"}} {
		r := toolCallWithMeta(t, srv, "register", map[string]any{"name": a.name, "host": a.label}, meta)
		if r["token"] == nil {
			t.Fatalf("setup: %v", r)
		}
		if a.name == "older" {
			olderToken, _ = r["token"].(string)
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
	// A real later checkpoint selects that member's label for the whole machine.
	result = toolCall(t, srv, "check_in", map[string]any{"token": olderToken})
	board = result["board"].(map[string]any)
	for _, raw := range board["agents"].([]any) {
		row := raw.(map[string]any)
		if row["id"] == "older" || row["id"] == "newer" {
			if row["host"] != "old-remote" {
				t.Errorf("later checkpoint display=%v", row["host"])
			}
		}
	}
}
