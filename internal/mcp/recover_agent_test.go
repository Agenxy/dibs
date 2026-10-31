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

func recoveryFixture(t *testing.T, version string) *supportedMergeFixture {
	t.Helper()
	f := newSupportedMerge(t, version)
	f.call("update", map[string]any{"token": f.into["token"], "name": "worker-recovery"})
	return f
}

func recoveryPreview(f *supportedMergeFixture) map[string]any {
	f.t.Helper()
	return f.call("recover_agent", map[string]any{"token": f.into["token"], "agent": f.source["agent_id"], "dry_run": true})
}

func recoveryRequest(f *supportedMergeFixture) any {
	f.t.Helper()
	p := recoveryPreview(f)
	return f.call("recover_agent", map[string]any{"token": f.into["token"], "agent": f.source["agent_id"], "fence": p["fence"]})["msg_serial"]
}

func TestRecoverAgentRefusesChangedAuthorityAndEvidence(t *testing.T) {
	mutations := []struct {
		name, code string
		change     func(*supportedMergeFixture)
	}{
		{"active-source", "E_AGENT_LIVE", func(f *supportedMergeFixture) { f.call("check_in", map[string]any{"token": f.source["token"]}) }},
		{"source-binding", "E_RECOVERY_NEEDS_HUMAN", func(f *supportedMergeFixture) {
			f.call("bind_session", map[string]any{"token": f.source["token"], "session_id": "source-still-bound"})
			_, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{"worker"}})
			if err != nil {
				f.t.Fatal(err)
			}
			f.call("check_in", map[string]any{"token": f.into["token"]})
		}},
		{"unknown-process", "E_AGENT_LIVE", func(f *supportedMergeFixture) {
			f.call("resume", map[string]any{"nonce": "source-secret", "resume_id": "unknown-process", "pid": 1234567})
			_, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{"worker"}})
			if err != nil {
				f.t.Fatal(err)
			}
		}},
		{"privileged-source", "E_RECOVERY_NEEDS_HUMAN", func(f *supportedMergeFixture) {
			if _, err := f.e.GrantRole(context.Background(), "worker", core.RoleAdmin); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"coordinator-role-lost", "E_NOT_COORDINATOR", func(f *supportedMergeFixture) {
			if _, err := f.e.GrantRole(context.Background(), "lead", core.RoleMember); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"survivor-unbound", "E_RECOVERY_NEEDS_HUMAN", func(f *supportedMergeFixture) {
			f.call("update", map[string]any{"token": f.into["token"], "release_session": true})
		}},
		{"foreign-host", "E_RECOVERY_NEEDS_HUMAN", func(f *supportedMergeFixture) {
			_, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpUpdate, Token: f.into["token"].(string), Agent: &core.AgentInfo{HostID: "other-host"}})
			if err != nil {
				f.t.Fatal(err)
			}
		}},
		{"foreign-harness", "E_RECOVERY_NEEDS_HUMAN", func(f *supportedMergeFixture) {
			f.into = f.call("register", map[string]any{"name": "foreign-worker", "harness": "Claude Code", "surface": "cli", "session_id": "foreign-worker-session"})
			f.call("check_in", map[string]any{"token": f.into["token"]})
		}},
		{"foreign-surface", "E_RECOVERY_NEEDS_HUMAN", func(f *supportedMergeFixture) {
			_, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpUpdate, Token: f.into["token"].(string), Agent: &core.AgentInfo{Surface: "other-surface"}})
			if err != nil {
				f.t.Fatal(err)
			}
		}},
	}
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		for _, mutation := range mutations {
			for _, approval := range []bool{false, true} {
				// Harness is established at registration, not writable by update.
				if mutation.name == "foreign-harness" && approval {
					continue
				}
				t.Run(fmt.Sprintf("%s/%s/approval=%t", version, mutation.name, approval), func(t *testing.T) {
					f := recoveryFixture(t, version)
					var serial any
					if approval {
						serial = recoveryRequest(f)
					}
					mutation.change(f)
					before, err := f.e.Board(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					var refused map[string]any
					if approval {
						refused = f.raw("respond", map[string]any{"token": f.lead["token"], "msg_serial": serial, "disposition": "approve"})
					} else {
						refused = f.raw("recover_agent", map[string]any{"token": f.into["token"], "agent": f.source["agent_id"], "dry_run": true})
					}
					// No-coordinator is the discovery refusal before a request exists.
					want := mutation.code
					if mutation.name == "coordinator-role-lost" && !approval {
						want = "E_NO_COORDINATOR"
					}
					if refused["code"] != want {
						t.Fatalf("want %s: %v", want, refused)
					}
					after, err := f.e.Board(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					if after["serial"] != before["serial"] {
						t.Fatal("refusal changed durable serial")
					}
					if r := f.raw("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.mail}); r["code"] == nil {
						t.Fatalf("refusal exposed source mail: %v", r)
					}
				})
			}
		}
	}
}

func TestRecoverAgentPreviewConsentAndStaleFence(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			f := recoveryFixture(t, version)
			p := recoveryPreview(f)
			if strings.Contains(fmt.Sprint(p), "private original mail") || !strings.Contains(fmt.Sprint(p), "new mail") {
				t.Fatalf("unsafe or incomplete preview: %v", p)
			}
			serial := recoveryRequest(f)
			if r := f.raw("register", map[string]any{"name": "worker-recovery", "session_id": "worker-session"}); r["code"] != "E_NEEDS_NONCE" {
				t.Fatalf("pending recovery allowed public-session takeover: %v", r)
			}
			if r := f.raw("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.mail}); r["code"] == nil {
				t.Fatal("request alone moved mail")
			}
			f.call("send", map[string]any{"token": f.lead["token"], "to": f.source["agent_id"], "type": "notify", "body": "mail after consent"})
			if r := f.raw("respond", map[string]any{"token": f.lead["token"], "msg_serial": serial, "disposition": "approve"}); r["code"] != "E_RECOVERY_STALE" {
				t.Fatalf("stale approval accepted: %v", r)
			}
			if r := f.raw("recover_agent", map[string]any{"token": f.into["token"], "agent": f.source["agent_id"], "into": f.lead["agent_id"]}); r["rpc_error"] == nil {
				t.Fatalf("third party destination accepted: %v", r)
			}
		})
	}
}

func TestRecoverAgentDistinctHandsAndRegistrationOffer(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		for _, session := range []string{"", "coordinator-session"} {
			t.Run(version+"/session="+session, func(t *testing.T) {
				f := recoveryFixture(t, version)
				puppet := f.call("register", map[string]any{"name": "new-recovering-seat", "session_id": session, "harness": "Codex", "surface": "cli", "recover_from": f.source["agent_id"]})
				offer, ok := puppet["recovery_offer"].(map[string]any)
				if !ok || offer["state"] != "pending" || offer["agent"] != f.source["agent_id"] || puppet["token"] == nil || puppet["nonce"] == nil {
					t.Fatalf("registration lost actual credentials/offer: %v", puppet)
				}
				if puppet["agent_id"] == f.source["agent_id"] {
					t.Fatal("public recovery claims reattached source")
				}
				f.call("check_in", map[string]any{"token": puppet["token"]})
				if r := f.raw("recover_agent", map[string]any{"token": puppet["token"], "agent": f.source["agent_id"], "dry_run": true}); r["code"] != "E_NOT_PERMITTED" {
					t.Fatalf("coordinator alternate identity admitted: %v", r)
				}
				if _, err := f.e.GrantRole(context.Background(), "lead", core.RoleAdmin); err != nil {
					t.Fatal(err)
				}
				if r := f.raw("recover_agent", map[string]any{"token": puppet["token"], "agent": f.source["agent_id"], "dry_run": true}); r["code"] != "E_NOT_PERMITTED" {
					t.Fatalf("admin bypassed distinct recovery approval: %v", r)
				}
				if r := f.raw("read_mail", map[string]any{"token": puppet["token"], "msg_serial": f.mail}); r["code"] == nil {
					t.Fatal("offer disclosed old mail")
				}
			})
		}
	}
}

func TestRecoverAgentRepairsAdoptionWithoutChangingAdoptionSemantics(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			f := recoveryFixture(t, version)
			f.call("adopt_agent", map[string]any{"token": f.lead["token"], "agent": f.source["agent_id"], "into": f.into["agent_id"]})
			stranded := f.call("send", map[string]any{"token": f.lead["token"], "to": f.source["agent_id"], "type": "question", "body": "after one-time adoption"})
			if r := f.raw("read_mail", map[string]any{"token": f.into["token"], "msg_serial": stranded["msg_serial"]}); r["code"] == nil {
				t.Fatal("adoption silently became a standing redirect")
			}
			serial := recoveryRequest(f)
			f.call("respond", map[string]any{"token": f.lead["token"], "msg_serial": serial, "disposition": "approve"})
			moved := f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": stranded["msg_serial"]})
			if moved["message"].(map[string]any)["to"] != f.into["agent_id"] {
				t.Fatal("recovery missed newly stranded mail")
			}
			// Restart the real encrypted-ledger server before addressing the old id.
			f.stop()
			f.srv, f.e, f.stop = supportedMergeServer(t, f.dir)
			future := f.call("send", map[string]any{"token": f.lead["token"], "to": f.source["agent_id"], "type": "question", "body": "after recovery and restart"})
			r := f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": future["msg_serial"]})
			if r["message"].(map[string]any)["to"] != f.into["agent_id"] {
				t.Fatal("new mail route did not survive restart")
			}
		})
	}
}

func TestRecoverAgentInheritedWaitDoesNotParkRealOwedWork(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			f := recoveryFixture(t, version)
			owed := f.call("send", map[string]any{"token": f.lead["token"], "to": f.source["agent_id"], "type": "request", "body": "real unfinished work"})["msg_serial"]
			f.call("check_in", map[string]any{"token": f.source["token"]})
			f.call("respond", map[string]any{"token": f.source["token"], "msg_serial": owed, "disposition": "approve"})
			f.call("declare", map[string]any{"token": f.source["token"], "slot_id": "s1", "text": "old wait", "waiting": "review", "refs": []string{fmt.Sprint("request:", owed)}})
			if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{"worker"}}); err != nil {
				t.Fatal(err)
			}
			f.call("check_in", map[string]any{"token": f.into["token"]})
			serial := recoveryRequest(f)
			f.call("respond", map[string]any{"token": f.lead["token"], "msg_serial": serial, "disposition": "approve"})
			board, err := f.e.Board(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range board["agents"].([]map[string]any) {
				if row["id"] != f.into["agent_id"] {
					continue
				}
				found = true
				if row["work"] == "waiting" || row["work"] == "idle" || len(row["owes"].([]uint64)) != 1 {
					t.Fatalf("inherited wait hid live debt: %v", row)
				}
			}
			if !found {
				t.Fatal("missing survivor")
			}
		})
	}
}

func TestRecoverAgentRefusalDoesNotWakeDormantCoordinator(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			f := recoveryFixture(t, version)
			serial := recoveryRequest(f)
			f.call("check_in", map[string]any{"token": f.source["token"]})
			if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{"lead"}}); err != nil {
				t.Fatal(err)
			}
			before, err := f.e.Board(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			refused := f.raw("respond", map[string]any{"token": f.lead["token"], "msg_serial": serial, "disposition": "approve"})
			if refused["code"] != "E_AGENT_LIVE" {
				t.Fatalf("expected protected refusal: %v", refused)
			}
			after, err := f.e.Board(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if after["serial"] != before["serial"] {
				t.Fatal("protected refusal woke coordinator and changed durable serial")
			}
		})
	}
}

// Enter through HTTP, as the recovering worker and the distinct coordinator do.
// Adoption alone leaves new mail at the abandoned address; recovery must not.
func TestRecoverAgentRoutesFutureMailAndInheritsDeclarations(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			f := newSupportedMerge(t, version)
			f.call("check_in", map[string]any{"token": f.source["token"]})
			f.call("update", map[string]any{"token": f.source["token"], "name": "worker-before"})
			f.call("update", map[string]any{"token": f.source["token"], "name": "worker-lost"})
			for i := 1; i <= 17; i++ {
				if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSetSlot, Token: f.source["token"].(string), SlotID: fmt.Sprint("s", i), Text: fmt.Sprint("old work ", i), Holds: []string{"port:12345"}}); err != nil {
					t.Fatal(err)
				}
			}
			f.call("update", map[string]any{"token": f.into["token"], "name": "worker-recovery"})
			f.call("declare", map[string]any{"token": f.into["token"], "slot_id": "s1", "text": "current work"})
			if _, err := f.e.Do(context.Background(), &core.Op{
				Kind:        core.OpSweep,
				StaleAgents: []string{f.source["agent_id"].(string)},
			}); err != nil {
				t.Fatal(err)
			}
			f.call("check_in", map[string]any{"token": f.into["token"]})
			preview := f.call("recover_agent", map[string]any{"token": f.into["token"], "agent": f.source["agent_id"], "dry_run": true})
			if preview["inherited_declarations"] != float64(17) || preview["fence"] == nil {
				t.Fatalf("incomplete preview: %v", preview)
			}
			requested := f.call("recover_agent", map[string]any{"token": f.into["token"], "agent": f.source["agent_id"], "fence": preview["fence"]})
			f.call("respond", map[string]any{"token": f.lead["token"], "msg_serial": requested["msg_serial"], "disposition": "approve"})
			for _, address := range []any{f.source["agent_id"], "worker-lost", "worker-before"} {
				future := f.call("send", map[string]any{"token": f.lead["token"], "to": address, "type": "question", "body": "new work after recovery"})
				mail := f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": future["msg_serial"], "include_body": true})
				if mail["message"].(map[string]any)["to"] != f.into["agent_id"] {
					t.Fatalf("future mail stranded: %v", mail)
				}
			}
			board := f.call("check_in", map[string]any{"token": f.into["token"], "detail": true})
			if board["inherited_declaration_count"] != float64(17) {
				t.Fatalf("history lost: %v", board)
			}
			history := board["inherited_declarations"].([]any)
			if len(history) != 8 {
				t.Fatalf("unbounded history view: %v", history)
			}
			first := history[0].(map[string]any)
			inherited := first["history"].(map[string]any)
			if inherited["source"] != f.source["agent_id"] || inherited["host"] != "fixture-host" || inherited["updated_at"] == nil {
				t.Fatalf("history provenance lost: %v", inherited)
			}
			original := inherited["declaration"].(map[string]any)
			if original["updated_serial"] == nil || original["holds"] == nil {
				t.Fatalf("history content lost: %v", original)
			}
			found := false
			for _, raw := range board["board"].(map[string]any)["agents"].([]any) {
				row := raw.(map[string]any)
				if row["id"] != f.into["agent_id"] {
					continue
				}
				slots := row["slots"].([]any)
				if len(slots) != 1 || slots[0].(map[string]any)["text"] != "current work" {
					t.Fatalf("history reactivated/overwrote survivor: %v", slots)
				}
				found = true
			}
			if !found {
				t.Fatal("survivor missing")
			}
			f.call("undeclare", map[string]any{"token": f.into["token"], "slot_id": first["slot_id"]})
			refused := f.raw("register", map[string]any{"nonce": "source-secret"})
			if refused["code"] != "E_SEAT_MERGED" {
				t.Fatalf("old nonce recovered survivor: %v", refused)
			}
		})
	}
}
