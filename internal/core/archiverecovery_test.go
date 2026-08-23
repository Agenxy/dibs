package core

import (
	"testing"
	"time"
)

// An archived agent is still recoverable by the credential it was told to keep.
//
// WHY THIS IS PINNED. An ephemeral agent that makes no Dibs call for
// AgentTTL + StaleGrace, 35 minutes on the defaults, is swept to stale and then
// archived, and archiving clears its token: the next call it makes returns
// E_BAD_TOKEN having worked a moment earlier. Reported from a live board by an
// agent that hit it three times in one session while doing ordinary work
// between two coordination calls.
//
// The recovery is real and E_BAD_TOKEN names it: register again with the same
// name and the same nonce. This test exists because that recovery rests on
// something non-obvious. Archiving also clears the agent's own Nonce FIELD,
// while the s.Nonces index that register actually looks in is kept until
// ArchiveRetention. So the credential survives in one place and is destroyed in
// another, and only one of them is on the recovery path.
//
// Nobody should have to know that to be sure this works, and the next person
// tidying "an archived agent has no nonce, so drop its index entry" would take
// the mail with it. This fails if they do.
func TestAnArchivedAgentComesBackWithItsNonceAndItsMail(t *testing.T) {
	s := NewState("test", DefaultLimits())
	now := time.Unix(1700000000, 0)

	res, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", NewToken: "tok-1", Nonce: "n-keepme",
	}, now)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	id, _ := res["agent_id"].(string)
	if id == "" {
		t.Fatalf("setup: no agent_id in %v", res)
	}

	// A peer leaves mail the agent has not read.
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "peer", NewToken: "tok-peer",
	}, now); err != nil {
		t.Fatalf("register peer: %v", err)
	}
	if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: "tok-peer"}, now); err != nil {
		t.Fatalf("peer ack: %v", err)
	}
	if _, _, err := s.Apply(&Op{
		Kind: OpSendMessage, Token: "tok-peer", To: id, MsgType: "notify", Text: "read me",
	}, now); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Silence: swept stale, then past the grace period, archived.
	stale := now.Add(s.Limits.AgentTTL + time.Minute)
	if _, _, err := s.Apply(&Op{Kind: OpSweep, StaleAgents: []string{id}}, stale); err != nil {
		t.Fatalf("sweep to stale: %v", err)
	}
	archived := stale.Add(s.Limits.StaleGrace + time.Minute)
	if _, _, err := s.Apply(&Op{Kind: OpSweep}, archived); err != nil {
		t.Fatalf("sweep to archived: %v", err)
	}
	if got := s.Agents[id].Status; got != StatusArchived {
		t.Fatalf("setup: agent is %s, not archived; this test then proves nothing", got)
	}
	// The premise: the live credential really is gone.
	if s.AgentByToken("tok-1") != nil {
		t.Fatal("setup: the old token still works, so there is no recovery to test")
	}

	// What the error tells the agent to do.
	back, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", NewToken: "tok-2", Nonce: "n-keepme",
	}, archived.Add(time.Minute))
	if err != nil {
		t.Fatalf("re-register with the same name and nonce, which is exactly what "+
			"E_BAD_TOKEN instructs: %v", err)
	}
	if got, _ := back["agent_id"].(string); got != id {
		t.Fatalf("came back as %q instead of %q: a new agent under the same name is "+
			"the sibling failure, and every message sent before the archive is "+
			"stranded in a row nobody occupies", got, id)
	}
	if len(s.Inbox(id)) == 0 {
		t.Error("the agent is back but its mailbox is empty: the mail it was archived " +
			"holding is what makes recovery worth having")
	}
}

// A recovered agent gets its durable identity back, not just its mail.
//
// Archival blanks Agent.Nonce and keeps the nonce INDEX, which is what lets
// recovery find the row at all. Reattaching restored the token, the session and
// the mailbox and left Nonce empty, so AgentIdentity returned "" and a declared
// role could never reconcile onto that agent again: an admin that went dormant
// for a month came back as itself and permanently without the role dibs.toml
// grants it. The secret is the one this path just matched on, so putting it
// back asserts nothing new.
func TestRecoveringAnArchivedAgentRestoresItsNonce(t *testing.T) {
	s := NewState("test", DefaultLimits())
	now := time.Now()

	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "fleet-lead", AgentKind: KindPersistent, Nonce: "the-secret",
	}, now); err != nil {
		t.Fatal("setup:", err)
	}
	l := s.Agents["fleet-lead"]
	// What archival does: the row survives, the credential on it does not.
	l.Status, l.Token, l.Nonce = StatusArchived, "", ""
	if s.Nonces["the-secret"] != "fleet-lead" {
		t.Fatal("setup: the nonce index was not retained, so recovery cannot find " +
			"this row and the test is about a different path")
	}

	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "fleet-lead", AgentKind: KindPersistent, Nonce: "the-secret",
	}, now); err != nil {
		t.Fatal("recovering:", err)
	}
	if got := s.Agents["fleet-lead"].Nonce; got != "the-secret" {
		t.Errorf("the recovered agent's nonce is %q. It has no durable identity, so "+
			"AgentIdentity returns nothing and a role declared in dibs.toml can "+
			"never be reconciled onto it again", got)
	}
}
