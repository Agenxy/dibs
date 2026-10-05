package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// Both protocol dispatchers, the actual argument decoder and the writer run.
// No selection helper or nonce index is installed by a test.
func recoveryCall(t *testing.T, srv *httptest.Server, version, name string, args map[string]any, meta map[string]any) map[string]any {
	t.Helper()
	params := map[string]any{"name": name, "arguments": args}
	if meta != nil {
		params["_meta"] = meta
	}
	out := rpc(t, srv, version, "tools/call", params)
	if fault, ok := out["error"].(map[string]any); ok {
		return map[string]any{"rpc_error": fault}
	}
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing tool response: %v", out)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatal("missing tool content")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content[0].(map[string]any)["text"].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func recoveryOK(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	id, _ := result["agent_id"].(string)
	token, _ := result["token"].(string)
	if result["ok"] != true && (id == "" || token == "") && result["message"] == nil {
		t.Fatalf("setup/operation did not succeed: %v", result)
	}
	return result
}

func TestCredentialRecoveryChoosesOldestThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, eng, _ := newServerWithEngine(t)
			call := func(name string, args map[string]any) map[string]any {
				return recoveryCall(t, srv, version, name, args, nil)
			}
			older := recoveryOK(t, call("register", map[string]any{"name": "worker", "nonce": "old-secret"}))
			newer := recoveryOK(t, call("register", map[string]any{"name": "worker", "nonce": "new-secret"}))
			if older["agent_id"] == newer["agent_id"] {
				t.Fatal("setup: two secrets did not create siblings")
			}
			recoveryOK(t, call("check_in", map[string]any{"token": newer["token"]}))
			mail := recoveryOK(t, call("send", map[string]any{"token": newer["token"], "to": older["agent_id"], "type": "question", "body": "original mail"}))
			recoveryOK(t, call("sign_off", map[string]any{"token": older["token"]}))
			got := recoveryOK(t, call("register", map[string]any{"name": "worker", "recovery_nonces": []string{"new-secret", "old-secret"}}))
			if got["agent_id"] != older["agent_id"] || got["recovery_nonce_index"] != float64(1) {
				t.Fatalf("oldest credential not selected: %v", got)
			}
			read := recoveryOK(t, call("read_mail", map[string]any{"token": got["token"], "msg_serial": mail["msg_serial"]}))
			if !mentions(read, "original mail") {
				t.Fatal("original mailbox was lost")
			}
			board, err := eng.Board(context.Background())
			if err != nil || len(board["agents"].([]map[string]any)) != 2 {
				t.Fatalf("recovery allocated a sibling: %v", err)
			}
			control := recoveryOK(t, call("register", map[string]any{"name": "worker", "nonce": "new-secret"}))
			if control["agent_id"] != newer["agent_id"] || control["token"] != newer["token"] {
				t.Fatal("unselected credential/token was changed")
			}
		})
	}
}

func TestCredentialRecoveryFailureAndGuardPaths(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, test := range []struct {
			name string
			args map[string]any
			meta map[string]any
			code string
		}{
			{"unknown", map[string]any{"name": "worker", "recovery_nonces": []string{"unknown-a", "unknown-b"}}, nil, "E_RECOVERY_UNPROVEN"},
			{"wrong-name", map[string]any{"name": "other", "recovery_nonces": []string{"worker-secret", "unknown"}}, nil, "E_RECOVERY_UNPROVEN"},
			{"wrong-host", map[string]any{"name": "worker", "recovery_nonces": []string{"worker-secret", "unknown"}}, map[string]any{HostMetaKey: "another-host"}, "E_RECOVERY_UNPROVEN"},
			{"explicit-conflict", map[string]any{"name": "worker", "nonce": "worker-secret", "recovery_nonces": []string{"worker-secret", "unknown"}}, nil, "E_BAD_ARG"},
			{"empty", map[string]any{"name": "worker", "recovery_nonces": []string{}}, nil, "E_BAD_ARG"},
			{"duplicate", map[string]any{"name": "worker", "recovery_nonces": []string{"worker-secret", "worker-secret"}}, nil, "E_BAD_ARG"},
			{"oversized", map[string]any{"name": "worker", "recovery_nonces": []string{"worker-secret", strings.Repeat("a", core.DefaultLimits().MaxIDBytes+1)}}, nil, "E_TOO_LARGE"},
		} {
			t.Run(version+"/"+test.name, func(t *testing.T) {
				srv, eng, _ := newServerWithEngine(t)
				recoveryOK(t, recoveryCall(t, srv, version, "register", map[string]any{"name": "worker", "nonce": "worker-secret"}, nil))
				before, err := eng.Board(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				got := recoveryCall(t, srv, version, "register", test.args, test.meta)
				after, err := eng.Board(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if before["serial"] != after["serial"] {
					t.Fatal("failed recovery mutated replayable state")
				}
				if got["rpc_error"] != nil {
					t.Log("old-schema refusal: serial unchanged; no register op applied")
				}
				if got["code"] != test.code {
					t.Fatalf("want %s, got %v", test.code, got)
				}
			})
		}
		t.Run(version+"/one-valid-and-over-limit", func(t *testing.T) {
			srv, eng, _ := newServerWithEngine(t)
			call := func(args map[string]any) map[string]any { return recoveryCall(t, srv, version, "register", args, nil) }
			older := recoveryOK(t, call(map[string]any{"name": "worker", "nonce": "worker-secret"}))
			got := recoveryOK(t, call(map[string]any{"name": "worker", "recovery_nonces": []string{"unknown", "worker-secret"}}))
			if got["agent_id"] != older["agent_id"] || got["recovery_nonce_index"] != float64(1) {
				t.Fatal("the one valid credential was not used")
			}
			group := make([]string, 17)
			for i := range group {
				group[i] = fmt.Sprintf("nonce-%d", i)
			}
			before, err := eng.Board(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			bad := call(map[string]any{"name": "worker", "recovery_nonces": group})
			if bad["code"] != "E_BAD_ARG" {
				t.Fatalf("overflow accepted: %v", bad)
			}
			after, err := eng.Board(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if before["serial"] != after["serial"] {
				t.Fatal("overflow mutated state")
			}
		})
		t.Run(version+"/active-session", func(t *testing.T) {
			srv, _, _ := newServerWithEngine(t)
			recoveryOK(t, recoveryCall(t, srv, version, "register", map[string]any{"name": "victim", "nonce": "victim-secret", "session_id": "3f2504e0-4f89-11d3-9a0c-0305e82c3301"}, nil))
			recoveryOK(t, recoveryCall(t, srv, version, "register", map[string]any{"name": "worker", "nonce": "worker-secret"}, nil))
			got := recoveryCall(t, srv, version, "register", map[string]any{"name": "worker", "recovery_nonces": []string{"worker-secret", "unknown"}, "session_id": "3f2504e0-4f89-11d3-9a0c-0305e82c3301"}, nil)
			if got["code"] != "E_SESSION_TAKEN" {
				t.Fatalf("credential selection bypassed session guard: %v", got)
			}
		})
	}
}

func TestCredentialRecoveryAdoptHintCoordinatorFirst(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, coordinator := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/coordinator-%t", version, coordinator), func(t *testing.T) {
				srv, eng, _ := newServerWithEngine(t)
				call := func(name string, args map[string]any, sid string) map[string]any {
					return recoveryCall(t, srv, version, name, args, map[string]any{"threadId": sid})
				}
				boss := recoveryOK(t, call("register", map[string]any{"name": "boss", "nonce": "boss-secret"}, "boss-session"))
				if coordinator {
					if _, err := eng.Do(context.Background(), &core.Op{Kind: core.OpGrantRole, To: "boss", Mode: core.RoleCoordinator, RoleByHuman: true}); err != nil {
						t.Fatal(err)
					}
				}
				recoveryOK(t, call("check_in", map[string]any{"token": boss["token"]}, "boss-session"))
				old := recoveryOK(t, call("register", map[string]any{"name": "worker", "nonce": "old-secret"}, "old-session"))
				mail := recoveryOK(t, call("send", map[string]any{"token": boss["token"], "to": old["agent_id"], "type": "question", "body": "stranded original"}, "boss-session"))
				recoveryOK(t, call("sign_off", map[string]any{"token": old["token"]}, "old-session"))
				fresh := recoveryOK(t, call("register", map[string]any{"name": "worker", "nonce": "fresh-secret"}, "new-session"))
				hint, ok := fresh["recovery_request"].(map[string]any)
				if !ok {
					t.Fatalf("no ready adoption request: %v", fresh)
				}
				want := "human"
				if coordinator {
					want = "coordinator"
				}
				if hint["to"] != want || hint["adopt"] != old["agent_id"] || !strings.Contains(fresh["name_note"].(string), "await approval") {
					t.Fatalf("wrong fallback: %v", fresh)
				}
				if !coordinator {
					return
				} // do not raise a native human notification
				recoveryOK(t, call("check_in", map[string]any{"token": fresh["token"]}, "new-session"))
				hint["token"] = fresh["token"]
				request := recoveryOK(t, call("send", hint, "new-session"))
				recoveryOK(t, call("check_in", map[string]any{"token": boss["token"]}, "boss-session"))
				recoveryOK(t, call("respond", map[string]any{"token": boss["token"], "msg_serial": request["msg_serial"], "disposition": "approve"}, "boss-session"))
				read := recoveryOK(t, call("read_mail", map[string]any{"token": fresh["token"], "msg_serial": mail["msg_serial"]}, "new-session"))
				if !mentions(read, "stranded original") {
					t.Fatal("approved adoption failed to recover original mail")
				}
			})
		}
	}
}
