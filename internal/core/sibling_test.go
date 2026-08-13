package core

import (
	"strings"
	"testing"
	"time"
)

// A one-shot agent that re-registers under a taken name becomes a sibling, and
// the mail addressed to the original is unreachable from the new agent. That is
// correct (a new session is a new agent) but it happened in silence, and two
// real opencode agents lost an answer to it. The registration must say so.
func TestRegisterUnderTakenNameWarnsAndNamesTheLostMail(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	t0 := time.Now()

	first, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "beta", NewToken: "t1",
		SessionID: "bridge-1",
	}, t0)
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	if _, has := first["name_taken"]; has {
		t.Fatalf("the first agent of a name is not a collision, got %v", first["name_taken"])
	}
	betaID, _ := first["agent_id"].(string)

	// Someone writes to beta.
	asker, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "alpha", NewToken: "t2",
		SessionID: "bridge-2",
	}, t0)
	if err != nil {
		t.Fatalf("asker register: %v", err)
	}
	if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: "t2"}, t0); err != nil {
		t.Fatalf("ack: %v", err)
	}
	_ = asker
	if _, _, err := s.Apply(&Op{
		Kind: OpSendMessage, Token: "t2", To: betaID,
		MsgType: "question", Body: "did you find it?", OpID: "q1",
	}, t0); err != nil {
		t.Fatalf("send: %v", err)
	}

	// A NEW session of the same agent: new bridge, new session_id, same name.
	second, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "beta", NewToken: "t3",
		SessionID: "bridge-3",
	}, t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("second register: %v", err)
	}
	if id, _ := second["agent_id"].(string); id == betaID {
		t.Fatalf("a different session must not silently take over agent %s", betaID)
	}
	warn, _ := second["name_taken"].(string)
	if warn == "" {
		t.Fatal("registering under a taken name must warn: this is how the answer got lost")
	}
	if !strings.Contains(warn, betaID) {
		t.Errorf("warning must name the sibling agent %q, got: %s", betaID, warn)
	}
	if !strings.Contains(warn, "1 message") {
		t.Errorf("warning must say how much mail is unreachable, got: %s", warn)
	}
	// The fix has to be one the caller can actually perform. This used to say
	// "resume", which is the standing-role path and does nothing for an
	// ephemeral agent, so the warning correctly identified the problem and then
	// sent the agent somewhere that could not solve it. Point at the nonce, which
	// reattaches any kind of agent, and at merge_spaces for an agent that kept none.
	if !strings.Contains(warn, "nonce") {
		t.Errorf("warning must name the credential that reattaches, got: %s", warn)
	}
	if !strings.Contains(warn, "merge_spaces") {
		t.Errorf("warning must offer a route for an agent with no nonce, got: %s", warn)
	}
}

// The warning must point at the mailbox the caller cannot read, not merely the
// most recent namesake. Ranking by recency alone named an empty sibling while
// the lost answer sat in an older one.
func TestWarningNamesTheSiblingHoldingMailNotTheNewest(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	t0 := time.Now()

	// "worker" holds the mail...
	held, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", NewToken: "t1",
		SessionID: "b1",
	}, t0)
	if err != nil {
		t.Fatalf("register held: %v", err)
	}
	heldID, _ := held["agent_id"].(string)

	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "asker", NewToken: "t2",
		SessionID: "b2",
	}, t0); err != nil {
		t.Fatalf("register asker: %v", err)
	}
	if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: "t2"}, t0); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if _, _, err := s.Apply(&Op{
		Kind: OpSendMessage, Token: "t2", To: heldID,
		MsgType: "question", Body: "?", OpID: "q1",
	}, t0); err != nil {
		t.Fatalf("send: %v", err)
	}

	// ...and a NEWER, empty namesake exists.
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", NewToken: "t3",
		SessionID: "b3",
	}, t0.Add(time.Minute)); err != nil {
		t.Fatalf("register empty sibling: %v", err)
	}

	third, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", NewToken: "t4",
		SessionID: "b4",
	}, t0.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("register third: %v", err)
	}
	warn, _ := third["name_taken"].(string)
	if !strings.Contains(warn, heldID) {
		t.Errorf("warning must name %q, the agent holding the mail; got: %s", heldID, warn)
	}
	if !strings.Contains(warn, "1 message") {
		t.Errorf("warning must report the unreachable mail; got: %s", warn)
	}
}

// A closed agent is not a live sibling: its mail is nobody's pending business,
// so reusing its name is ordinary, not a collision worth reporting.
func TestClosedSpaceDoesNotTriggerTheNameWarning(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	t0 := time.Now()
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", NewToken: "t1",
		SessionID: "b1",
	}, t0); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, _, err := s.Apply(&Op{Kind: OpSignOff, Token: "t1"}, t0); err != nil {
		t.Fatalf("close: %v", err)
	}
	again, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", NewToken: "t2",
		SessionID: "b2",
	}, t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if w, has := again["name_taken"]; has {
		t.Errorf("closed agent must not read as a collision, got: %v", w)
	}
}
