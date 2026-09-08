package core

import (
	"testing"
	"time"
)

// The adoption event filter matched the source and the heir and never asked
// when the move happened: a second adoption from the same source announced
// every earlier one's mail again as a new blocking arrival, to the
// subscription and to the wake.
func TestASecondAdoptionAnnouncesOnlyWhatItMoved(t *testing.T) {
	s := NewState("probe", DefaultLimits())
	now := time.Unix(1700000000, 0)
	ackReg(t, s, "sender", "tok-s", now)
	ackReg(t, s, "lost", "tok-l", now)
	ackReg(t, s, "heir", "tok-h", now)
	s.Agents["heir"].Role = RoleCoordinator
	q := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "tok-s", To: "lost", MsgType: MsgQuestion, Body: "?", DeadlineSec: 3600, V7Semantics: true}, now)
	qSerial, _ := q["msg_serial"].(uint64)
	s.Agents["lost"].Status = StatusDormant
	_, evs, err := s.Apply(&Op{Kind: OpAdoptAgent, Token: "tok-h", To: "lost", AdoptAuthorised: true, V7Semantics: true}, now.Add(time.Minute))
	if err != nil {
		t.Fatal("setup:", err)
	}
	if n := countAdopted(evs); n != 1 {
		t.Fatalf("setup: the first adoption announced %d message(s), want the question", n)
	}
	// More mail reaches the abandoned source, and it is adopted again.
	s.Agents["lost"].Status = StatusActive
	h := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "tok-s", To: "lost", MsgType: MsgHandoff, Body: "take this", V7Semantics: true}, now.Add(2*time.Minute))
	hSerial, _ := h["msg_serial"].(uint64)
	s.Agents["lost"].Status = StatusDormant
	_, evs, err = s.Apply(&Op{Kind: OpAdoptAgent, Token: "tok-h", To: "lost", AdoptAuthorised: true, V7Semantics: true}, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal("setup:", err)
	}
	var announced []uint64
	for _, ev := range evs {
		if ev.Type == "message.adopted" {
			ms, _ := ev.Data["msg_serial"].(uint64)
			announced = append(announced, ms)
		}
	}
	if len(announced) != 1 || announced[0] != hSerial {
		t.Fatalf("the second adoption announced %v: want only the handoff %d, not the question %d it "+
			"moved the first time, which the heir is now told is a new blocking arrival", announced, hSerial, qSerial)
	}
}

func countAdopted(evs []Event) int {
	n := 0
	for _, ev := range evs {
		if ev.Type == "message.adopted" {
			n++
		}
	}
	return n
}
