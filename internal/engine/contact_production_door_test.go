package engine

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// This enters at the same send event the production writer publishes, using
// only the pre-feature API shape so it can be run against the parent commit.
func TestUnreachableUnreadRequestAppearsOnTheBoard(t *testing.T) {
	now := time.Now().Add(-time.Hour)
	state := core.NewState("contact-door", core.DefaultLimits())
	for _, name := range []string{"asker", "closed"} {
		if _, _, err := state.Apply(&core.Op{
			Kind: core.OpRegister, Name: name,
			NewToken: "tok-" + name, AgentKind: core.KindPersistent,
			Nonce: "nonce-" + name,
		}, now); err != nil {
			t.Fatal(err)
		}
	}
	state.Agents["closed"].Status = core.StatusDormant
	sent, _, err := state.Apply(&core.Op{
		Kind: core.OpSendMessage, Token: "tok-asker",
		To: "closed", MsgType: core.MsgRequest, Body: "private request",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	serial := sent["msg_serial"].(uint64)
	e := New(state, &memLedger{}, deadProber{})
	e.maybeWake(core.Event{
		Type: "message.sent", To: "closed", Agent: "asker",
		Data: map[string]any{"msg_type": core.MsgRequest, "msg_serial": serial},
	})
	alerts, ok := state.Board()["contact_alerts"]
	if !ok || alerts == nil {
		t.Fatal("unreachable unread request has no board contact alert")
	}
}

func TestUnreachableHighNotifyEscalatesButNormalNotifyDoesNot(t *testing.T) {
	for _, priority := range []string{"normal", "high", "urgent"} {
		t.Run(priority, func(t *testing.T) {
			now := time.Now().Add(-time.Hour)
			state := core.NewState("contact-notify", core.DefaultLimits())
			for _, name := range []string{"sender", "closed"} {
				if _, _, err := state.Apply(&core.Op{
					Kind: core.OpRegister, Name: name, NewToken: "tok-" + name,
					AgentKind: core.KindPersistent, Nonce: "nonce-" + name,
				}, now); err != nil {
					t.Fatal(err)
				}
			}
			state.Agents["closed"].Status = core.StatusDormant
			sent, _, err := state.Apply(&core.Op{
				Kind: core.OpSendMessage, Token: "tok-sender", To: "closed",
				MsgType: core.MsgNotify, Body: "operational alert", RequestPriority: priority,
			}, now)
			if err != nil {
				t.Fatal(err)
			}
			serial := sent["msg_serial"].(uint64)
			e := New(state, &memLedger{}, deadProber{})
			e.maybeWake(core.Event{
				Type: "message.sent", To: "closed", Agent: "sender",
				Data: map[string]any{"msg_type": core.MsgNotify, "msg_serial": serial},
			})
			alerts, _ := state.Board()["contact_alerts"].([]*core.ContactEscalation)
			if got := len(alerts) > 0; got != (priority != "normal") {
				t.Fatalf("priority %s contact alert = %v, want %v", priority, got, priority != "normal")
			}
		})
	}
}
