package engine

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A sender is not told their message was delivered to somebody who cannot read it.
//
// "delivered" is a claim made to the SENDER. markDelivered swept every pending
// message addressed to the agent's id, and an id is derived from the name, so a
// name that comes back reuses it: mail addressed to a previous occupant sits
// below the returning agent's TruncatedBefore watermark, is filtered out of the
// inbox it just read, and was marked delivered anyway.
//
// That is worse than failing to deliver it. An undelivered message still reads
// as undelivered, so the sender knows to ask; a falsely delivered one removes
// the only signal that anything went wrong, and the message is unreachable
// forever, because the recipient cannot see it to answer or consume it.
//
// The decision is tested rather than the wrapper: e.query on a zero-value
// Engine blocks forever instead of failing. See AGENTS.md.
func TestDeliveryIsNotClaimedForMailTheAgentCannotRead(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	l := &core.Agent{ID: "target", Name: "target", TruncatedBefore: 5}
	st.Agents[l.ID] = l
	st.Messages = map[uint64]*core.Message{
		3: {Serial: 3, To: "target", State: core.MsgStatePending},
		4: {Serial: 4, To: "target", State: core.MsgStatePending},
		7: {Serial: 7, To: "target", State: core.MsgStatePending},
		8: {Serial: 8, To: "other", State: core.MsgStatePending},
	}

	got := pendingFor(st, l)
	seen := map[uint64]bool{}
	for _, s := range got {
		seen[s] = true
	}
	for _, below := range []uint64{3, 4} {
		if seen[below] {
			t.Errorf("message %d is below the watermark, so this agent never saw it, "+
				"and marking it delivered tells its sender otherwise", below)
		}
	}
	if !seen[7] {
		t.Error("message 7 is this agent's own pending mail and must still be " +
			"marked delivered: a filter that filters everything is not a fix")
	}
	if seen[8] {
		t.Error("message 8 is addressed to another agent entirely")
	}
}
