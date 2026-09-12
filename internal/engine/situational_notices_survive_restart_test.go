package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A situational notice survives a restart, or the instruction it carried is
// what disappears.
//
// Verdicts were rebuilt from state in v0.0.7; everything else a notice can
// carry (evicted, admitted, absorbed, requeued, a member joining your space)
// lived only in memory. The comment used to say the cost of losing one was a
// repeated notice. True of "somebody joined", false of "stop work there": an
// agent told to stop can carry on, because the instruction is what vanished.
// Issue #75.
//
// A REBUILT ENGINE over the replayed ring, which is what a restart is: the
// daemon seeds the ring from replay and hands it to New.
func TestSituationalNoticesAreRebuiltFromTheRingAfterARestart(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	st.Agents = map[string]*core.Agent{
		// Checked in at 40, evicted at 50: has not heard.
		"evicted": {ID: "evicted", Name: "evicted", Status: core.StatusActive, AckedSerial: 40, CreatedSerial: 1},
		// Admitted at 60, checked in at 70: already told, by that check_in.
		"caughtup": {ID: "caughtup", Name: "caughtup", Status: core.StatusActive, AckedSerial: 70, CreatedSerial: 1},
		// A member of the space somebody joined at 80, last checked in at 75.
		"member": {ID: "member", Name: "member", Status: core.StatusActive, AckedSerial: 75, CreatedSerial: 1},
		// Registered at 90, after everything: none of it happened to this one,
		// however its watermark reads.
		"newcomer": {ID: "newcomer", Name: "newcomer", Status: core.StatusActive, AckedSerial: 0, CreatedSerial: 90},
		"director": {ID: "director", Name: "director", Status: core.StatusActive, AckedSerial: 100, CreatedSerial: 1},
	}
	st.Spaces = map[string]*core.Space{
		"auth": {ID: "auth", Members: map[string]*core.Membership{"member": {}, "joiner": {}}},
	}
	at := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	ring := []core.Event{
		{Type: "agent.evicted", Agent: "evicted", Serial: 50, TS: at, Data: map[string]any{"agent_id": "auth", "by": "director"}},
		{Type: "agent.joined", Agent: "caughtup", Serial: 60, TS: at, Data: map[string]any{"agent_id": "auth", "admitted_by": "director"}},
		{Type: "agent.joined", Agent: "joiner", Serial: 80, TS: at, Data: map[string]any{"agent_id": "auth"}},
		{Type: "agent.evicted", Agent: "newcomer", Serial: 85, TS: at, Data: map[string]any{"agent_id": "auth", "by": "director"}},
	}

	restarted := New(st, &memLedger{}, deadProber{}, ring)

	texts := func(agent string) []string {
		var out []string
		for _, n := range restarted.notices[agent] {
			out = append(out, n.Text)
		}
		return out
	}
	if got := texts("evicted"); len(got) != 1 || !strings.Contains(got[0], "stop work there") {
		t.Errorf("evicted agent's notices after restart = %v, want the eviction: the "+
			"instruction to stop is the thing that used to vanish with the daemon", got)
	}
	if got := texts("caughtup"); len(got) != 0 {
		t.Errorf("an agent that checked in after being admitted was told again: %v", got)
	}
	if got := texts("member"); len(got) != 1 || !strings.Contains(got[0], "joiner") {
		t.Errorf("member's notices = %v, want to be told joiner arrived in its space", got)
	}
	if got := texts("newcomer"); len(got) != 0 {
		t.Errorf("an agent registered after the event was told about it: %v (a reused "+
			"id inherits nothing that happened to its predecessor)", got)
	}
	// And the age is the event's, not the restart's: an agent must not be able
	// to tell that its board restarted from the wording of its own mail.
	for _, n := range restarted.notices["evicted"] {
		if !n.At.Equal(at) {
			t.Errorf("rebuilt notice is dated %v, want the event's %v", n.At, at)
		}
	}
}

// A verdict is rebuilt from state, and the ring holds its event too: one
// notice, not two.
func TestAVerdictInBothStateAndRingIsRebuiltOnce(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	st.Agents = map[string]*core.Agent{
		"asker": {ID: "asker", Name: "asker", Status: core.StatusActive, CreatedSerial: 1},
		"human": {ID: "human", Name: "human", Status: core.StatusActive, CreatedSerial: 1},
	}
	st.Messages[42] = &core.Message{
		Serial: 42, From: "asker", To: "human", Type: core.MsgRequest,
		State: core.MsgStateApproved, RespondedAt: 43,
	}
	ring := []core.Event{{
		Type: "message.approved", Agent: "human", To: "asker", Serial: 43,
		Data: map[string]any{"msg_serial": uint64(42)},
	}}
	restarted := New(st, &memLedger{}, deadProber{}, ring)
	if n := len(restarted.notices["asker"]); n != 1 {
		t.Errorf("%d notices for one approval, want 1: the ring must not duplicate "+
			"what state already rebuilt", n)
	}
}
