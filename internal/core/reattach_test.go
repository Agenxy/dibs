package core

import (
	"strings"
	"testing"
	"time"
)

// A lifecycle hook can wake an agent, but a fresh turn carries no token. Without
// reattach the agent registers again, gets a sibling agent, and cannot read or
// answer the mail that woke it: observed live in opencode as E_NO_MESSAGE.
func TestRegisterReattachesToItsOwnSession(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()
	first, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "oc-agent",
		SessionID: "ses_abc", NewToken: "tok1",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	agentID := first["agent_id"].(string)

	// Same session, same name, no token in hand: must return the SAME agent.
	again, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "oc-agent",
		SessionID: "ses_abc", NewToken: "tok2",
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if again["agent_id"] != agentID {
		t.Fatalf("got a sibling agent %v, want reattach to %v", again["agent_id"], agentID)
	}
	if again["reattached"] != true {
		t.Error("reattach not signalled to the caller")
	}
	if again["token"] != "tok2" {
		t.Error("token was not rotated on reattach")
	}
	if got := len(s.Agents); got != 1 {
		t.Errorf("agent count = %d, want 1: reattach must not duplicate", got)
	}
	// The gate must re-arm: a new activation has not yet seen the board.
	if s.Agents[agentID].AckedSerial != 0 {
		t.Error("awareness gate not re-armed on reattach")
	}
}

// The restart that forked a whole fleet.
//
// Four agents restarted, four re-registered under their own names, and all four
// became siblings, builder-2, api-a-2, api-b-2, orchestrator-2, with every message
// addressed to them beforehand stranded in an agent nobody occupied. Nothing looked
// broken; the board showed four healthy agents throughout.
//
// The cause is that reattach keys on (name, session_id) and the bridge derives
// session_id from the harness's process id, so the credential cannot survive the
// one event it exists to recover from. A nonce can: the agent chooses it, keeps
// it, and presents it after anything.
func TestRestartWithNonceReattaches(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	t0 := time.Now()

	// Registered inside harness process 43782, keeping a nonce.
	first, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "builder",
		SessionID: "host-43782", Nonce: "nonce-builder-secret", NewToken: "tok1",
	}, t0)
	if err != nil {
		t.Fatal(err)
	}
	agentID := first["agent_id"].(string)

	// Mail arrives for it, from an agent that has to exist to send.
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "orchestrator", SessionID: "s-orch", NewToken: "tok-orch",
	}, t0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Apply(&Op{
		Kind: OpAckBoard, Token: "tok-orch",
	}, t0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Apply(&Op{
		Kind: OpSendMessage, Token: "tok-orch", To: agentID,
		MsgType: MsgNotify, Body: "report any Dibs bugs you hit",
	}, t0); err != nil {
		t.Fatal(err)
	}
	if n := len(s.Inbox(agentID)); n != 1 {
		t.Fatalf("setup: want 1 message before the restart, got %d", n)
	}

	// The harness restarts: same agent, same name, same nonce, NEW session id.
	// This is the step that used to fork the agent.
	again, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "builder",
		SessionID: "host-35645", Nonce: "nonce-builder-secret", NewToken: "tok2",
	}, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("re-registration after restart: %v", err)
	}

	if again["agent_id"] != agentID {
		t.Fatalf("restart forked the agent: got %v, want reattach to %v", again["agent_id"], agentID)
	}
	if again["reattached"] != true {
		t.Error("reattach not signalled to the caller")
	}
	if again["via"] != "nonce" {
		t.Errorf("via = %v, want nonce: the agent should learn which credential saved it", again["via"])
	}
	if again["token"] != "tok2" {
		t.Error("token was not rotated on reattach")
	}
	// The entire point: the mail survived.
	if n := len(s.Inbox(agentID)); n != 1 {
		t.Fatalf("mail stranded by the restart: inbox has %d message(s), want 1", n)
	}
	// And the live harness owns it now, so the claim guard can still name this agent.
	if got := s.Agents[agentID].SessionID; got != "host-35645" {
		t.Errorf("session_id = %q, want the restarted harness host-35645", got)
	}
}

// Without a nonce the restart still forks, and that is correct: a genuinely new
// session IS a new agent and Dibs cannot tell the two apart. What it must not do
// is fork in silence.
func TestRestartWithoutNonceForksButSaysSo(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	t0 := time.Now()

	first, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "k7-a", SessionID: "host-43782", NewToken: "tok1",
	}, t0)
	if err != nil {
		t.Fatal(err)
	}
	agentID := first["agent_id"].(string)
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "orchestrator", SessionID: "s-orch", NewToken: "tok-orch",
	}, t0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: "tok-orch"}, t0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Apply(&Op{
		Kind: OpSendMessage, Token: "tok-orch", To: agentID, MsgType: MsgNotify, Body: "unread",
	}, t0); err != nil {
		t.Fatal(err)
	}

	again, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "k7-a", SessionID: "host-35645", NewToken: "tok2",
	}, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if again["agent_id"] == agentID {
		t.Fatal("without a shared credential these are different agents; want a sibling")
	}
	taken, _ := again["name_taken"].(string)
	if taken == "" {
		t.Fatal("a SILENT fork is the actual bug; want name_taken to be set")
	}
	if !strings.Contains(taken, "1 message") {
		t.Errorf("want the stranded mail counted, got %q", taken)
	}
	if !strings.Contains(taken, "nonce") {
		t.Errorf("want the warning to name the credential that prevents this, got %q", taken)
	}
}

// Reattach keys on (name, session_id) and the bridge supplies the session_id, so
// an agent that is never told it holds one half of a two-part credential. That
// made the documented recovery unreachable in exactly the case it exists for.
func TestRegisterEchoesSessionIDAndItsLimit(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	res, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "solo", SessionID: "host-999", NewToken: "tok1",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res["session_id"] != "host-999" {
		t.Fatalf("agent cannot present a credential it is never told; session_id = %v", res["session_id"])
	}
	rec, _ := res["recovery"].(string)
	if !strings.Contains(rec, "restart") {
		t.Errorf("want recovery to warn that session_id dies with the harness, got %q", rec)
	}
}

// A nonce recovers the identity it was bound to. Pointing it at a second name is
// not recovery; it is two identities sharing one secret.
func TestNonceForADifferentNameIsRefused(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	t0 := time.Now()
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "alpha", SessionID: "s1", Nonce: "shared", NewToken: "t1",
	}, t0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "beta", SessionID: "s2", Nonce: "shared", NewToken: "t2",
	}, t0.Add(2*time.Hour)); err == nil {
		t.Fatal("want a refusal when one nonce is pointed at a second identity")
	}
}

// Reattach keys on session AND name, so a different agent in the same session,
// or the same name in another session, still gets its own agent.
func TestReattachDoesNotCollapseDistinctLanes(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()
	a, _, _ := s.Apply(&Op{Kind: OpRegister, Name: "writer", SessionID: "ses_1", NewToken: "t1"}, now)
	b, _, _ := s.Apply(&Op{Kind: OpRegister, Name: "reviewer", SessionID: "ses_1", NewToken: "t2"}, now)
	c, _, _ := s.Apply(&Op{Kind: OpRegister, Name: "writer", SessionID: "ses_2", NewToken: "t3"}, now)
	if a["agent_id"] == b["agent_id"] {
		t.Error("different names in one session collapsed into one agent")
	}
	if a["agent_id"] == c["agent_id"] {
		t.Error("same name in different sessions collapsed into one agent")
	}
	if got := len(s.Agents); got != 3 {
		t.Errorf("agent count = %d, want 3", got)
	}
}
