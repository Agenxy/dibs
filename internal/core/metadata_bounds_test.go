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
		{"an oversized agent.title", &Op{Kind: OpRegister, Agent: &AgentInfo{Title: longName}}},
		// Bounded as a PATH, not a name: 128 bytes rejected working directories
		// that real agents register from, and what it refused was the whole
		// register, not the field.
		{"an oversized agent.cwd", &Op{Kind: OpRegister, Agent: &AgentInfo{CWD: huge}}},
		{"an oversized agent.project", &Op{Kind: OpRegister, Agent: &AgentInfo{Project: longName}}},
		{"an oversized agent.harness", &Op{Kind: OpRegister, Agent: &AgentInfo{Harness: longName}}},
	} {
		if err := Admit(tc.op, lim); err == nil {
			t.Errorf("%s was admitted: it would sit in the ledger forever", tc.what)
		}
	}

	// The ordinary case still passes, at the size real callers send.
	ok := &Op{
		Kind: OpSetSlot,
		Dirs: []string{"internal/core", "cmd/dibs"},
		Refs: []string{"internal/core/apply.go"},
		// A hold is a host resource name, so the honest values are tiny.
		Holds: []string{"port:8080", "lock:.git/index"},
		Agent: &AgentInfo{Harness: "Claude Code", CWD: "/Users/x/proj", Branch: "main"},
	}
	if err := Admit(ok, lim); err != nil {
		t.Fatalf("a perfectly ordinary slot was rejected: %v", err)
	}

	// A working directory a few levels inside a home directory is ORDINARY, and
	// at 128 bytes it was refused. The cost was not a missing label: the whole
	// registration failed, so an agent in a deep checkout could not join the
	// board at all. On a machine running several projects, deep paths are the
	// normal case rather than the exotic one.
	deep := "/Users/somebody/Library/CloudStorage/Dropbox/Development/clients/" +
		"northwind/services/payments-api/internal/store/migrations/2026/q3"
	if len(deep) <= lim.MaxNameBytes {
		t.Fatalf("the fixture is no longer long enough to exercise the bound (%d bytes)", len(deep))
	}
	if err := Admit(&Op{Kind: OpRegister, Agent: &AgentInfo{CWD: deep}}, lim); err != nil {
		t.Errorf("an agent in an ordinary deep checkout could not register: %v", err)
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
	if _, _, err := st.Apply(&Op{Kind: OpRegister, Name: "a", NewToken: tok}, t0); err != nil {
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
		t.Fatalf("Apply refused an op a previous version accepted: every daemon "+
			"with one in its ledger now fails to start: %v", err)
	}
}
