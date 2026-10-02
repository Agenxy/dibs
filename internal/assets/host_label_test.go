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
