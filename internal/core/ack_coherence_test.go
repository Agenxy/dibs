package core

import (
	"testing"
	"time"
)

// check_in's checkpoint must agree with itself.
//
// It is the recovery call: an agent that lost context asks for the board, its
// mail, what it owes, and its cursor, in one atomic answer. SPEC.md promises a
// coherent POST-STATE snapshot, and the embedded board was built while Apply
// still held the pre-op serial, so the cursor said N+1 and the board said N. A
// client treating board.serial as the cut, which is the obvious reading, would
// reason from a board one event behind the cursor it was handed in the same
// object.
func TestAckBoardsCheckpointAgreesWithItself(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()
	res, _, err := s.Apply(&Op{
		Kind: OpRegisterLane, Name: "worker", NewToken: "w1", SessionID: "s1",
	}, now)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	tok, _ := res["token"].(string)

	ack, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: tok}, now)
	if err != nil {
		t.Fatalf("check_in: %v", err)
	}
	cursor, _ := ack["serial"].(uint64)
	board, _ := ack["board"].(map[string]any)
	if board == nil {
		t.Fatal("check_in returned no board")
	}
	boardSerial, _ := board["serial"].(uint64)
	if boardSerial != cursor {
		t.Errorf("board.serial = %d but the checkpoint's cursor is %d: the two halves "+
			"of one atomic answer disagree, and a client that trusts the board's own "+
			"stamp reasons from a state a serial behind the cursor it was given",
			boardSerial, cursor)
	}
	if acked, _ := ack["acked_serial"].(uint64); acked != cursor {
		t.Errorf("acked_serial = %d, cursor = %d", acked, cursor)
	}
}
