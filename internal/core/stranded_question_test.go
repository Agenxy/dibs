package core

import "testing"

// A question owed by a lane that closes is unanswerable from that instant, and
// the asker was told nothing until the deadline.
//
// The sweep already reaches the right verdict — it has a branch and a sentence
// for precisely this case — but it does not run until the deadline elapses. So
// an agent that asked with a ten-minute deadline blocked for ten minutes on an
// answer that became impossible in the first second, while the board knew:
// resume_lane refuses a closed lane with E_LANE_CLOSED, and Gone() is
// documented as "never comes back".
func TestClosingALaneEndsTheQuestionsItWillNeverAnswer(t *testing.T) {
	st := NewState("test", DefaultLimits())
	now := t0
	reg := func(name string) string {
		tok := "tok-" + name
		if _, _, err := st.Apply(&Op{Kind: OpRegisterLane, Name: name, NewToken: tok}, now); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		if _, _, err := st.Apply(&Op{Kind: OpAckBoard, Token: tok}, now); err != nil {
			t.Fatalf("ack %s: %v", name, err)
		}
		return tok
	}
	asker, answerer := reg("asker"), reg("answerer")

	res, _, err := st.Apply(&Op{
		Kind: OpSendMessage, Token: asker, To: "answerer", MsgType: "question",
		Body: "will you answer?", OpID: "q1", DeadlineSec: 600,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := res["msg_serial"].(uint64)
	if serial == 0 {
		t.Fatalf("no message serial in %v", res)
	}
	if st.Messages[serial].Terminal() {
		t.Fatal("precondition: the question should be pending")
	}

	_, evs, err := st.Apply(&Op{Kind: OpCloseLane, Token: answerer}, now)
	if err != nil {
		t.Fatal(err)
	}

	m := st.Messages[serial]
	if !m.Terminal() {
		t.Fatalf("the question is still pending after its recipient closed — the "+
			"asker waits out the full deadline for an answer that cannot come "+
			"(state %q)", m.State)
	}
	if m.State != MsgStateExpiredDead {
		t.Errorf("state = %q, want %q", m.State, MsgStateExpiredDead)
	}
	// The distinction the detail text exists to draw: this was a deliberate
	// finish, not a crash, so nobody should be sent to inspect its directories.
	if m.ExpireDetail == "" {
		t.Error("no detail — the sender cannot tell a clean close from a crash")
	}
	var told bool
	for _, e := range evs {
		if e.Type == "message."+MsgStateExpiredDead && e.To == "asker" {
			told = true
		}
	}
	if !told {
		t.Errorf("no event addressed to the asker; events: %+v", evs)
	}
}

// The mirror image: the ASKER closes while the answer is being composed.
//
// This cannot be prevented — leaving mid-thought is allowed — but it was
// reported as an unqualified success. respond() returned {"ok": true} for an
// answer addressed to a lane that will never read it, which reads as "the other
// side has your answer" and leaves the responder waiting for a follow-up that
// is not coming.
func TestAnsweringADepartedAskerSaysSo(t *testing.T) {
	st := NewState("test", DefaultLimits())
	now := t0
	reg := func(name string) string {
		tok := "tok-" + name
		if _, _, err := st.Apply(&Op{Kind: OpRegisterLane, Name: name, NewToken: tok}, now); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		if _, _, err := st.Apply(&Op{Kind: OpAckBoard, Token: tok}, now); err != nil {
			t.Fatalf("ack %s: %v", name, err)
		}
		return tok
	}
	asker, answerer := reg("asker"), reg("answerer")

	sent, _, err := st.Apply(&Op{
		Kind: OpSendMessage, Token: asker, To: "answerer", MsgType: "question",
		Body: "what do you think?", OpID: "q1", DeadlineSec: 600,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := sent["msg_serial"].(uint64)

	if _, _, err := st.Apply(&Op{Kind: OpCloseLane, Token: asker}, now); err != nil {
		t.Fatal(err)
	}

	res, _, err := st.Apply(&Op{
		Kind: OpRespond, Token: answerer, MsgSerial: serial,
		Disposition: "answer", Body: "here is my answer",
	}, now)
	if err != nil {
		t.Fatalf("answering should still be allowed — the work was done: %v", err)
	}
	if res["delivered"] != false {
		t.Errorf("respond reported plain success for an answer nobody will read: %v", res)
	}
	if res["note"] == nil {
		t.Error("no note saying why the answer has nowhere to go")
	}

	// An answer to a LIVE asker is unqualified, with no scary note attached.
	live := reg("live")
	sent2, _, err := st.Apply(&Op{
		Kind: OpSendMessage, Token: live, To: "answerer", MsgType: "question",
		Body: "and this one?", OpID: "q2", DeadlineSec: 600,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := sent2["msg_serial"].(uint64)
	res2, _, err := st.Apply(&Op{
		Kind: OpRespond, Token: answerer, MsgSerial: s2,
		Disposition: "answer", Body: "yes",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := res2["delivered"]; present {
		t.Errorf("an ordinary answer was qualified for no reason: %v", res2)
	}
}
