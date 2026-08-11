package core

import (
	"math"
	"testing"
	"time"
)

// A huge positive deadline clamps to the ceiling, never into the past.
//
// The conversion to a Duration happened before the clamp, so a large
// deadline_s overflowed int64 nanoseconds and came out negative, and min()
// then preferred that "smaller" value over the ceiling. The real MCP surface
// accepted MaxInt64 and returned a deadline one second BEFORE the send. The
// next sweep expires a question the moment it is asked, which the sender reads
// as an agent that ignored them.
func TestAnEnormousDeadlineClampsRatherThanWrapping(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()

	reg := func(name string) string {
		if _, _, err := s.Apply(&Op{
			Kind: OpRegisterLane, Name: name, NewToken: "t-" + name, SessionID: name,
		}, now); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: "t-" + name}, now); err != nil {
			t.Fatalf("ack %s: %v", name, err)
		}
		return "t-" + name
	}
	sender := reg("sender")
	reg("recipient")

	for _, secs := range []int{math.MaxInt32, int(math.MaxInt64 / 1000000000), math.MaxInt} {
		res, _, err := s.Apply(&Op{
			Kind: OpSendMessage, Token: sender, To: "recipient", MsgType: MsgQuestion,
			Body: "still there?", DeadlineSec: secs,
		}, now)
		if err != nil {
			t.Fatalf("deadline_s=%d: %v", secs, err)
		}
		serial, _ := res["msg_serial"].(uint64)
		m := s.Messages[serial]
		if m == nil {
			t.Fatalf("deadline_s=%d: no message stored", secs)
		}
		if !m.Deadline.After(now) {
			t.Errorf("deadline_s=%d produced a deadline in the past (%v, %v before the "+
				"send): the next sweep expires a question that was just asked",
				secs, m.Deadline, now.Sub(m.Deadline))
		}
		if got := m.Deadline.Sub(now); got > s.Limits.MaxDeadlineDormant+time.Second {
			t.Errorf("deadline_s=%d exceeded the ceiling: %v > %v",
				secs, got, s.Limits.MaxDeadlineDormant)
		}
	}
}
