package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A lost cursor of 1 was decremented to 0 by the refill, and the engine read
// 0 as "give me the whole ring", suppressing E_CURSOR_TOO_OLD. So a first
// serial that had left the ring took its unread mail with it and the resync
// that would have recovered the question never ran.
func TestARefillFromCursorOneStillResyncsWhenTheRingHasPassedIt(t *testing.T) {
	srv, eng, s := newServerWithEngine(t)
	eng.SetRingCap(4)
	ctx := context.Background()
	asker := toolCall(t, srv, "register", map[string]any{"name": "asker", "cwd": t.TempDir()})
	toolCall(t, srv, "register", map[string]any{"name": "busy", "cwd": t.TempDir()})
	// The one question this agent must not lose, sent at a low serial.
	toolCall(t, srv, "send", map[string]any{"token": asker["token"], "to": "busy", "type": "question", "body": "the one that matters"})
	// Push the ring floor well past serial 1 with unrelated traffic.
	other := toolCall(t, srv, "register", map[string]any{"name": "other", "cwd": t.TempDir()})
	for i := 0; i < 10; i++ {
		toolCall(t, srv, "send", map[string]any{"token": asker["token"], "to": "other", "type": "notify", "body": "filler"})
	}
	_ = other
	// A stream complete up to serial 1, whose channel then dropped: the
	// refill reads from its genuine position of 1.
	sub, cancel := eng.SubscribeTracked(1)
	defer cancel()
	rec := httptest.NewRecorder()
	p := &pumpState{
		s: s, ctx: ctx, stream: sseStream{w: rec, fl: rec}, sub: sub,
		last: 1, lastSub: 0, subID: json.RawMessage(`"listen"`),
		wants: func() (string, bool, bool) { return "busy", true, false },
	}
	sub.MarkLost()
	if !p.refill() {
		t.Fatal("the refill reported the stream closed")
	}
	if got := rec.Body.String(); !strings.Contains(got, `"uri":"dibs://inbox"`) {
		t.Fatalf("a refill from a position of one, with the ring floor past it, recovered no inbox "+
			"notice: the question that arrived before the ring floor was lost:\n%s", got)
	}
	_ = core.Event{}
}
