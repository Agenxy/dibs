package main

import (
	"strings"
	"testing"
	"time"
)

// The op and the agent must never run together.
//
// The op column's width is a guess at the longest ledgered op name, and the guess
// has been wrong twice. At 18 it collided with activity_checkpoint. At 19 it
// padded that op to exactly zero characters and the agent was concatenated
// straight onto it ("activity_checkpointorchestrator") which is what a reviewer
// reported reading in `dibs log`.
//
// So this pins the property rather than the width: whatever the longest op grows
// to, there is a separator. A test that only checked the current constant would
// pass again the day somebody adds a longer op kind.
func TestTheOpNeverRunsIntoTheLane(t *testing.T) {
	when := time.Date(2026, 8, 7, 11, 5, 36, 0, time.UTC)
	for _, op := range []string{
		"activity_checkpoint",        // the exact width of the column
		"a_very_long_future_op_kind", // longer than it
		"send",                       // comfortably shorter
	} {
		line := logLine(43, when, op, "orchestrator", "")
		if strings.Contains(line, op+"orchestrator") {
			t.Errorf("op %q runs straight into the agent: %q", op, line)
		}
		if !strings.Contains(line, "orchestrator") {
			t.Errorf("op %q lost the agent entirely: %q", op, line)
		}
	}
}
