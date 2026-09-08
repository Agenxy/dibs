package core

import (
	"testing"
	"time"
)

// The op named `resume` was the one recovery path that rotated the token,
// bumped the activation, and left every session binding pointing at the
// session the agent had just left. The bridge in the new session then opens a
// subscription the daemon rightly withholds mail from, and the daemon's wake
// routes still name the old session: awake, subscribed and unreachable.
func TestAResumeTakesTheSessionItArrivesFrom(t *testing.T) {
	threadA := "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	threadB := "019ffe52-0eaf-7f60-81cc-6ab1298d76ed"
	s := NewState("test", DefaultLimits())
	now := time.Unix(1700000000, 0)
	res, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", NewToken: "tok-1", Nonce: "n-keep",
		AgentKind: KindPersistent, SessionID: "host-1", SessionAlias: threadA,
		V7Semantics: true,
	}, now)
	if err != nil {
		t.Fatalf("setup register: %v", err)
	}
	id, _ := res["agent_id"].(string)
	if got := s.Agents[id].CurrentSession; got != threadA {
		t.Fatalf("setup: the agent's current session is %q, not thread A", got)
	}

	// It comes back through resume, from a different thread.
	if _, _, err := s.Apply(&Op{
		Kind: OpResume, Nonce: "n-keep", ResumeID: "r-1", NewToken: "tok-2",
		SessionAlias: threadB, V7Semantics: true,
	}, now.Add(time.Minute)); err != nil {
		t.Fatalf("resume: %v", err)
	}

	l := s.Agents[id]
	if l.CurrentSession != threadB {
		t.Errorf("after resuming from thread B the current session is %q: the daemon's wake "+
			"routes still name the session the agent left, and the subscription opened in B "+
			"has its inbox withheld because the row belongs elsewhere", l.CurrentSession)
	}
	if !l.holdsSession(threadB) {
		t.Errorf("the agent does not hold thread B at all, so nothing in B can reach it")
	}
}

// And a v0.0.6 resume still binds nothing, because rebinding on replay would
// move sessions history never moved.
func TestAPreV007ResumeBindsNoSession(t *testing.T) {
	threadB := "019ffe52-0eaf-7f60-81cc-6ab1298d76ed"
	s := NewState("test", DefaultLimits())
	now := time.Unix(1700000000, 0)
	res, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", NewToken: "tok-1", Nonce: "n-keep",
		AgentKind: KindPersistent, SessionID: "host-1",
	}, now)
	if err != nil {
		t.Fatalf("setup register: %v", err)
	}
	id, _ := res["agent_id"].(string)
	before := s.Agents[id].CurrentSession

	if _, _, err := s.Apply(&Op{
		Kind: OpResume, Nonce: "n-keep", ResumeID: "r-1", NewToken: "tok-2",
		SessionAlias: threadB, // present on the wire, but the op records no v0.0.7 decision
	}, now.Add(time.Minute)); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := s.Agents[id].CurrentSession; got != before {
		t.Errorf("a historical resume moved the current session to %q: replaying a v0.0.6 "+
			"ledger would rebind sessions the original fold never touched", got)
	}
	if s.Agents[id].holdsSession(threadB) {
		t.Error("a historical resume bound a thread, so one ledger builds two different boards")
	}
}
