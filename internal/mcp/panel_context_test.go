package mcp

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A cursor reaching far enough back returns the entire history.
func TestActivityListIsBounded(t *testing.T) {
	var evs []core.Result
	for i := range 300 {
		evs = append(evs, core.Result{"serial": i, "type": "agent.registered", "agent": "x"})
	}
	got := panelPayload(core.Result{"events": evs})["events"]
	if n := len(asMaps(got)); n != maxPanelEvents {
		t.Errorf("activity carried %d events, want capped at %d", n, maxPanelEvents)
	}
}
