package core

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// A stranded report is pointed at the one address that always exists.
//
// An agent that finishes work can find its recipient gone: lanes get reaped,
// and the agent that asked for the work may be the one that ended. A reviewer hit
// this exactly: its report was addressed to a reaped lane, the refusal listed
// live lanes, and it concluded there was no durable delivery path. It then tried
// broadcast, which is coordinator-only and correctly refused, and the review
// survived only in its own stdout.
//
// The path existed and nothing pointed at it. The operator's lane is persistent,
// outlives every agent, and belongs to the participant who always wants to know,
// and it was already in that list, spelled like any other agent.
func TestAMissingRecipientPointsAtTheOperator(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()

	// The human's lane, as engine.HumanAgent creates it.
	if _, _, err := s.Apply(&Op{
		Kind: OpRegisterLane, Name: "operator", Description: "the human at the board",
		LaneKind: KindPersistent, Nonce: "human:operator", NewToken: "h1", SessionID: "human:operator",
		Agent: &AgentInfo{Harness: "lanes web", Surface: "web"},
	}, now); err != nil {
		t.Fatalf("setup: human lane: %v", err)
	}
	res, _, err := s.Apply(&Op{
		Kind: OpRegisterLane, Name: "worker", NewToken: "w1", SessionID: "s1",
	}, now)
	if err != nil {
		t.Fatalf("setup: worker: %v", err)
	}
	tok, _ := res["token"].(string)
	if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: tok}, now); err != nil {
		t.Fatalf("setup: ack: %v", err)
	}

	_, _, serr := s.Apply(&Op{
		Kind: OpSendMessage, Token: tok, To: "an-agent-that-is-gone",
		MsgType: MsgNotify, Body: "my review", OpID: "r1",
	}, now)
	if serr == nil {
		t.Fatal("sending to a lane that does not exist succeeded")
	}
	// The HINT, not Error(): Error() is code plus message, and everything
	// instructive in this project lives in the hint the agent is handed.
	var le *Error
	if !errors.As(serr, &le) {
		t.Fatalf("refusal is not a structured error: %v", serr)
	}
	hint := le.Hint
	if !strings.Contains(hint, "operator") {
		t.Errorf("the refusal does not name the operator's lane, so an agent with a "+
			"report and a dead recipient still has nowhere to send it: %s", hint)
	}
	if !strings.Contains(hint, "persistent") {
		t.Errorf("the refusal does not say WHY that lane is the durable one: %s", hint)
	}
}

// On a board no human has opened, nothing is offered.
//
// Inventing a fallback that does not exist would be worse than the silence: an
// agent told to send its report to a lane that is not there loses it twice.
func TestNoOperatorMeansNoFallbackIsClaimed(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()
	res, _, err := s.Apply(&Op{
		Kind: OpRegisterLane, Name: "worker", NewToken: "w1", SessionID: "s1",
	}, now)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	tok, _ := res["token"].(string)
	if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: tok}, now); err != nil {
		t.Fatalf("setup: ack: %v", err)
	}
	_, _, serr := s.Apply(&Op{
		Kind: OpSendMessage, Token: tok, To: "nobody", MsgType: MsgNotify, Body: "x", OpID: "r1",
	}, now)
	if serr == nil {
		t.Fatal("sending to a lane that does not exist succeeded")
	}
	var le2 *Error
	if !errors.As(serr, &le2) {
		t.Fatalf("refusal is not a structured error: %v", serr)
	}
	if strings.Contains(le2.Hint, "the operator is on this board") {
		t.Errorf("a fallback was offered on a board with no human lane: %s", serr)
	}
}
