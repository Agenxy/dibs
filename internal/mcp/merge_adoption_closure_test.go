// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestMergeClosesSatisfiedAdoptionThroughRealTools(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, route := range []string{"coordinator", "admin", "human-approval"} {
			t.Run(version+"/"+route, func(t *testing.T) {
				f := newSupportedMerge(t, version)
				request := f.call("send", map[string]any{"token": f.into["token"], "to": f.lead["agent_id"], "type": "request", "adopt": f.source["agent_id"], "body": "recover my previous seat"})["msg_serial"]
				var humanToken string
				actor := f.lead["agent_id"]
				if route == "admin" {
					if _, err := f.e.GrantRole(context.Background(), "lead", core.RoleAdmin); err != nil {
						t.Fatal(err)
					}
				}
				if route == "human-approval" {
					var err error
					actor, humanToken, err = f.e.HumanAgent(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					f.call("check_in", map[string]any{"token": humanToken})
				}
				a := f.args()
				plan := f.call("merge_agents", a)
				var mergeRequest any
				if route == "human-approval" {
					mergeRequest = f.call("send", map[string]any{"token": f.lead["token"], "to": "human", "type": "request", "body": "merge", "merge": map[string]any{"from": f.source["agent_id"], "into": f.into["agent_id"], "fence": plan["fence"]}})["msg_serial"]
				}
				before, err := f.e.Board(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if route == "human-approval" {
					f.call("respond", map[string]any{"token": humanToken, "msg_serial": mergeRequest, "disposition": "approve"})
				} else {
					a["dry_run"], a["fence"] = false, plan["fence"]
					f.call("merge_agents", a)
				}
				after, err := f.e.Board(context.Background())
				if err != nil || after["serial"].(uint64) != before["serial"].(uint64)+1 {
					t.Fatalf("merge and closure must share exactly one serial: before=%v after=%v err=%v", before["serial"], after["serial"], err)
				}
				mergeSerial := after["serial"].(uint64)
				assertClosed := func() {
					t.Helper()
					m := f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": request})["message"].(map[string]any)
					if m["state"] != "approved" || m["consumed"] != true || m["responded_serial"] != float64(mergeSerial) {
						t.Fatalf("satisfied adoption was not closed in the merge op: %v", m)
					}
					response := fmt.Sprint(m["response"])
					for _, evidence := range []string{"merge_agents", fmt.Sprint(actor), fmt.Sprint(mergeSerial)} {
						if !strings.Contains(response, evidence) {
							t.Fatalf("closure omitted actual merge evidence %q: %v", evidence, m)
						}
					}
				}
				assertClosed()
				f.stop()
				f.srv, f.e, f.stop = supportedMergeServer(t, f.dir)
				assertClosed()
				for _, disposition := range []string{"done", "answer"} {
					r := f.raw("respond", map[string]any{"token": f.lead["token"], "msg_serial": request, "disposition": disposition})
					if r["code"] == nil || !strings.Contains(fmt.Sprint(r["hint"]), "nothing") {
						t.Fatalf("closed adoption must say nothing remains to close: %v", r)
					}
				}
			})
		}
	}
}

func TestMergeDoesNotResolveUnrelatedAdoptionRequests(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, situation := range []string{"ordinary-work", "wrong-requester", "wrong-source", "denied", "withdrawn", "rewritten-source-requester"} {
			t.Run(version+"/"+situation, func(t *testing.T) {
				f := newSupportedMerge(t, version)
				args := map[string]any{"token": f.into["token"], "to": f.lead["agent_id"], "type": "request", "adopt": f.source["agent_id"], "body": "keep this distinct"}
				switch situation {
				case "ordinary-work":
					delete(args, "adopt")
				case "wrong-requester", "wrong-source":
					other := f.call("register", map[string]any{"name": "other", "nonce": "other-closure", "session_id": "other-session"})
					f.call("check_in", map[string]any{"token": other["token"]})
					if situation == "wrong-requester" {
						args["token"] = other["token"]
					} else {
						args["adopt"] = other["agent_id"]
					}
				case "rewritten-source-requester":
					f.call("check_in", map[string]any{"token": f.source["token"]})
					args["token"] = f.source["token"]
				}
				request := f.call("send", args)["msg_serial"]
				if situation == "denied" {
					f.call("respond", map[string]any{"token": f.lead["token"], "msg_serial": request, "disposition": "deny"})
				}
				if situation == "withdrawn" {
					f.call("respond", map[string]any{"token": f.into["token"], "msg_serial": request, "disposition": "withdraw"})
				}
				before := f.call("read_mail", map[string]any{"token": f.lead["token"], "msg_serial": request})["message"].(map[string]any)
				if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{f.source["agent_id"].(string)}}); err != nil {
					t.Fatal(err)
				}
				f.call("check_in", map[string]any{"token": f.into["token"]})
				a := f.args()
				plan := f.call("merge_agents", a)
				a["dry_run"], a["fence"] = false, plan["fence"]
				f.call("merge_agents", a)
				after := f.call("read_mail", map[string]any{"token": f.lead["token"], "msg_serial": request})["message"].(map[string]any)
				if after["state"] != before["state"] || after["responded_serial"] != before["responded_serial"] || after["response"] != before["response"] {
					t.Fatalf("unrelated %s was resolved: before=%v after=%v", situation, before, after)
				}
			})
		}
	}
}

func TestTypedAdoptionDispositionHintsExecuteThroughRealTools(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, check := range []string{"answer", "done", "description", "ordinary-done"} {
			t.Run(version+"/"+check, func(t *testing.T) {
				f := newSupportedMerge(t, version)
				if check == "description" {
					list := result(t, rpc(t, f.srv, version, "tools/list", map[string]any{}), "tools/list")
					for _, tool := range list["tools"].([]any) {
						row := tool.(map[string]any)
						if row["name"] == "respond" {
							text := fmt.Sprint(row["description"])
							if !strings.Contains(text, "ordinary") || !strings.Contains(text, "typed") || !strings.Contains(text, "approve") {
								t.Fatalf("respond description omits scope of one-call done: %s", text)
							}
							return
						}
					}
					t.Fatal("respond missing from tools/list")
				}
				args := map[string]any{"token": f.into["token"], "to": f.lead["agent_id"], "type": "request", "body": "work"}
				if check != "ordinary-done" {
					args["adopt"] = f.source["agent_id"]
				}
				request := f.call("send", args)["msg_serial"]
				if check == "ordinary-done" {
					if r := f.call("respond", map[string]any{"token": f.lead["token"], "msg_serial": request, "disposition": "done"}); r["state"] != "done" {
						t.Fatalf("ordinary pending done lost its convenience: %v", r)
					}
					return
				}
				r := f.raw("respond", map[string]any{"token": f.lead["token"], "msg_serial": request, "disposition": check})
				hint := fmt.Sprint(r["hint"])
				if r["code"] != "E_BAD_DISPOSITION" || !strings.Contains(hint, `disposition:"approve"`) || strings.Contains(hint, `disposition:"done"`) {
					t.Fatalf("typed request hint does not name its executable approval: %v", r)
				}
				f.call("respond", map[string]any{"token": f.lead["token"], "msg_serial": request, "disposition": "approve"})
			})
		}
	}
}
