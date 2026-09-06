package core

import "testing"

func TestOnlyBlockingMailIsWorthAWake(t *testing.T) {
	cases := []struct {
		ev, msg string
		want    bool
	}{
		{"message.sent", MsgNotify, false},
		{"message.sent", MsgQuestion, true},
		{"message.sent", MsgRequest, true},
		{"message.sent", MsgHandoff, true},
		{"message.answered", "", true}, // a verdict has no msg_type
		{"message.approved", "", true},
		{"message.denied", "", true},
		{"message.declined", "", true},
		{"message.acked", MsgQuestion, false},
		{"agent.registered", "", false},
	}
	for _, c := range cases {
		if got := WakeWorthy(c.ev, c.msg); got != c.want {
			t.Errorf("WakeWorthy(%q, %q) = %v, want %v", c.ev, c.msg, got, c.want)
		}
	}
}
