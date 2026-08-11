package mcp

import (
	"testing"

	"github.com/agenxy/lanes/internal/core"
)

// Every call showing the same board turns the panel into noise: three identical
// dashboards stacked in one conversation. Each tool must open the view that
// matches what it just did.
func TestEachToolOpensItsOwnView(t *testing.T) {
	for tool, want := range map[string]string{
		"ack_board":    "board",
		"inbox":        "mail",
		"await_events": "activity",
		"send_message": "mail",
		"respond":      "mail",
	} {
		if got := panelTools[tool]; got != want {
			t.Errorf("%s opens %q, want %q", tool, got, want)
		}
	}
	if _, ok := panelTools["show_board"]; !ok {
		t.Error("show_board must be a panel tool, honouring its own view argument")
	}
}

// A call that found nothing should not draw a panel to say so.
func TestPanelSuppressedWhenThereIsNothingNew(t *testing.T) {
	if panelWorthShowing("inbox", core.Result{"messages": []core.Result{}}) {
		t.Error("empty mailbox should not open a panel")
	}
	if !panelWorthShowing("inbox", core.Result{"messages": []core.Result{{"serial": 1}}}) {
		t.Error("mailbox WITH mail must open the panel: the inbox tool returns messages at the top level")
	}
	if panelWorthShowing("await_events", core.Result{"events": nil}) {
		t.Error("a timed-out await should not open a panel")
	}
	if !panelWorthShowing("ack_board", core.Result{}) {
		t.Error("ack_board is deliberate orientation; it always draws")
	}
}

// A cursor reaching far enough back returns the entire history.
func TestActivityListIsBounded(t *testing.T) {
	var evs []core.Result
	for i := range 300 {
		evs = append(evs, core.Result{"serial": i, "type": "lane.registered", "lane": "x"})
	}
	got := panelPayload(core.Result{"events": evs})["events"]
	if n := len(asMaps(got)); n != maxPanelEvents {
		t.Errorf("activity carried %d events, want capped at %d", n, maxPanelEvents)
	}
}
