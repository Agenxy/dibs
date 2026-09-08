package mcp

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The inbox notification says WHAT arrived, so the bridge can apply the
// daemon's own wake rule instead of waking for every change.
func TestTheInboxNotificationSaysWhatArrived(t *testing.T) {
	n := resourceUpdated("dibs://inbox", nil, core.Event{
		Type: "message.sent", To: "a", Data: map[string]any{"msg_type": core.MsgNotify},
	})
	params, _ := n["params"].(map[string]any)
	meta, _ := params["_meta"].(map[string]any)
	if meta[EventMetaKey] != "message.sent" || meta[MsgTypeMetaKey] != core.MsgNotify || meta[SerialMetaKey] == nil {
		t.Fatalf("an inbox notification carries _meta %v: a subscriber cannot tell a notify "+
			"from a question and wakes a session for either", meta)
	}
	b := resourceUpdated("dibs://board", nil, core.Event{Type: "agent.registered"})
	bp, _ := b["params"].(map[string]any)
	if bm, _ := bp["_meta"].(map[string]any); bm[EventMetaKey] != nil {
		t.Errorf("a board notification names an event it has no use for: %v", bm)
	}
}
