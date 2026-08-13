package core

import (
	"strings"
	"testing"
	"time"
)

// A chat surface only touches the API when its human types, so minutes of
// silence are its normal state. Reporting proc_alive:false for an agent that never
// claimed a process reads as "it crashed" when nothing did.
func TestStaleSpaceWithoutPIDIsIdleNotDead(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()
	_, _, _ = s.Apply(&Op{Kind: OpRegister, Name: "chat", NewToken: "t1"}, now)

	_, evs, err := s.Apply(&Op{Kind: OpSweep, StaleAgents: []string{"chat"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range evs {
		if e.Type != "agent.stale" {
			continue
		}
		found = true
		if got := e.Data["reason"]; got != "idle_no_activity" {
			t.Errorf("reason = %v, want idle_no_activity: no PID means no evidence of death", got)
		}
		if _, has := e.Data["proc_alive"]; has {
			t.Error("proc_alive reported for an agent that never gave a PID")
		}
	}
	if !found {
		t.Fatal("no agent.stale event emitted")
	}
}

// With a PID we can actually check, so the stricter reading is earned.
func TestStaleSpaceWithPIDKeepsLeaseSemantics(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()
	_, _, _ = s.Apply(&Op{Kind: OpRegister, Name: "agent", PID: 4242, NewToken: "t1"}, now)

	// Giving a PID is not the same as having it CHECKED, and this used to
	// conflate them: a sweep that probed nothing still recorded
	// proc_alive:false, which is a claim the process was looked at and found
	// gone. An agent running a long build has a perfectly alive process.
	//
	// So the verdict appears only when something measured it. The lease still
	// governs the transition either way: that is what "keeps lease semantics"
	// means here.
	_, evs, _ := s.Apply(&Op{Kind: OpSweep, StaleAgents: []string{"agent"}}, now)
	for _, e := range evs {
		if e.Type != "agent.stale" {
			continue
		}
		if got := e.Data["reason"]; got != "lease_lapsed" {
			t.Errorf("reason = %v, want lease_lapsed", got)
		}
		if _, has := e.Data["proc_alive"]; has {
			t.Error("nothing probed this pid; recording a verdict about it is a fabrication")
		}
	}

	// And when the sweep DID probe, the verdict is there.
	_, evs2, _ := s.Apply(&Op{
		Kind: OpSweep, StaleAgents: []string{"agent"}, AlivePIDs: []int{4242},
	}, now)
	for _, e := range evs2 {
		if e.Type != "agent.stale" {
			continue
		}
		if v, has := e.Data["proc_alive"]; !has || v != true {
			t.Errorf("a probed-alive process must be recorded as such, got %v/%v", v, has)
		}
	}
}

// The grace period must differ, or a human-paced agent flaps forever.
func TestIdleTTLIsLongerThanLeaseTTL(t *testing.T) {
	l := DefaultLimits()
	if l.IdleTTL <= l.AgentTTL {
		t.Fatalf("IdleTTL %v must exceed AgentTTL %v", l.IdleTTL, l.AgentTTL)
	}
}

// "name + session_id" is guessable, and presenting both rotates the token,
// taking the mailbox, the actor identity, and any role the agent holds.
//
// The bridge derives the session id from the host's process id
// (`host-<ppid>`), which any same-user process can enumerate with ps, and the
// agent's name is on the board. Verified against a running daemon: a second
// registration with a victim's name and session id returned a working token
// that read the victim's private mail.
//
// Losing your context must not lose your mailbox, so the weak path survives for
// agents that have nothing better, and those are TOLD so. An agent that
// registered with a nonce has a real secret, and that is what reclaims it.
func TestASpaceWithARealCredentialIsNotReclaimedByAGuessableOne(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	const nonce = "real-secret-0123456789abcdef"

	guarded, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "guarded", NewToken: "t1",
		SessionID: "guarded-sess", Nonce: nonce,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	guardedID, _ := guarded["agent_id"].(string)
	before := s.Agents[guardedID].Token

	// Name and session id are both knowable. They must not be enough.
	taken, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "guarded", NewToken: "stolen", SessionID: "guarded-sess",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if taken["reattached"] == true {
		t.Fatal("an agent holding a real secret must not be reclaimed by a guessable pair")
	}
	if s.Agents[guardedID].Token != before {
		t.Fatal("and its token must not have been rotated out from under it")
	}

	// An agent with only a session id keeps working: losing context must not
	// lose your mailbox, but it is told what that costs.
	plain, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "plain", NewToken: "t2", SessionID: "plain-sess",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	note, _ := plain["recovery"].(string)
	if !strings.Contains(note, "is secret") || !strings.Contains(note, "nonce") {
		t.Errorf("an agent reclaimable by a guessable pair must be told so, and told the fix; got %q", note)
	}
	back, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "plain", NewToken: "t3", SessionID: "plain-sess",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if back["reattached"] != true {
		t.Fatal("recovery by session id must still work for an agent that has nothing better")
	}
}
