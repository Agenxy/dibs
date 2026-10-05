package engine

import (
	"io"
	"log/slog"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/logs"
)

// Future unsupported payloads still need an actionable INFO diagnosis. This
// enters the production output filter, with the same redacting handler as
// the daemon: an attribute called "key" hid the field name in the real log.
func TestUnsupportedStrictHookOutputNamesItsEventAndField(t *testing.T) {
	ring := logs.NewRing(8)
	previous := slog.Default()
	slog.SetDefault(slog.New(logs.NewHandler(slog.NewTextHandler(io.Discard, nil), ring)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	e := &Engine{}
	out := e.hookOutput(core.Result{
		"future_payload": "unsupported delivery", "decision": "block", "reason": "kept digest",
	}, true, "Stop")
	if out["future_payload"] != nil || out["decision"] != "block" || out["reason"] != "kept digest" {
		t.Fatalf("the filter must drop only the unsupported field: %v", out)
	}
	for _, record := range ring.Tail(0) {
		if record.Level == "INFO" && record.Attrs["event"] == "Stop" &&
			record.Attrs["field"] == "future_payload" && record.Attrs["value"] == "unsupported delivery" {
			return
		}
	}
	t.Fatalf("unsupported output lost its readable INFO diagnosis: %v", ring.Tail(0))
}
