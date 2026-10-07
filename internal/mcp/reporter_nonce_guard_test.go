package mcp

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// Stall notices depend on daemon-only authorship. Enter through both real MCP
// decoders and the writer: a helper-only test cannot establish that callers are
// refused before the public nonce mints or recovers the reporting identity.
func TestReporterNonceCannotBeAcquiredThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, minted := range []bool{false, true} {
			name := version + "/fresh"
			if minted {
				name = version + "/minted"
			}
			t.Run(name, func(t *testing.T) {
				srv, eng, _ := newServerWithEngine(t)
				ctx := context.Background()
				var reporterToken string
				if minted {
					res, err := eng.Do(ctx, &core.Op{
						Kind: core.OpRegister, HumanMint: true, Name: "dibs",
						Nonce: core.DibsNonce, NewToken: "reporter-probe-token",
						AgentKind: core.KindPersistent, NoProcess: true,
						Agent: &core.AgentInfo{HostID: "reporter-probe-host"},
					})
					reporterToken, _ = res["token"].(string)
					if err != nil || res["agent_id"] == "" || reporterToken == "" {
						t.Fatalf("setup: daemon mint failed: %v %v", res, err)
					}
				}
				for _, method := range []string{"register", "resume"} {
					for _, callerName := range []string{"dibs", "attacker"} {
						_, before, err := eng.SubscribeInfo(ctx, "")
						if err != nil {
							t.Fatal("setup:", err)
						}
						args := map[string]any{"nonce": core.DibsNonce}
						if method == "register" {
							args["name"] = callerName
						} else {
							args["resume_id"] = "reporter-nonce-probe-resume"
						}
						got := recoveryCall(t, srv, version, method, args, nil)
						if got["code"] != "E_NOT_PERMITTED" || got["token"] != nil {
							t.Fatalf("%s %s was not refused before acquiring reporter identity (code %v, token returned %t)",
								method, callerName, got["code"], got["token"] != nil)
						}
						_, after, err := eng.SubscribeInfo(ctx, "")
						if err != nil || after != before {
							t.Fatalf("refused call changed state: %d -> %d, %v", before, after, err)
						}
					}
				}
				if minted {
					_, before, err := eng.SubscribeInfo(ctx, "")
					if err != nil {
						t.Fatal("setup:", err)
					}
					got := recoveryCall(t, srv, version, "register", map[string]any{
						"name": "dibs", "recovery_nonces": []string{"other-secret", core.DibsNonce},
					}, map[string]any{HostMetaKey: "reporter-probe-host"})
					if got["code"] != "E_NOT_PERMITTED" || got["token"] != nil {
						t.Fatalf("credential-group recovery was not refused (code %v, token returned %t)",
							got["code"], got["token"] != nil)
					}
					_, after, err := eng.SubscribeInfo(ctx, "")
					if err != nil || after != before {
						t.Fatalf("refused recovery changed state: %d -> %d, %v", before, after, err)
					}
					got = recoveryCall(t, srv, version, "check_in", map[string]any{
						"token": reporterToken,
					}, nil)
					if got["ok"] != true {
						t.Fatalf("reporter token was invalidated: %v", got)
					}
				}
			})
		}
	}
}
