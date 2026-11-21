// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestRegisterRefusesToForkSameDormantSessionWithoutItsNonce(t *testing.T) {
	const session = "38c10a4d-1509-4bbc-971e-0bd11f4179ea"
	for _, useAlias := range []bool{false, true} {
		name := "session argument"
		if useAlias {
			name = "harness alias"
		}
		t.Run(name, func(t *testing.T) {
			led := &memLedger{}
			e, ctx := runContactReplayEngine(t, core.NewState("dormant-fork", core.DefaultLimits()), led, nil)
			register := func(nonce string) *core.Op {
				op := &core.Op{
					Kind: core.OpRegister, Name: "worker", Nonce: nonce,
					AgentKind: core.KindPersistent, Agent: &core.AgentInfo{HostID: e.HostID()},
				}
				if useAlias {
					op.SessionAlias = session
				} else {
					op.SessionID = session
				}
				return op
			}
			original := contactReplayDo(t, e, ctx, register("worker-original-credential"))
			contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpSweep, StaleAgents: []string{"worker"}})
			onLoop(t, ctx, e, func(s *core.State) {
				a := s.Agents["worker"]
				if a.Status != core.StatusDormant || a.CurrentSession != session {
					t.Fatalf("setup: registered %q with status %q", a.CurrentSession, a.Status)
				}
			})
			before := contactLedgerSize(t, e, ctx, led)
			for _, nonce := range []string{"stray-bridge-credential", ""} {
				res, err := e.Do(ctx, register(nonce))
				var fault *core.Error
				if !errors.As(err, &fault) || fault.Code != "E_NEEDS_NONCE" {
					t.Fatalf("dormant session retry got agent %v and error %v, want E_NEEDS_NONCE", res["agent_id"], err)
				}
				if !strings.Contains(fault.Hint, "recovery_nonces") || !strings.Contains(fault.Hint, "coordinator") {
					t.Fatal("refusal omitted the credential and coordinator recovery paths")
				}
			}
			if contactLedgerSize(t, e, ctx, led) != before {
				t.Fatal("refusing a duplicate wrote to the ledger")
			}
			retry := register("")
			retry.RecoveryNonces = []string{"stray-bridge-credential", "worker-original-credential"}
			recovered := contactReplayDo(t, e, ctx, retry)
			if recovered["agent_id"] != original["agent_id"] || recovered["token"] == original["token"] {
				t.Fatal("proved credential did not recover the same identity with a rotated token")
			}
		})
	}
}

func TestRegisterRecoversDormantSessionWithMintedNonce(t *testing.T) {
	const session = "f3fd0820-7f34-4e3a-bc10-51ae6e248b75"
	for _, retry := range []struct {
		name, nonce, sessionID, alias string
		protected                     bool
	}{
		{name: "no nonce", sessionID: session},
		{name: "no nonce with harness alias", sessionID: "host-499", alias: session},
		{name: "stray nonce", nonce: "stray-bridge-credential", sessionID: session},
		{name: "stray nonce with harness alias", nonce: "stray-bridge-credential", sessionID: "host-499", alias: session},
		{name: "role reserved", nonce: "stray-bridge-credential", sessionID: "host-499", alias: session, protected: true},
	} {
		t.Run(retry.name, func(t *testing.T) {
			e, ctx := runContactReplayEngine(t, core.NewState("minted-fork", core.DefaultLimits()), &memLedger{}, nil)
			info := &core.AgentInfo{HostID: e.HostID()}
			original := contactReplayDo(t, e, ctx, &core.Op{
				Kind: core.OpRegister, Name: "worker", SessionID: session, Agent: info,
			})
			contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpSweep, StaleAgents: []string{"worker"}})
			onLoop(t, ctx, e, func(s *core.State) {
				a := s.Agents["worker"]
				if a.Status != core.StatusDormant || a.CurrentSession != session || !a.NonceMinted {
					t.Fatal("setup: original identity is not dormant with a minted nonce and the stated session")
				}
			})
			if retry.protected {
				e.SetPrivilegedNames([]string{"worker"})
			}
			result, err := e.Do(ctx, &core.Op{
				Kind: core.OpRegister, Name: "worker", Nonce: retry.nonce,
				SessionID: retry.sessionID, SessionAlias: retry.alias, Agent: info,
			})
			if retry.protected {
				var fault *core.Error
				if !errors.As(err, &fault) || fault.Code != "E_NEEDS_NONCE" || !strings.Contains(fault.Msg, "holds a role") {
					t.Fatalf("expected the existing reserved-role refusal, got %v", err)
				}
				return
			}
			if err != nil || result["agent_id"] != original["agent_id"] || result["via"] != "session_id" ||
				result["token"] == original["token"] {
				t.Fatalf("wanted to reattach the minted identity by session, got agent %v via %v, error %v",
					result["agent_id"], result["via"], err)
			}
			onLoop(t, ctx, e, func(s *core.State) {
				if s.Agents["worker"].CurrentSession != session || s.Agents["worker"].Nonce != original["nonce"] ||
					s.Nonces["stray-bridge-credential"] != "" {
					t.Fatal("reattachment changed the session binding or the original credential")
				}
			})
		})
	}
}

func TestRecoverAgentRefusesSignedOffForkSharingLivePID(t *testing.T) {
	led := &memLedger{}
	e, ctx := runContactReplayEngine(t, core.NewState("closed-recovery", core.DefaultLimits()), led, nil)
	register := func(name, session string, pid int) core.Result {
		return contactReplayDo(t, e, ctx, &core.Op{
			Kind: core.OpRegister, Name: name, Nonce: name + "-credential", SessionID: session, PID: pid,
			Agent: &core.AgentInfo{HostID: e.HostID(), Harness: "test", Surface: "cli"},
		})
	}
	source := register("fork", "", 499)
	survivor := register("survivor", "survivor-session", 499)
	register("coordinator", "independent-session", 501)
	if _, err := e.GrantRole(ctx, "coordinator", core.RoleCoordinator); err != nil {
		t.Fatal("setup coordinator:", err)
	}
	contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpSignOff, Token: source["token"].(string)})
	onLoop(t, ctx, e, func(s *core.State) {
		a, b := s.Agents["fork"], s.Agents["survivor"]
		if a.Status != core.StatusClosed || a.PID != 499 || b.PID != 499 || !e.prober.Alive(a.PID) {
			t.Fatal("setup: the signed-off row and its survivor must share a live bridge process")
		}
	})
	before := contactLedgerSize(t, e, ctx, led)
	_, err := e.RecoverAgent(ctx, survivor["token"].(string), "fork", MergeOptions{DryRun: true})
	var fault *core.Error
	if !errors.As(err, &fault) || fault.Code != "E_AGENT_LIVE" || !strings.Contains(fault.Hint, "merge_agents") {
		t.Fatalf("expected a process-evidence refusal with the reviewed merge path for the signed-off source, got %v", err)
	}
	if contactLedgerSize(t, e, ctx, led) != before {
		t.Fatal("refused recovery wrote to the ledger")
	}
}

func TestSupportedMergeAcceptsSignedOffForkSharingLivePID(t *testing.T) {
	for _, typed := range []bool{false, true} {
		name := "direct"
		if typed {
			name = "typed approval"
		}
		t.Run(name, func(t *testing.T) { testSignedOffForkMerge(t, typed) })
	}
}

func testSignedOffForkMerge(t *testing.T, typed bool) {
	t.Helper()
	e, ctx := runContactReplayEngine(t, core.NewState("closed-fork", core.DefaultLimits()), &memLedger{}, nil)
	_, human, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	register := func(nonce string) core.Result {
		return contactReplayDo(t, e, ctx, &core.Op{
			Kind: core.OpRegister, Name: "worker", Nonce: nonce, PID: 499,
			Agent: &core.AgentInfo{HostID: e.HostID(), Harness: "test"},
		})
	}
	source, survivor := register("source-credential"), register("survivor-credential")
	from, into := source["agent_id"].(string), survivor["agent_id"].(string)
	if from == into {
		t.Fatal("setup: there is no fork to merge")
	}
	sender := registerAgent(t, e, "observer")
	mail := contactReplayDo(t, e, ctx, &core.Op{
		Kind: core.OpSendMessage, Token: sender, To: from, MsgType: core.MsgNotify, Body: "owned by fork",
	})
	if _, err := e.MergeAgents(ctx, human, from, into, MergeOptions{DryRun: true}); err == nil {
		t.Fatal("merge preview accepted an active fork")
	}
	contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpSignOff, Token: source["token"].(string)})
	onLoop(t, ctx, e, func(s *core.State) {
		a, b := s.Agents[from], s.Agents[into]
		if a.Status != core.StatusClosed || a.PID == 0 || a.PID != b.PID {
			t.Fatal("setup: closed source no longer has the survivor's live PID")
		}
	})
	plan, err := e.MergeAgents(ctx, human, from, into, MergeOptions{DryRun: true})
	if err != nil {
		t.Fatal("closed fork preview refused because its bridge is still running:", err)
	}
	fence := plan["fence"].(string)
	if typed {
		contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpAckBoard, Token: human})
		request := contactReplayDo(t, e, ctx, &core.Op{
			Kind: core.OpSendMessage, Token: sender, To: "human", MsgType: core.MsgRequest, Body: "merge the signed-off fork",
			Merge: &core.MergeRequest{From: from, Into: into, Fence: fence},
		})
		contactReplayDo(t, e, ctx, &core.Op{
			Kind: core.OpRespond, Token: human, MsgSerial: request["msg_serial"].(uint64), Disposition: "approve",
		})
	} else if _, err := e.MergeAgents(ctx, human, from, into, MergeOptions{Fence: fence}); err != nil {
		t.Fatal("closed fork merge:", err)
	}
	onLoop(t, ctx, e, func(s *core.State) {
		if s.Agents[from].MergedInto != into || s.Messages[mail["msg_serial"].(uint64)].To != into {
			t.Fatal("closed fork mailbox was not moved to its survivor")
		}
	})
}
