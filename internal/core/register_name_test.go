package core

import (
	"strings"
	"testing"
	"time"
)

// An agent that asks for a taken name is told, and told who has it.
//
// It used to be silent: a reviewer registering as `sol` was handed `sol-4` over
// three runs and only noticed because it happened to read the id back. An agent
// that does not notice publishes the wrong address, tells colleagues to write to
// `sol`, and never finds out why the mail stops arriving.
//
// The suffix itself is correct and must stay. A stale or dormant lane still owns
// its mailbox, so handing its name to a newcomer would redirect somebody else's
// mail, which is the failure the suffix exists to prevent. The defect was the
// silence, not the rename.
func TestATakenNameIsExplainedRatherThanSilentlySuffixed(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()

	first, _, err := s.Apply(&Op{
		Kind: OpRegisterLane, Name: "sol", Description: "first",
		NewToken: "t1", SessionID: "s1",
	}, now)
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	if got, _ := first["lane_id"].(string); got != "sol" {
		t.Fatalf("first lane_id = %q, want sol", got)
	}
	if _, noted := first["name_note"]; noted {
		t.Errorf("the first registration got a name note but nothing was taken: %v",
			first["name_note"])
	}

	second, _, err := s.Apply(&Op{
		Kind: OpRegisterLane, Name: "sol", Description: "second, different session",
		NewToken: "t2", SessionID: "s2",
	}, now)
	if err != nil {
		t.Fatalf("second register: %v", err)
	}
	id, _ := second["lane_id"].(string)
	if id == "sol" {
		t.Fatal("the second lane took the first one's name: mail addressed to sol " +
			"would now reach the wrong agent")
	}
	note, _ := second["name_note"].(string)
	if note == "" {
		t.Fatalf("lane %q was renamed with no explanation", id)
	}
	for _, want := range []string{"sol", id, "reattach"} {
		if !strings.Contains(note, want) {
			t.Errorf("name_note does not mention %q: %s", want, note)
		}
	}
}
