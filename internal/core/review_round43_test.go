package core

import (
	"testing"
	"time"
)

// A nonce recovery from a different session that stated no pid kept the pid
// the row had, which belonged to the process that is gone: the next liveness
// sweep found it dead and retired the agent that had just come back. A new
// activation with no pid stated is a process unknown.
func TestARecoveryFromANewSessionDropsTheOldProcess(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	mustApply(t, s, &Op{Kind: OpRegister, Name: "r", NewToken: "tok-1", Nonce: "n-r-0123456789abcdef", AgentKind: KindPersistent, SessionID: "host-111", PID: 111, ProcStart: 5, V7Semantics: true}, now)
	if s.Agents["r"].PID != 111 {
		t.Fatal("setup: the pid was not recorded")
	}
	// The same session recovering with no pid keeps what it knew.
	res := mustApply(t, s, &Op{Kind: OpRegister, Name: "r", NewToken: "tok-2", Nonce: "n-r-0123456789abcdef", AgentKind: KindPersistent, SessionID: "host-111", V7Semantics: true}, now.Add(time.Minute))
	if (res["reattached"] != true && res["resumed"] != true) || s.Agents["r"].PID != 111 {
		t.Fatalf("a recovery from the same session lost its pid: %v pid=%d", res, s.Agents["r"].PID)
	}
	// A new session recovering with no pid is a process unknown.
	mustApply(t, s, &Op{Kind: OpRegister, Name: "r", NewToken: "tok-3", Nonce: "n-r-0123456789abcdef", AgentKind: KindPersistent, SessionID: "host-222", V7Semantics: true}, now.Add(2*time.Minute))
	if got := s.Agents["r"].PID; got != 0 {
		t.Fatalf("recovered from a new session with no pid stated, the row still names pid %d: the "+
			"process that is gone, and the next liveness sweep retires the agent that just came back", got)
	}
	// And one that states its pid is believed.
	mustApply(t, s, &Op{Kind: OpRegister, Name: "r", NewToken: "tok-4", Nonce: "n-r-0123456789abcdef", AgentKind: KindPersistent, SessionID: "host-333", PID: 333, V7Semantics: true}, now.Add(3*time.Minute))
	if got := s.Agents["r"].PID; got != 333 {
		t.Fatalf("a stated pid was not taken: %d", got)
	}
	// A bridge restarted under the SAME session id states its new pid, and
	// that alone is a change worth recording.
	res = mustApply(t, s, &Op{Kind: OpRegister, Name: "r", NewToken: "tok-4b", Nonce: "n-r-0123456789abcdef", AgentKind: KindPersistent, SessionID: "host-333", PID: 334, V7Semantics: true}, now.Add(3*time.Minute+time.Second))
	if res["resumed"] != true || s.Agents["r"].PID != 334 {
		t.Fatalf("a resume under the same session stating a new pid kept pid %d (%v): the next "+
			"liveness sweep finds the old process dead", s.Agents["r"].PID, res)
	}
	// The same rule on the other recovery path: a row that went dormant,
	// recovered by nonce from yet another session with no pid stated.
	s.Agents["r"].Status = StatusDormant
	res = mustApply(t, s, &Op{Kind: OpRegister, Name: "r", NewToken: "tok-5", Nonce: "n-r-0123456789abcdef", AgentKind: KindPersistent, SessionID: "host-444", V7Semantics: true}, now.Add(4*time.Minute))
	if res["via"] != "nonce" {
		t.Fatalf("setup: the dormant row was not recovered by nonce: %v", res)
	}
	if got := s.Agents["r"].PID; got != 0 {
		t.Fatalf("a dormant row recovered from a new session with no pid stated still names pid %d", got)
	}
}
