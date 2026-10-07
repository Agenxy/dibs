// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"errors"
	"testing"
)

// The awareness gate belongs to the session, not to the board's opinion of it.
//
// IT RE-ARMED ON EVERY LIVENESS FLIP, AND THAT LOCKED LIVE AGENTS OUT. The gate
// asks that an agent has seen the board before it declares or claims, and it
// reset whenever the sweep marked the agent dormant or stale, and again when
// the agent's next call woke it. So whenever the board's liveness guess was
// wrong, the agent acknowledged the board, was swept, was woken, and found its
// acknowledgement gone before its next call: declare refused on every attempt,
// with a hint to call check_in, which it had just done. The architect and
// k7-dev both hit it on the same day, on a process restart the board misread
// as a crash.
//
// the maintainer's rule settles which way to cut it: an agent that is not archived is
// live. A dormancy flip is the board relabelling an agent that did nothing, so
// it must not cost that agent anything it earned. What genuinely makes an
// agent's awareness stale is a NEW SESSION taking the identity, and every one
// of those rotates the token: register, resume, reattach. A wake does not; its
// own comment says so. So the gate re-arms exactly when the credential rotates.
func TestALivenessFlipDoesNotCostAnAgentItsAcknowledgement(t *testing.T) {
	s := NewState("gate", DefaultLimits())
	regPersistent(t, s, "worker", "tw", "n0nce", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tw"}, t0)
	if s.Agents["worker"].AckedSerial == 0 {
		t.Fatal("setup: the acknowledgement did not take")
	}

	// The board decides the agent is gone, wrongly, and its next call wakes it.
	mustApply(t, s, &Op{Kind: OpSweep, StaleAgents: []string{"worker"}}, t0)
	if !s.Agents["worker"].Sleeping() {
		t.Fatal("setup: the sweep did not mark the agent, so nothing below is about a flip")
	}
	mustApply(t, s, &Op{Kind: OpWake, Token: "tw"}, t0)

	// Same session, same token, same awareness: it may declare.
	if _, _, err := s.Apply(&Op{
		Kind: OpSetSlot, Token: "tw", Text: "still working", Activity: "implement",
	}, t0); err != nil {
		t.Errorf("declare after a liveness flip: %v. The agent acknowledged the board and "+
			"did nothing since; only the board's label changed. This is the refusal that "+
			"looped for as long as the flip repeated", err)
	}
}

// And a new session still has to look before it writes. That is the half of
// the gate worth keeping: a fresh process taking an identity has none of the
// awareness its predecessor had, whatever the board thinks of either.
func TestANewSessionStillHasToCheckIn(t *testing.T) {
	s := NewState("gate", DefaultLimits())
	regPersistent(t, s, "worker", "tw", "n0nce", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tw"}, t0)

	// The old session is gone; a new one reclaims the identity with its nonce.
	// Re-registering a LIVE agent is read as a retry and rotates nothing, which
	// was this test's first setup and proved nothing, so the old session ends
	// first.
	mustApply(t, s, &Op{Kind: OpSweep, StaleAgents: []string{"worker"}}, t0)
	mustApply(t, s, &Op{Kind: OpRegister, Name: "worker", Nonce: "n0nce", NewToken: "tw2", AgentKind: KindPersistent}, t0)
	if got := s.Agents["worker"].Token; got != "tw2" {
		t.Fatalf("setup: the token is %q, want the rotated tw2: no new session happened, "+
			"so the gate below would be tested on the wrong event", got)
	}
	_, _, err := s.Apply(&Op{
		Kind: OpSetSlot, Token: "tw2", Text: "picking up", Activity: "implement",
	}, t0)
	var ge *Error
	if !errors.As(err, &ge) || ge.Code != "E_MUST_ACK_BOARD" {
		t.Errorf("a new session declared without looking at the board: %v. The token "+
			"rotated, which is exactly when awareness is new", err)
	}
}
