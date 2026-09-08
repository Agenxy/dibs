package engine

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// R4-4: reading adopted mail marks it delivered, as it does for any other mail.
func TestAdoptedMailBelowTheWatermarkIsMarkedDelivered(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	l := &core.Agent{
		ID: "heir", Name: "heir", Status: core.StatusActive, Token: "tok",
		CreatedSerial: 15, TruncatedBefore: 100, Slots: map[string]core.Slot{},
	}
	st.Agents["heir"] = l
	st.Messages[5] = &core.Message{
		Serial: 5, From: "asker", To: "heir", Type: core.MsgQuestion,
		State: core.MsgStatePending, AdoptedFrom: "lost", AdoptedAt: 20,
	}
	if got := pendingFor(st, l); len(got) != 1 || got[0] != 5 {
		t.Errorf("pendingFor returned %v: inbox shows this adopted message and the delivery "+
			"receipt never goes back to its sender", got)
	}
}
