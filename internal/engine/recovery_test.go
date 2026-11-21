// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// Recovery and an actual synthetic command wake enter their production doors.
// No hand-set continuation flag can make inherited work look harmless here.
func TestRecoveryInheritedHistoryDoesNotContinueARealDibsTurn(t *testing.T) {
	b := newContinuationBoard(t)
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, err := b.e.Do(b.ctx, op)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	do(&core.Op{Kind: core.OpUpdate, Token: b.token, Agent: &core.AgentInfo{HostID: b.e.HostID(), Surface: "cli"}})
	source := do(&core.Op{Kind: core.OpRegister, Name: "source", Nonce: "source-nonce", AgentKind: core.KindPersistent, Agent: &core.AgentInfo{HostID: b.e.HostID(), Harness: "Codex", Surface: "cli"}})
	sourceToken := source["token"].(string)
	do(&core.Op{Kind: core.OpAckBoard, Token: sourceToken})
	do(&core.Op{Kind: core.OpSetSlot, Token: sourceToken, SlotID: "s1", Text: "old work", Holds: []string{"port:12345"}, Refs: []string{"request:42"}})
	do(&core.Op{Kind: core.OpSweep, StaleAgents: []string{"source"}})
	do(&core.Op{Kind: core.OpAckBoard, Token: b.token})
	lead := do(&core.Op{Kind: core.OpRegister, Name: "lead", Nonce: "lead-nonce", AgentKind: core.KindPersistent, SessionID: "lead-session"})
	if _, err := b.e.GrantRole(b.ctx, "lead", core.RoleCoordinator); err != nil {
		t.Fatal(err)
	}
	do(&core.Op{Kind: core.OpAckBoard, Token: lead["token"].(string)})
	p, err := b.e.RecoverAgent(b.ctx, b.token, "source", MergeOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	req, err := b.e.RecoverAgent(b.ctx, b.token, "source", MergeOptions{Fence: p["fence"].(string)})
	if err != nil {
		t.Fatal(err)
	}
	do(&core.Op{Kind: core.OpRespond, Token: lead["token"].(string), MsgSerial: req["msg_serial"].(uint64), Disposition: "approve"})
	r, err := b.e.query(context.Background(), func() core.Result {
		a := b.e.state.AgentByToken(b.token)
		return core.Result{"inherited": len(a.InheritedDeclarations), "work": b.e.workStateOf(a), "slots": len(b.e.workSlotsOf(a, b.e.state.Agents["source"].MergedAt))}
	})
	if err != nil {
		t.Fatal(err)
	}
	if r["inherited"] != 1 || r["slots"] != 0 || r["work"] != "idle" {
		t.Fatalf("history counted as current work: %v", r)
	}
	if _, err := b.e.ReadMail(b.ctx, b.token, req["msg_serial"].(uint64), true); err != nil {
		t.Fatal(err)
	}
	do(&core.Op{Kind: core.OpAckBoard, Token: b.token})
	b.wake(t)
	stopped := b.stop(t, false)
	if continued(stopped) {
		t.Fatalf("inherited history continued turn: %v", stopped)
	}
}

func TestRecoveryUsesAnIndependentCoordinatorWhenTheFirstHolderIsTheSurvivor(t *testing.T) {
	b := newContinuationBoard(t)
	do := func(op *core.Op) core.Result {
		t.Helper()
		result, err := b.e.Do(b.ctx, op)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	register := func(name, session string) string {
		t.Helper()
		result := do(&core.Op{
			Kind: core.OpRegister, Name: name, SessionID: session,
			Nonce: "test-nonce-" + name, AgentKind: core.KindPersistent,
			Agent: &core.AgentInfo{HostID: b.e.HostID(), Harness: "Codex", Surface: "cli"},
		})
		token, _ := result["token"].(string)
		if token == "" {
			t.Fatalf("setup: no token for %s", name)
		}
		do(&core.Op{Kind: core.OpAckBoard, Token: token})
		return token
	}
	register("old-identity", "")
	survivor := register("a-survivor", "survivor-session")
	lead := register("z-other-lead", "other-session")
	for _, name := range []string{"a-survivor", "z-other-lead"} {
		if _, err := b.e.GrantRole(b.ctx, name, core.RoleCoordinator); err != nil {
			t.Fatal("setup: grant coordinator:", err)
		}
	}
	do(&core.Op{Kind: core.OpSweep, StaleAgents: []string{"old-identity"}})
	preview, err := b.e.RecoverAgent(b.ctx, survivor, "old-identity", MergeOptions{DryRun: true})
	if err != nil {
		t.Fatalf("recovery refused while an independent coordinator was available: %v", err)
	}
	request, err := b.e.RecoverAgent(b.ctx, survivor, "old-identity", MergeOptions{Fence: preview["fence"].(string)})
	if err != nil {
		t.Fatal("submit the recovery:", err)
	}
	serial := request["msg_serial"].(uint64)
	if _, err := b.e.Do(b.ctx, &core.Op{Kind: core.OpRespond, Token: survivor, MsgSerial: serial, Disposition: "approve"}); err == nil {
		t.Fatal("the coordinator who requested recovery could approve its own recovery")
	}
	do(&core.Op{Kind: core.OpRespond, Token: lead, MsgSerial: serial, Disposition: "approve"})
}
