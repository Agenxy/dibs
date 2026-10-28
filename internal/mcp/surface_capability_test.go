// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
)

// Enter the HTTP dispatcher on both protocol eras. Merely filtering the
// private RPC is insufficient: the registration reply must describe the
// capability this caller can actually use.
func TestBridgeSurfaceCapabilityIsOnlyOnLocalRegistrationReplies(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, tc := range []struct {
			name     string
			meta     map[string]any
			remote   bool
			invited  bool
			eligible bool
		}{
			{name: "local", eligible: true},
			{name: "local-explicit-host", meta: map[string]any{HostMetaKey: "test"}, eligible: true},
			{name: "foreign-through-loopback", meta: map[string]any{HostMetaKey: "another-host"}},
			{name: "hostless", meta: map[string]any{HostlessMetaKey: true}},
			{name: "hostless-claiming-own-host", meta: map[string]any{HostlessMetaKey: true, HostMetaKey: "test"}},
			{name: "remote", remote: true},
			{name: "remote-claiming-own-host", remote: true, meta: map[string]any{HostMetaKey: "test"}},
			{name: "invited", invited: true, meta: map[string]any{HostMetaKey: "test"}},
		} {
			t.Run(version+"/"+tc.name, func(t *testing.T) {
				_, _, server := newServerWithEngine(t)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if tc.remote {
						r.RemoteAddr = "192.0.2.1:12345"
					}
					if tc.invited {
						r = r.WithContext(engine.WithInvitation(r.Context(), engine.Invitation{
							Name: "surface-capability", IssuedBy: core.HumanActor,
						}))
					}
					server.ServeHTTP(w, r)
				}))
				defer srv.Close()
				call := func(name string, args map[string]any, want bool) map[string]any {
					t.Helper()
					out := rpc(t, srv, version, "tools/call", map[string]any{
						"name": name, "arguments": args, "_meta": tc.meta,
					})
					result, ok := out["result"].(map[string]any)
					if !ok || result["isError"] == true {
						t.Fatalf("setup: %s did not succeed: %v", name, out)
					}
					meta, _ := result["_meta"].(map[string]any)
					if got := meta[BridgeSurfaceEvidenceMetaKey]; (got == true) != want || (!want && got != nil) {
						t.Errorf("%s capability=%v, want advertised=%v", name, got, want)
					}
					content, ok := result["content"].([]any)
					if !ok || len(content) != 1 {
						t.Fatalf("setup: %s content missing: %v", name, result)
					}
					var payload map[string]any
					if err := json.Unmarshal([]byte(content[0].(map[string]any)["text"].(string)), &payload); err != nil {
						t.Fatal(err)
					}
					return payload
				}
				reg := call("register", map[string]any{
					"name": "surface-capability", "description": "capability scope fixture", "nonce": "surface-capability-nonce",
				}, tc.eligible)
				if reg["token"] == nil || reg["agent_id"] == nil {
					t.Fatalf("setup: successful registration has no own credentials: %v", reg)
				}
				resumed := call("resume", map[string]any{
					"nonce": "surface-capability-nonce", "resume_id": "surface-capability-resume",
				}, tc.eligible)
				if resumed["token"] == nil || resumed["agent_id"] != reg["agent_id"] {
					t.Fatalf("setup: resume did not recover its own row: %v", resumed)
				}
				call("check_in", map[string]any{"token": resumed["token"]}, false)
				call("update", map[string]any{"token": resumed["token"], "description": "still no surface RPC capability"}, false)
			})
		}
	}
}
