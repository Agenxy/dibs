package core

import (
	"strings"
	"testing"
)

// Everything checked here is REPLAYED metadata: it is written to the ledger and
// read back into memory on every daemon start, forever. The count of dirs and
// refs was bounded and the size of each element was not, which bounds nothing.
func TestReplayedMetadataIsBounded(t *testing.T) {
	lim := DefaultLimits()
	huge := strings.Repeat("x", lim.MaxPathBytes+1)
	longName := strings.Repeat("n", lim.MaxNameBytes+1)

	many := make([]string, lim.MaxDirs+1)
	for i := range many {
		many[i] = "port:8080"
	}

	for _, tc := range []struct {
		what string
		op   *Op
	}{
		{"an oversized dir", &Op{Kind: OpSetSlot, Dirs: []string{"ok", huge}}},
		{"an oversized ref", &Op{Kind: OpSetSlot, Refs: []string{huge}}},
		{"an oversized hold", &Op{Kind: OpSetSlot, Holds: []string{huge}}},
		{"too many holds", &Op{Kind: OpSetSlot, Holds: many}},
		{"an oversized session_id", &Op{Kind: OpBindSession, SessionID: longName}},
		{"an oversized agent.title", &Op{Kind: OpRegisterLane, Agent: &AgentInfo{Title: longName}}},
		{"an oversized agent.cwd", &Op{Kind: OpRegisterLane, Agent: &AgentInfo{CWD: longName}}},
		{"an oversized agent.harness", &Op{Kind: OpRegisterLane, Agent: &AgentInfo{Harness: longName}}},
	} {
		if err := Admit(tc.op, lim); err == nil {
			t.Errorf("%s was admitted — it would sit in the ledger forever", tc.what)
		}
	}

	// The ordinary case still passes, at the size real callers send.
	ok := &Op{
		Kind: OpSetSlot,
		Dirs: []string{"internal/core", "cmd/lanes"},
		Refs: []string{"internal/core/apply.go"},
		// A hold is a host resource name, so the honest values are tiny.
		Holds: []string{"port:8080", "lock:.git/index"},
		Agent: &AgentInfo{Harness: "Claude Code", CWD: "/Users/x/proj", Branch: "main"},
	}
	if err := Admit(ok, lim); err != nil {
		t.Fatalf("a perfectly ordinary slot was rejected: %v", err)
	}
}

// The bound must bind at INGRESS and not in the fold. Apply is the fold: a rule
// added there applies retroactively to a ledger already on disk, and the daemon
// refuses to replay ops it wrote and acknowledged itself. That failure has
// happened here before, which is why Admit exists.
func TestTheNewBoundsDoNotBindHistory(t *testing.T) {
	lim := DefaultLimits()
	st := NewState("test", lim)

	tok := "t1"
	if _, _, err := st.Apply(&Op{Kind: OpRegisterLane, Name: "a", NewToken: tok}, t0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Apply(&Op{Kind: OpAckBoard, Token: tok}, t0); err != nil {
		t.Fatal(err)
	}

	// An op that Admit would now reject, arriving the way replay delivers it.
	oversized := &Op{
		Kind: OpSetSlot, Token: tok, Text: "work",
		Holds: []string{strings.Repeat("x", lim.MaxPathBytes+1)},
	}
	if err := Admit(oversized, lim); err == nil {
		t.Fatal("precondition: Admit should reject this")
	}
	if _, _, err := st.Apply(oversized, t0); err != nil {
		t.Fatalf("Apply refused an op a previous version accepted — every daemon "+
			"with one in its ledger now fails to start: %v", err)
	}
}
