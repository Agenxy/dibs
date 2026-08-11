package core

import (
	"strings"
	"testing"
	"time"
)

// A chat surface only touches the API when its human types, so minutes of
// silence are its normal state. Reporting proc_alive:false for a lane that never
// claimed a process reads as "it crashed" when nothing did.
func TestStaleLaneWithoutPIDIsIdleNotDead(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()
	_, _, _ = s.Apply(&Op{Kind: OpRegisterLane, Name: "chat", NewToken: "t1"}, now)

	_, evs, err := s.Apply(&Op{Kind: OpSweep, StaleLanes: []string{"chat"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range evs {
		if e.Type != "lane.stale" {
			continue
		}
		found = true
		if got := e.Data["reason"]; got != "idle_no_activity" {
			t.Errorf("reason = %v, want idle_no_activity: no PID means no evidence of death", got)
		}
		if _, has := e.Data["proc_alive"]; has {
			t.Error("proc_alive reported for a lane that never gave a PID")
		}
	}
	if !found {
		t.Fatal("no lane.stale event emitted")
	}
}

// With a PID we can actually check, so the stricter reading is earned.
func TestStaleLaneWithPIDKeepsLeaseSemantics(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()
	_, _, _ = s.Apply(&Op{Kind: OpRegisterLane, Name: "agent", PID: 4242, NewToken: "t1"}, now)

	// Giving a PID is not the same as having it CHECKED, and this used to
	// conflate them: a sweep that probed nothing still recorded
	// proc_alive:false, which is a claim the process was looked at and found
	// gone. A lane running a long build has a perfectly alive process.
	//
	// So the verdict appears only when something measured it. The lease still
	// governs the transition either way: that is what "keeps lease semantics"
	// means here.
	_, evs, _ := s.Apply(&Op{Kind: OpSweep, StaleLanes: []string{"agent"}}, now)
	for _, e := range evs {
		if e.Type != "lane.stale" {
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
		Kind: OpSweep, StaleLanes: []string{"agent"}, AlivePIDs: []int{4242},
	}, now)
	for _, e := range evs2 {
		if e.Type != "lane.stale" {
			continue
		}
		if v, has := e.Data["proc_alive"]; !has || v != true {
			t.Errorf("a probed-alive process must be recorded as such, got %v/%v", v, has)
		}
	}
}

// The grace period must differ, or a human-paced lane flaps forever.
func TestIdleTTLIsLongerThanLeaseTTL(t *testing.T) {
	l := DefaultLimits()
	if l.IdleTTL <= l.LaneTTL {
		t.Fatalf("IdleTTL %v must exceed LaneTTL %v", l.IdleTTL, l.LaneTTL)
	}
}

// "name + session_id" is guessable, and presenting both rotates the token,
// taking the mailbox, the actor identity, and any role the lane holds.
//
// The bridge derives the session id from the host's process id
// (`host-<ppid>`), which any same-user process can enumerate with ps, and the
// lane's name is on the board. Verified against a running daemon: a second
// registration with a victim's name and session id returned a working token
// that read the victim's private mail.
//
// Losing your context must not lose your mailbox, so the weak path survives for
// lanes that have nothing better, and those are TOLD so. A lane that
// registered with a nonce has a real secret, and that is what reclaims it.
func TestALaneWithARealCredentialIsNotReclaimedByAGuessableOne(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	const nonce = "real-secret-0123456789abcdef"

	guarded, _, err := s.Apply(&Op{
		Kind: OpRegisterLane, Name: "guarded", NewToken: "t1",
		SessionID: "guarded-sess", Nonce: nonce,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	guardedID, _ := guarded["lane_id"].(string)
	before := s.Lanes[guardedID].Token

	// Name and session id are both knowable. They must not be enough.
	taken, _, err := s.Apply(&Op{
		Kind: OpRegisterLane, Name: "guarded", NewToken: "stolen", SessionID: "guarded-sess",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if taken["reattached"] == true {
		t.Fatal("a lane holding a real secret must not be reclaimed by a guessable pair")
	}
	if s.Lanes[guardedID].Token != before {
		t.Fatal("and its token must not have been rotated out from under it")
	}

	// A lane with only a session id keeps working: losing context must not
	// lose your mailbox, but it is told what that costs.
	plain, _, err := s.Apply(&Op{
		Kind: OpRegisterLane, Name: "plain", NewToken: "t2", SessionID: "plain-sess",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	note, _ := plain["recovery"].(string)
	if !strings.Contains(note, "is secret") || !strings.Contains(note, "nonce") {
		t.Errorf("a lane reclaimable by a guessable pair must be told so, and told the fix; got %q", note)
	}
	back, _, err := s.Apply(&Op{
		Kind: OpRegisterLane, Name: "plain", NewToken: "t3", SessionID: "plain-sess",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if back["reattached"] != true {
		t.Fatal("recovery by session id must still work for a lane that has nothing better")
	}
}
