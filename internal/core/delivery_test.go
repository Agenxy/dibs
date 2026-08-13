package core

import (
	"strings"
	"testing"
	"time"
)

// Sending into an agent nobody occupies used to succeed in silence.
//
// The fleet restart that forked every agent left `orchestrator` dormant and
// `orchestrator-2` live under the same name. An agent addressed its bug report to
// `orchestrator`, Dibs returned ok, and the report was never read by anyone. The
// failure was invisible from both ends at once: the sender saw success, and the
// agent it was reaching for saw nothing. Two full reports were lost that way
// before anybody noticed, and only then because a THIRD space happened to
// mention it.
func TestSendToSupersededLaneWarnsAndNamesTheLiveOne(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	t0 := time.Now()

	// The agent that will be superseded, and a sender.
	mustApply(t, s, &Op{Kind: OpRegister, Name: "orchestrator", SessionID: "s1", NewToken: "t-old"}, t0)
	reg(t, s, "builder", "t-builder", t0)

	// It goes dormant, and the same agent comes back as a sibling.
	s.Agents["orchestrator"].Status = StatusDormant
	again := mustApply(t, s, &Op{
		Kind: OpRegister, Name: "orchestrator", SessionID: "s2", NewToken: "t-new",
	}, t0.Add(time.Hour))
	liveID := again["agent_id"].(string)
	if liveID == "orchestrator" {
		t.Fatal("setup: expected a sibling agent")
	}

	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "t-builder"}, t0)
	res := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "t-builder", To: "orchestrator",
		MsgType: MsgNotify, Body: "here is my full bug report",
	}, t0.Add(time.Hour))

	warn, _ := res["note"].(string)
	if warn == "" {
		t.Fatal("silent success is the bug; sending to a superseded agent must say so")
	}
	if !strings.Contains(warn, liveID) {
		t.Errorf("note must name the live agent %q so the sender can resend, got: %s", liveID, warn)
	}
	// It still DELIVERS. The message is not the sender's to lose, and an agent can
	// come back; what was missing was the sender knowing.
	if res["ok"] != true {
		t.Error("the message must still be delivered, not refused")
	}
	if n := len(s.Inbox("orchestrator")); n != 1 {
		t.Errorf("message not delivered to the addressed agent: inbox %d", n)
	}
}

// A dormant agent with NO live sibling is a standing role asleep between
// activations. That is what persistent agents are for, so this must not be
// refused, but the sender still deserves to know nothing is owed to it.
func TestSendToDormantLaneDeliversWithNotice(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	t0 := time.Now()
	mustApply(t, s, &Op{
		Kind: OpRegister, Name: "nightly", SessionID: "s1", NewToken: "t-n",
	}, t0)
	reg(t, s, "sender", "t-s", t0)
	s.Agents["nightly"].Status = StatusDormant

	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "t-s"}, t0)
	res := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "t-s", To: "nightly", MsgType: MsgNotify, Body: "fyi",
	}, t0)

	if res["ok"] != true {
		t.Fatal("mail to a sleeping standing role is the mechanism, not an error")
	}
	warn, _ := res["note"].(string)
	if !strings.Contains(warn, "dormant") {
		t.Errorf("want the sender told the recipient is asleep, got: %q", warn)
	}
	if n := len(s.Inbox("nightly")); n != 1 {
		t.Errorf("dormant agents must still collect mail, inbox %d", n)
	}
}

// The common case must stay quiet. A warning on every ordinary send is noise, and
// noise is what makes real warnings invisible.
func TestSendToActiveLaneIsNotWarned(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	t0 := time.Now()
	reg(t, s, "a", "ta", t0)
	reg(t, s, "b", "tb", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "ta"}, t0)
	res := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "ta", To: "b", MsgType: MsgNotify, Body: "hello",
	}, t0)
	if w, ok := res["note"]; ok {
		t.Errorf("no note expected for a live recipient, got %v", w)
	}
}
