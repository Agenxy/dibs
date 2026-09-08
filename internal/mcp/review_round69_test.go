package mcp

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// Events rebuilt from the inbox carry no Sub, so they all land on zero. A
// stream whose position was already (serial, 0) skipped one as delivered: an
// adoption emits agent.updated at sub 0 and its mail event after it, so
// delivering the first and losing the second left the rebuilt notice looking
// seen, and the mail sat in the inbox with nothing to announce it.
func TestARebuiltNoticeIsNotSkippedByThePositionItLandsOn(t *testing.T) {
	p := &pumpState{last: 42, lastSub: 0}

	// The ordinary rule is unchanged: a ring event at the position is seen.
	if !p.seen(core.Event{Serial: 42, Sub: 0, Type: "agent.updated"}) {
		t.Fatal("a ring event at the stream's own position is no longer treated as delivered")
	}
	// And one after it is not.
	if p.seen(core.Event{Serial: 42, Sub: 1, Type: "message.adopted"}) {
		t.Fatal("a later sub at the same serial is treated as delivered")
	}
	// The rebuilt one, which ResyncFor marks, must survive the same position.
	rebuilt := core.Event{
		Serial: 42, Sub: 0, Type: "message.adopted", To: "heir",
		Data: map[string]any{"msg_serial": uint64(7), "msg_type": core.MsgQuestion, "resynced": true},
	}
	if p.seen(rebuilt) {
		t.Fatal("a notice rebuilt from the inbox was skipped as already delivered because it " +
			"landed on the position's own (serial, sub): the mail stays in the inbox with " +
			"nothing to announce it, which is the loss the resync exists to repair")
	}
}
