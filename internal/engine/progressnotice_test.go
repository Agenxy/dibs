// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The sender of a task is told what moved, without being woken for it, and
// done tells it where the work landed (Dibs #7424).
func TestTheSenderOfATaskIsToldWhatMoved(t *testing.T) {
	who, text, blocking := situationalNotice(core.Event{
		Type: "message.progress", Agent: "worker", To: "lead",
		Data: map[string]any{"msg_serial": uint64(7), "milestone": 2, "label": "draft written", "reached": 2, "total": 4},
	})
	if who != "lead" || blocking {
		t.Errorf("progress went to %q, blocking=%v: it is for the sender, and it is not a wake", who, blocking)
	}
	for _, want := range []string{"worker", "msg 7", `"draft written"`, "2 of 4"} {
		if !strings.Contains(text, want) {
			t.Errorf("the notice %q does not say %s", text, want)
		}
	}
	_, done, _ := situationalNotice(core.Event{
		Type: "message.done", Agent: "worker", To: "lead",
		Data: map[string]any{"msg_serial": uint64(7), "deliverable": "/reports/q3.md"},
	})
	if !strings.Contains(done, "/reports/q3.md") {
		t.Errorf("done says %q and not where the work landed", done)
	}
}
