package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The board does not carry the scorer's own footprint.
//
// `Slot.Predicted` is the work-overlap scorer's intermediate: a per-path weight
// vector the daemon derives to decide whether two agents are near each other's
// work. Matching reads it from state. No view has ever read it from a board:
// not the human panel, not board.js, not `dibs board`, not the e2e suites.
//
// It was half the payload. Measured on a live 32-agent board: 77,770 chars, of
// which slots were 51,803 and `predicted` alone 38,070. Board() is what both
// `register` and `check_in` return, and dibs://skills tells every agent to
// check in at the start of every activation, so each one paid roughly 18,000
// tokens to learn who else was on the board. This project guards tools/list
// with a hard test at 8,700 and then shipped twice that, per activation,
// unmeasured.
//
// The state keeps its footprint. Only the copy handed out loses it.
func TestTheBoardOmitsTheScorersFootprint(t *testing.T) {
	s := NewState("test", DefaultLimits())
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", NewToken: "tok",
		AgentKind: KindPersistent, Nonce: "n1",
	}, time.Now()); err != nil {
		t.Fatal("setup:", err)
	}
	// The awareness gate: declare is refused until the board is acknowledged.
	if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: "tok"}, time.Now()); err != nil {
		t.Fatal("setup:", err)
	}
	pred := make([]PredFile, 0, 200)
	for i := range 200 {
		pred = append(pred, PredFile{Path: strings.Repeat("deep/", 8) + string(rune('a'+i%26)), Weight: 1})
	}
	if _, _, err := s.Apply(&Op{
		Kind: OpSetSlot, Token: "tok", SlotID: "s1", Text: "reviewing the release",
		Dirs: []string{"/work"}, Predicted: pred,
	}, time.Now()); err != nil {
		t.Fatal("setup:", err)
	}
	if got := s.Agents["worker"].Slots["s1"].Predicted; len(got) != len(pred) {
		t.Fatalf("setup: state did not keep the footprint (%d of %d), so this "+
			"measures nothing", len(got), len(pred))
	}

	out, err := json.Marshal(s.Board())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "\"predicted\"") {
		t.Errorf("the board carries the scorer's footprint. It is charged to every "+
			"agent on every check_in, and nothing that reads a board has ever "+
			"looked at it. Board is %d chars", len(out))
	}
	// The things a peer actually needs are still there: what the work IS.
	for _, want := range []string{"reviewing the release", "/work", "worker"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the board no longer says %q, so trimming took coordination "+
				"content and not just the scorer's workings", want)
		}
	}
	// And matching still has what it reads, which is state, not the board.
	if len(s.Agents["worker"].Slots["s1"].Predicted) != len(pred) {
		t.Error("rendering the board mutated the agent's footprint: matching reads " +
			"that from state and would now score against nothing")
	}
}
