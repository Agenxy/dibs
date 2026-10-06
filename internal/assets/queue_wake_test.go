package assets

import (
	"os/exec"
	"testing"
)

// Both human surfaces call this renderer. Test its production entry point,
// without calling a new rendering helper or seeding an eligibility flag.
func TestSharedRendererShowsObservedQueueWakeAndUnknownAge(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	for _, kind := range []string{"pending", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			js := BoardJS() + `
const kind = "` + kind + `";
const queue = {admission:"retained", pending:kind, thread_state:"unknown"};
if (kind === "pending") {
  queue.issued_at = new Date(Date.now() - 13 * 3600000).toISOString();
  queue.observed_at = new Date().toISOString();
}
const html = Board.laneHTML({id:"worker", queue_wake:queue});
if (!html.includes("thread state unknown") || !html.includes("does not confirm a started turn or read mail")) throw new Error(html);
if (kind === "pending" && (!html.includes("wake last seen pending") || !html.includes("13h"))) throw new Error(html);
if (kind === "unknown" && (!html.includes("pending unknown") || !html.includes("age unknown"))) throw new Error(html);
`
			if out, err := exec.Command(bun, "-e", js).CombinedOutput(); err != nil {
				t.Fatalf("queue wake hidden or overstated in shared renderer: %v: %s", err, out)
			}
		})
	}
}
