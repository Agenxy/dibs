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
	}, nil)
	params, _ := n["params"].(map[string]any)
	meta, _ := params["_meta"].(map[string]any)
	if meta[EventMetaKey] != "message.sent" || meta[MsgTypeMetaKey] != core.MsgNotify || meta[SerialMetaKey] == nil {
		t.Fatalf("an inbox notification carries _meta %v: a subscriber cannot tell a notify "+
			"from a question and wakes a session for either", meta)
	}
	b := resourceUpdated("dibs://board", nil, core.Event{Type: "agent.registered"}, nil)
	bp, _ := b["params"].(map[string]any)
	if bm, _ := bp["_meta"].(map[string]any); bm[EventMetaKey] != nil {
		t.Errorf("a board notification names an event it has no use for: %v", bm)
	}
}

// And it says what the wake should SAY, for the subscriber that will say it.
//
// ONE WRITER TO THE SESSION SOCKET. The bridge running inside a Claude Code
// session and this daemon were both writing to that session's one message
// socket, so every message produced two notifications; the bridge arrived first
// and carried a fixed sentence with no facts in it. The digest travels on the
// notification the bridge is already receiving, so the surviving card is the
// informative one, and it is computed here because the two calls that would
// tell the bridge (inbox, hook_poll) both mark mail delivered.
//
// Only for a subscriber that asked, and only on the inbox: the digest quotes
// message bodies.
func TestTheInboxNotificationCarriesTheWakeDigest(t *testing.T) {
	const digest = "mail for your agent \"a\"."
	ev := core.Event{Type: "message.sent", To: "a", Data: map[string]any{"msg_type": core.MsgQuestion}}
	n := resourceUpdated("dibs://inbox", nil, ev, func(core.Event) string { return digest })
	params, _ := n["params"].(map[string]any)
	meta, _ := params["_meta"].(map[string]any)
	if meta[DigestMetaKey] != digest {
		t.Errorf("the inbox notification carries _meta[%q] = %v, want the digest: the "+
			"in-session bridge has nothing else to say and composing a sentence there is "+
			"what put a second, emptier card in front of every message",
			DigestMetaKey, meta[DigestMetaKey])
	}
	// A daemon with nothing to quote yet says nothing, rather than a placeholder.
	empty := resourceUpdated("dibs://inbox", nil, ev, func(core.Event) string { return "" })
	ep, _ := empty["params"].(map[string]any)
	em, _ := ep["_meta"].(map[string]any)
	if _, present := em[DigestMetaKey]; present {
		t.Errorf("an empty digest was still put on the notification: %v. The bridge sends "+
			"what it is handed, so an empty string here is an empty interruption there", em)
	}
	// The board is not one agent's mail and never carries it.
	b := resourceUpdated("dibs://board", nil, core.Event{Type: "agent.registered"},
		func(core.Event) string { return digest })
	bp, _ := b["params"].(map[string]any)
	if bm, _ := bp["_meta"].(map[string]any); bm[DigestMetaKey] != nil {
		t.Errorf("a board notification carries an agent's mail digest: %v. Every agent may "+
			"watch the board, and this quotes message bodies", bm)
	}
}
