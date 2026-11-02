// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"fmt"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// Actual HTTP preview/request/approval surround one recorded daemon operation.
// Every forced operation must change its marker. No test setter edits the
// fenced message or relaxes recovery eligibility.
func TestRecoveryConsentSurvivesDeliveryBookkeeping(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		for _, direction := range []string{"survivor", "coordinator"} {
			for _, boundary := range []string{"preview", "approval"} {
				for _, field := range []string{"contact", "stall"} {
					t.Run(version+"/"+direction+"/"+boundary+"/"+field, func(t *testing.T) {
						limits := core.DefaultLimits()
						if field == "contact" {
							limits.MaxMailboxDepth = 3
						}
						f := newSupportedMerge(t, version, limits)
						f.call("update", map[string]any{"token": f.into["token"], "name": "worker-recovery"})
						change := recoveryBookkeepingChange(t, f, field)
						f.call("check_in", map[string]any{"token": f.into["token"]})
						preview := recoveryPreview
						verb, proposer, approver := "recover_agent", f.into, f.lead
						if direction == "coordinator" {
							preview = coordinatorRecoveryPreview
							verb, proposer, approver = "merge_agents", f.lead, f.into
						}
						p := preview(f)
						if boundary == "preview" {
							change()
						}
						args := map[string]any{"token": proposer["token"], "agent": f.source["agent_id"], "fence": p["fence"]}
						if direction == "coordinator" {
							args["into"] = f.into["agent_id"]
						}
						request := f.call(verb, args)["msg_serial"]
						if boundary == "approval" {
							change()
						}
						approved := f.call("respond", map[string]any{"token": approver["token"], "msg_serial": request, "disposition": "approve"})
						if approved["recovered"] == nil {
							t.Fatalf("bookkeeping prevented recovery: %v", approved)
						}
						f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.mail})
						f.stop()
						f.srv, f.e, f.stop = supportedMergeServer(t, f.dir, limits)
						f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.mail})
					})
				}
			}
		}
	}
}

func recoveryBookkeepingChange(t *testing.T, f *supportedMergeFixture, field string) func() {
	t.Helper()
	if field == "contact" {
		// Start with an unread oldest ask and a normal contact marker. Only an
		// actual rejected send derives a full-mailbox escalation in production;
		// Do deliberately ignores caller-supplied ContactMailboxFull evidence.
		f.call("adopt_agent", map[string]any{"token": f.lead["token"], "agent": f.source["agent_id"], "into": f.into["agent_id"]})
		f.mail = f.call("send", map[string]any{"token": f.lead["token"], "to": f.source["agent_id"], "type": "question", "body": "oldest unread source ask"})["msg_serial"]
		initial, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpContactEscalate, MsgSerial: uint64(f.mail.(float64))})
		if err != nil || initial["contact_serial"] == nil {
			t.Fatalf("setup: initial ordinary contact missing: %v %v", initial, err)
		}
		before := initial["contact_serial"].(uint64)
		for range 2 {
			f.call("send", map[string]any{"token": f.lead["token"], "to": f.source["agent_id"], "type": "question", "body": "fills source mailbox"})
		}
		return func() {
			refused := f.raw("send", map[string]any{"token": f.lead["token"], "to": f.source["agent_id"], "type": "question", "body": "not admitted"})
			if refused["code"] != "E_MAILBOX_FULL" {
				t.Fatalf("setup: full mailbox did not refuse the actual send: %v", refused)
			}
			// The idempotent ordinary operation returns the marker now stored on
			// this ask; it must name a NEW full contact, not our initial attempt.
			result, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpContactEscalate, MsgSerial: uint64(f.mail.(float64))})
			if err != nil || result["deduplicated"] != true || result["contact_serial"] == nil || result["contact_serial"] == before {
				t.Fatalf("setup: contact marker did not change: %v %v", result, err)
			}
		}
	}
	request := f.call("send", map[string]any{"token": f.lead["token"], "to": f.source["agent_id"], "type": "request", "body": "owed work"})["msg_serial"]
	f.call("respond", map[string]any{"token": f.source["token"], "msg_serial": request, "disposition": "approve"})
	f.call("declare", map[string]any{"token": f.source["token"], "text": "working on owed work", "refs": []string{fmt.Sprint("request:", request)}})
	if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{f.source["agent_id"].(string)}}); err != nil {
		t.Fatal(err)
	}
	reporter, err := f.e.Do(context.Background(), &core.Op{
		Kind: core.OpRegister, HumanMint: true, Name: "dibs", Nonce: core.DibsNonce,
		AgentKind: core.KindPersistent, NoProcess: true, SessionID: core.DibsNonce,
		Agent: &core.AgentInfo{Harness: "dibs", Surface: "daemon"},
	})
	if err != nil {
		t.Fatal("setup reporter:", err)
	}
	board, err := f.e.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		result, err := f.e.Do(context.Background(), &core.Op{
			Kind: core.OpStallNotified, Token: reporter["token"].(string), To: f.lead["agent_id"].(string),
			MsgSerial: uint64(request.(float64)), DeclarationSerial: board["serial"].(uint64), Body: "stall already reported",
		})
		if err != nil || result["msg_serial"] == nil || result["deduplicated"] == true {
			t.Fatalf("setup: stall marker did not change: %v %v", result, err)
		}
	}
}
