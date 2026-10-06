package assets

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSharedRendererUsesTheMachineRowLabel(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	// Run the actual pure renderer used by both the panel and the web board.
	js := BoardJS() + `
const row = {id:"one", host:"shared<&machine", agent:{host:"raw-agent-label"}};
const html = Board.laneHTML(row);
if (!html.includes("shared&lt;&amp;machine") || html.includes("raw-agent-label")) throw new Error(html);
console.log("ok");`
	out, err := exec.Command(bun, "-e", js).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("renderer: %v: %s", err, out)
	}
}

func TestContactAlertRendererUsesOnlyBoundedMetadata(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	js := BoardJS() + `
const html = Board.contactAlertsHTML([{recipient:"worker<&", oldest_serial:42, count:3}]);
if (!html.includes("worker&lt;&amp;") || !html.includes("#42") || !html.includes("3 coalesced")) throw new Error(html);
if (html.includes("<script") || Board.contactAlertsHTML([]) !== "") throw new Error(html);
console.log("ok");`
	out, err := exec.Command(bun, "-e", js).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("contact renderer: %v: %s", err, out)
	}
}

func TestUnreadResponseWindowIsShownWithoutInventingADeadline(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	js := BoardJS() + `
const pending = Board.messageHTML({serial:7, type:"request", state:"pending", body:"work", response_window_s:600});
if (!pending.includes("clock starts on first read") || pending.includes("response due")) throw new Error(pending);
const delivered = Board.messageHTML({serial:7, type:"request", state:"delivered", body:"work", response_window_s:600, deadline:"2026-10-06T12:00:00Z"});
if (!delivered.includes("response due") || delivered.includes("clock starts on first read")) throw new Error(delivered);
console.log("ok");`
	out, err := exec.Command(bun, "-e", js).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("window renderer: %v: %s", err, out)
	}
}
