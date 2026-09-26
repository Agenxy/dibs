package main

import (
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/mcp"
)

// The in-session route must say what arrived, like the other two.
//
// It said one fixed content-free sentence while holding the best channel in
// the product: in-session, authenticated, no argv, no peer preamble. A peer
// running a wake-path test on 2026-09-25 was reached by THIS route first and
// got the least informative notice on the board. The notification already
// names the message type, so the fix cost nothing.
func TestTheInSessionWakeSaysWhatArrived(t *testing.T) {
	got := selfWakeLine(map[string]any{mcp.MsgTypeMetaKey: "question"})
	if !strings.Contains(got, "question") {
		t.Errorf("the in-session wake does not say what arrived: %q", got)
	}
	if strings.Contains(strings.ToLower(got), "check the board") {
		t.Errorf("the retired imperative is back on the self-wake route: %q", got)
	}
	// An older daemon sends no type. Saying something true and vague beats
	// saying nothing, and beats inventing a type.
	if bare := selfWakeLine(nil); bare != selfWakeNotice {
		t.Errorf("with no type in the notification the fallback changed: %q", bare)
	}
	if strings.Contains(strings.ToLower(selfWakeNotice), "check the board") {
		t.Errorf("the fallback is the retired imperative: %q", selfWakeNotice)
	}
}
