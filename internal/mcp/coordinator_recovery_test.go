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

// These clients use only merge_agents/read_mail/respond, all present before
// recover_agent existed. No new schema argument or manual engine flag helps.
func coordinatorRecoveryPreview(f *supportedMergeFixture) map[string]any {
	f.t.Helper()
	p := f.call("merge_agents", map[string]any{"token": f.lead["token"], "agent": f.source["agent_id"], "into": f.into["agent_id"], "dry_run": true})
	if p["mode"] != "recovery_request" || p["approval_by"] != f.into["agent_id"] || !strings.HasPrefix(fmt.Sprint(p["fence"]), "recovery:") {
		f.t.Fatalf("preview omitted explicit survivor-consent mode: %v", p)
	}
	if strings.Contains(fmt.Sprint(p), "private original mail") {
		f.t.Fatal("preview leaked source mail")
	}
	return p
}

func coordinatorRecoveryRequest(f *supportedMergeFixture) any {
	f.t.Helper()
	p := coordinatorRecoveryPreview(f)
	return f.call("merge_agents", map[string]any{"token": f.lead["token"], "agent": f.source["agent_id"], "into": f.into["agent_id"], "fence": p["fence"]})["msg_serial"]
}

func TestCoordinatorRecoveryUsesExistingToolsAndSurvivorConsent(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			f := recoveryFixture(t, version)
			for i := 1; i <= 17; i++ {
				if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSetSlot, Token: f.source["token"].(string), SlotID: fmt.Sprint("s", i), Text: fmt.Sprint("old work ", i)}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{f.source["agent_id"].(string)}}); err != nil {
				t.Fatal(err)
			}
			f.call("check_in", map[string]any{"token": f.into["token"]})
			serial := coordinatorRecoveryRequest(f)
			// The recipient follows the ordinary wake protocol before reading
			// and consenting. Its own awareness checkpoint is not new custody.
			f.call("check_in", map[string]any{"token": f.into["token"]})
			envelope := f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": serial})["message"].(map[string]any)
			if envelope["from"] != f.lead["agent_id"] || envelope["to"] != f.into["agent_id"] || envelope["recovery"] == nil {
				t.Fatalf("untyped or misdirected proposal: %v", envelope)
			}
			if r := f.raw("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.mail}); r["code"] == nil {
				t.Fatal("proposal alone moved source mail")
			}
			if r := f.raw("respond", map[string]any{"token": f.lead["token"], "msg_serial": serial, "disposition": "approve"}); r["code"] == nil {
				t.Fatal("coordinator consented for survivor")
			}
			f.stop()
			f.srv, f.e, f.stop = supportedMergeServer(t, f.dir)
			approved := f.call("respond", map[string]any{"token": f.into["token"], "msg_serial": serial, "disposition": "approve"})
			if approved["recovered"] == nil {
				t.Fatalf("no recovery fold: %v", approved)
			}
			own := f.call("check_in", map[string]any{"token": f.into["token"]})
			if own["inherited_declaration_count"] != float64(17) {
				t.Fatalf("lost source declarations: %v", own)
			}
			f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.mail})
			if r := f.raw("resume", map[string]any{"nonce": "source-secret", "resume_id": "source-after-recovery"}); r["code"] != "E_SEAT_MERGED" {
				t.Fatalf("source nonce gained survivor authority: %v", r)
			}
			f.stop()
			f.srv, f.e, f.stop = supportedMergeServer(t, f.dir)
			future := f.call("send", map[string]any{"token": f.lead["token"], "to": f.source["agent_id"], "type": "question", "body": "after reverse recovery"})
			f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": future["msg_serial"]})
		})
	}
}

func TestCoordinatorRecoveryRechecksBeforeConsentAndNeverWakesOnRefusal(t *testing.T) {
	changes := []struct {
		name, code string
		change     func(*supportedMergeFixture)
	}{
		{"live-source", "E_AGENT_LIVE", func(f *supportedMergeFixture) { f.call("check_in", map[string]any{"token": f.source["token"]}) }},
		{"coordinator-lost-role", "E_NOT_COORDINATOR", func(f *supportedMergeFixture) {
			if _, err := f.e.GrantRole(context.Background(), "lead", core.RoleMember); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"coordinator-signed-off", "E_NOT_COORDINATOR", func(f *supportedMergeFixture) {
			f.call("sign_off", map[string]any{"token": f.lead["token"]})
		}},
		{"unknown-process", "E_AGENT_LIVE", func(f *supportedMergeFixture) {
			f.call("resume", map[string]any{"nonce": "source-secret", "resume_id": "new-source-process", "pid": 1234567})
			if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{f.source["agent_id"].(string)}}); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"source-bound", "E_RECOVERY_NEEDS_HUMAN", func(f *supportedMergeFixture) {
			f.call("bind_session", map[string]any{"token": f.source["token"], "session_id": "source-bound-again"})
			if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{f.source["agent_id"].(string)}}); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"changed-survivor-work", "E_RECOVERY_STALE", func(f *supportedMergeFixture) {
			f.call("check_in", map[string]any{"token": f.into["token"]})
			f.call("declare", map[string]any{"token": f.into["token"], "text": "substantive new input"})
		}},
		{"survivor-unbound", "E_RECOVERY_NEEDS_HUMAN", func(f *supportedMergeFixture) {
			f.call("update", map[string]any{"token": f.into["token"], "release_session": true})
		}},
		{"source-privileged", "E_RECOVERY_NEEDS_HUMAN", func(f *supportedMergeFixture) {
			if _, err := f.e.GrantRole(context.Background(), "worker", core.RoleAdmin); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"foreign-host", "E_RECOVERY_NEEDS_HUMAN", func(f *supportedMergeFixture) {
			if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpUpdate, Token: f.into["token"].(string), Agent: &core.AgentInfo{HostID: "foreign"}}); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"changed-fence", "E_RECOVERY_STALE", func(f *supportedMergeFixture) {
			f.call("send", map[string]any{"token": f.lead["token"], "to": f.source["agent_id"], "type": "notify", "body": "new source input"})
		}},
	}
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, change := range changes {
			t.Run(version+"/"+change.name, func(t *testing.T) {
				f := recoveryFixture(t, version)
				serial := coordinatorRecoveryRequest(f)
				change.change(f)
				if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{f.into["agent_id"].(string)}}); err != nil {
					t.Fatal(err)
				}
				before, err := f.e.Board(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				r := f.raw("respond", map[string]any{"token": f.into["token"], "msg_serial": serial, "disposition": "approve"})
				if r["code"] != change.code {
					t.Fatalf("want %s: %v", change.code, r)
				}
				after, err := f.e.Board(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if before["serial"] != after["serial"] {
					t.Fatal("refusal woke survivor or changed replayable state")
				}
				if r := f.raw("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.mail}); r["code"] == nil {
					t.Fatal("refusal exposed source mail")
				}
			})
		}
	}
}

func TestCoordinatorRecoveryStillRequiresOtherHandsForAdmin(t *testing.T) {
	for _, era := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(era, func(t *testing.T) {
			f := recoveryFixture(t, era)
			f.into = f.call("register", map[string]any{"name": "coordinator-puppet", "session_id": "coordinator-session", "harness": "Codex", "surface": "cli", "recover_from": f.source["agent_id"]})
			if _, err := f.e.GrantRole(context.Background(), "lead", core.RoleAdmin); err != nil {
				t.Fatal(err)
			}
			p := f.call("merge_agents", map[string]any{"token": f.lead["token"], "agent": f.source["agent_id"], "into": f.into["agent_id"], "dry_run": true})
			if p["mode"] == "recovery_request" {
				t.Fatal("admin obtained recovery proposal for its own session")
			}
			r := f.raw("merge_agents", map[string]any{"token": f.lead["token"], "agent": f.source["agent_id"], "into": f.into["agent_id"], "fence": "recovery:" + fmt.Sprint(p["fence"])})
			if r["code"] != "E_NOT_PERMITTED" {
				t.Fatalf("admin bypassed distinct hands with recovery prefix: %v", r)
			}
		})
	}
}
