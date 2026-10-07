package mcp

import (
	"fmt"
	"strings"
	"testing"
)

func TestPersistentSignOffGuidanceThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _, _ := aliasReplayServer(t, t.TempDir())
			r := toolCallOn(t, srv, version, "register", map[string]any{
				"name": "standing-worker", "nonce": "signoff-standing", "kind": "persistent",
			})
			id, token := r["agent_id"], r["token"]
			if id == nil || token == nil {
				t.Fatalf("setup registration: %v", r)
			}
			checkpoint := toolCallOn(t, srv, version, "check_in", map[string]any{"token": token})
			if checkpoint["ok"] != true {
				t.Fatalf("setup checkpoint: %v", checkpoint)
			}
			declared := toolCallOn(t, srv, version, "declare", map[string]any{"token": token, "text": "one task"})
			if declared["slot_id"] == nil {
				t.Fatalf("setup declaration: %v", declared)
			}
			cleared := toolCallOn(t, srv, version, "undeclare", map[string]any{
				"token": token, "slot_id": declared["slot_id"],
			})
			if cleared["ok"] != true {
				t.Fatalf("task completion: %v", cleared)
			}
			_, sender := aliasRegister(t, srv, "sender", "signoff-sender")
			mail := toolCallOn(t, srv, version, "send", map[string]any{
				"token": sender, "to": id, "type": "notify", "body": "next task is reachable",
			})
			if mail["msg_serial"] == nil {
				t.Fatalf("undeclare made persistent worker unreachable: %v", mail)
			}
			closed := toolCallOn(t, srv, version, "sign_off", map[string]any{"token": token})
			if closed["ok"] != true {
				t.Fatalf("sign_off: %v", closed)
			}
			if closed["waiting"] != nil {
				t.Errorf("closed credential is directed to inaccessible mail: %v", closed)
			}
			// Verify the advertised consequences through the same ingress, before
			// testing the advice. A receipt cannot claim a reversible close.
			retry := toolCallOn(t, srv, version, "register", map[string]any{"nonce": "signoff-standing"})
			if retry["code"] != "E_AGENT_CLOSED" {
				t.Fatalf("closed identity reopened: %v", retry)
			}
			if toolCallOn(t, srv, version, "check_in", map[string]any{"token": token})["ok"] == true {
				t.Fatal("closed credential remains usable")
			}
			note := fmt.Sprint(closed["note"])
			for _, phrase := range []string{"unreachable", "cannot reopen", "new nonce", "new identity"} {
				if !strings.Contains(note, phrase) {
					t.Fatalf("persistent close receipt hides %q: %v", phrase, closed)
				}
			}
		})
	}
}

func TestSignOffGuidanceDistinguishesTasksFromAgents(t *testing.T) {
	for _, def := range toolDefs {
		if def["name"] != "sign_off" {
			continue
		}
		desc := fmt.Sprint(def["description"])
		for _, phrase := range []string{"persistent", "undeclare", "unreachable", "permanently"} {
			if !strings.Contains(desc, phrase) {
				t.Errorf("sign_off description hides %q: %s", phrase, desc)
			}
		}
		if strings.Contains(desc, "when your work is done") {
			t.Error("sign_off still prescribes agent retirement after task completion")
		}
	}
	for _, phrase := range []string{"sign_off", "undeclare", "cannot reopen", "new nonce"} {
		if !strings.Contains(skillsDoc, phrase) {
			t.Errorf("embedded skills lack persistent lifetime guidance %q", phrase)
		}
	}
}
