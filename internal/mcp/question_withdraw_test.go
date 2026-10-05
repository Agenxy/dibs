package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/engine"
)

// Use the same HTTP tools/call door as a connected harness, including the
// legacy handshake. A tool-level refusal remains distinct from an RPC error.
func questionWithdrawalCall(t *testing.T, srv *httptest.Server, version, name string, args map[string]any) map[string]any {
	t.Helper()
	out := rpc(t, srv, version, "tools/call", map[string]any{"name": name, "arguments": args})
	box := result(t, out, name)
	content, ok := box["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("%s: no content: %v", name, box)
	}
	item, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("%s: invalid content: %v", name, content)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(item["text"].(string)), &payload); err != nil {
		t.Fatal("tool payload:", err)
	}
	payload["__is_error"] = box["isError"] == true
	return payload
}

func questionWithdrawalServer(t *testing.T, dir, version string) (*httptest.Server, *engine.Engine, func()) {
	t.Helper()
	srv, eng, stop := restartableQueueServer(t, dir)
	if version == "2025-11-25" {
		handshake := result(t, rpc(t, srv, "", "initialize", map[string]any{
			"protocolVersion": version, "capabilities": map[string]any{},
			"clientInfo": map[string]any{"name": "question-withdrawal", "version": "1"},
		}), "setup legacy initialize")
		if handshake["protocolVersion"] != version {
			t.Fatal("setup: wrong legacy version", handshake)
		}
	}
	return srv, eng, stop
}

func questionWithdrawalOK(t *testing.T, srv *httptest.Server, version, name string, args map[string]any) map[string]any {
	t.Helper()
	r := questionWithdrawalCall(t, srv, version, name, args)
	if r["__is_error"] == true {
		t.Fatalf("setup %s refused: %v", name, r)
	}
	return r
}

func TestQuestionWithdrawalThroughMCPAndEncryptedRestart(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			for _, state := range []string{"pending", "delivered", "acked"} {
				t.Run(state, func(t *testing.T) {
					dir := t.TempDir()
					srv, _, stop := questionWithdrawalServer(t, dir, version)
					lead := questionWithdrawalOK(t, srv, version, "register", map[string]any{"name": "lead", "nonce": "question-lead"})["token"]
					worker := questionWithdrawalOK(t, srv, version, "register", map[string]any{"name": "worker", "nonce": "question-worker"})["token"]
					n := questionWithdrawalOK(t, srv, version, "send", map[string]any{
						"token": lead, "to": "worker", "type": "question", "body": "Still needed?",
					})["msg_serial"]
					if n == nil || lead == nil || worker == nil {
						t.Fatal("setup: missing send serial or registration token")
					}
					switch state {
					case "delivered":
						mail := questionWithdrawalOK(t, srv, version, "inbox", map[string]any{"token": worker})
						if len(mail["messages"].([]any)) != 1 {
							t.Fatalf("setup delivery: %v", mail)
						}
					case "acked":
						r := questionWithdrawalOK(t, srv, version, "ack", map[string]any{"token": worker, "msg_serial": n})
						if r["state"] != "acked" {
							t.Fatalf("setup acknowledgement: %v", r)
						}
					}
					r := questionWithdrawalCall(t, srv, version, "respond", map[string]any{
						"token": lead, "msg_serial": n, "disposition": "withdraw", "body": "answered elsewhere",
					})
					if r["__is_error"] == true || r["state"] != "withdrawn" {
						t.Fatalf("question withdrawal: %v", r)
					}
					stop()
					srv, _, stop = questionWithdrawalServer(t, dir, version)
					checkpoint := questionWithdrawalOK(t, srv, version, "check_in", map[string]any{"token": worker})
					updates, _ := json.Marshal(checkpoint["agent_updates"])
					if !strings.Contains(string(updates), "withdrew question") || strings.Contains(string(updates), "answered your question") {
						t.Fatalf("restart receipt: %s", updates)
					}
					mail := questionWithdrawalOK(t, srv, version, "read_mail", map[string]any{"token": worker, "msg_serial": n})
					m := mail["message"].(map[string]any)
					if m["state"] != "withdrawn" || m["withdrawal_reason"] != "answered elsewhere" || m["consumed"] != false {
						t.Fatalf("durable receipt: %v", mail)
					}
					late := questionWithdrawalCall(t, srv, version, "respond", map[string]any{
						"token": worker, "msg_serial": n, "disposition": "answer", "body": "late",
					})
					if late["__is_error"] != true || late["code"] != "E_MSG_FINAL" {
						t.Fatalf("withdrawn question answered: %v", late)
					}
					ack := questionWithdrawalOK(t, srv, version, "ack", map[string]any{"token": worker, "msg_serial": n})
					if ack["ok"] != true {
						t.Fatalf("ack: %v", ack)
					}
					stop()
					srv, _, _ = questionWithdrawalServer(t, dir, version)
					checkpoint = questionWithdrawalOK(t, srv, version, "check_in", map[string]any{"token": worker})
					updates, _ = json.Marshal(checkpoint["agent_updates"])
					if strings.Contains(string(updates), "withdrew question") {
						t.Fatalf("acknowledged withdrawal repeated: %s", updates)
					}
				})
			}
		})
	}
}

func TestQuestionWithdrawalReplacementMayBeEitherOwnedType(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			for _, source := range []string{"question", "request"} {
				for _, replacement := range []string{"question", "request"} {
					t.Run(source+"-to-"+replacement, func(t *testing.T) {
						srv, _, _ := questionWithdrawalServer(t, t.TempDir(), version)
						lead := questionWithdrawalOK(t, srv, version, "register", map[string]any{"name": "lead"})["token"]
						questionWithdrawalOK(t, srv, version, "register", map[string]any{"name": "worker"})
						send := func(kind string) any {
							return questionWithdrawalOK(t, srv, version, "send", map[string]any{
								"token": lead, "to": "worker", "type": kind, "body": "replacement test",
							})["msg_serial"]
						}
						n, next := send(source), send(replacement)
						r := questionWithdrawalCall(t, srv, version, "respond", map[string]any{
							"token": lead, "msg_serial": n, "disposition": "withdraw", "superseded_by": next,
						})
						if r["__is_error"] == true || r["state"] != "withdrawn" || r["superseded_by"] != next {
							t.Fatalf("owned question/request replacement: %v", r)
						}
						mail := questionWithdrawalOK(t, srv, version, "read_mail", map[string]any{"token": lead, "msg_serial": next})
						if mail["message"].(map[string]any)["state"] != "pending" {
							t.Fatalf("withdrawal started its replacement: %v", mail)
						}
					})
				}
			}
		})
	}
}

func TestQuestionWithdrawalRefusalsNameTheActualRuleThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			for _, scenario := range []string{"notify", "handoff", "answered", "denied", "done", "foreign"} {
				t.Run(scenario, func(t *testing.T) {
					srv, eng, _ := questionWithdrawalServer(t, t.TempDir(), version)
					lead := questionWithdrawalOK(t, srv, version, "register", map[string]any{"name": "lead"})["token"]
					worker := questionWithdrawalOK(t, srv, version, "register", map[string]any{"name": "worker"})["token"]
					kind, code, hint := "question", "E_MSG_FINAL", "finished"
					switch scenario {
					case "notify", "handoff":
						kind, code, hint = scenario, "E_BAD_DISPOSITION", scenario
					case "denied", "done":
						kind = "request"
					case "foreign":
						code, hint = "E_NOT_SENDER", "YOU sent"
					}
					n := questionWithdrawalOK(t, srv, version, "send", map[string]any{
						"token": lead, "to": "worker", "type": kind, "body": "refusal test",
					})["msg_serial"]
					if scenario == "answered" || scenario == "denied" || scenario == "done" {
						disposition := map[string]string{"answered": "answer", "denied": "deny", "done": "approve"}[scenario]
						decision := questionWithdrawalOK(t, srv, version, "respond", map[string]any{"token": worker, "msg_serial": n, "disposition": disposition})
						if decision["state"] != map[string]string{"answered": "answered", "denied": "denied", "done": "approved"}[scenario] {
							t.Fatal("setup: wrong decision state", decision)
						}
						if scenario == "done" {
							questionWithdrawalOK(t, srv, version, "respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "done"})
						}
					}
					before, err := eng.Board(context.Background())
					if err != nil {
						t.Fatal("setup board:", err)
					}
					token := lead
					if scenario == "foreign" {
						token = worker
					}
					r := questionWithdrawalCall(t, srv, version, "respond", map[string]any{"token": token, "msg_serial": n, "disposition": "withdraw"})
					if r["__is_error"] != true || r["code"] != code || !strings.Contains(r["hint"].(string), hint) {
						t.Fatalf("false withdrawal diagnosis: %v; want %s and hint %q", r, code, hint)
					}
					if scenario != "foreign" && !strings.Contains(r["message"].(string), scenario) {
						t.Fatalf("refusal omitted actual type/state %s: %v", scenario, r)
					}
					after, err := eng.Board(context.Background())
					if err != nil || after["serial"] != before["serial"] {
						t.Fatalf("refused operation changed serial: %v -> %v, %v", before["serial"], after["serial"], err)
					}
				})
			}
		})
	}
}

func TestQuestionWithdrawalEligibilityIsVisibleInBothToolLists(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _, _ := questionWithdrawalServer(t, t.TempDir(), version)
			list := result(t, rpc(t, srv, version, "tools/list", map[string]any{}), "tools/list")
			for _, item := range list["tools"].([]any) {
				tool := item.(map[string]any)
				if tool["name"] != "respond" {
					continue
				}
				description := tool["description"].(string)
				for _, rule := range []string{"unfinished request", "unanswered question", "notify, handoff and final", "superseded_by"} {
					if !strings.Contains(description, rule) {
						t.Errorf("withdrawal tool omitted %q: %s", rule, description)
					}
				}
				return
			}
			t.Fatal("setup: respond missing from tools/list")
		})
	}
}
